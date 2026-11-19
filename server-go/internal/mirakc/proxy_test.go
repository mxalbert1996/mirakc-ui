package mirakc

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestAPIURLAndEventsURL(t *testing.T) {
	cases := []struct {
		mirakcURL, api, events string
	}{
		{"http://mirakc:40772", "http://mirakc:40772/api", "http://mirakc:40772/events"},
		{"http://mirakc:40772/", "http://mirakc:40772/api", "http://mirakc:40772/events"},
		{"http://mirakc:40772///", "http://mirakc:40772/api", "http://mirakc:40772/events"},
	}
	for _, c := range cases {
		if got := APIURL(c.mirakcURL); got != c.api {
			t.Errorf("APIURL(%q) = %q, want %q", c.mirakcURL, got, c.api)
		}
		if got := EventsURL(c.mirakcURL); got != c.events {
			t.Errorf("EventsURL(%q) = %q, want %q", c.mirakcURL, got, c.events)
		}
	}
}

// mountedProxy serves the handler the way main.ts mounts it, so tests
// exercise the /api/mirakc prefix stripping.
func mountedProxy(mirakcURL string, hooks ProxyHooks) *httptest.Server {
	mux := http.NewServeMux()
	handler := NewProxy(mirakcURL, hooks, nil)
	mux.Handle("/api/mirakc", handler)
	mux.Handle("/api/mirakc/", handler)
	return httptest.NewServer(mux)
}

func TestProxyUnsetURLReturnsConvention500(t *testing.T) {
	srv := mountedProxy("", ProxyHooks{})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/mirakc/services")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if got := string(body); got != `{"error":"MIRAKC_URL is not set"}` {
		t.Errorf("body = %q, want %q", got, `{"error":"MIRAKC_URL is not set"}`)
	}
}

func TestProxyPassesMethodHeadersQueryBodyStatusThrough(t *testing.T) {
	var mu sync.Mutex
	var gotReq *http.Request
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotReq = r
		gotBody = body
		mu.Unlock()
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, "upstream body")
	}))
	defer upstream.Close()

	srv := mountedProxy(upstream.URL, ProxyHooks{})
	defer srv.Close()

	resp, err := http.Post(
		srv.URL+"/api/mirakc/services/1/stream?decode=1&x=%20",
		"application/json",
		bytes.NewReader([]byte(`{"programId":123}`)),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	mu.Lock()
	defer mu.Unlock()
	if gotReq == nil {
		t.Fatal("upstream saw no request")
	}
	if want := "/api/services/1/stream"; gotReq.URL.Path != want {
		t.Errorf("upstream path = %q, want %q", gotReq.URL.Path, want)
	}
	if want := "decode=1&x=%20"; gotReq.URL.RawQuery != want {
		t.Errorf("upstream query = %q, want %q", gotReq.URL.RawQuery, want)
	}
	if gotReq.Method != http.MethodPost {
		t.Errorf("upstream method = %q, want POST", gotReq.Method)
	}
	if got := gotReq.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("upstream content-type = %q, want application/json", got)
	}
	if string(gotBody) != `{"programId":123}` {
		t.Errorf("upstream body = %q, want the client body", gotBody)
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
	if got := resp.Header.Get("X-Upstream"); got != "yes" {
		t.Errorf("X-Upstream = %q, want yes", got)
	}
	if string(body) != "upstream body" {
		t.Errorf("body = %q, want %q", body, "upstream body")
	}
}

