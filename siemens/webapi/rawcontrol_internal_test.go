package webapi

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestEscapeRawControls(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"newline in a string", "{\"a\":\"x\ny\"}", `{"a":"x\ny"}`},
		{"tab and cr in a string", "{\"a\":\"x\ty\rz\"}", `{"a":"x\ty\rz"}`},
		{"backspace and form feed", "[\"\b\f\"]", `["\b\f"]`},
		{"other control as u00XX", "[\"\x01\x1f\x00\"]", `["\u0001\u001f\u0000"]`},
		{"whitespace between tokens stays", "{\n \"a\": 1,\r\n\t\"b\":\"c\"\n}\n", "{\n \"a\": 1,\r\n\t\"b\":\"c\"\n}\n"},
		{"escaped backslash then closing quote", "[\"a\\\\\"\n,\"b\nc\"]", "[\"a\\\\\"\n,\"b\\nc\"]"},
		{"escaped quote keeps the string open", "[\"a\\\"\nb\"]", `["a\"\nb"]`},
		{"already escaped newline untouched", `["a\nb"]`, `["a\nb"]`},
		{"utf-8 and printable untouched", "[\"a…b é\"]", "[\"a…b é\"]"},
		{"empty", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := escapeRawControls([]byte(c.in))
			if string(got) != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestEscapeRawControlsOutputIsValidJSON(t *testing.T) {
	var v []string
	if err := json.Unmarshal(escapeRawControls([]byte("[\"a\nb\",\"c\td\"]")), &v); err != nil || v[0] != "a\nb" || v[1] != "c\td" {
		t.Fatalf("v = %q, err = %v", v, err)
	}
}

func TestEscapeRawControlsDoesNotCopyCleanInput(t *testing.T) {
	in := []byte("{\n \"a\": \"b\"\n}")
	got := escapeRawControls(in)
	if !bytes.Equal(got, in) || &got[0] != &in[0] {
		t.Fatal("clean input must come back as the same slice")
	}
}
