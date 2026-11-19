package comments

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const testServiceID int64 = 3273601024

const testServiceJSON = `{"id":3273601024,"serviceId":1024,"networkId":32736,` +
	`"name":"NHK総合・東京","channel":{"type":"GR","channel":"27"}}`

func mirakcStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == fmt.Sprintf("/api/services/%d", testServiceID) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, testServiceJSON)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newMux(deps Deps) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /api/comments/services/{id}/stream", NewHandler(deps))
	return mux
}

func streamURL(srv *httptest.Server, id string) string {
	return srv.URL + "/api/comments/services/" + id + "/stream"
}

// fakeSource is a channel-based Source: scripted comments are pushed and the
// channel closed on subscribe unless hold is set. It records targets and
// closes a per-subscription channel when the handler cancels the ctx.
type fakeSource struct {
	id          string
	unsupported bool
	comments    []SourceComment
	hold        bool

	onSub    func()
	mu       sync.Mutex
	targets  []Target
	ctxDones []chan struct{}
	chans    []chan SourceComment
}

func (f *fakeSource) ID() string { return f.id }

func (f *fakeSource) Subscribe(ctx context.Context, target Target) <-chan SourceComment {
	f.mu.Lock()
	f.targets = append(f.targets, target)
	done := make(chan struct{})
	f.ctxDones = append(f.ctxDones, done)
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		close(done)
	}()
	if f.onSub != nil {
		f.onSub()
	}
	if f.unsupported {
		return nil
	}
	ch := make(chan SourceComment, len(f.comments)+8)
	f.mu.Lock()
	f.chans = append(f.chans, ch)
	f.mu.Unlock()
	for _, c := range f.comments {
		ch <- c
	}
	if !f.hold {
		close(ch)
	}
	return ch
}

func (f *fakeSource) lastChan() chan SourceComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chans[len(f.chans)-1]
}

func (f *fakeSource) recordedTargets() []Target {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Target(nil), f.targets...)
}

func (f *fakeSource) waitCancelled(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	done := f.ctxDones[0]
	f.mu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subscription context was not cancelled")
	}
}

type sseEvent struct {
	name string
	data string
}

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	for _, block := range strings.Split(body, "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		var e sseEvent
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				e.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				e.data = strings.TrimPrefix(line, "data: ")
			}
		}
		events = append(events, e)
	}
	return events
}

func decodeErrorBody(t *testing.T, res *http.Response) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decoding error body: %v", err)
	}
	return body.Error
}

