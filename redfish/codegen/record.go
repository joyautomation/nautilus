// record.go is `browse --record`: walk a live service and keep what it
// served as a Tree, which WriteDir lays out as a DMTF mockup directory —
// the fixture `import --mockup`, `serve` and the tests consume.
package codegen

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/redfish"
	"github.com/joyautomation/nautilus/redfish/mockup"
)

// RecordOptions bound a walk.
type RecordOptions struct {
	// Max caps the resources fetched (default 2000): a BMC with a deep log
	// service or thousands of sensors must not turn a commissioning poke
	// into an hour of GETs.
	Max int
	// Progress, when set, is called after each fetch.
	Progress func(uri string, n int)
}

// skipped are subtrees a recording never follows: schema and registry
// files (megabytes, identical on every BMC), accounts and other people's
// sessions (who logs in is not ours to copy), log entries and task
// history (unbounded, and nothing the driver reads).
var skipped = []string{
	"/redfish/v1/JsonSchemas", "/redfish/v1/Registries", "/redfish/v1/$metadata", "/redfish/v1/odata",
	"/redfish/v1/AccountService", "/redfish/v1/CertificateService", "/redfish/v1/TaskService/Tasks/",
	"/redfish/v1/SessionService/Sessions/", "/redfish/v1/EventService/Subscriptions/",
}

func skip(uri string) bool {
	for _, p := range skipped {
		if uri == strings.TrimSuffix(p, "/") && strings.HasSuffix(p, "/") {
			continue // the collection itself is kept; its members are not
		}
		if strings.HasPrefix(uri, p) || uri == p {
			return true
		}
	}
	return strings.Contains(uri, "/Entries") || strings.Contains(uri, "/Certificates")
}

// Record walks the service breadth-first from the root, following every
// @odata.id under /redfish/v1 that is a resource (no #fragment) and not a
// skipped subtree. A resource the service refuses (404, 403) is simply
// absent from the recording — exactly how import treats it live.
func Record(ctx context.Context, f redfish.Fetcher, o RecordOptions) (mockup.Tree, []string, error) {
	if o.Max <= 0 {
		o.Max = 2000
	}
	t := mockup.Tree{}
	var notes []string
	queue := []string{mockup.Root}
	seen := map[string]bool{mockup.Root: true}
	for len(queue) > 0 && len(t) < o.Max {
		uri := queue[0]
		queue = queue[1:]
		resp, err := f.Get(ctx, uri)
		if err != nil {
			return nil, nil, err
		}
		if !resp.OK() {
			notes = append(notes, uri+": HTTP "+strconv.Itoa(resp.Status)+" (not recorded)")
			continue
		}
		doc, err := mockup.Decode(resp.Body)
		if err != nil {
			notes = append(notes, uri+": not JSON (not recorded)")
			continue
		}
		raw := json.RawMessage(resp.Body)
		if uri == redfish.SessionsPath {
			// Our own session (and anyone else's) is not part of the device.
			doc["Members"] = []any{}
			doc["Members@odata.count"] = 0
			if raw, err = json.Marshal(doc); err != nil {
				return nil, nil, err
			}
		}
		t[uri] = raw
		if o.Progress != nil {
			o.Progress(uri, len(t))
		}
		for _, l := range mockup.Links(doc) {
			l = mockup.Normalize(l)
			if seen[l] || strings.ContainsAny(l, "#?") || !strings.HasPrefix(l, mockup.Root+"/") || skip(l) {
				continue
			}
			if _, ok := mockup.RelPath(l); !ok {
				continue
			}
			seen[l] = true
			queue = append(queue, l)
		}
	}
	if len(queue) > 0 {
		notes = append(notes, "stopped at the resource cap; raise --max to record more")
	}
	return t, notes, nil
}
