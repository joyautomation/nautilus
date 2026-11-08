// Command share keeps the playground's saved projects: the Share button
// posts the project here and gets back a short link, /playground/p/<id>.
//
// A saved project never changes. Its id is a hash of what was saved, so
// saving the same project twice gives the same link, and editing a project
// and saving it again gives a new one (that is all a fork is, for now).
// Each project is one gzipped JSON file on disk: there is no database.
//
//	POST   /playground/api/p        {title, open, hmi, name, files} -> {id}
//	GET    /playground/api/p/<id>   the project, as saved
//	DELETE /playground/api/p/<id>   take it down (Authorization: Bearer $SHARE_ADMIN_TOKEN)
//	GET    /playground/api/healthz
//
// A project taken down leaves a marker behind, so the same project saved
// again stays down.
//
// With -site, it also serves a built docs site (website/dist) the way
// nginx does, for trying the whole thing on one machine.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Limits on one saved project.
const (
	maxBody    = 2 << 20 // the request, JSON escaping included
	maxContent = 1 << 20 // the files' text, all together
	maxFiles   = 200
	maxPath    = 200
	maxTitle   = 120
	maxOpen    = 50
	idLen      = 12 // base32: 60 bits

	// The ingress mostly sees a node's address, not the visitor's
	// (externalTrafficPolicy: Cluster), so "per visitor" can be many
	// people: generous. What bounds a flood is the limit for everyone, and
	// above all the bytes kept a day, so the volume fills slowly if at all.
	savesPerHour = 120 // per visitor (address)
	savesBurst   = 30
	savesPerDay  = 5000
	bytesPerDay  = 100 << 20 // gzipped, as kept
)

var (
	validName = regexp.MustCompile(`^[\w-]{1,64}$`)
	validID   = regexp.MustCompile(`^[a-z2-7]{12}$`)
	idEncode  = base32.StdEncoding.WithPadding(base32.NoPadding)
)

// project is what is kept: anything else in the request is dropped.
type project struct {
	Title string            `json:"title"`
	Open  []string          `json:"open,omitempty"`
	HMI   string            `json:"hmi,omitempty"`
	Name  string            `json:"name,omitempty"` // the example it began as, if any
	Files map[string]string `json:"files"`
}

func (p *project) check() error {
	if p.Files == nil {
		return errors.New("no files")
	}
	if len(p.Files) > maxFiles {
		return fmt.Errorf("more than %d files", maxFiles)
	}
	if _, ok := p.Files["nautilus.yaml"]; !ok {
		return errors.New("not a nautilus project (no nautilus.yaml)")
	}
	size := 0
	for name, text := range p.Files {
		if err := checkPath(name); err != nil {
			return err
		}
		if !utf8.ValidString(text) {
			return fmt.Errorf("%s is not text", name)
		}
		size += len(text)
	}
	if size > maxContent {
		return fmt.Errorf("the project is larger than %d KB", maxContent>>10)
	}
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" {
		p.Title = "shared project"
	}
	if utf8.RuneCountInString(p.Title) > maxTitle {
		return errors.New("the title is too long")
	}
	if len(p.Open) > maxOpen {
		p.Open = p.Open[:maxOpen]
	}
	open := p.Open[:0]
	for _, f := range p.Open {
		if _, ok := p.Files[f]; ok {
			open = append(open, f)
		}
	}
	p.Open = open
	if p.HMI != "" && !validName.MatchString(p.HMI) {
		p.HMI = ""
	}
	if p.Name != "" && !validName.MatchString(p.Name) {
		p.Name = ""
	}
	return nil
}

// checkPath allows a plain relative path: no leading slash, no . or ..
// segments, no backslashes or control characters.
func checkPath(name string) error {
	bad := name == "" || len(name) > maxPath || strings.ContainsAny(name, "\\\x00") ||
		strings.HasPrefix(name, "/") || path.Clean(name) != name
	for _, seg := range strings.Split(name, "/") {
		bad = bad || seg == "." || seg == ".."
	}
	for _, r := range name {
		bad = bad || r < 0x20 || r == 0x7f
	}
	if bad {
		return fmt.Errorf("%q is not a file name a project can have", name)
	}
	return nil
}

type store struct {
	dir string
	mu  sync.Mutex // writes only
}

func (s *store) file(id, ext string) string { return filepath.Join(s.dir, id[:2], id+ext) }

func (s *store) gone(id string) bool {
	_, err := os.Stat(s.file(id, ".gone"))
	return err == nil
}

// put keeps body (canonical JSON) under its id, once. afford is asked
// before a new file is written, with its size.
func (s *store) put(body []byte, afford func(int) bool) (string, error) {
	sum := sha256.Sum256(body)
	id := strings.ToLower(idEncode.EncodeToString(sum[:]))[:idLen]
	if s.gone(id) {
		return id, errGone
	}
	name := s.file(id, ".json.gz")
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(name); err == nil {
		return id, nil
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return "", err
	}
	var gz bytes.Buffer
	w, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	w.Write(body)
	w.Close()
	if !afford(gz.Len()) {
		return id, errFull
	}
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, gz.Bytes(), 0o644); err != nil {
		return "", err
	}
	return id, os.Rename(tmp, name)
}

