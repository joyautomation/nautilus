package cip

import "testing"

// sym encodes one 0x91 ANSI extended-symbol segment, padding odd-length names.
func sym(name string) []byte {
	out := []byte{AnsiExtendedSymbol, byte(len(name))}
	out = append(out, []byte(name)...)
	if len(name)%2 == 1 {
		out = append(out, 0x00)
	}
	return out
}

// idx8 encodes an 8-bit logical Member (array element) segment.
func idx8(v uint8) []byte { return []byte{0x28, v} }

func TestParseSymbolicTag(t *testing.T) {
	cases := []struct {
		name string
		path []byte
		want string
	}{
		{
			name: "controller-scoped member",
			path: append(sym("TRS_90001311"), sym("EosPec")...),
			want: "TRS_90001311.EosPec",
		},
		{
			name: "bare atomic",
			path: sym("Lane2_SwmDivertReason"),
			want: "Lane2_SwmDivertReason",
		},
		{
			name: "program-scoped array element nested member",
			path: concat(
				sym("Program:MBD_3To1_Merge"),
				sym("Plt"),
				idx8(4),
				sym("Header"),
				sym("Displacement"),
			),
			want: "Program:MBD_3To1_Merge.Plt[4].Header.Displacement",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			segs, err := ParseSymbolicTag(tc.path)
			if err != nil {
				t.Fatalf("ParseSymbolicTag: %v", err)
			}
			if got := CanonicalTagPath(segs); got != tc.want {
				t.Fatalf("canonical = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseSymbolicTagErrors(t *testing.T) {
	if _, err := ParseSymbolicTag(idx8(1)); err == nil {
		t.Fatal("expected error: path starting with an index")
	}
	if _, err := ParseSymbolicTag([]byte{AnsiExtendedSymbol, 0x05, 'a', 'b'}); err == nil {
		t.Fatal("expected error: name length exceeds buffer")
	}
	if _, err := ParseSymbolicTag([]byte{0x20, 0x02}); err == nil {
		t.Fatal("expected error: non-symbolic logical class segment")
	}
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
