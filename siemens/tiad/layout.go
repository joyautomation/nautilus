package tiad

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

// The data layout gate (#1341, siemens-ide.md §3.5 "data layout"): the
// agent's Layout.cs, and the Go copy the fake runs. Change both or
// neither.
//
// A download that loads only what changed keeps the CPU's data as long as
// no data block's layout changed (bench 1510SP, 2026-11-24). The agent
// hashes each program block's and PLC data type's XML export (the root
// object's attribute list: interface, start values, number, memory
// layout, the FB an instance DB belongs to) and compares it with the
// layout recorded with the deploy the CPU verifiably holds. Any object
// whose hash moved, or that was added, removed or could not be read, needs
// Decisions.ResetData; so does a download with no such baseline, or one
// that loads hardware.

// LayoutScheme names how the agent hashes a layout. A baseline hashed
// with another scheme, or read by another TIA version, is no baseline.
const LayoutScheme = "attributes-v1"

// LayoutVerifyMaxAge is how long a verify that found the CPU equal to its
// newest load stands as the baseline's proof.
const LayoutVerifyMaxAge = time.Hour

// Layout change kinds.
const (
	LayoutChanged    = "changed"
	LayoutAdded      = "added"
	LayoutRemoved    = "removed"
	LayoutUnreadable = "unreadable"
)

// LayoutObject is one program block or PLC data type as the gate reads
// it. Kind is "block" or "type"; Type is Openness's type name (GlobalDB,
// InstanceDB, FB, PlcStruct, ...); Hash is "" when the object could not
// be read. Refs are the names its layout uses.
type LayoutObject struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Type       string   `json:"type"`
	Hash       string   `json:"hash"`
	Unreadable string   `json:"-"`
	Refs       []string `json:"-"`
}

// LayoutChange is one object whose layout differs from the baseline.
type LayoutChange struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Type   string `json:"type"`
	Change string `json:"change"`
}

// Layout is the agent's answer to a layout check: what a download of the
// session's compiled project would do to the CPU's data.
type Layout struct {
	Scheme string `json:"scheme"`
	TIA    string `json:"tia"`
	// Baseline is the deploy compared with, "" for none (NoBaseline says
	// why).
	Baseline   string         `json:"baseline"`
	NoBaseline string         `json:"noBaseline,omitempty"`
	Changes    []LayoutChange `json:"changes"`
	// DataBlocks are the data blocks whose values the load may lose: each
	// the project holds whose own layout changed, or whose layout uses a
	// changed object, however deep; then each the load removes.
	// AllDataBlocks: every one (no baseline, or a hardware load).
	DataBlocks      []string `json:"dataBlocks"`
	AllDataBlocks   bool     `json:"allDataBlocks"`
	ResetDataNeeded bool     `json:"resetDataNeeded"`
	Why             string   `json:"why"`
	// Digest names this exact layout and baseline: a download without the
	// consent sends it (DownloadRequest.LayoutDigest).
	Digest string `json:"digest"`
}

// LayoutBaseline is the layout a deploy record holds.
type LayoutBaseline struct {
	Deploy  string
	Scheme  string
	TIA     string
	Objects []LayoutObject
}

// DeployLayout is a deploy record's layout, as the intent line keeps it:
// the next download's baseline once a verify finds the CPU equal to that
// deploy.
type DeployLayout struct {
	Scheme  string         `json:"scheme"`
	TIA     string         `json:"tia"`
	Objects []LayoutObject `json:"objects"`
}

// LayoutConsentSentence ends every why that needs the consent.
const LayoutConsentSentence = "decisions.resetData yes is the consent to it"

// Layout asks the agent what a download of the session's compiled project
// to the CPU at address (with its hardware configuration, if hardware)
// would do to the CPU's data. It reads the project only.
func (s *Session) Layout(ctx context.Context, address string, hardware bool) (Layout, error) {
	var r Layout
	_, err := s.c.doJSON(ctx, http.MethodPost, s.path("layout"), map[string]any{"address": address, "hardware": hardware}, &r)
	return r, err
}

