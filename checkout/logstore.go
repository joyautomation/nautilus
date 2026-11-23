package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// The checkout on a redundant pair (#719 slice 5). The runs live in the
// operator-intent log, not in a file: every entry is proposed through the log,
// and applied, at commit, by every replica, with the one apply the file store
// uses. The store is then the same on both controllers because it is the same
// function of the same sequence.
//
// What follows from that:
//   - apply is deterministic: it uses the log entry's own sequence number and
//     leader time, never the clock, and an entry it refuses (a second run
//     begun on the other controller at the same moment) is skipped by every
//     replica alike and remembered by sequence, so the proposer is told;
//   - the hash chain of the results file is derived: seq and prev are counted
//     over the entries apply accepted, in committed order, so every replica can
//     write the same canonical checkout.jsonl (ExportChain) for
//     naut checkout verify --chain;
//   - a seal is signed by the controller that proposes it, over the chain as it
//     stood; if another entry committed first, apply refuses it (errSealRaced)
//     and the signer builds it again;
//   - a sealed record is not a file: the entry carries the signed payload and
//     the key, and a replica rebuilds the export from its own copy of the
//     entries, checking it against the payload's hash;
//   - a force does not fail over: the controller that took over ends a standing
//     one with InterruptStanding.

// ErrNoMajority is a write the operator-intent log could not take: neither the
// other controller nor the witness answers, so nothing is recorded (and
// nothing is queued).
var ErrNoMajority = errors.New("checkout: the operator-intent log has no majority: neither the other controller nor the witness answers, so nothing can be recorded until one of them returns")

// ErrUnknown is a write whose outcome is not known: it was proposed and no
// answer came back in time. It may have been recorded.
var ErrUnknown = errors.New("checkout: the operator-intent log did not answer: this may have been recorded, so look at the run before doing it again")

// errSealRaced: another entry committed between a seal being signed and being
// applied.
var errSealRaced = errors.New("checkout: the run changed while it was being sealed")

// proposeWait bounds one proposal's wait for its commit.
const proposeWait = 10 * time.Second

// OpenLog is a store for a pair: no results file, the entries come from the
// log. dir is the state directory, for this controller's signing key.
func OpenLog(dir string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	s := &Store{now: now, runs: map[string]*Run{}, rejected: map[uint64]error{}}
	var created bool
	var err error
	if s.key, created, err = loadOrCreateKey(filepath.Join(dir, KeyFile)); err != nil {
		s.key, s.keyErr = nil, err
	}
	_ = created
	return s, nil
}

// AttachLog sets where this replica's entries are proposed: a function that
// returns once the entry is committed and applied here, with its sequence
// number in the log, or ErrNoMajority / ErrUnknown / ErrInvalid.
func (s *Store) AttachLog(propose func(ctx context.Context, by string, body []byte) (uint64, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.propose = propose
}

// IsLog says the store keeps its runs in the log.
func (s *Store) IsLog() bool { return s.propose != nil }

// SetSheet gives a replica the sheet it rebuilds a sealed export from: the
// one its run was begun against, or the record cannot be served here.
func (s *Store) SetSheet(sh *Sheet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sheet = sh
}

// Reset empties the store: the log's history it was built from was replaced
// (a loss accepted on the other copy), so the new history's entries are
// exactly what it holds.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs, s.ord = map[string]*Run{}, nil
	s.seq, s.prev, s.lines = 0, "", nil
	s.rejected = map[uint64]error{}
	s.keys = nil
}

// AnnounceKey puts this controller's signing key fingerprint in the log under
// its name, so each controller's page shows both keys of the pair (a seal is
// signed by the controller it is made on). It does nothing and returns nil on a
// store with no key, and an error when the log cannot take it (it is asked
// again).
func (s *Store) AnnounceKey(controller string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == nil || s.propose == nil {
		return nil
	}
	if s.keys[controller] == Fingerprint(s.key.pub) {
		return nil
	}
	return s.append(entry{Kind: kindKey, By: controller, Note: Fingerprint(s.key.pub)})
}

// KeyFingerprints is each controller's signing key as it announced it (a pair).
func (s *Store) KeyFingerprints() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.keys) == 0 {
		return nil
	}
	out := make(map[string]string, len(s.keys))
	for k, v := range s.keys {
		out[k] = v
	}
	return out
}

