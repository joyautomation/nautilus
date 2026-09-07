package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/hist"
)

// fakeHistStore is a histStore double so handleAgg/handleAt can be tested
// without a real Postgres — it records the arguments it was called with
// and returns whatever the test preloads, mirroring fakeSink's role for
// the collector side.
type fakeHistStore struct {
	aggQuery hist.AggQuery
	aggRows  []hist.AggRow
	aggErr   error
	snapTags []string
	snapAt   time.Time
	snapRows []hist.AggRow
	snapErr  error
}

func (f *fakeHistStore) Query(from, to time.Time, tags []string, maxPoints int) (map[string]hist.Series, error) {
	return nil, nil
}

func (f *fakeHistStore) Span() (first, last time.Time, count int64, err error) {
	return time.Time{}, time.Time{}, 0, nil
}

func (f *fakeHistStore) Aggregate(ctx context.Context, q hist.AggQuery) ([]hist.AggRow, error) {
	f.aggQuery = q
	return f.aggRows, f.aggErr
}

func (f *fakeHistStore) Snapshot(ctx context.Context, tags []string, at time.Time) ([]hist.AggRow, error) {
	f.snapTags = tags
	f.snapAt = at
	return f.snapRows, f.snapErr
}

var _ histStore = (*fakeHistStore)(nil)