// readUntil accumulates from body until want appears in it.
func readUntil(t *testing.T, body io.Reader, want string, timeout time.Duration) string {
	t.Helper()
	var acc []byte
	buf := make([]byte, 4096)
	deadline := time.Now().Add(timeout)
	for {
		if strings.Contains(string(acc), want) {
			return string(acc)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q; got:\n%s", want, acc)
		}
		n, err := body.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
		}
		if err != nil {
			t.Fatalf("stream ended before %q; got:\n%s", want, acc)
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestMIRAKCURLUnsetReturns503(t *testing.T) {
	srv := httptest.NewServer(newMux(Deps{}))
	defer srv.Close()

	res, err := srv.Client().Get(streamURL(srv, "1"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", res.StatusCode)
	}
	if msg := decodeErrorBody(t, res); msg != "MIRAKC_URL is not set" {
		t.Fatalf("error = %q", msg)
	}
}

func TestInvalidServiceIDReturns400(t *testing.T) {
	srv := httptest.NewServer(newMux(Deps{APIURL: mirakcStub(t).URL + "/api"}))
	defer srv.Close()

	for _, id := range []string{"abc", "1.5", "99999999999999999999"} {
		res, err := srv.Client().Get(streamURL(srv, id))
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusBadRequest {
			res.Body.Close()
			t.Fatalf("id %q: status = %d, want 400", id, res.StatusCode)
		}
		if msg := decodeErrorBody(t, res); msg != "invalid service id" {
			res.Body.Close()
			t.Fatalf("id %q: error = %q", id, msg)
		}
		res.Body.Close()
	}
}

func TestServiceNotFoundReturns404(t *testing.T) {
	srv := httptest.NewServer(newMux(Deps{APIURL: mirakcStub(t).URL + "/api"}))
	defer srv.Close()

	res, err := srv.Client().Get(streamURL(srv, "999"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
	if msg := decodeErrorBody(t, res); msg != "service not found" {
		t.Fatalf("error = %q", msg)
	}
}

func TestMirakcUnreachableReturns404(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close()

	srv := httptest.NewServer(newMux(Deps{APIURL: dead.URL + "/api"}))
	defer srv.Close()

	res, err := srv.Client().Get(streamURL(srv, "1"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
	if msg := decodeErrorBody(t, res); msg != "service not found" {
		t.Fatalf("error = %q", msg)
	}
}

func TestSourcesAndCommentsSSEFlow(t *testing.T) {
	comments := []SourceComment{
		{ID: "a", Source: "nicolive", At: 1718240401000, Text: "one"},
		{ID: "b", Source: "nicolive", At: 1718240402000, Text: "two", Author: "名前"},
	}
	nicolive := &fakeSource{id: "nicolive", comments: comments}
	unsupported := &fakeSource{id: "nx-jikkyo", unsupported: true}

	srv := httptest.NewServer(newMux(Deps{
		APIURL:  mirakcStub(t).URL + "/api",
		Sources: []Source{nicolive, unsupported},
	}))
	defer srv.Close()

	res, err := srv.Client().Get(streamURL(srv, fmt.Sprint(testServiceID)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}

	wantTarget := Target{
		ID:          testServiceID,
		NetworkID:   32736,
		ServiceID:   1024,
		Channel:     "27",
		ServiceName: "NHK総合・東京",
	}
	// The unsupported source is subscribed too (comments.ts filters only
	// after calling subscribe on every source).
	for _, src := range []*fakeSource{nicolive, unsupported} {
		targets := src.recordedTargets()
		if len(targets) != 1 || targets[0] != wantTarget {
			t.Fatalf("%s targets = %+v, want %+v", src.id, targets, wantTarget)
		}
	}

	events := parseSSE(t, string(body))
	if len(events) != 3 {
		t.Fatalf("events = %d (%+v), want 3", len(events), events)
	}
	if events[0] != (sseEvent{"sources", `["nicolive"]`}) {
		t.Fatalf("first event = %+v, want sources [\"nicolive\"]", events[0])
	}
	if events[1] != (sseEvent{"comment", `{"id":"a","source":"nicolive","at":1718240401000,"text":"one"}`}) {
		t.Fatalf("second event = %+v", events[1])
	}
	if events[2] != (sseEvent{"comment", `{"id":"b","source":"nicolive","at":1718240402000,"text":"two","author":"名前"}`}) {
		t.Fatalf("third event = %+v", events[2])
	}
}

func TestNoSourcesYieldsEmptySourcesList(t *testing.T) {
	unsupported := &fakeSource{id: "nicolive", unsupported: true}
	srv := httptest.NewServer(newMux(Deps{
		APIURL:  mirakcStub(t).URL + "/api",
		Sources: []Source{unsupported},
	}))
	defer srv.Close()

	res, err := srv.Client().Get(streamURL(srv, fmt.Sprint(testServiceID)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	events := parseSSE(t, string(body))
	if len(events) != 1 || events[0] != (sseEvent{"sources", "[]"}) {
		t.Fatalf("events = %+v, want a single sources []", events)
	}
}

func TestPerSourceOrderPreservedWithBothSources(t *testing.T) {
	a := &fakeSource{id: "nicolive", comments: []SourceComment{
		{ID: "a1", Source: "nicolive", At: 1, Text: "a1"},
		{ID: "a2", Source: "nicolive", At: 2, Text: "a2"},
		{ID: "a3", Source: "nicolive", At: 3, Text: "a3"},
	}}
	b := &fakeSource{id: "nx-jikkyo", comments: []SourceComment{
		{ID: "b1", Source: "nx-jikkyo", At: 1, Text: "b1"},
		{ID: "b2", Source: "nx-jikkyo", At: 2, Text: "b2"},
	}}
	srv := httptest.NewServer(newMux(Deps{
		APIURL:  mirakcStub(t).URL + "/api",
		Sources: []Source{a, b},
	}))
	defer srv.Close()

	res, err := srv.Client().Get(streamURL(srv, fmt.Sprint(testServiceID)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}

	events := parseSSE(t, string(body))
	if len(events) != 6 {
		t.Fatalf("events = %d, want 6", len(events))
	}
	if events[0] != (sseEvent{"sources", `["nicolive","nx-jikkyo"]`}) {
		t.Fatalf("first event = %+v", events[0])
	}
	var aOrder, bOrder []string
	for _, e := range events[1:] {
		if e.name != "comment" {
			t.Fatalf("unexpected event %+v", e)
		}
		var c SourceComment
		if err := json.Unmarshal([]byte(e.data), &c); err != nil {
			t.Fatalf("bad comment event %q: %v", e.data, err)
		}
		switch c.Source {
		case "nicolive":
			aOrder = append(aOrder, c.ID)
		case "nx-jikkyo":
			bOrder = append(bOrder, c.ID)
		}
	}
	if strings.Join(aOrder, ",") != "a1,a2,a3" {
		t.Fatalf("nicolive order = %v", aOrder)
	}
	if strings.Join(bOrder, ",") != "b1,b2" {
		t.Fatalf("nx-jikkyo order = %v", bOrder)
	}
}

func TestPingKeepAlive(t *testing.T) {
	src := &fakeSource{id: "nicolive", hold: true}
	srv := httptest.NewServer(newMux(Deps{
		APIURL:    mirakcStub(t).URL + "/api",
		Sources:   []Source{src},
		KeepAlive: 25 * time.Millisecond,
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		streamURL(srv, fmt.Sprint(testServiceID)), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	acc := readUntil(t, res.Body, "event: ping", 5*time.Second)
	events := parseSSE(t, acc)
	if len(events) == 0 || events[0] != (sseEvent{"sources", `["nicolive"]`}) {
		t.Fatalf("first event = %+v, want sources [\"nicolive\"]", events[0:])
	}

	cancel()
	src.waitCancelled(t)
}

func TestSourceClosingEarlyEndsOnlyItsPump(t *testing.T) {
	early := &fakeSource{id: "nx-jikkyo", comments: []SourceComment{
		{ID: "a", Source: "nx-jikkyo", At: 1, Text: "from early"},
	}}
	late := &fakeSource{id: "nicolive", hold: true}
	srv := httptest.NewServer(newMux(Deps{
		APIURL:  mirakcStub(t).URL + "/api",
		Sources: []Source{early, late},
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		streamURL(srv, fmt.Sprint(testServiceID)), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	acc := readUntil(t, res.Body, `"text":"from early"`, 5*time.Second)

	// early's channel is closed; late's pump must still be delivering.
	late.lastChan() <- SourceComment{ID: "b", Source: "nicolive", At: 2, Text: "from late"}
	acc += readUntil(t, res.Body, `"text":"from late"`, 5*time.Second)
	if !strings.Contains(acc, `"text":"from early"`) {
		t.Fatalf("early comment missing from stream:\n%s", acc)
	}

	cancel()
	late.waitCancelled(t)
	early.waitCancelled(t)
}

func TestClientDisconnectCancelsSubscriptionsAndReturns(t *testing.T) {
	src := &fakeSource{id: "nicolive", hold: true}
	subscribed := make(chan struct{})
	var once sync.Once
	src.onSub = func() { once.Do(func() { close(subscribed) }) }

	h := NewHandler(Deps{APIURL: mirakcStub(t).URL + "/api", Sources: []Source{src}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet,
		"/api/comments/services/"+fmt.Sprint(testServiceID)+"/stream", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h(rec, req)
		close(done)
	}()

	<-subscribed
	select {
	case <-done:
		t.Fatal("handler returned before the client disconnected")
	default:
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after client disconnect")
	}
	src.waitCancelled(t)

	events := parseSSE(t, rec.Body.String())
	if len(events) == 0 || events[0] != (sseEvent{"sources", `["nicolive"]`}) {
		t.Fatalf("first event = %+v, want sources [\"nicolive\"]", events)
	}
}

func TestNoGoroutineLeakAfterDisconnect(t *testing.T) {
	warmup := &fakeSource{id: "nicolive", comments: []SourceComment{
		{ID: "w", Source: "nicolive", At: 1, Text: "warmup"},
	}}
	src := &fakeSource{id: "nicolive", hold: true}
	srv := httptest.NewServer(newMux(Deps{
		APIURL:    mirakcStub(t).URL + "/api",
		Sources:   []Source{src},
		KeepAlive: 20 * time.Millisecond,
	}))
	defer srv.Close()

	// Warm the client transport so its pooled-connection goroutines are
	// counted in the baseline.
	func() {
		srv2 := httptest.NewServer(newMux(Deps{
			APIURL:  mirakcStub(t).URL + "/api",
			Sources: []Source{warmup},
		}))
		defer srv2.Close()
		res, err := srv.Client().Get(streamURL(srv2, fmt.Sprint(testServiceID)))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if _, err := io.Copy(io.Discard, res.Body); err != nil {
			t.Fatal(err)
		}
	}()

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		streamURL(srv, fmt.Sprint(testServiceID)), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, res.Body, "event: ping", 5*time.Second)
	cancel()
	res.Body.Close()

	src.waitCancelled(t)
	waitFor(t, 5*time.Second, func() bool {
		return runtime.NumGoroutine() <= before+2
	})
}
