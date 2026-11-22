package checkout

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// A sealed run is a record the controller signs: the run's export, who signed
// it off, the evidence about what the controller was connected to, and where
// in the results file the seal sits. The signature proves the record came from
// this controller's key and was not altered since; the people it names were
// identified by the controller's sign-in, not by a personal certificate. It is
// not an electronic signature in any legal or regulatory sense, and the record
// says so. Anyone who holds the controller's state directory (the results file
// and the key) can forge both; a copy of the file made elsewhere is what makes
// an edit show.

const (
	// KeyFile is the signing key's name in the state directory.
	KeyFile = "checkout.key"
	// SealFormat names the sealed record's shape.
	SealFormat = "nautilus-checkout-seal/1"
	// SealNotice is said in every sealed record and report.
	SealNotice = "A tamper-evident record signed by this controller's key. The signature proves the record came from this controller and was not altered since; the people named were identified by the controller's sign-in, not by a personal certificate. It is not an electronic signature in a legal or regulatory sense."
)

// Seal refusals: the run is not in a state that may be signed off. The reason
// is the error's text.
var ErrSealRefused = errors.New("checkout: this run cannot be sealed")

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrSealRefused}, a...)...)
}

// signKey is the controller's Ed25519 key for sealing.
type signKey struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// Fingerprint is a short name for a public key, to compare out of band.
func Fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:16])
	return h[:8] + "-" + h[8:16] + "-" + h[16:24] + "-" + h[24:]
}

// loadOrCreateKey reads the key (its seed, as hex) or makes one: written to a
// new file, synced, then renamed in, with mode 0600. A key readable by other
// users is refused.
func loadOrCreateKey(path string) (*signKey, error) {
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if fi, serr := os.Stat(path); serr == nil && fi.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%s is mode %o: other users can read the key; chmod 600 it", path, fi.Mode().Perm())
		}
		seed, derr := hex.DecodeString(strings.TrimSpace(string(b)))
		if derr != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s is not a signing key (a %d-byte seed as hex)", path, ed25519.SeedSize)
		}
		priv := ed25519.NewKeyFromSeed(seed)
		return &signKey{priv: priv, pub: priv.Public().(ed25519.PublicKey)}, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, werr := f.WriteString(hex.EncodeToString(seed) + "\n")
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp)
		return nil, werr
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	syncDir(filepath.Dir(path))
	priv := ed25519.NewKeyFromSeed(seed)
	return &signKey{priv: priv, pub: priv.Public().(ed25519.PublicKey)}, nil
}

// KeyFingerprint is the fingerprint of the store's signing key ("" with none).
func (s *Store) KeyFingerprint() string {
	if s.key == nil {
		return ""
	}
	return Fingerprint(s.key.pub)
}

// DriverEvidence and DeviceEvidence say what the controller was connected to
// when it sealed, so a reader can judge whether real field devices were
// checked. The controller derives it; no client supplies it.
type DriverEvidence struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type DeviceEvidence struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint,omitempty"`
	// Class is network, loopback or virtual.
	Class string `json:"class"`
}

type Evidence struct {
	Drivers []DriverEvidence `json:"drivers"`
	Devices []DeviceEvidence `json:"devices"`
}

// SealInput is what a seal asks: who signs, under which rule, and the
// controller's evidence. The store re-checks everything else under its lock.
type SealInput struct {
	Run       string
	Sheet     *Sheet
	By        string
	Principal string
	Note      string
	// Mode is the sign-off rule: two-person (the signer recorded none of the
	// run's results) or single.
	Mode     string
	Evidence Evidence
}

// SealPayload is what the controller signs (compact JSON, field order fixed).
type SealPayload struct {
	Format          string   `json:"format"`
	Notice          string   `json:"notice"`
	Run             string   `json:"run"`
	ExportSha256    string   `json:"exportSha256"`
	Mode            string   `json:"mode"`
	Statement       string   `json:"statement,omitempty"`
	Signer          string   `json:"signer"`
	SignerPrincipal string   `json:"signerPrincipal"`
	Recorders       []string `json:"recorders"`
	Note            string   `json:"note,omitempty"`
	Evidence        Evidence `json:"evidence"`
	SealedAt        int64    `json:"sealedAt"`
	PrevSeq         int64    `json:"prevSeq"`
	PrevHash        string   `json:"prevHash"`
	KeyFingerprint  string   `json:"keyFingerprint"`
}

// SealedRecord is the file a seal makes: the payload as it was signed, the
// signature and public key, and the export the payload's hash covers.
type SealedRecord struct {
	Format    string          `json:"format"`
	Payload   json.RawMessage `json:"payload"`
	Signature string          `json:"signature"`
	PublicKey string          `json:"publicKey"`
	Export    json.RawMessage `json:"export"`
}

func compact(raw []byte) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// PayloadSha256 is the SHA-256 of a sealed payload (its compact JSON), as hex:
// what the seal entry in the results file and the controller's audit entry for
// the seal both carry, so the three can be matched.
func PayloadSha256(payload []byte) string {
	c, err := compact(payload)
	if err != nil {
		c = payload
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:])
}

// SealedPath is where a run's sealed record is kept, beside the results file.
func (s *Store) SealedPath(run string) string {
	return filepath.Join(filepath.Dir(s.path), "checkout-sealed-"+FileStem(run)[len("checkout-"):]+".json")
}

// ReadSealed returns a sealed run's record as it was written.
func (s *Store) ReadSealed(run string) ([]byte, error) {
	s.mu.Lock()
	r := s.runs[run]
	sealed := r != nil && r.State == StateSealed
	s.mu.Unlock()
	if !sealed {
		return nil, ErrNoRun
	}
	return os.ReadFile(s.SealedPath(run))
}