// TestHandleAggRequiresTags confirms /history/agg without ?tags is
// rejected before it ever reaches the store, matching handleQuery's
// treatment of an empty tags param (empty series map) except agg needs at
// least one tag to mean anything.
func TestHandleAggRequiresTags(t *testing.T) {
	fake := &fakeHistStore{}
	srv := &historianServer{store: fake}
	req := httptest.NewRequest(http.MethodGet, "/history/agg?fn=avg", nil)
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// TestHandleAggRequiresFn confirms a missing ?fn is also rejected before
// hitting the store.
func TestHandleAggRequiresFn(t *testing.T) {
	fake := &fakeHistStore{}
	srv := &historianServer{store: fake}
	req := httptest.NewRequest(http.MethodGet, "/history/agg?tags=a,b", nil)
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// TestHandleAggRejectsBadBucket confirms an unparseable ?bucket duration
// is a 400, not a silently-ignored zero bucket.
func TestHandleAggRejectsBadBucket(t *testing.T) {
	fake := &fakeHistStore{}
	srv := &historianServer{store: fake}
	req := httptest.NewRequest(http.MethodGet, "/history/agg?tags=a&fn=avg&bucket=not-a-duration", nil)
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// TestHandleAggParsesQueryIntoAggQuery confirms the HTTP query string
// (tags, from/to as ms epoch, bucket as a Go duration, fn) is translated
// into the AggQuery the store sees, and that the store's []hist.AggRow
// result comes back as the documented [{tag, ts (ms epoch), value}] JSON.
func TestHandleAggParsesQueryIntoAggQuery(t *testing.T) {
	from := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 21, 6, 0, 0, 0, time.UTC)
	bucketStart := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	fake := &fakeHistStore{
		aggRows: []hist.AggRow{
			{Tag: "RES1.LIT", Ts: bucketStart, Value: 42.5},
		},
	}
	srv := &historianServer{store: fake}

	url := "/history/agg?tags=RES1.LIT,RES2.LIT&fn=max&bucket=1h&from=" +
		strconv.FormatInt(from.UnixMilli(), 10) + "&to=" + strconv.FormatInt(to.UnixMilli(), 10)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	wantQuery := hist.AggQuery{
		Tags:   []string{"RES1.LIT", "RES2.LIT"},
		From:   from,
		To:     to,
		Bucket: time.Hour,
		Fn:     "max",
	}
	if !reflect.DeepEqual(fake.aggQuery.Tags, wantQuery.Tags) ||
		!fake.aggQuery.From.Equal(wantQuery.From) ||
		!fake.aggQuery.To.Equal(wantQuery.To) ||
		fake.aggQuery.Bucket != wantQuery.Bucket ||
		fake.aggQuery.Fn != wantQuery.Fn {
		t.Fatalf("store saw AggQuery %+v, want %+v", fake.aggQuery, wantQuery)
	}

	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, w.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1: %v", len(got), got)
	}
	if got[0]["tag"] != "RES1.LIT" {
		t.Fatalf("row tag = %v, want RES1.LIT", got[0]["tag"])
	}
	if got[0]["value"] != 42.5 {
		t.Fatalf("row value = %v, want 42.5", got[0]["value"])
	}
	if got[0]["ts"] != float64(bucketStart.UnixMilli()) {
		t.Fatalf("row ts = %v, want %v (ms epoch, not RFC3339)", got[0]["ts"], bucketStart.UnixMilli())
	}
}

// TestHandleAggPropagatesStoreError confirms a store error surfaces as a
// 500, not a silently-empty result.
func TestHandleAggPropagatesStoreError(t *testing.T) {
	fake := &fakeHistStore{aggErr: errors.New("boom")}
	srv := &historianServer{store: fake}
	req := httptest.NewRequest(http.MethodGet, "/history/agg?tags=a&fn=avg", nil)
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

// TestHandleAtRequiresTags mirrors TestHandleAggRequiresTags for
// /history/at.
func TestHandleAtRequiresTags(t *testing.T) {
	fake := &fakeHistStore{}
	srv := &historianServer{store: fake}
	req := httptest.NewRequest(http.MethodGet, "/history/at", nil)
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// TestHandleAtParsesQueryAndReturnsSnapshot confirms ?tags/?at reach
// Snapshot correctly and the result comes back as ms-epoch JSON, e.g. a
// reservoir level snapshot at 6am.
func TestHandleAtParsesQueryAndReturnsSnapshot(t *testing.T) {
	at := time.Date(2026, 8, 22, 6, 0, 0, 0, time.UTC)
	snapTs := at.Add(-90 * time.Second) // nearest sample at-or-before at

	fake := &fakeHistStore{
		snapRows: []hist.AggRow{{Tag: "RES1.LIT", Ts: snapTs, Value: 118400}},
	}
	srv := &historianServer{store: fake}

	req := httptest.NewRequest(http.MethodGet, "/history/at?tags=RES1.LIT&at="+strconv.FormatInt(at.UnixMilli(), 10), nil)
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !reflect.DeepEqual(fake.snapTags, []string{"RES1.LIT"}) {
		t.Fatalf("store saw tags %v, want [RES1.LIT]", fake.snapTags)
	}
	if !fake.snapAt.Equal(at) {
		t.Fatalf("store saw at = %v, want %v", fake.snapAt, at)
	}

	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, w.Body.String())
	}
	if len(got) != 1 || got[0]["tag"] != "RES1.LIT" || got[0]["value"] != 118400.0 {
		t.Fatalf("got %v, want a single RES1.LIT=118400 row", got)
	}
	if got[0]["ts"] != float64(snapTs.UnixMilli()) {
		t.Fatalf("row ts = %v, want %v", got[0]["ts"], snapTs.UnixMilli())
	}
}

// TestHandleAtPropagatesStoreError mirrors TestHandleAggPropagatesStoreError.
func TestHandleAtPropagatesStoreError(t *testing.T) {
	fake := &fakeHistStore{snapErr: errors.New("boom")}
	srv := &historianServer{store: fake}
	req := httptest.NewRequest(http.MethodGet, "/history/at?tags=a", nil)
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

// TestExistingRoutesUnaffectedByStoreInterface is a light regression check
// that /history and /history/span still route and answer through the new
// histStore interface field, not just the two new endpoints.
func TestExistingRoutesUnaffectedByStoreInterface(t *testing.T) {
	fake := &fakeHistStore{}
	srv := &historianServer{store: fake}

	req := httptest.NewRequest(http.MethodGet, "/history/span", nil)
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/history/span status = %d, want 200", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/history?tags=a", nil)
	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/history status = %d, want 200", w.Code)
	}
}
