package plcnext

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// The ch:N spelling and the old bit:N one bind the same bits: channel n
// of a 16-channel module is bit:((n+7) mod 16) in the big-endian word.
// Every channel, read from and written into random images, both ways.
func TestChannelSpellingBindsTheSameBits(t *testing.T) {
	rng := rand.New(rand.NewSource(27))
	for n := 1; n <= 16; n++ {
		ch := Binding{Name: "CH", Offset: 3, Format: "ch:" + strconv.Itoa(n)}
		bit := Binding{Name: "BIT", Offset: 3, Format: "bit:" + strconv.Itoa((n+7)%16)}

		// The channel is bit (n-1)%8 of byte (n-1)/8 of the module.
		img := make([]byte, 8)
		if err := encode(ch, true, img, "big"); err != nil {
			t.Fatal(err)
		}
		want := make([]byte, 8)
		want[3+(n-1)/8] = 1 << ((n - 1) % 8)
		if !bytes.Equal(img, want) {
			t.Errorf("ch:%d sets % x, want % x", n, img, want)
		}

		for i := 0; i < 64; i++ {
			img := make([]byte, 8)
			rng.Read(img)
			a, errA := decode(ch, img, "big")
			b, errB := decode(bit, img, "big")
			if errA != nil || errB != nil || a != b {
				t.Fatalf("ch:%d / %s read % x as %v / %v (%v %v)", n, bit.Format, img, a, b, errA, errB)
			}
			for _, v := range []bool{true, false} {
				x, y := append([]byte(nil), img...), append([]byte(nil), img...)
				if err := encode(ch, v, x, "big"); err != nil {
					t.Fatal(err)
				}
				if err := encode(bit, v, y, "big"); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(x, y) {
					t.Fatalf("ch:%d=%v wrote % x, %s=%v wrote % x (from % x)", n, v, x, bit.Format, v, y, img)
				}
			}
		}
	}
}

// oldSpelling is the example's manifest with every ch:N written back as
// the bit:N it replaced.
func oldSpelling(t *testing.T, manifest []byte) []byte {
	t.Helper()
	re := regexp.MustCompile(`"ch:(\d+)"`)
	return re.ReplaceAllFunc(manifest, func(m []byte) []byte {
		n, _ := strconv.Atoi(string(re.FindSubmatch(m)[1]))
		return []byte(`"bit:` + strconv.Itoa((n+7)%16) + `"`)
	})
}

// The bench manifest in ch:N and the same manifest in bit:N are the same
// on the wire: the same spans read and written, the same inputs decoded,
// the same output bytes — through the driver, against the fake firmware.
func TestChannelSpellingSameOnTheWire(t *testing.T) {
	raw, err := os.ReadFile("../examples/plcnext-bench/plcnext_manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"ch:16"`)) {
		t.Fatal("the example manifest is not in ch:N any more; this test compares the two spellings")
	}
	oldRaw := oldSpelling(t, raw)
	if bytes.Contains(oldRaw, []byte(`"ch:`)) {
		t.Fatal("conversion left a ch:")
	}
	nu, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	old, err := ParseManifest(oldRaw)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{DirIn, DirOut} {
		if a, b := nu.span(dir), old.span(dir); a != b {
			t.Fatalf("%s span: ch:N %d bytes, bit:N %d", dir, a, b)
		}
	}

	run := func(m Manifest) (nio.Values, []byte, string) {
		m.Transport = TransportGRPC // the fake is the data-access service
		f := newFakeGDS()
		addr := serve(t, f)
		d, err := New(m, WithScanRate(10*time.Millisecond), WithDialer(func(ctx context.Context) (dataAccess, error) {
			return Dial(ctx, addr)
		}))
		if err != nil {
			t.Fatal(err)
		}
		f.setInput(2, 0x05) // DI16 channels 01 and 03
		f.setInput(3, 0x80) // … and 16
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		d.Start(ctx)
		defer d.Stop()
		if err := d.WriteOutputs(nio.Values{"DO_00": true, "DO_08": true, "DO_15": true}); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		var in nio.Values
		for {
			in, _ = d.ReadInputs()
			if in["DI_00"] == true && f.output(0) != 0 && f.output(1) != 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("never exchanged: %v, out % x", in, []byte{f.output(0), f.output(1)})
			}
			time.Sleep(5 * time.Millisecond)
		}
		var image []string
		for _, w := range strings.Fields(f.wrote()) {
			if strings.HasPrefix(w, DefaultOutputs+"[") {
				image = append(image, w)
			}
		}
		return in, []byte{f.output(0), f.output(1), f.output(2), f.output(3)}, strings.Join(image, " ")
	}
	inNew, outNew, wNew := run(nu)
	inOld, outOld, wOld := run(old)
	if !reflect.DeepEqual(inNew, inOld) {
		t.Errorf("inputs differ:\n ch:  %v\n bit: %v", inNew, inOld)
	}
	if !bytes.Equal(outNew, outOld) || wNew != wOld {
		t.Errorf("outputs differ:\n ch:  % x %s\n bit: % x %s", outNew, wNew, outOld, wOld)
	}
	// And they are the channels on the terminals: DO 01 and 09 are bit 0
	// of bytes 0 and 1, DO 16 bit 7 of byte 1; DI 01, 03 and 16 read true.
	if !bytes.Equal(outNew[:2], []byte{0x01, 0x81}) {
		t.Errorf("output bytes % x, want 01 81", outNew[:2])
	}
	for name, want := range map[string]bool{"DI_00": true, "DI_01": false, "DI_02": true, "DI_08": false, "DI_15": true} {
		if inNew[name] != want {
			t.Errorf("%s = %v, want %v", name, inNew[name], want)
		}
	}
}