// Seal signs off a run. Everything is decided under the store's lock, against
// the run as it stands: the caller cannot seal a rehearsal, a run begun on
// another sheet than the controller's, one with a force standing, one with a
// point not yet checked or failing, or (two-person) one the signer recorded
// results in. The record is written and synced before the seal entry is
// appended, so a seal in the file always has its record beside it.
func (s *Store) Seal(in SealInput) (SealedRecord, error) {
	if in.Principal == "" || in.By == "" {
		return SealedRecord{}, invalid("a seal names the person who signs it off")
	}
	if in.Mode != "two-person" && in.Mode != "single" {
		return SealedRecord{}, invalid("the sign-off rule is two-person or single")
	}
	if utf8.RuneCountInString(in.Note) > MaxNote {
		return SealedRecord{}, invalid("the note is longer than %d characters", MaxNote)
	}
	if in.Sheet == nil {
		return SealedRecord{}, errors.New("checkout: no sheet to seal against")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == nil {
		return SealedRecord{}, errors.New("checkout: no signing key")
	}
	r := s.runs[in.Run]
	if r == nil {
		return SealedRecord{}, ErrNoRun
	}
	if r.State != StateOpen {
		return SealedRecord{}, ErrNotOpen
	}
	if r.Rehearsal {
		return SealedRecord{}, refuse("it is a rehearsal run (against virtual devices), and a rehearsal can never be signed off")
	}
	if r.SheetHash != in.Sheet.Hash() {
		return SealedRecord{}, refuse("the sheet changed since this run began (checkout.yaml was edited and deployed): a seal signs the results against one sheet, so abandon the run and begin again")
	}
	if r.standing != nil {
		return SealedRecord{}, refuse("a checkout force is standing on %s: let it go first", r.standing.rec.Point)
	}
	results := s.resultsLocked(in.Run)
	latest := map[string]Result{}
	recorders := map[string]bool{}
	for _, res := range results {
		latest[res.Point] = res // results are in sequence order: the last is the latest
		who := res.Principal
		if who == "" {
			who = res.By
		}
		recorders[who] = true
	}
	var todo, failing []string
	for _, p := range in.Sheet.Points {
		switch l, ok := latest[p.ID]; {
		case !ok:
			todo = append(todo, p.ID)
		case l.Outcome == Fail:
			failing = append(failing, p.ID)
		}
	}
	if len(todo) > 0 {
		return SealedRecord{}, refuse("%d point(s) have no result yet: %s", len(todo), listIDs(todo))
	}
	if len(failing) > 0 {
		return SealedRecord{}, refuse("%d point(s) are failing: %s (a later pass supersedes a fail; both stay in the file)", len(failing), listIDs(failing))
	}
	statement := ""
	if recorders[in.Principal] {
		if in.Mode == "two-person" {
			return SealedRecord{}, refuse("%s recorded results in this run, and the sign-off rule is two-person: someone who recorded none of them signs it off", in.By)
		}
		statement = "single-person sign-off: the signer also recorded results in this run"
	}
	var rec []string
	for p := range recorders {
		rec = append(rec, p)
	}
	sort.Strings(rec)

	ex := BuildExport(r.view(), in.Sheet, results)
	ex.Sealed = true
	ex.Status = "SEALED: signed off by " + in.By + ", see the signature of this record"
	exb, err := ex.JSON()
	if err != nil {
		return SealedRecord{}, err
	}
	exc, err := compact(exb)
	if err != nil {
		return SealedRecord{}, err
	}
	sum := sha256.Sum256(exc)
	pl := SealPayload{Format: SealFormat, Notice: SealNotice, Run: in.Run, ExportSha256: hex.EncodeToString(sum[:]), Mode: in.Mode, Statement: statement,
		Signer: in.By, SignerPrincipal: in.Principal, Recorders: rec, Note: in.Note, Evidence: in.Evidence,
		SealedAt: s.now().UnixMilli(), PrevSeq: s.seq, PrevHash: s.prev, KeyFingerprint: Fingerprint(s.key.pub)}
	if pl.Evidence.Drivers == nil {
		pl.Evidence.Drivers = []DriverEvidence{}
	}
	if pl.Evidence.Devices == nil {
		pl.Evidence.Devices = []DeviceEvidence{}
	}
	plb, err := json.Marshal(pl)
	if err != nil {
		return SealedRecord{}, err
	}
	sig := ed25519.Sign(s.key.priv, plb)
	psum := sha256.Sum256(plb)
	sr := SealedRecord{Format: SealFormat, Payload: plb, Signature: base64.StdEncoding.EncodeToString(sig),
		PublicKey: base64.StdEncoding.EncodeToString(s.key.pub), Export: exc}
	body, err := json.MarshalIndent(sr, "", "  ")
	if err != nil {
		return SealedRecord{}, err
	}
	if err := writeFileSynced(s.SealedPath(in.Run), append(body, '\n')); err != nil {
		return SealedRecord{}, fmt.Errorf("checkout: could not keep the sealed record: %w", err)
	}
	e := entry{Kind: kindSeal, Run: in.Run, SealSha: hex.EncodeToString(psum[:]), Sig: sr.Signature, Note: in.Note, By: in.By, Principal: in.Principal}
	if err := s.append(e); err != nil {
		os.Remove(s.SealedPath(in.Run)) // nothing was sealed: leave no record that says so
		return SealedRecord{}, err
	}
	return sr, nil
}

func listIDs(ids []string) string {
	if len(ids) > 8 {
		return strings.Join(ids[:8], ", ") + fmt.Sprintf(" and %d more", len(ids)-8)
	}
	return strings.Join(ids, ", ")
}

// writeFileSynced writes a file by temp and rename, synced, mode 0600.
func writeFileSynced(path string, b []byte) error {
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp)
		return werr
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}
