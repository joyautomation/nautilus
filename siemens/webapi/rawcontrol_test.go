package webapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/webapi"
	"github.com/joyautomation/nautilus/siemens/webapi/webapitest"
)

// TestDiagnosticBufferWithRawNewlinesInStrings: a real 1510SP (FW 3.1)
// sends raw 0x0A bytes inside long_text and help_text (#1334). The reply
// is served byte for byte from testdata; login goes to the fake CPU.
func TestDiagnosticBufferWithRawNewlinesInStrings(t *testing.T) {
	fixture, err := os.ReadFile("testdata/diag-browse-fw31-raw-newlines.json")
	if err != nil {
		t.Fatal(err)
	}
	fake := webapitest.New()
	fake.Users["naut"] = webapitest.User{Password: "pw", Permissions: []string{"read_value"}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var rq struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &rq) == nil && rq.Method == "DiagnosticBuffer.Browse" {
			w.Header().Set("Content-Type", "application/json")
			w.Write(bytes.Replace(fixture, []byte(`"id":3`), []byte(`"id":`+strconv.Itoa(rq.ID)), 1))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	c, err := webapi.New(srv.URL, webapi.Options{User: "naut", Password: "pw", CertSHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.DiagnosticBuffer(context.Background(), "en-US", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Entries) != 3 || b.Count != 579 || b.Max != 1000 {
		t.Fatalf("entries %d, count %d, max %d", len(b.Entries), b.Count, b.Max)
	}
	if lt := b.Entries[0].LongText; !strings.Contains(lt, "\n") || !strings.Contains(lt, "Client IPAddr: 192.0.2.201") {
		t.Fatalf("long text = %q", lt)
	}
}