// ApplyLogged folds one committed log entry into the store, on every replica:
// seq and ts are the log's. An entry it cannot apply is skipped, the same on
// every replica, and its reason kept for the proposer. It never returns an
// error for that: a committed entry the log must keep is not a failure of the
// log.
func (s *Store) ApplyLogged(seq uint64, ts int64, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var e entry
	if err := json.Unmarshal(body, &e); err != nil {
		s.reject(seq, fmt.Errorf("%w: the entry is not readable: %v", ErrInvalid, err))
		return nil
	}
	if e.Kind == kindSeal && len(e.Payload) == 0 {
		// a seal with no signed payload would leave a sealed run no replica
		// can serve a record for
		s.reject(seq, fmt.Errorf("%w: a seal carries its signed payload", ErrInvalid))
		return nil
	}
	e.Seq, e.At, e.Prev = s.seq+1, ts, s.prev
	line, err := json.Marshal(e)
	if err != nil {
		s.reject(seq, err)
		return nil
	}
	if err := s.apply(e); err != nil {
		s.reject(seq, err)
		return nil
	}
	s.seq, s.prev = e.Seq, lineHash(line)
	s.lines = append(s.lines, line)
	return nil
}

func (s *Store) reject(seq uint64, err error) {
	if len(s.rejected) >= 256 {
		for k := range s.rejected { // old ones: a proposer asks within seconds
			if k+256 < seq {
				delete(s.rejected, k)
			}
		}
	}
	s.rejected[seq] = err
}

// appendLogged proposes e and waits for it to commit. Called with s.mu held,
// it lets go of it while the entry commits (the commit's apply takes it), so
// what was checked before the call may have changed: the decision is apply's.
func (s *Store) appendLogged(e entry) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(body) > MaxLine && e.Seen != nil {
		e.Seen = "(too large to record)"
		if body, err = json.Marshal(e); err != nil {
			return err
		}
	}
	if len(body) > MaxLine {
		return invalid("the entry is larger than %d bytes", MaxLine)
	}
	propose := s.propose
	s.mu.Unlock()
	s.propMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), proposeWait)
	seq, err := propose(ctx, e.By, body)
	cancel()
	s.propMu.Unlock()
	s.mu.Lock()
	if err != nil {
		return err
	}
	if rej := s.rejected[seq]; rej != nil {
		delete(s.rejected, seq)
		return rej
	}
	return nil
}

// ExportChain is the canonical results file of a pair: the entries apply
// accepted, as lines, in committed order, with seq and prev counted over them.
// Every replica writes the same bytes, so naut checkout verify --chain takes
// either one's.
func (s *Store) ExportChain() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []byte
	for _, l := range s.lines {
		out = append(append(out, l...), '\n')
	}
	return out
}

// sealedExportBytes is the export a seal signs, from the run as it stood
// before the seal: the same function for the signer and for a replica that
// rebuilds the record.
func sealedExportBytes(view Run, sh *Sheet, results []Result, signer string) ([]byte, error) {
	ex := BuildExport(view, sh, results)
	ex.Sealed = true
	ex.Status = "SEALED: signed off by " + signer + ", see the signature of this record"
	// the bytes the signature covers: encoded with Go's default escaping of <, >
	// and &, which is stable through the indented file the record is written as
	// (an encoder that escaped differently would hash one way and verify another)
	return json.Marshal(ex)
}

// readSealedLogged rebuilds a sealed run's record from the entries this
// replica holds, and checks it against the payload's hash: a replica whose
// copy differs (another sheet, a different history) refuses to serve it.
func (s *Store) readSealedLogged(run string) ([]byte, error) {
	s.mu.Lock()
	r := s.runs[run]
	if r == nil || r.State != StateSealed || len(r.sealPayload) == 0 {
		s.mu.Unlock()
		return nil, ErrNoRun
	}
	view := r.view()
	payload, sig, key := r.sealPayload, r.sealSig, r.sealKey
	results := s.resultsLocked(run)
	sh := s.sheet
	s.mu.Unlock()
	if sh == nil || sh.Hash() != view.SheetHash {
		return nil, errors.New("checkout: this controller does not hold the sheet this run was sealed against (checkout.yaml differs between the controllers), so it cannot rebuild the signed record: ask the other controller")
	}
	view.State, view.SealedAt, view.SealedBy, view.SealSeq = StateOpen, 0, "", 0
	var pl SealPayload
	if err := json.Unmarshal(payload, &pl); err != nil {
		return nil, err
	}
	exc, err := sealedExportBytes(view, sh, results, pl.Signer)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(exc); hex.EncodeToString(sum[:]) != pl.ExportSha256 {
		return nil, errors.New("checkout: the record this controller rebuilds from its copy of the log does not match what was signed: the controllers' histories differ")
	}
	sr := SealedRecord{Format: SealFormat, Payload: payload, Signature: sig, PublicKey: key, Export: exc}
	b, err := json.MarshalIndent(sr, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// sealedKey is the public key of a sealed run's signer (log mode), for the
// pair's page.
func b64pub(k *signKey) string { return base64.StdEncoding.EncodeToString(k.pub) }
