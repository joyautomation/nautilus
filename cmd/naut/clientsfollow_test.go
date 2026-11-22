package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// apiDo is one request to member's tag API with the rig's token.
func (r *boxRig) apiDo(t *testing.T, member, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, r.apiURL[member]+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer api-secret")
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s on %s: %v", method, path, member, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

type clusterView struct {
	Leader string `json:"leader"`
	Peers  []struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		Leader bool   `json:"leader"`
	} `json:"peers"`
}

func (r *boxRig) cluster(t *testing.T, member string) clusterView {
	t.Helper()
	code, body := r.apiDo(t, member, "GET", "/api/cluster", "")
	if code != 200 {
		t.Fatalf("GET /api/cluster on %s: %d %s", member, code, body)
	}
	var v clusterView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("/api/cluster on %s: %v: %s", member, err, body)
	}
	return v
}

// A client of a real pair (two controllers and a witness), through a
// takeover (#716, test plan §9 case 1): each controller lists both
// addresses and who leads, the same on both; a client sent to the leader
// writes and reads back; after the takeover both name the new leader, the
// same holds there, and the time from the
// takeover until a client that asked the old leader's peer would be sent
// right (t_api) is measured. (The browser's own following is the kit's,
// tested in hmi/tests/auth.test.ts and live in the rig.)
func TestClientsOfARealPairFollowATakeover(t *testing.T) {
	r := newBoxRigAPI(t)
	check := func(leader string) {
		t.Helper()
		for _, asked := range []string{"a", "b"} {
			v := r.cluster(t, asked)
			if v.Leader != leader || len(v.Peers) != 2 {
				t.Fatalf("asked %s: leader %q, peers %+v; want %s leading and both listed", asked, v.Leader, v.Peers, leader)
			}
			for _, p := range v.Peers {
				if p.URL != r.apiURL[p.Name] || p.Leader != (p.Name == leader) {
					t.Errorf("asked %s: peer %+v: want url %s, leader %v", asked, p, r.apiURL[p.Name], p.Name == leader)
				}
			}
		}
		// a client sent to the leader writes and reads back
		val := map[string]string{"a": "11", "b": "22"}[leader]
		if code, body := r.apiDo(t, leader, "POST", "/api/tags", `{"name":"SP","value":`+val+`}`); code/100 != 2 {
			t.Fatalf("write to the leader %s: %d %s", leader, code, body)
		}
		pairWait(t, "the leader's store to hold the write", 5*time.Second, func() bool {
			_, st := r.apiDo(t, leader, "GET", "/api/state", "")
			return strings.Contains(strings.ReplaceAll(st, " ", ""), `"SP":`+val) || strings.Contains(strings.ReplaceAll(st, " ", ""), `"SP":{"value":`+val)
		})
	}
	r.handLeadTo("a")
	check("a")

	t0 := time.Now()
	r.handLeadTo("b")
	pairWait(t, "both controllers to name b", 10*time.Second, func() bool {
		return r.cluster(t, "a").Leader == "b" && r.cluster(t, "b").Leader == "b"
	})
	t.Logf("t_api (takeover to both controllers naming the new leader): %v", time.Since(t0).Round(time.Millisecond))
	check("b")
}
