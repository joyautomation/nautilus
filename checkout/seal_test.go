package checkout

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sealedStore is a store with a live run in which alice passed every point of
// sheet().
func sealedStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	s, p := openStore(t)
	run, err := s.Begin(false, sheet().Hash(), "Test", "alice", "user:alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, pt := range sheet().Points {
		if _, err := s.Record(run.ID, pt.ID, Pass, "", 1.5, "good", "alice", "user:alice"); err != nil {
			t.Fatal(err)
		}
	}
	return s, p, run.ID
}

func sealIn(run, who string) SealInput {
	return SealInput{Run: run, Sheet: sheet(), By: who, Principal: "user:" + who, Mode: "two-person",
		Evidence: Evidence{Drivers: []DriverEvidence{{Name: "plc", Kind: "modbus"}}}}
}

func TestSealSignsAndVerifies(t *testing.T) {
	s, p, run := sealedStore(t)
	sr, err := s.Seal(sealIn(run, "bob"))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(run)
	if got.State != StateSealed || got.SealedBy != "bob" || got.SealSeq == 0 {
		t.Errorf("run after the seal: %+v", got)
	}
	file, err := s.ReadSealed(run)
	if err != nil {
		t.Fatal(err)
	}
	chain, _ := os.ReadFile(p)
	rep, err := Verify(file, chain)
	if err != nil || !rep.ChainChecked || rep.Signer != "bob" || rep.Results != 3 || rep.Fingerprint != s.KeyFingerprint() {
		t.Fatalf("verify: %+v, %v", rep, err)
	}
	if _, err := Verify(file, nil); err != nil {
		t.Errorf("verify without the results file: %v", err)
	}
	for _, want := range []string{SealNotice, `"sealed": true`, "SEALED"} {
		if !strings.Contains(string(file), want) && !strings.Contains(string(sr.Export), strings.Trim(want, `"`)) && !strings.Contains(string(sr.Payload), strings.Trim(want, `"`)) {
			t.Errorf("the sealed record does not say %q", want)
		}
	}
	// a sealed run takes nothing more, and a new run may begin
	if _, err := s.Record(run, "XV101", Fail, "late", nil, "", "alice", "user:alice"); !errors.Is(err, ErrNotOpen) {
		t.Errorf("a result after the seal: %v", err)
	}
	if _, err := s.Seal(sealIn(run, "carol")); !errors.Is(err, ErrNotOpen) {
		t.Errorf("a second seal: %v", err)
	}
	if _, err := s.Begin(false, sheet().Hash(), "", "alice", "user:alice"); err != nil {
		t.Errorf("a new run after a seal: %v", err)
	}
	// and it survives a restart
	s.Close()
	s2, err := Open(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if g, _ := s2.Get(run); g.State != StateSealed {
		t.Errorf("after reopening: %+v", g)
	}
	if s2.KeyFingerprint() != s.KeyFingerprint() {
		t.Errorf("the key changed across a restart")
	}
}

func TestSealRefusals(t *testing.T) {
	type tc struct {
		name string
		set  func(t *testing.T, s *Store, run string, in *SealInput)
		want string
	}
	for _, c := range []tc{
		{"the signer recorded results (two-person)", func(t *testing.T, s *Store, run string, in *SealInput) { in.By, in.Principal = "alice", "user:alice" }, "recorded results"},
		{"a sheet that changed", func(t *testing.T, s *Store, run string, in *SealInput) {
			sh := sheet()
			sh.Points[0].Expect = "edited"
			in.Sheet = sh
		}, "sheet changed"},
		{"a point with no result", func(t *testing.T, s *Store, run string, in *SealInput) {
			sh := sheet()
			sh.Points = append(sh.Points, Point{ID: "NEW", Tag: "n", Kind: DI, Method: Observe})
			in.Sheet = sh
			// the run was begun against the old sheet: that refusal comes first, so begin a run on the new one
			s.Abandon(run, "x", "alice", "user:alice")
			r2, _ := s.Begin(false, sh.Hash(), "", "alice", "user:alice")
			in.Run = r2.ID
			s.Record(r2.ID, "XV101", Pass, "", nil, "", "alice", "user:alice")
		}, "no result yet: LSH101, FIT201, NEW"},
		{"a failing point", func(t *testing.T, s *Store, run string, in *SealInput) {
			s.Record(run, "LSH101", Fail, "stuck", nil, "", "alice", "user:alice")
		}, "failing: LSH101"},
		{"a standing force", func(t *testing.T, s *Store, run string, in *SealInput) {
			if _, err := s.StartForce(run, "XV101", true, 30, "alice", "user:alice"); err != nil {
				t.Fatal(err)
			}
		}, "force is standing"},
		{"no signer", func(t *testing.T, s *Store, run string, in *SealInput) { in.By, in.Principal = "", "" }, "names the person"},
		{"a bad rule", func(t *testing.T, s *Store, run string, in *SealInput) { in.Mode = "anyone" }, "two-person or single"},
		{"an unknown run", func(t *testing.T, s *Store, run string, in *SealInput) { in.Run = "nope" }, "no such run"},
	} {
		s, p, run := sealedStore(t)
		in := sealIn(run, "bob")
		c.set(t, s, run, &in)
		before, _ := os.ReadFile(p)
		_, err := s.Seal(in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		after, _ := os.ReadFile(p)
		if !bytes.Equal(before, after) {
			t.Errorf("%s: a refused seal wrote to the results file", c.name)
		}
		if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(p), "checkout-sealed-*")); len(matches) != 0 {
			t.Errorf("%s: a refused seal left a record: %v", c.name, matches)
		}
		s.Close()
	}
}

