package checkout

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// VerifyReport is what Verify found out about a sealed record.
type VerifyReport struct {
	Run         string
	Signer      string
	Mode        string
	Statement   string
	SealedAt    int64
	Fingerprint string
	Results     int
	// ChainChecked says the results file was read: the seal entry is in it, at
	// the place the payload names, and the export holds exactly the results
	// the file holds for the run.
	ChainChecked bool
}

// Verify checks a sealed record: the public key is the one the payload names,
// the signature is the payload's, and the export is the one the payload
// hashes. With the results file (chain, may be nil) it also checks the file's
// own chain, that it holds the seal entry for this payload at the place the
// payload names, and that the export's results are the file's results for the
// run, entry for entry. It says nothing about who holds the key: compare the
// fingerprint with one you recorded from the controller.
func Verify(record, chain []byte) (VerifyReport, error) {
	var sr SealedRecord
	if err := json.Unmarshal(record, &sr); err != nil {
		return VerifyReport{}, fmt.Errorf("not a sealed record: %w", err)
	}
	if sr.Format != SealFormat {
		return VerifyReport{}, fmt.Errorf("not a sealed record: format %q, want %q", sr.Format, SealFormat)
	}
	pub, err := base64.StdEncoding.DecodeString(sr.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return VerifyReport{}, errors.New("the public key is not an Ed25519 key")
	}
	sig, err := base64.StdEncoding.DecodeString(sr.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return VerifyReport{}, errors.New("the signature is not an Ed25519 signature")
	}
	pl, err := compact(sr.Payload)
	if err != nil {
		return VerifyReport{}, fmt.Errorf("the payload is not JSON: %w", err)
	}
	if !ed25519.Verify(pub, pl, sig) {
		return VerifyReport{}, errors.New("the signature does not match the payload: the record was altered, or was not signed by this key")
	}
	var p SealPayload
	if err := json.Unmarshal(pl, &p); err != nil {
		return VerifyReport{}, fmt.Errorf("the payload: %w", err)
	}
	if p.Format != SealFormat {
		return VerifyReport{}, fmt.Errorf("the payload's format is %q", p.Format)
	}
	if fp := Fingerprint(pub); p.KeyFingerprint != fp {
		return VerifyReport{}, fmt.Errorf("the payload names key %s but the record carries key %s", p.KeyFingerprint, fp)
	}
	ex, err := compact(sr.Export)
	if err != nil {
		return VerifyReport{}, fmt.Errorf("the export is not JSON: %w", err)
	}
	if sum := sha256.Sum256(ex); hex.EncodeToString(sum[:]) != p.ExportSha256 {
		return VerifyReport{}, errors.New("the export does not match the hash the signature covers: the results were altered")
	}
	var e Export
	if err := json.Unmarshal(ex, &e); err != nil {
		return VerifyReport{}, fmt.Errorf("the export: %w", err)
	}
	if !e.Sealed || e.Rehearsal || e.Run.ID != p.Run {
		return VerifyReport{}, errors.New("the export is not a sealed live export of the run the payload names")
	}
	rep := VerifyReport{Run: p.Run, Signer: p.Signer, Mode: p.Mode, Statement: p.Statement, SealedAt: p.SealedAt, Fingerprint: p.KeyFingerprint}
	for _, pt := range e.Points {
		rep.Results += len(pt.Results)
	}
	rep.Results += len(e.Orphans)
	if chain == nil {
		return rep, nil
	}
	if err := verifyChain(chain, p, pl, e); err != nil {
		return rep, err
	}
	rep.ChainChecked = true
	return rep, nil
}

// verifyChain reads the results file the way the store does and checks the
// seal and the results against it.
func verifyChain(chain []byte, p SealPayload, payload []byte, e Export) error {
	psum := sha256.Sum256(payload)
	want := hex.EncodeToString(psum[:])
	var seq int64
	var prev string
	var sealAt int64
	got := map[int64]Result{}
	sc := bufio.NewScanner(bytes.NewReader(chain))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var en entry
		if err := json.Unmarshal(line, &en); err != nil {
			return fmt.Errorf("the results file: line %d does not parse: %w", seq+1, err)
		}
		if en.Seq != seq+1 || en.Prev != prev {
			return fmt.Errorf("the results file: line %d is not the next entry of the chain: it was edited, or a line was removed", seq+1)
		}
		if en.Run == p.Run {
			switch en.Kind {
			case kindResult:
				got[en.Seq] = Result{Run: en.Run, Seq: en.Seq, At: en.At, Point: en.Point, Outcome: en.Outcome, Note: en.Note, By: en.By, Principal: en.Principal}
			case kindSeal:
				if en.SealSha == want {
					if en.Seq != p.PrevSeq+1 || en.Prev != p.PrevHash {
						return errors.New("the seal entry is not where the signed payload says it is in the results file")
					}
					sealAt = en.Seq
				}
			}
		}
		seq, prev = en.Seq, lineHash(line)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if sealAt == 0 {
		return errors.New("the results file has no seal entry for this record: it is another controller's file, an older copy, or the seal was removed")
	}
	// every result the file holds for the run before the seal is in the export,
	// and the export holds no other
	inExport := map[int64]Result{}
	add := func(r Result) { inExport[r.Seq] = r }
	for _, pt := range e.Points {
		for _, r := range pt.Results {
			add(r)
		}
	}
	for _, r := range e.Orphans {
		add(r)
	}
	for sq, g := range got {
		if sq > sealAt {
			continue
		}
		x, ok := inExport[sq]
		if !ok {
			return fmt.Errorf("the results file has a result (entry %d, point %s) the export does not hold", sq, g.Point)
		}
		if x.Point != g.Point || x.Outcome != g.Outcome || x.Note != g.Note || x.By != g.By || x.Principal != g.Principal || x.At != g.At {
			return fmt.Errorf("result entry %d differs between the export and the results file", sq)
		}
	}
	for sq := range inExport {
		if _, ok := got[sq]; !ok {
			return fmt.Errorf("the export holds a result (entry %d) the results file does not", sq)
		}
	}
	return nil
}
