package l5x

import (
	"bytes"
	"regexp"
)

// Normalizing an L5X is what drift detection rests on, and it turns out to
// be nearly trivial.
//
// Measured on ECHO1 (docs/design/logix-target.md §11): the same unchanged
// ACD exported twice through the Studio 5000 SDK produced files that
// differ on ONE line of 13,194 — the ExportDate attribute. Not attribute
// ordering, not the DataExchangeId GUID, not CDATA whitespace, not element
// ordering. So this is a handful of attribute substitutions, not a
// canonicalizing XML rewriter, and it deliberately stays that way: an
// edit that re-serializes the document would reformat lines nobody
// changed, which is the opposite of what a reviewable diff needs.
//
// Everything is rewritten IN PLACE, byte for byte, so a normalized export
// still diffs cleanly against the export it came from.

// Pinned is the placeholder a normalized attribute carries. It is not a
// timestamp, so it cannot be mistaken for one.
const Pinned = "(pinned)"

// NormalizeOptions selects what to pin. The zero value pins the two
// attributes that actually move between exports of unchanged code.
type NormalizeOptions struct {
	// PinIDs also pins DataExchangeId and ProjectSN. Both measured stable
	// across a re-export and both are real identity, so they are left
	// alone by default; pin them when a project has been round-tripped
	// through a copy and only the logic matters.
	PinIDs bool
	// DropL5K removes the redundant <Data Format="L5K"> copy of every
	// value. Every tag value is carried twice, once as L5K CDATA and once
	// Decorated; dropping the L5K copy halved the diff of a one-setpoint
	// change (4 lines → 2). It is not a general win — it only applies to
	// value changes — and it makes the file no longer importable, so it
	// is for comparison artifacts, not for round-tripping.
	DropL5K bool
}

var (
	reExportDate      = regexp.MustCompile(`(?i)(\sExportDate=")[^"]*(")`)
	reLastModified    = regexp.MustCompile(`(?i)(\sLastModifiedDate=")[^"]*(")`)
	reProjectCreation = regexp.MustCompile(`(?i)(\sProjectCreationDate=")[^"]*(")`)
	// An upload writes the path it connected over into the export. It
	// describes the machine that did the upload, not the code, so two
	// exports of identical logic taken over different paths must compare
	// equal. A project exported offline carries no CommPath at all, so it
	// is dropped rather than pinned.
	reCommPath        = regexp.MustCompile(`(?i)\sCommPath="[^"]*"`)
	reDataExchangeID  = regexp.MustCompile(`(?i)(\sDataExchangeId=")[^"]*(")`)
	reProjectSN       = regexp.MustCompile(`(?i)(\sProjectSN=")[^"]*(")`)
	reL5KData         = regexp.MustCompile(`(?s)[ \t]*<Data Format="L5K">.*?</Data>\n?`)
	pinnedReplacement = []byte("${1}" + Pinned + "${2}")
)

// Normalize pins an export's volatile attributes so two exports of
// unchanged code compare equal.
//
// Pinned by default:
//
//	ExportDate            moves on every export — the one difference
//	                      measured between two exports of an unchanged ACD
//	LastModifiedDate      moves when the project is opened and saved
//	ProjectCreationDate   differs between a project and a copy of it
//	CommPath              DROPPED: an upload records the path it came over,
//	                      an offline export has none at all
//
// Whitespace BETWEEN ATTRIBUTES is also collapsed to a single space.
// Logix wraps a long attribute list at a column, so removing one
// attribute moves the wrap and two exports of identical code differ by a
// line break. Text and CDATA are untouched: rung text and comments mean
// what they say.
//
// Left alone by default: DataExchangeId and ProjectSN. Both measured
// stable across a re-export, and both are real identity — pinning them
// would hide a project actually being replaced. PinIDs turns them off for
// the case where a project HAS been round-tripped through a copy and only
// the logic matters.
func Normalize(src []byte, opts NormalizeOptions) []byte {
	out := collapseTagSpace(src)
	out = reExportDate.ReplaceAll(out, pinnedReplacement)
	out = reLastModified.ReplaceAll(out, pinnedReplacement)
	out = reProjectCreation.ReplaceAll(out, pinnedReplacement)
	out = collapseTagSpace(reCommPath.ReplaceAll(out, nil))
	if opts.PinIDs {
		out = reDataExchangeID.ReplaceAll(out, pinnedReplacement)
		out = reProjectSN.ReplaceAll(out, pinnedReplacement)
	}
	if opts.DropL5K {
		out = reL5KData.ReplaceAll(out, nil)
	}
	return out
}

// Equivalent reports whether two exports describe the same project once
// normalized — the drift check, in one call.
func Equivalent(a, b []byte, opts NormalizeOptions) bool {
	return bytes.Equal(Normalize(a, opts), Normalize(b, opts))
}

// collapseTagSpace rewrites runs of whitespace between a start tag's
// attributes to a single space, and drops the whitespace before the
// closing ">" or "/>".
//
// It is a scanner rather than a regex because an L5X carries ST and RLL
// source inside CDATA, and that source contains "<" and ">" as comparison
// operators. A regex for "inside a tag" mangles the code it is meant to
// be comparing.
func collapseTagSpace(src []byte) []byte {
	out := make([]byte, 0, len(src))
	for i := 0; i < len(src); {
		// CDATA is copied through verbatim, angle brackets and all.
		if bytes.HasPrefix(src[i:], cdataOpen) {
			end := bytes.Index(src[i:], cdataClose)
			if end < 0 {
				return append(out, src[i:]...) // truncated; leave as-is
			}
			end += len(cdataClose)
			out = append(out, src[i:i+end]...)
			i += end
			continue
		}
		if bytes.HasPrefix(src[i:], commentOpen) {
			end := bytes.Index(src[i:], commentClose)
			if end < 0 {
				return append(out, src[i:]...)
			}
			end += len(commentClose)
			out = append(out, src[i:i+end]...)
			i += end
			continue
		}
		if src[i] != '<' {
			out = append(out, src[i])
			i++
			continue
		}
		// A tag: copy it, collapsing whitespace outside quoted values.
		var quote byte
		tag := make([]byte, 0, 256)
		for ; i < len(src); i++ {
			c := src[i]
			if quote != 0 {
				tag = append(tag, c)
				if c == quote {
					quote = 0
				}
				continue
			}
			switch c {
			case '"', '\'':
				quote = c
				tag = append(tag, c)
			case ' ', '\t', '\r', '\n':
				// One space, and never right before the tag closes.
				if n := len(tag); n > 0 && tag[n-1] != ' ' && tag[n-1] != '<' {
					tag = append(tag, ' ')
				}
			case '>':
				// Trim before ">" and before the "/" of "/>", so
				// <T a="1"/> and <T a="1" /> agree.
				for n := len(tag); n > 0; n = len(tag) {
					if tag[n-1] == ' ' {
						tag = tag[:n-1]
						continue
					}
					if n > 1 && tag[n-1] == '/' && tag[n-2] == ' ' {
						tag = append(tag[:n-2], '/')
					}
					break
				}
				tag = append(tag, '>')
				i++
				goto done
			default:
				tag = append(tag, c)
			}
		}
	done:
		out = append(out, tag...)
	}
	return out
}

var (
	cdataOpen    = []byte("<![CDATA[")
	cdataClose   = []byte("]]>")
	commentOpen  = []byte("<!--")
	commentClose = []byte("-->")
)