func (s *store) takeDown(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.file(id, "")), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.file(id, ".gone"), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Remove(s.file(id, ".json.gz")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

var (
	errGone = errors.New("taken down")
	errFull = errors.New("enough saved for today")
)

// limiter: a token bucket per visitor, one for everyone's saves, and one
// for the bytes kept.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	all     bucket
	bytes   bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func (b *bucket) take(now time.Time, perHour, burst, n float64) bool {
	if b.at.IsZero() {
		b.tokens = burst
	} else {
		b.tokens = min(burst, b.tokens+now.Sub(b.at).Hours()*perHour)
	}
	b.at = now
	if b.tokens < n {
		return false
	}
	b.tokens -= n
	return true
}

func (l *limiter) allow(who string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil || len(l.buckets) > 100_000 {
		l.buckets = map[string]*bucket{} // forgetting everyone is fine
	}
	b := l.buckets[who]
	if b == nil {
		b = &bucket{}
		l.buckets[who] = b
	}
	if !b.take(now, savesPerHour, savesBurst, 1) {
		return false
	}
	return l.all.take(now, savesPerDay/24.0, savesPerDay/4, 1)
}

// keep: room today for n more bytes on disk.
func (l *limiter) keep(n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bytes.take(time.Now(), bytesPerDay/24.0, bytesPerDay/4, float64(n))
}

// visitor is who is saving, for the rate limit only (never written down):
// the address ingress-nginx saw, else the connection's.
func visitor(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type server struct {
	store *store
	limit limiter
	admin string
}

func (sv *server) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /playground/api/p", sv.save)
	mux.HandleFunc("GET /playground/api/p/{id}", sv.load)
	mux.HandleFunc("DELETE /playground/api/p/{id}", sv.remove)
	mux.HandleFunc("GET /playground/api/healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok\n") })
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func refuse(w http.ResponseWriter, code int, msg string) {
	reply(w, code, map[string]string{"error": msg})
}

func (sv *server) save(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		refuse(w, http.StatusUnsupportedMediaType, "send the project as application/json")
		return
	}
	if !sv.limit.allow(visitor(r)) {
		w.Header().Set("Retry-After", "600")
		refuse(w, http.StatusTooManyRequests, "too many projects saved; try again in a few minutes")
		return
	}
	var p project
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&p); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			refuse(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the project is larger than %d KB", maxContent>>10))
		} else {
			refuse(w, http.StatusBadRequest, "not a project")
		}
		return
	}
	if err := p.check(); err != nil {
		refuse(w, http.StatusBadRequest, err.Error())
		return
	}
	body, _ := json.Marshal(p) // map keys sorted: the same project, the same bytes
	id, err := sv.store.put(body, sv.limit.keep)
	switch {
	case errors.Is(err, errGone):
		refuse(w, http.StatusGone, "this project was taken down")
	case errors.Is(err, errFull):
		w.Header().Set("Retry-After", "3600")
		refuse(w, http.StatusTooManyRequests, "the playground has saved all it can for now; try again later")
	case err != nil:
		log.Printf("save: %v", err)
		refuse(w, http.StatusInternalServerError, "could not save the project")
	default:
		reply(w, http.StatusCreated, map[string]string{"id": id})
	}
}

func (sv *server) load(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID.MatchString(id) {
		refuse(w, http.StatusNotFound, "no such project")
		return
	}
	if sv.store.gone(id) {
		refuse(w, http.StatusGone, "this project was taken down")
		return
	}
	gz, err := os.ReadFile(sv.store.file(id, ".json.gz"))
	if err != nil {
		refuse(w, http.StatusNotFound, "no such project")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	// It never changes, but can be taken down: the browser asks each time
	// and, while it is still up, gets a 304.
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", `"`+id+`"`)
	h.Set("Vary", "Accept-Encoding")
	if r.Header.Get("If-None-Match") == `"`+id+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		w.Write(gz)
		return
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		refuse(w, http.StatusInternalServerError, "the saved project is damaged")
		return
	}
	io.Copy(w, zr)
}

func (sv *server) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if sv.admin == "" || subtle.ConstantTimeCompare([]byte(auth), []byte(sv.admin)) != 1 {
		refuse(w, http.StatusUnauthorized, "not allowed")
		return
	}
	if !validID.MatchString(id) {
		refuse(w, http.StatusNotFound, "no such project")
		return
	}
	if err := sv.store.takeDown(id); err != nil {
		log.Printf("take down %s: %v", id, err)
		refuse(w, http.StatusInternalServerError, "could not take it down")
		return
	}
	reply(w, http.StatusOK, map[string]string{"id": id, "status": "taken down"})
}

// Every response: never sniffed, never framed elsewhere.
func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// site serves a built docs site as nginx.conf does, enough to try the
// playground and its short links locally.
func site(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	short := regexp.MustCompile(`^/playground/p/[a-z2-7]{12}/?$`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if short.MatchString(r.URL.Path) {
			http.ServeFile(w, r, filepath.Join(dir, "playground", "index.html"))
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dir := flag.String("data", "data", "where saved projects are kept")
	siteDir := flag.String("site", "", "also serve this built docs site (website/dist), for local testing")
	flag.Parse()

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatal(err)
	}
	sv := &server{store: &store{dir: *dir}, admin: os.Getenv("SHARE_ADMIN_TOKEN")}
	mux := http.NewServeMux()
	sv.routes(mux)
	if *siteDir != "" {
		mux.Handle("/", site(*siteDir))
	}
	log.Printf("playground share: %s, projects in %s", *addr, *dir)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           headers(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