func TestChannelFormatParseAndErrors(t *testing.T) {
	if f, err := ParseFormat("ch:01"); err != nil || f.Kind != "ch" || f.Ch != 1 || f.String() != "ch:1" || f.Bytes() != 1 {
		t.Errorf("ch:01 = %+v %v", f, err)
	}
	if f, _ := ParseFormat("ch:16"); f.Bytes() != 2 {
		t.Errorf("ch:16 spans %d bytes, want 2", f.Bytes())
	}
	for _, s := range []string{"ch:0", "ch:x", "ch:-1", "ch:"} {
		if _, err := ParseFormat(s); err == nil || !strings.Contains(err.Error(), "ch:1 or more") {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"bit:19", "word"} {
		if _, err := ParseFormat(s); err == nil || !strings.Contains(err.Error(), "ch:N") {
			t.Errorf("%s: the error should offer ch:N: %v", s, err)
		}
	}

	const mods = "modules:\n  - {slot: 1, part: DO16, in: 2, out: 2}\n  - {slot: 2, part: DI16, in: 2, out: 2}\n"
	_, err := ParseManifest([]byte(mods + "tags:\n  - {name: X, dir: in, offset: 2, format: \"ch:17\"}\n  - {name: Y, dir: in, offset: 0, format: \"ch:1\", scale: 2}\n"))
	if err == nil {
		t.Fatal("ch:17 on a 2-byte module and scale on a channel accepted")
	}
	if s := err.Error(); !strings.Contains(s, "tag X: ch:17 at offset 2 reaches byte 4, past DI16 (slot 2, in bytes 2..3, channels 1..16 from offset 2)") || !strings.Contains(s, "tag Y: scale/bias on a bit") {
		t.Errorf("errors: %v", err)
	}
	// Without a module list there is nothing to hold a channel against.
	if _, err := ParseManifest([]byte("tags:\n  - {name: X, dir: in, offset: 2, format: \"ch:17\"}\n")); err != nil {
		t.Errorf("ch:17 without modules: %v", err)
	}
	// bit:N is held to nothing new, even where it straddles a module.
	if _, err := ParseManifest([]byte(mods + "tags:\n  - {name: X, dir: in, offset: 3, format: \"bit:0\"}\n")); err != nil {
		t.Errorf("bit:N changed: %v", err)
	}
}

// A tag with no desc is described by its module and the module's own
// channel number, whichever spelling bound it.
func TestTagsDescribeChannels(t *testing.T) {
	m, err := ParseManifest([]byte(`
modules:
  - {slot: 1, part: "AXL SE DO16/1", in: 2, out: 2}
  - {slot: 2, part: "AXL SE DI16/1", in: 2, out: 2}
  - {slot: 3, part: "AXL SE AI4 U", in: 8, out: 8}
tags:
  - {name: A, dir: in, offset: 2, format: "ch:1"}
  - {name: B, dir: in, offset: 2, format: "bit:8"}
  - {name: C, dir: out, offset: 0, format: "ch:16"}
  - {name: D, dir: out, offset: 0, format: "bit:7"}
  - {name: E, dir: in, offset: 4, format: int16}
`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := TagsYAML(m, "tags/plcnext.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name: A, role: input, desc: "AXL SE DI16/1 (slot 2) input channel 01"`,
		`name: B, role: input, desc: "AXL SE DI16/1 (slot 2) input channel 01"`,
		`name: C, role: output, init: false, desc: "AXL SE DO16/1 (slot 1) output channel 16"`,
		`name: D, role: output, init: false, desc: "AXL SE DO16/1 (slot 1) output channel 16"`,
		`name: E, role: input, desc: "AXL SE AI4 U (slot 3) in byte 4 int16"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("tag file lacks %s:\n%s", want, body)
		}
	}
}
