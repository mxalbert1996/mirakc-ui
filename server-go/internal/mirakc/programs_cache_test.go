package mirakc

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newProgramsServer answers GET /programs with respond() and counts hits.
// Each handler call also waits for the release channel before
// responding, so tests can gate an in-flight fetch.
func newProgramsServer(t *testing.T, respond func(w http.ResponseWriter)) (srv *httptest.Server, release chan struct{}, fetches func() int32) {
	t.Helper()
	var count int32
	releaseCh := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/programs", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		<-releaseCh
		respond(w)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, releaseCh, func() int32 { return atomic.LoadInt32(&count) }
}

func respondJSON(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
}

func TestProgramsCacheSingleFlight(t *testing.T) {
	srv, release, fetches := newProgramsServer(t, respondJSON(`[]`))
	cache := NewProgramsCache(srv.URL, nil, time.Minute, func() time.Time { return time.Unix(0, 0) })

	const callers = 10
	results := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cache.GetPrograms()
			results <- err
		}()
	}
	// Let every caller pile onto the in-flight fetch, then release it.
	for fetches() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	for i := 0; i < callers; i++ {
		if err := <-results; err != nil {
			t.Fatalf("GetPrograms: %v", err)
		}
	}
	if got := fetches(); got != 1 {
		t.Errorf("upstream fetches = %d, want 1", got)
	}
}

func TestProgramsCacheWithinTTLNoRefetch(t *testing.T) {
	srv, release, fetches := newProgramsServer(t, respondJSON(`[]`))
	cache := NewProgramsCache(srv.URL, nil, time.Minute, func() time.Time { return time.Unix(0, 0) })

	close(release)
	if _, err := cache.GetPrograms(); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.GetPrograms(); err != nil {
		t.Fatal(err)
	}
	if got := fetches(); got != 1 {
		t.Errorf("upstream fetches = %d, want 1", got)
	}
}

func TestProgramsCacheTTLExpiryRefetches(t *testing.T) {
	srv, release, fetches := newProgramsServer(t, respondJSON(`[]`))
	now := int64(0)
	clock := func() time.Time { return time.UnixMilli(now) }
	cache := NewProgramsCache(srv.URL, nil, 1000*time.Millisecond, clock)

	close(release)
	if _, err := cache.GetPrograms(); err != nil {
		t.Fatal(err)
	}
	now = 500
	if _, err := cache.GetPrograms(); err != nil {
		t.Fatal(err)
	}
	if got := fetches(); got != 1 {
		t.Errorf("fetches before expiry = %d, want 1", got)
	}

	// fetchedAt + ttl == now -> expired (the freshness check is strict <).
	now = 1000
	if _, err := cache.GetPrograms(); err != nil {
		t.Fatal(err)
	}
	if got := fetches(); got != 2 {
		t.Errorf("fetches after expiry = %d, want 2", got)
	}
}

func TestProgramsCacheInvalidateRefetches(t *testing.T) {
	srv, release, fetches := newProgramsServer(t, respondJSON(`[]`))
	cache := NewProgramsCache(srv.URL, nil, time.Minute, func() time.Time { return time.Unix(0, 0) })

	close(release)
	if _, err := cache.GetPrograms(); err != nil {
		t.Fatal(err)
	}
	cache.Invalidate()
	if _, err := cache.GetPrograms(); err != nil {
		t.Fatal(err)
	}
	if got := fetches(); got != 2 {
		t.Errorf("upstream fetches = %d, want 2", got)
	}
}

func TestProgramsCacheGenerationGuard(t *testing.T) {
	srv, release, fetches := newProgramsServer(t, respondJSON(`[]`))
	cache := NewProgramsCache(srv.URL, nil, time.Minute, func() time.Time { return time.Unix(0, 0) })

	type result struct {
		programs []SlimProgram
		err      error
	}
	first := make(chan result, 1)
	go func() {
		programs, err := cache.GetPrograms()
		first <- result{programs, err}
	}()

	// The fetch is in flight; invalidate during it.
	for fetches() == 0 {
		time.Sleep(time.Millisecond)
	}
	cache.Invalidate()
	close(release)

	res := <-first
	if res.err != nil {
		t.Fatalf("in-flight GetPrograms: %v", res.err)
	}
	if got := fetches(); got != 1 {
		t.Fatalf("upstream fetches = %d, want 1", got)
	}

	// The invalidated fetch must not commit: the next call refetches.
	if _, err := cache.GetPrograms(); err != nil {
		t.Fatal(err)
	}
	if got := fetches(); got != 2 {
		t.Errorf("upstream fetches = %d, want 2 (stale data was committed)", got)
	}
}

func TestProgramsCacheFetchFailureLeavesNoEntry(t *testing.T) {
	var ok atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok.Load() {
			respondJSON(`[]`)(w)
			return
		}
		http.Error(w, "ng", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	cache := NewProgramsCache(upstream.URL, nil, time.Minute, func() time.Time { return time.Unix(0, 0) })

	_, err := cache.GetPrograms()
	if err == nil {
		t.Fatal("GetPrograms succeeded, want failure")
	}
	if want := fmt.Sprintf("GET %s/programs failed: 500", upstream.URL); err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}

	// No cache poison: the next call retries and succeeds.
	ok.Store(true)
	programs, err := cache.GetPrograms()
	if err != nil {
		t.Fatalf("retry GetPrograms: %v", err)
	}
	if len(programs) != 0 {
		t.Errorf("programs = %v, want empty", programs)
	}
}

func TestProgramsCacheSlimMapping(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{
			"id": 327360102706471,
			"eventId": 100,
			"isFree": true,
			"name": "ニュースウォッチ",
			"startAt": 1000,
			"duration": 1800000,
			"networkId": 32736,
			"serviceId": 1024,
			"genres": [{"lv1": 0, "lv2": 0, "un1": 0, "un2": 0}],
			"audio": {"componentType": 1},
			"audios": [],
			"description": "heavy field the slim form drops",
			"extended": {"heavy": "field"},
			"relatedItems": [],
			"series": {"heavy": true},
			"video": {"componentType": 2}
		}]`)
	}))
	defer upstream.Close()
	cache := NewProgramsCache(upstream.URL, nil, time.Minute, func() time.Time { return time.Unix(0, 0) })

	programs, err := cache.GetPrograms()
	if err != nil {
		t.Fatal(err)
	}
	name := "ニュースウォッチ"
	want := []SlimProgram{{
		ID:        327360102706471,
		EventID:   100,
		IsFree:    true,
		Name:      &name,
		StartAt:   1000,
		Duration:  1800000,
		NetworkID: 32736,
		ServiceID: 1024,
		Genres:    []SlimGenre{{Lv1: 0, Lv2: 0, Un1: 0, Un2: 0}},
	}}
	if !reflect.DeepEqual(programs, want) {
		t.Errorf("slim mapping = %+v, want %+v", programs, want)
	}
}