func TestSealRefusesARehearsal(t *testing.T) {
	s, _ := openStore(t)
	defer s.Close()
	run, _ := s.Begin(true, sheet().Hash(), "", "alice", "user:alice")
	for _, pt := range sheet().Points {
		s.Record(run.ID, pt.ID, Pass, "", nil, "", "alice", "user:alice")
	}
	if _, err := s.Seal(sealIn(run.ID, "bob")); err == nil || !strings.Contains(err.Error(), "rehearsal") {
		t.Errorf("a rehearsal: %v", err)
	}
}

// A later pass supersedes a fail, both stay in the file, and both are in the
// sealed export.
func TestSealKeepsTheFailThatAPassSuperseded(t *testing.T) {
	s, _, run := sealedStore(t)
	defer s.Close()
	s.Record(run, "FIT201", Fail, "reads high", 9.9, "good", "alice", "user:alice")
	s.Record(run, "FIT201", Pass, "recalibrated", 1.5, "good", "alice", "user:alice")
	sr, err := s.Seal(sealIn(run, "bob"))
	if err != nil {
		t.Fatal(err)
	}
	var ex Export
	json.Unmarshal(sr.Export, &ex)
	for _, p := range ex.Points {
		if p.ID == "FIT201" && (len(p.Results) != 3 || p.Results[1].Outcome != Fail || p.Status != Pass) {
			t.Errorf("FIT201 in the sealed export: %+v", p)
		}
	}
}

func TestSingleModeSaysSoInTheRecord(t *testing.T) {
	s, _, run := sealedStore(t)
	defer s.Close()
	in := sealIn(run, "alice")
	in.Mode = "single"
	sr, err := s.Seal(in)
	if err != nil {
		t.Fatal(err)
	}
	var pl SealPayload
	json.Unmarshal(sr.Payload, &pl)
	if pl.Mode != "single" || !strings.Contains(pl.Statement, "single-person") || pl.SignerPrincipal != "user:alice" {
		t.Errorf("payload %+v", pl)
	}
	// single, but the signer recorded none: no statement of it
	s2, _, run2 := sealedStore(t)
	defer s2.Close()
	in = sealIn(run2, "bob")
	in.Mode = "single"
	sr, _ = s2.Seal(in)
	pl = SealPayload{}
	json.Unmarshal(sr.Payload, &pl)
	if pl.Statement != "" {
		t.Errorf("a statement with nothing to say: %q", pl.Statement)
	}
}

// Every way of altering the record or the file shows.
func TestVerifyCatchesTampering(t *testing.T) {
	s, p, run := sealedStore(t)
	defer s.Close()
	if _, err := s.Seal(sealIn(run, "bob")); err != nil {
		t.Fatal(err)
	}
	file, _ := s.ReadSealed(run)
	chain, _ := os.ReadFile(p)
	var sr SealedRecord
	json.Unmarshal(file, &sr)
	enc := func(m func(*SealedRecord)) []byte {
		x := sr
		x.Export, _ = compact(x.Export) // the file is indented: edit the compact form, as a forger would
		x.Payload, _ = compact(x.Payload)
		m(&x)
		b, _ := json.Marshal(x)
		return b
	}
	for _, c := range []struct {
		name  string
		file  []byte
		chain []byte
		want  string
	}{
		{"an edited result in the export", enc(func(x *SealedRecord) {
			x.Export = json.RawMessage(strings.Replace(string(x.Export), `"outcome":"pass"`, `"outcome":"fail"`, 1))
		}), chain, "export does not match"},
		{"an edited payload (the signer)", enc(func(x *SealedRecord) {
			x.Payload = json.RawMessage(strings.Replace(string(x.Payload), `"signer":"bob"`, `"signer":"eve"`, 1))
		}), chain, "signature does not match"},
		{"a swapped signature", enc(func(x *SealedRecord) { x.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64)) }), chain, "signature does not match"},
		{"another key", enc(func(x *SealedRecord) { x.PublicKey = base64.StdEncoding.EncodeToString(make([]byte, 32)) }), chain, "signature"},
		{"not a record", []byte(`{"format":"x"}`), chain, "not a sealed record"},
		{"an edited line in the results file", file, bytes.Replace(chain, []byte(`"outcome":"pass"`), []byte(`"outcome":"fail"`), 1), "chain"},
		{"a line removed from the results file", file, dropLine(chain, 2), "chain"},
		{"the seal entry removed (a truncated file)", file, dropLast(chain), "no seal entry"},
		{"an empty results file", file, []byte{}, "no seal entry"},
	} {
		if _, err := Verify(c.file, c.chain); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	// the export carrying a result the file does not hold (a made-up result, re-signed is impossible without the key, so test the chain check on its own)
	s3, p3, run3 := sealedStore(t)
	defer s3.Close()
	s3.Seal(sealIn(run3, "bob"))
	otherFile, _ := s3.ReadSealed(run3)
	otherChain, _ := os.ReadFile(p3)
	if _, err := Verify(file, otherChain); err == nil {
		t.Errorf("a record verified against another controller's results file")
	}
	if _, err := Verify(otherFile, chain); err == nil {
		t.Errorf("another record verified against this results file")
	}
}

