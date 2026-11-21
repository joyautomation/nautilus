package main

import (
	"fmt"
	"strings"
)

// unifiedDiff renders a unified diff of a to b, three lines of context; ""
// when they are equal. Files here are manifests and device profiles, so the
// O(n·m) line match is fine.
func unifiedDiff(nameA, nameB, a, b string) string {
	if a == b {
		return ""
	}
	la, lb := splitLines(a), splitLines(b)
	n, m := len(la), len(lb)
	// lcs[i][j] is the longest common subsequence of la[i:] and lb[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if la[i] == lb[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type op struct {
		kind byte // ' ', '-', '+'
		text string
	}
	var ops []op
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && la[i] == lb[j]:
			ops = append(ops, op{' ', la[i]})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, op{'-', la[i]})
			i++
		default:
			ops = append(ops, op{'+', lb[j]})
			j++
		}
	}
	const ctx = 3
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", nameA, nameB)
	// Hunks: runs of changes with ctx lines of context each side, merged
	// when their contexts touch.
	for k := 0; k < len(ops); {
		if ops[k].kind == ' ' {
			k++
			continue
		}
		start := max(k-ctx, 0)
		end := k
		for end < len(ops) {
			// extend over this change and any change within 2*ctx lines
			if ops[end].kind != ' ' {
				end++
				continue
			}
			gap := 0
			for e := end; e < len(ops) && ops[e].kind == ' '; e++ {
				gap++
			}
			if end+gap < len(ops) && gap <= 2*ctx {
				end += gap
				continue
			}
			break
		}
		stop := min(end+ctx, len(ops))
		aStart, bStart := 1, 1
		for _, o := range ops[:start] {
			if o.kind != '+' {
				aStart++
			}
			if o.kind != '-' {
				bStart++
			}
		}
		var aLen, bLen int
		var body strings.Builder
		for _, o := range ops[start:stop] {
			if o.kind != '+' {
				aLen++
			}
			if o.kind != '-' {
				bLen++
			}
			body.WriteByte(o.kind)
			body.WriteString(strings.TrimSuffix(o.text, noEOL))
			body.WriteByte('\n')
			if strings.HasSuffix(o.text, noEOL) {
				body.WriteString("\\ No newline at end of file\n")
			}
		}
		if aLen == 0 {
			aStart--
		}
		if bLen == 0 {
			bStart--
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n%s", aStart, aLen, bStart, bLen, body.String())
		k = stop
	}
	return sb.String()
}

// noEOL marks a last line the text ends without a newline after: it is a
// different line from the same text with one, and is printed with git's
// "\ No newline at end of file", so a patch of it applies.
const noEOL = "\x00"

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if !strings.HasSuffix(s, "\n") {
		lines[len(lines)-1] += noEOL
	}
	return lines
}
