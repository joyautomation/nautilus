package fbd

import (
	"fmt"
	"strconv"
	"strings"
)

// Network edit ops (#207). A network is text: its NETWORK line and the
// lines after it up to the next one (the implicit first network: from the
// FBD line to the first NETWORK line). Ops address networks by number,
// `n:<number>`, the way the diagram draws them.
//
//	addNetwork     Text (title, optional); Node n:K inserts after network K,
//	               else at the end of the body
//	renameNetwork  Node n:K, Text (the new title; empty clears it)
//	moveNetwork    Node n:K, Value "up" | "down" — swap with its neighbour
//	removeNetwork  Node n:K — drop the NETWORK line; its statements join
//	               the network before (nothing is deleted but the header)
//	insertStatement with Node n:K appends to network K instead of the end

// netRange is network k's lines: [start, end) 1-based, header included.
func (b *modelBuilder) netRange(k int) (start, end int, err error) {
	nets := b.nl.networks
	if k < 0 || k >= len(nets) {
		return 0, 0, fmt.Errorf("fbd edit: no network %d", k+1)
	}
	body, endFBD, err := b.bodyLines()
	if err != nil {
		return 0, 0, err
	}
	start = nets[k].line
	if nets[k].implicit {
		start = body
	}
	end = endFBD
	if k+1 < len(nets) {
		end = nets[k+1].line
	}
	return start, end, nil
}

// bodyLines is the first body line (just after FBD) and the END_FBD line,
// both 1-based, found on comment-stripped text.
func (b *modelBuilder) bodyLines() (int, int, error) {
	start, end := -1, -1
	for i, l := range b.srcStripped {
		switch strings.ToUpper(strings.TrimSpace(l)) {
		case "FBD":
			if start == -1 {
				start = i + 2
			}
		case "END_FBD":
			end = i + 1
		}
	}
	if start == -1 || end == -1 {
		return 0, 0, fmt.Errorf("fbd edit: no FBD … END_FBD body")
	}
	return start, end, nil
}

// netIndex resolves an n:<number> id to a network index.
func (b *modelBuilder) netIndex(id string) (int, error) {
	num, err := strconv.Atoi(strings.TrimPrefix(id, "n:"))
	if !strings.HasPrefix(id, "n:") || err != nil {
		return 0, fmt.Errorf("fbd edit: %q is not a network (n:<number>)", id)
	}
	if num < 1 || num > len(b.nl.networks) {
		return 0, fmt.Errorf("fbd edit: no network %d", num)
	}
	return num - 1, nil
}

// networkLine renders a NETWORK line. A title cannot hold a quote (the
// netlist's strings have no escapes) or a line break: a ' becomes ’.
func networkLine(indent, title string) string {
	title = strings.Join(strings.Fields(strings.ReplaceAll(title, "'", "’")), " ")
	if title == "" {
		return indent + "NETWORK\n"
	}
	return indent + "NETWORK '" + title + "'\n"
}

func (b *modelBuilder) opAddNetwork(op EditOp) ([]TextEdit, error) {
	_, at, err := b.bodyLines()
	if err != nil {
		return nil, err
	}
	if op.Node != "" {
		k, err := b.netIndex(op.Node)
		if err != nil {
			return nil, err
		}
		if _, at, err = b.netRange(k); err != nil {
			return nil, err
		}
	}
	return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: networkLine("  ", op.Text)}}, nil
}

func (b *modelBuilder) opRenameNetwork(op EditOp) ([]TextEdit, error) {
	k, err := b.netIndex(op.Node)
	if err != nil {
		return nil, err
	}
	nw := b.nl.networks[k]
	if nw.implicit {
		// The statements before the first NETWORK line get a header of
		// their own — that is where a title lives.
		start, _, err := b.netRange(k)
		if err != nil {
			return nil, err
		}
		return []TextEdit{{Line: start, Col: 1, EndLine: start, EndCol: 1, NewText: networkLine("  ", op.Text)}}, nil
	}
	old := b.src[nw.line-1]
	indent := old[:len(old)-len(strings.TrimLeft(old, " \t"))]
	return []TextEdit{{Line: nw.line, Col: 1, EndLine: nw.line + 1, EndCol: 1, NewText: networkLine(indent, op.Text)}}, nil
}

func (b *modelBuilder) opMoveNetwork(op EditOp) ([]TextEdit, error) {
	k, err := b.netIndex(op.Node)
	if err != nil {
		return nil, err
	}
	var a int // the upper of the two networks that swap
	switch strings.ToLower(op.Value) {
	case "up":
		a = k - 1
	case "down":
		a = k
	default:
		return nil, fmt.Errorf("fbd edit: move a network \"up\" or \"down\", not %q", op.Value)
	}
	if a < 0 || a+1 >= len(b.nl.networks) {
		return nil, fmt.Errorf("fbd edit: network %d is already the %s", k+1, map[bool]string{true: "first", false: "last"}[a < 0])
	}
	sa, ea, err := b.netRange(a)
	if err != nil {
		return nil, err
	}
	sb, eb, err := b.netRange(a + 1)
	if err != nil {
		return nil, err
	}
	text := func(k, s, e int) string {
		var out strings.Builder
		if b.nl.networks[k].implicit {
			// No longer first: it needs a NETWORK line to stay a network.
			out.WriteString(networkLine("  ", ""))
		}
		for l := s; l < e; l++ {
			out.WriteString(b.src[l-1] + "\n")
		}
		return out.String()
	}
	return []TextEdit{{Line: sa, Col: 1, EndLine: eb, EndCol: 1, NewText: text(a+1, sb, eb) + text(a, sa, ea)}}, nil
}

func (b *modelBuilder) opRemoveNetwork(op EditOp) ([]TextEdit, error) {
	k, err := b.netIndex(op.Node)
	if err != nil {
		return nil, err
	}
	nw := b.nl.networks[k]
	if nw.implicit {
		return nil, fmt.Errorf("fbd edit: network %d has no NETWORK line to remove", k+1)
	}
	return []TextEdit{{Line: nw.line, Col: 1, EndLine: nw.line + 1, EndCol: 1}}, nil
}

// stmtInsertLine is where new statements go: just above END_FBD, or — with
// op.Node n:K — at the end of network K.
func (b *modelBuilder) stmtInsertLine(op EditOp) (int, error) {
	if strings.HasPrefix(op.Node, "n:") {
		k, err := b.netIndex(op.Node)
		if err != nil {
			return 0, err
		}
		_, end, err := b.netRange(k)
		return end, err
	}
	_, end, err := b.bodyLines()
	return end, err
}