func TestProxyScheduleCreatedHookFiresOn2xxPost(t *testing.T) {
	scheduleJSON := `{"program":{"id":123,"name":"ニュース","startAt":0,"duration":1},"state":"scheduled"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, scheduleJSON)
	}))
	defer upstream.Close()

	created := make(chan CreatedSchedule, 1)
	hooks := ProxyHooks{
		OnScheduleCreated: func(s CreatedSchedule) { created <- s },
	}
	srv := mountedProxy(upstream.URL, hooks)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/mirakc/recording/schedules", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201", resp.StatusCode)
	}
	// The client still receives the full response body the hook parsed.
	if string(body) != scheduleJSON {
		t.Errorf("body = %q, want %q", body, scheduleJSON)
	}

	select {
	case s := <-created:
		if s.Program.ID != 123 {
			t.Errorf("hook program id = %d, want 123", s.Program.ID)
		}
		if s.Program.Name == nil || *s.Program.Name != "ニュース" {
			t.Errorf("hook program name = %v, want ニュース", s.Program.Name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onScheduleCreated never fired")
	}
}

func TestProxyHookNotFiredOnNon2xx(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "conflict", http.StatusConflict)
	}))
	defer upstream.Close()

	created := make(chan CreatedSchedule, 1)
	removed := make(chan int64, 1)
	hooks := ProxyHooks{
		OnScheduleCreated: func(CreatedSchedule) { created <- CreatedSchedule{} },
		OnScheduleRemoved: func(id int64) { removed <- id },
	}
	srv := mountedProxy(upstream.URL, hooks)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/mirakc/recording/schedules", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", resp.StatusCode)
	}

	time.Sleep(100 * time.Millisecond)
	select {
	case <-created:
		t.Error("onScheduleCreated fired on a non-2xx response")
	default:
	}
}

func TestProxyScheduleRemovedHookFires(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	removed := make(chan int64, 1)
	srv := mountedProxy(upstream.URL, ProxyHooks{
		OnScheduleRemoved: func(id int64) { removed <- id },
	})
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/mirakc/recording/schedules/327360102706471", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}

	select {
	case id := <-removed:
		if id != 327360102706471 {
			t.Errorf("hook program id = %d, want 327360102706471", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onScheduleRemoved never fired")
	}
}

func TestProxyRemovedHookSkipsNonSchedulePaths(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	removed := make(chan int64, 1)
	srv := mountedProxy(upstream.URL, ProxyHooks{
		OnScheduleRemoved: func(id int64) { removed <- id },
	})
	defer srv.Close()

	for _, path := range []string{
		"/api/mirakc/recording/schedules/abc",
		"/api/mirakc/recording/schedules/",
		"/api/mirakc/recording/schedules/123/extra",
		"/api/mirakc/other/1",
	} {
		req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	time.Sleep(100 * time.Millisecond)
	select {
	case id := <-removed:
		t.Errorf("onScheduleRemoved fired for a non-schedule path (id %d)", id)
	default:
	}
}

func TestProxyHookFailureDoesNotAffectResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"program":{"id":7}}`)
	}))
	defer upstream.Close()

	created := make(chan struct{}, 1)
	srv := mountedProxy(upstream.URL, ProxyHooks{
		OnScheduleCreated: func(CreatedSchedule) {
			defer func() { created <- struct{}{} }()
			panic("hook blew up")
		},
	})
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/mirakc/recording/schedules", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201 (hook must not affect the response)", resp.StatusCode)
	}
	if string(body) != `{"program":{"id":7}}` {
		t.Errorf("body = %q, want the upstream body", body)
	}
	select {
	case <-created:
	case <-time.After(2 * time.Second):
		t.Fatal("hook never ran")
	}
}

func TestProxyHookNotFiredWhenUnset(t *testing.T) {
	// A hook-eligible POST with no hook installed must still succeed; in
	// particular the unset hook must not change the code path.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"program":{"id":9}}`)
	}))
	defer upstream.Close()

	srv := mountedProxy(upstream.URL, ProxyHooks{})
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/mirakc/recording/schedules", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201", resp.StatusCode)
	}
	if string(body) != `{"program":{"id":9}}` {
		t.Errorf("body = %q, want the upstream body", body)
	}
}

func TestProxyStreamsLargeBodiesUnmodified(t *testing.T) {
	const size = 1 << 20
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(payload)
	}))
	defer upstream.Close()

	srv := mountedProxy(upstream.URL, ProxyHooks{})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/mirakc/docs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, payload) {
		t.Errorf("streamed body differs: got %d bytes, want %d", len(body), len(payload))
	}
	if got := resp.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("content-type = %q, want application/octet-stream", got)
	}
}
