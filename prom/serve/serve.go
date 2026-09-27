// Package serve is the Prometheus driver's in-repo stand-in: it answers
// `/metrics` with a recorded (or live-mutated) exposition body on
// 127.0.0.1:0, so the driver tests and `naut prometheus serve` need no real
// node_exporter. It is the same role modbus/slave and snmp/agent play for
// their protocols (docs/design/it-drivers.md §9.2).
//
// Beyond replaying a fixed body, Server can Bump a counter series between
// polls — the rate tests' "a counter step across two polls becomes the
// right rate" needs a body that actually changes, not just a static replay
// — and Ramp can drift every counter-typed series continuously, for a
// bench that looks alive under `naut run` with no exporter at all.
package serve

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/prom"
)

// Server replays one mutable scrape body over HTTP.
type Server struct {
	mu      sync.Mutex
	fam     map[string]prom.Family
	samples []prom.Sample

	ln  net.Listener
	srv *http.Server
	wg  sync.WaitGroup

	stopRamp func()
}

// New parses body once and builds a Server ready to Start. body must be a
// well-formed exposition-format scrape (ParseText's rules).
func New(body []byte) (*Server, error) {
	sc, err := prom.ParseText(body)
	if err != nil {
		return nil, fmt.Errorf("serve: %w", err)
	}
	fam := sc.Families
	if fam == nil {
		fam = map[string]prom.Family{}
	}
	return &Server{fam: fam, samples: sc.Samples}, nil
}

// Start listens on listen ("127.0.0.1:0" for an ephemeral port) and begins
// answering `/metrics`. It returns once the listener is up; Addr reports
// where.
func (s *Server) Start(listen string) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	s.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", s.handleMetrics)
	s.srv = &http.Server{Handler: mux}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		_ = s.srv.Serve(ln)
	}()
	return nil
}

// Addr is the listener's address ("127.0.0.1:54321").
func (s *Server) Addr() string { return s.ln.Addr().String() }

// URL is the full scrape URL a manifest source would use.
func (s *Server) URL() string { return "http://" + s.Addr() + "/metrics" }

// Stop ends the listener and any Ramp, and waits for the serve goroutine.
func (s *Server) Stop() {
	if s.stopRamp != nil {
		s.stopRamp()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
	s.wg.Wait()
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	body := s.render()
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write(body)
}

// Bump adds delta to the first series matching metric+labels (an exact
// match on the given labels; labels may be a subset, as a binding's own
// Labels: are). It is how a driver test advances a counter between two
// polls to check the rate that results — see prom/driver_test.go.
func (s *Server) Bump(metric string, labels map[string]string, delta float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, sm := range s.samples {
		if sm.Name != metric {
			continue
		}
		match := true
		for k, v := range labels {
			if sm.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			s.samples[i].Value += delta
			return nil
		}
	}
	return fmt.Errorf("serve: no series %s%v to bump", metric, labels)
}

// Set replaces the first matching series' value outright (a gauge, or
// seeding a counter to a known starting point before a test's first poll).
func (s *Server) Set(metric string, labels map[string]string, value float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, sm := range s.samples {
		if sm.Name != metric {
			continue
		}
		match := true
		for k, v := range labels {
			if sm.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			s.samples[i].Value = value
			return nil
		}
	}
	return fmt.Errorf("serve: no series %s%v to set", metric, labels)
}

// RemoveAll deletes every series of metric — a exporter release renaming or
// dropping a metric entirely, for the "a bound metric absent from the
// scrape" and "an expr selector matching nothing" test paths. Unlike Bump/
// Set, this changes what NEXT renders, not what the driver has already
// read, so a realistic test drives one or more clean polls first and only
// then calls RemoveAll to simulate the drift happening mid-run.
func (s *Server) RemoveAll(metric string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.samples[:0]
	for _, sm := range s.samples {
		if sm.Name != metric {
			kept = append(kept, sm)
		}
	}
	s.samples = kept
}

// Ramp drifts every counter-typed series upward by amountPerSec, every
// interval, so a bench started with `naut prometheus serve` looks alive
// (rates move) with no real exporter behind it — modbus serve's `--ramp`,
// the same idea. Calling Ramp again replaces the previous one.
func (s *Server) Ramp(interval time.Duration, amountPerSec float64) {
	if s.stopRamp != nil {
		s.stopRamp()
	}
	done := make(chan struct{})
	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-done:
				ticker.Stop()
				return
			case <-ticker.C:
				s.mu.Lock()
				for i := range s.samples {
					if s.fam[s.samples[i].Name].Type == "counter" {
						s.samples[i].Value += amountPerSec * interval.Seconds()
					}
				}
				s.mu.Unlock()
			}
		}
	}()
	s.stopRamp = func() { close(done) }
}

// render renders the current samples back to exposition text via prom's own
// RenderText (families sorted by name, deterministic regardless of Bump/Set
// mutation order). Caller holds s.mu.
func (s *Server) render() []byte {
	return prom.RenderText(prom.Scrape{Samples: s.samples, Families: s.fam})
}