func layoutKey(name string) string { return strings.ToLower(name) }

// IsDataBlock is whether an Openness type name is a data block's.
func IsDataBlock(typ string) bool { return strings.HasSuffix(typ, "DB") }

// DiffLayout is the agent's LayoutRule.Diff: now compared with base (nil:
// none, noBaseline says why), read by TIA tia.
func DiffLayout(now []LayoutObject, base *LayoutBaseline, noBaseline, tia string, hardware bool) Layout {
	d := Layout{Scheme: LayoutScheme, TIA: tia, Changes: []LayoutChange{}, DataBlocks: []string{}}
	sorted := append([]LayoutObject(nil), now...)
	sort.SliceStable(sorted, func(i, j int) bool { return layoutKey(sorted[i].Name) < layoutKey(sorted[j].Name) })
	if base == nil {
		d.NoBaseline = noBaseline
		if d.NoBaseline == "" {
			d.NoBaseline = "no layout baseline"
		}
	} else {
		d.Baseline = base.Deploy
		was := map[string]LayoutObject{}
		for _, b := range base.Objects {
			was[layoutKey(b.Name)] = b
		}
		seen := map[string]bool{}
		for _, o := range sorted {
			k := layoutKey(o.Name)
			seen[k] = true
			b, ok := was[k]
			change := ""
			switch {
			case o.Hash == "":
				change = LayoutUnreadable
			case !ok:
				change = LayoutAdded
			case b.Hash != o.Hash: // an unreadable baseline object ("") differs from any hash read now
				change = LayoutChanged
			}
			if change != "" {
				d.Changes = append(d.Changes, LayoutChange{Name: o.Name, Kind: o.Kind, Type: o.Type, Change: change})
			}
		}
		var gone []LayoutObject
		for _, b := range base.Objects {
			if !seen[layoutKey(b.Name)] {
				gone = append(gone, b)
			}
		}
		sort.SliceStable(gone, func(i, j int) bool { return layoutKey(gone[i].Name) < layoutKey(gone[j].Name) })
		for _, b := range gone {
			d.Changes = append(d.Changes, LayoutChange{Name: b.Name, Kind: b.Kind, Type: b.Type, Change: LayoutRemoved})
		}
	}
	// An unreadable object has no known layout or references: any data
	// block may depend on it, so every one is named.
	unknown := slices.ContainsFunc(d.Changes, func(c LayoutChange) bool { return c.Change == LayoutUnreadable })
	d.AllDataBlocks = base == nil || hardware || unknown
	if d.AllDataBlocks {
		for _, o := range sorted {
			if o.Kind == "block" && IsDataBlock(o.Type) {
				d.DataBlocks = append(d.DataBlocks, o.Name)
			}
		}
	} else {
		d.DataBlocks = reachedDataBlocks(sorted, d.Changes)
	}
	if base != nil {
		for _, c := range d.Changes {
			if c.Change == LayoutRemoved && c.Kind == "block" && IsDataBlock(c.Type) {
				d.DataBlocks = append(d.DataBlocks, c.Name)
			}
		}
	}
	d.ResetDataNeeded = base == nil || hardware || len(d.Changes) > 0
	switch {
	case hardware:
		d.Why = "the download loads the hardware configuration, and what that does to the CPU's data is not measured: every data block may return to its start values, retained ones included; " + LayoutConsentSentence
	case base == nil:
		d.Why = "no layout baseline (" + d.NoBaseline + "), so the load replaces the whole program: every data block returns to its start values, retained ones included; " + LayoutConsentSentence
	case len(d.Changes) > 0:
		d.Why = fmt.Sprintf("the data layout changed since deploy %s (%d object(s)): the data blocks it reaches may return to their start values, retained ones included, and with the CPU in STOP TIA resets them without asking; %s", base.Deploy, len(d.Changes), LayoutConsentSentence)
	default:
		d.Why = "the data layout is unchanged since deploy " + base.Deploy + ", which the CPU compared equal to: the load keeps the CPU's data"
	}
	var sb strings.Builder
	hw := "software"
	if hardware {
		hw = "hardware"
	}
	sb.WriteString(d.Scheme + "\n" + d.TIA + "\n" + d.Baseline + "\n" + hw + "\n")
	for _, o := range sorted {
		sb.WriteString(o.Name + "\t" + o.Type + "\t" + o.Hash + "\n")
	}
	sum := sha256.Sum256([]byte(sb.String()))
	d.Digest = hex.EncodeToString(sum[:])
	return d
}