func dropLine(b []byte, n int) []byte {
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	lines = append(lines[:n], lines[n+1:]...)
	return append(bytes.Join(lines, []byte("\n")), '\n')
}

func dropLast(b []byte) []byte {
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	return append(bytes.Join(lines[:len(lines)-1], []byte("\n")), '\n')
}

// The signing key: made at Open, kept, mode 0600, refused when others can read it.
func TestTheSigningKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "checkout.jsonl")
	s, err := Open(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	fp := s.KeyFingerprint()
	s.Close()
	if fi, err := os.Stat(filepath.Join(dir, KeyFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	s, err = Open(p, nil)
	if err != nil || s.KeyFingerprint() != fp {
		t.Fatalf("reopen: %v, fingerprint %q want %q", err, s.KeyFingerprint(), fp)
	}
	s.Close()
	os.Chmod(filepath.Join(dir, KeyFile), 0o644)
	if _, err := Open(p, nil); err == nil || !strings.Contains(err.Error(), "other users can read") {
		t.Errorf("a world-readable key: %v", err)
	}
	os.Chmod(filepath.Join(dir, KeyFile), 0o600)
	os.WriteFile(filepath.Join(dir, KeyFile), []byte("not a key\n"), 0o600)
	if _, err := Open(p, nil); err == nil || !strings.Contains(err.Error(), "not a signing key") {
		t.Errorf("a damaged key: %v", err)
	}
}

// A seal whose entry cannot be written leaves no record, and the run stays open.
func TestAFailedSealLeavesNothingSealed(t *testing.T) {
	s, p, run := sealedStore(t)
	defer s.Close()
	ff := &failFile{appendFile: s.f, syncErr: true}
	s.f = ff
	if _, err := s.Seal(sealIn(run, "bob")); err == nil {
		t.Fatal("the seal was acknowledged")
	}
	if g, _ := s.Get(run); g.State != StateOpen {
		t.Errorf("run after a failed seal: %+v", g)
	}
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(p), "checkout-sealed-*")); len(matches) != 0 {
		t.Errorf("a failed seal left a record: %v", matches)
	}
	if _, err := s.Seal(sealIn(run, "bob")); err != nil {
		t.Errorf("the seal after the disk recovered: %v", err)
	}
}

// A record that claims its seal sits somewhere else in the results file than
// it does is refused, even though the signature is the key's and the chain is
// whole (the controller itself would have to have signed it).
func TestVerifyChecksWhereTheSealSits(t *testing.T) {
	s, p, run := sealedStore(t)
	defer s.Close()
	if _, err := s.Seal(sealIn(run, "bob")); err != nil {
		t.Fatal(err)
	}
	file, _ := s.ReadSealed(run)
	chain, _ := os.ReadFile(p)
	var sr SealedRecord
	json.Unmarshal(file, &sr)
	var pl SealPayload
	plc, _ := compact(sr.Payload)
	json.Unmarshal(plc, &pl)
	pl.PrevSeq++ // claims a later place
	b, _ := json.Marshal(pl)
	sr.Payload = b
	sr.Signature = base64.StdEncoding.EncodeToString(ed25519Sign(s.key, b))
	forged, _ := json.Marshal(sr)
	// the file's seal entry names this payload (its last line, so the chain stays whole)
	lines := bytes.Split(bytes.TrimRight(chain, "\n"), []byte("\n"))
	var en entry
	json.Unmarshal(lines[len(lines)-1], &en)
	en.SealSha = sha256hex(b)
	last, _ := json.Marshal(en)
	lines[len(lines)-1] = last
	chain2 := append(bytes.Join(lines, []byte("\n")), '\n')
	if _, err := Verify(forged, chain2); err == nil || !strings.Contains(err.Error(), "not where the signed payload says") {
		t.Errorf("a seal claiming another place: %v", err)
	}
}

func ed25519Sign(k *signKey, b []byte) []byte { return ed25519.Sign(k.priv, b) }
func sha256hex(b []byte) string               { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
