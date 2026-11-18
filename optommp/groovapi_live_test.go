package optommp

// groovapi_live_test.go proves the Scratch Pad is shared with Opto's own
// software, both ways: a value written through groov Manage's REST API (the
// path groov View, Node-RED's groov I/O nodes and scripts use) reaches a
// nautilus read-only tag, and a value nautilus writes reads back through the
// same API. Skipped unless both are set:
//
//	NAUT_OPTOMMP_LIVE=<processor>  the EPIC (OptoMMP on 2001, groov Manage on 443)
//	NAUT_GROOV_API_KEY=<key>       a groov Manage user's API key
//
// groov Manage serves a self-signed certificate unless one was installed:
// NAUT_GROOV_CA=<pem file> trusts that certificate, NAUT_GROOV_INSECURE=1
// skips the check (a bench on a trusted network only). It uses Scratch Pad
// float and int32 elements 1020 and 1021 and puts them back as found.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"testing"
	"time"
)

// groovAPI is the slice of groov Manage's REST API the test needs
// (spec: developer.opto22.com, manage-api-public.yaml, "scratchpad").
type groovAPI struct {
	base, key string
	hc        *http.Client
}

func newGroovAPI(t *testing.T, host, key string) *groovAPI {
	t.Helper()
	tc := &tls.Config{}
	switch {
	case os.Getenv("NAUT_GROOV_CA") != "":
		pem, err := os.ReadFile(os.Getenv("NAUT_GROOV_CA"))
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			t.Fatal("NAUT_GROOV_CA: no certificate in the file")
		}
		tc.RootCAs = pool
	case os.Getenv("NAUT_GROOV_INSECURE") == "1":
		tc.InsecureSkipVerify = true
	}
	return &groovAPI{
		base: "https://" + host + "/manage/api/v1/io/local/scratchpad/",
		key:  key,
		hc:   &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tc}},
	}
}

func (g *groovAPI) do(method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, g.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apiKey", g.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return out, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(out))
	}
	return out, nil
}

// put writes one element ("float" or "int32").
func (g *groovAPI) put(kind string, index int, v float64) error {
	_, err := g.do(http.MethodPut, fmt.Sprintf("%s/index/%d", kind, index), map[string]any{"value": v})
	return err
}

// get reads one element. The spec documents the answer in the shape of its
// memory-map reads ({"values":[{"numberValue":…}]}); a bare {"value":…} or
// [{"value":…}] is accepted too, so a firmware that answers otherwise fails
// with the body in the message rather than a wrong number.
func (g *groovAPI) get(kind string, index int) (float64, error) {
	raw, err := g.do(http.MethodGet, fmt.Sprintf("%s/index/%d", kind, index), nil)
	if err != nil {
		return 0, err
	}
	var shaped struct {
		Values []struct {
			NumberValue *float64 `json:"numberValue"`
			Value       *float64 `json:"value"`
		} `json:"values"`
		Value *float64 `json:"value"`
	}
	if json.Unmarshal(raw, &shaped) == nil {
		if shaped.Value != nil {
			return *shaped.Value, nil
		}
		if len(shaped.Values) > 0 {
			if v := shaped.Values[0].NumberValue; v != nil {
				return *v, nil
			}
			if v := shaped.Values[0].Value; v != nil {
				return *v, nil
			}
		}
	}
	var list []struct {
		Value *float64 `json:"value"`
	}
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 && list[0].Value != nil {
		return *list[0].Value, nil
	}
	return 0, fmt.Errorf("GET %s/index/%d: no value in %s", kind, index, raw)
}

func TestLiveScratchpadSharedWithGroovManage(t *testing.T) {
	host, key := os.Getenv("NAUT_OPTOMMP_LIVE"), os.Getenv("NAUT_GROOV_API_KEY")
	if host == "" || key == "" {
		t.Skip("NAUT_OPTOMMP_LIVE and NAUT_GROOV_API_KEY not both set")
	}
	api := newGroovAPI(t, host, key)
	const fromOpto, fromNaut = 1020, 1021

	// As found, put back at the end whatever happens.
	type elem struct {
		kind  string
		index int
	}
	was := map[elem]float64{}
	for _, kind := range []string{"float", "int32"} {
		for _, i := range []int{fromOpto, fromNaut} {
			v, err := api.get(kind, i)
			if err != nil {
				t.Fatal(err)
			}
			was[elem{kind, i}] = v
		}
	}
	defer func() {
		for e, v := range was {
			if err := api.put(e.kind, e.index, v); err != nil {
				t.Errorf("restoring %s %d: %v", e.kind, e.index, err)
			}
		}
	}()

	c, err := Dial(context.Background(), host, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := Scan(c)
	c.Close()
	if err != nil {
		t.Fatal(err)
	}
	m, err := GenerateManifest(ch, ManifestOptions{ID: "LIVE", Host: host})
	if err != nil {
		t.Fatal(err)
	}
	m.Tags = append(m.Tags,
		Binding{Name: "LIVE_SP_F_IN", Point: PointScratchFloat, Index: fromOpto},
		Binding{Name: "LIVE_SP_I_IN", Point: PointScratchInt, Index: fromOpto},
		Binding{Name: "LIVE_SP_F_OUT", Point: PointScratchFloat, Index: fromNaut, Writable: true},
		Binding{Name: "LIVE_SP_I_OUT", Point: PointScratchInt, Index: fromNaut, Writable: true},
	)
	drv, err := New(m, WithScanRate(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	drv.Start(context.Background())
	defer drv.Stop()
	eventually := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	eventually("first scan", func() bool { return drv.Health().Reads > 0 })

	// groov Manage → nautilus.
	for _, v := range []float64{37.25, -4.5} {
		if err := api.put("float", fromOpto, v); err != nil {
			t.Fatal(err)
		}
		if err := api.put("int32", fromOpto, math.Trunc(v)*1000); err != nil {
			t.Fatal(err)
		}
		eventually(fmt.Sprintf("nautilus to read %g written through groov Manage", v), func() bool {
			x, _ := drv.ReadInputs()
			return x["LIVE_SP_F_IN"] == v && x["LIVE_SP_I_IN"] == int64(math.Trunc(v)*1000)
		})
	}

	// nautilus → groov Manage.
	for _, v := range []float64{12.5, -0.75} {
		if err := drv.WriteOutputs(map[string]any{"LIVE_SP_F_OUT": v, "LIVE_SP_I_OUT": int64(v * 100)}); err != nil {
			t.Fatal(err)
		}
		eventually(fmt.Sprintf("groov Manage to read %g written by nautilus", v), func() bool {
			f, err1 := api.get("float", fromNaut)
			i, err2 := api.get("int32", fromNaut)
			return err1 == nil && err2 == nil && f == v && i == math.Trunc(v*100)
		})
	}
}