func reachedDataBlocks(sorted []LayoutObject, changes []LayoutChange) []string {
	hit := map[string]bool{}
	added := map[string]bool{}
	for _, c := range changes {
		if c.Change != LayoutRemoved {
			hit[layoutKey(c.Name)] = true
		}
		if c.Change == LayoutAdded {
			added[layoutKey(c.Name)] = true
		}
	}
	byName := map[string]LayoutObject{}
	for _, o := range sorted {
		byName[layoutKey(o.Name)] = o
	}
	var reaches func(o LayoutObject, visiting map[string]bool) bool
	reaches = func(o LayoutObject, visiting map[string]bool) bool {
		if visiting[layoutKey(o.Name)] {
			return false
		}
		visiting[layoutKey(o.Name)] = true
		for _, r := range o.Refs {
			if hit[layoutKey(r)] {
				return true
			}
			if next, ok := byName[layoutKey(r)]; ok && reaches(next, visiting) {
				return true
			}
		}
		return false
	}
	out := []string{}
	for _, o := range sorted {
		if o.Kind != "block" || !IsDataBlock(o.Type) || added[layoutKey(o.Name)] {
			continue
		}
		if hit[layoutKey(o.Name)] || reaches(o, map[string]bool{}) {
			out = append(out, o.Name)
		}
	}
	return out
}

// LayoutRecordOf is a layout as a deploy record's intent line keeps it.
func LayoutRecordOf(now []LayoutObject, tia string) *DeployLayout {
	return &DeployLayout{Scheme: LayoutScheme, TIA: tia, Objects: slices.Clone(now)}
}

// LayoutBaselineOf reads a deploy record's layout as the baseline for a
// download read by TIA tia, or returns why it is none, as the agent's
// LayoutRule.FromRecord.
func LayoutBaselineOf(deploy string, l *DeployLayout, tia string) (*LayoutBaseline, string) {
	switch {
	case l == nil:
		return nil, "deploy " + deploy + " recorded no data layout (it was made before the layout gate, #1341)"
	case l.Scheme != LayoutScheme:
		return nil, fmt.Sprintf("deploy %s's data layout was hashed with scheme %q, and this agent hashes with %q: a change of the scheme invalidates every recorded layout", deploy, l.Scheme, LayoutScheme)
	case l.TIA != tia:
		return nil, fmt.Sprintf("deploy %s's data layout was read by TIA %q, and this agent runs TIA %q: a TIA upgrade invalidates every recorded layout", deploy, l.TIA, tia)
	}
	return &LayoutBaseline{Deploy: deploy, Scheme: l.Scheme, TIA: l.TIA, Objects: slices.Clone(l.Objects)}, ""
}

// LayoutGateMissQuestion is whether a check TIA asks during a download
// without the consent means the gate missed a change: a stop, a
// reinitialize, or a load of every block.
func LayoutGateMissQuestion(id string) bool {
	return id == QuestionStopCPU || id == QuestionReinitialize || id == QuestionAllBlocks
}

// LayoutGateMiss is the agent's refusal of such a download.
func LayoutGateMiss(question, baseline string) string {
	return fmt.Sprintf("GATE MISS: TIA asked %q although the data layout check found nothing changed since deploy %s (no decisions.resetData was given, so tiad answers no such check): the download was cancelled at that check. The check missed a change TIA sees; the CPU's data is as it was if TIA asked before loading. Report it (#1341), and load with decisions.resetData if the reset is acceptable", question, baseline)
}
