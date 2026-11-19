// Tests for the nicolive source, port of
// server/lib/comments/sources/nicolive.test.ts plus the Go-specific
// watchdog / cancellation / leak coverage. The shared WebSocket fakes and
// receive helpers are also used by nxjikkyo_test.go.
package comments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// --- hand-crafted NDGR protobuf builders (port of ndgr-fixtures.ts,
// mirroring internal/comments/ndgr/fixtures_test.go, which is test-scoped
// and not importable from this package) ---

func ndgrVarint(value uint64) []byte {
	var out []byte
	for value >= 0x80 {
		out = append(out, byte(value&0x7f)|0x80)
		value >>= 7
	}
	return append(out, byte(value))
}

func ndgrCat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func ndgrLenField(fieldNo int32, payload []byte) []byte {
	return ndgrCat(ndgrVarint(uint64(fieldNo)<<3|2), ndgrVarint(uint64(len(payload))), payload)
}

func ndgrVarintField(fieldNo int32, value uint64) []byte {
	return ndgrCat(ndgrVarint(uint64(fieldNo)<<3), ndgrVarint(value))
}

func ndgrStringField(fieldNo int32, value string) []byte {
	return ndgrLenField(fieldNo, []byte(value))
}

// ndgrLengthDelimitedOf prefixes each message with a varint length and
// concatenates them (the View / Segment API response format).
func ndgrLengthDelimitedOf(messages ...[]byte) []byte {
	var out []byte
	for _, m := range messages {
		out = ndgrCat(out, ndgrVarint(uint64(len(m))), m)
	}
	return out
}

// ndgrSegmentEntryOf builds ChunkedEntry { segment(1): MessageSegment { uri(3) } }.
func ndgrSegmentEntryOf(uri string) []byte {
	return ndgrLenField(1, ndgrStringField(3, uri))
}

// ndgrNextEntryOf builds ChunkedEntry { next(4): ReadyForNext { at(1) } }.
func ndgrNextEntryOf(atSeconds int64) []byte {
	return ndgrLenField(4, ndgrVarintField(1, uint64(atSeconds)))
}

// ndgrChatMessageOf builds ChunkedMessage { meta(1): { id(1), at(2) },
// message(2): { chat(1): { content(1), name(2)? } } }.
func ndgrChatMessageOf(id string, atSeconds int64, atNanos *int64, content, name *string) []byte {
	var atNanosField []byte
	if atNanos != nil {
		atNanosField = ndgrVarintField(2, uint64(*atNanos))
	}
	meta := ndgrCat(
		ndgrStringField(1, id),
		ndgrLenField(2, ndgrCat(ndgrVarintField(1, uint64(atSeconds)), atNanosField)),
	)
	var chat []byte
	if content != nil {
		var nameField []byte
		if name != nil {
			nameField = ndgrStringField(2, *name)
		}
		chat = ndgrCat(ndgrStringField(1, *content), nameField)
		return ndgrCat(ndgrLenField(1, meta), ndgrLenField(2, ndgrLenField(1, chat)))
	}
	return ndgrLenField(1, meta)
}

// --- scripted WebSocket fake (Go counterpart of the Deno tests' fake
// sockets whose send() reacts to frames) ---

// fakeConn serves pushed messages in order via Reader; Writer records
// frames and optionally dispatches them to a script.
type fakeConn struct {
	mu        sync.Mutex
	msgs      chan []byte
	writes    []string
	closed    bool
	script    func(c *fakeConn, frame string)
	closeOnce sync.Once
	closedCh  chan struct{}
}

func newFakeConn(script func(*fakeConn, string)) *fakeConn {
	return &fakeConn{
		msgs:     make(chan []byte, 32),
		closedCh: make(chan struct{}),
		script:   script,
	}
}

func (c *fakeConn) push(msg string) { c.msgs <- []byte(msg) }

func (c *fakeConn) Reader(ctx context.Context) (websocket.MessageType, io.Reader, error) {
	// Deliver buffered messages before observing the close, like a real
	// socket draining its receive buffer.
	select {
	case msg := <-c.msgs:
		return websocket.MessageText, bytes.NewReader(msg), nil
	default:
	}
	select {
	case msg := <-c.msgs:
		return websocket.MessageText, bytes.NewReader(msg), nil
	case <-c.closedCh:
		return 0, nil, errors.New("fake connection closed")
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (c *fakeConn) Writer(ctx context.Context, typ websocket.MessageType) (io.WriteCloser, error) {
	return &fakeConnWriter{conn: c}, nil
}

type fakeConnWriter struct {
	conn *fakeConn
	buf  bytes.Buffer
}

func (w *fakeConnWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *fakeConnWriter) Close() error {
	c := w.conn
	c.mu.Lock()
	frame := w.buf.String()
	c.writes = append(c.writes, frame)
	script := c.script
	c.mu.Unlock()
	if script != nil {
		script(c, frame)
	}
	return nil
}

func (c *fakeConn) Close() error {
	c.closeOnce.Do(func() { close(c.closedCh) })
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *fakeConn) sentFrames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.writes...)
}

func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// watchSessionScript reacts like the watch-session server: startWatching
// gets the messageServer reply carrying viewURI, ping gets a pong request.
func watchSessionScript(viewURI string) func(*fakeConn, string) {
	return func(c *fakeConn, frame string) {
		var m struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(frame), &m) != nil {
			return
		}
		switch m.Type {
		case "startWatching":
			c.push(fmt.Sprintf(`{"type":"messageServer","data":{"viewUri":%s}}`, strconvQuote(viewURI)))
		}
	}
}

func strconvQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// --- receive / drain helpers shared with nxjikkyo_test.go ---

func recvComment(t *testing.T, ch <-chan SourceComment) SourceComment {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatal("subscription channel closed before the expected comment")
		}
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a comment")
		return SourceComment{}
	}
}

func drainComments(t *testing.T, ch <-chan SourceComment) []SourceComment {
	t.Helper()
	var got []SourceComment
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, c)
		case <-time.After(5 * time.Second):
			t.Fatal("subscription channel did not close")
			return nil
		}
	}
}

func assertNoGoroutineLeak(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak: %d running > baseline %d", runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- ParseEmbeddedData (direct ports of the Deno parseEmbeddedData tests) ---

func escapeHTMLAttr(value string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func watchPageOf(t *testing.T, props any) string {
	t.Helper()
	propsJSON, err := json.Marshal(props)
	if err != nil {
		t.Fatal(err)
	}
	return "<!DOCTYPE html><html><body>" +
		`<script id="embedded-data" data-props="` + escapeHTMLAttr(string(propsJSON)) + `"></script>` +
		"</body></html>"
}

func TestParseEmbeddedDataExtractsWebSocketURLAndStatus(t *testing.T) {
	// The wss URL contains "&", so this also checks entity restoration.
	html := watchPageOf(t, map[string]any{
		"site":    map[string]any{"relive": map[string]any{"webSocketUrl": "wss://a.live2.nicovideo.jp/wsapi?x=1&y=2"}},
		"program": map[string]any{"status": "ON_AIR"},
	})
	info, err := ParseEmbeddedData(html)
	if err != nil {
		t.Fatal(err)
	}
	want := NicoliveProgramInfo{WebSocketURL: "wss://a.live2.nicovideo.jp/wsapi?x=1&y=2", Status: "ON_AIR"}
	if info != want {
		t.Errorf("got %+v; want %+v", info, want)
	}
}

func TestParseEmbeddedDataTagAndAttributeOrderIndependent(t *testing.T) {
	propsJSON, err := json.Marshal(map[string]any{"program": map[string]any{"status": "ON_AIR"}})
	if err != nil {
		t.Fatal(err)
	}
	html := `<div class="x" data-props="` + escapeHTMLAttr(string(propsJSON)) + `" id="embedded-data"></div>`
	info, err := ParseEmbeddedData(html)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != "ON_AIR" {
		t.Errorf("status = %q; want ON_AIR", info.Status)
	}
}

func TestParseEmbeddedDataErrorsWhenMissing(t *testing.T) {
	if _, err := ParseEmbeddedData("<html></html>"); err == nil {
		t.Fatal("expected an error for a watch page without embedded-data")
	}
}

func TestParseEmbeddedDataAbsentFieldsAreEmpty(t *testing.T) {
	info, err := ParseEmbeddedData(watchPageOf(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if info != (NicoliveProgramInfo{}) {
		t.Errorf("got %+v; want the zero value", info)
	}
}

// --- watch session (fetchViewURI) ---

func watchCfg(script func(*fakeConn, string), timeout time.Duration) (*nicoliveConfig, **fakeConn) {
	conn := newFakeConn(script)
	var dialed *fakeConn
	cfg := &nicoliveConfig{
		Dial: func(ctx context.Context, url string) (wsConn, error) {
			dialed = conn
			return conn, nil
		},
		WatchSessionTimeout: timeout,
	}
	return cfg, &dialed
}

func TestFetchViewURIResolvesMessageServer(t *testing.T) {
	cfg, dialed := watchCfg(watchSessionScript("https://mpn.example/api/view/v4/abc"), 5*time.Second)
	uri, err := fetchViewURI(context.Background(), cfg, "wss://watch.example/session")
	if err != nil {
		t.Fatal(err)
	}
	if uri != "https://mpn.example/api/view/v4/abc" {
		t.Errorf("uri = %q", uri)
	}
	frames := (*dialed).sentFrames()
	if len(frames) != 1 {
		t.Fatalf("frames = %v; want exactly startWatching", frames)
	}
	var m struct {
		Type string `json:"type"`
		Data struct {
			Reconnect bool `json:"reconnect"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &m); err != nil {
		t.Fatal(err)
	}
	if m.Type != "startWatching" || m.Data.Reconnect {
		t.Errorf("frame = %q; want startWatching with reconnect=false", frames[0])
	}
	if !(*dialed).isClosed() {
		t.Error("watch session was not closed after the uri arrived")
	}
}

func TestFetchViewURIRespondsPingWithPong(t *testing.T) {
	script := func(c *fakeConn, frame string) {
		var m struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(frame), &m) != nil {
			return
		}
		switch m.Type {
		case "startWatching":
			c.push(`{"type":"ping"}`)
		case "pong":
			c.push(`{"type":"messageServer","data":{"viewUri":"https://mpn.example/api/view/v4/abc"}}`)
		}
	}
	cfg, dialed := watchCfg(script, 5*time.Second)
	uri, err := fetchViewURI(context.Background(), cfg, "wss://watch.example/session")
	if err != nil {
		t.Fatal(err)
	}
	if uri != "https://mpn.example/api/view/v4/abc" {
		t.Errorf("uri = %q", uri)
	}
	frames := (*dialed).sentFrames()
	want := []string{
		`{"type":"startWatching","data":{"reconnect":false}}`,
		`{"type":"pong"}`,
	}
	if !reflect.DeepEqual(frames, want) {
		t.Errorf("frames = %v; want %v", frames, want)
	}
}

func TestFetchViewURIServerErrorAndDisconnect(t *testing.T) {
	for _, typ := range []string{"error", "disconnect"} {
		t.Run(typ, func(t *testing.T) {
			script := func(c *fakeConn, frame string) {
				var m struct {
					Type string `json:"type"`
				}
				if json.Unmarshal([]byte(frame), &m) == nil && m.Type == "startWatching" {
					c.push(fmt.Sprintf(`{"type":%s}`, strconvQuote(typ)))
				}
			}
			cfg, _ := watchCfg(script, 5*time.Second)
			_, err := fetchViewURI(context.Background(), cfg, "wss://watch.example/session")
			if err == nil {
				t.Fatal("expected an error")
			}
			if err.Error() != "watch session: "+typ {
				t.Errorf("error = %q", err)
			}
		})
	}
}

func TestFetchViewURITimesOut(t *testing.T) {
	cfg, _ := watchCfg(nil, 30*time.Millisecond)
	start := time.Now()
	_, err := fetchViewURI(context.Background(), cfg, "wss://watch.example/session")
	if err == nil || err.Error() != "watch session timeout" {
		t.Fatalf("error = %v; want watch session timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout took %v", elapsed)
	}
}

func TestFetchViewURIAbortsOnContextCancel(t *testing.T) {
	cfg, _ := watchCfg(nil, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		uri string
		err error
	}
	done := make(chan result, 1)
	go func() {
		uri, err := fetchViewURI(ctx, cfg, "wss://watch.example/session")
		done <- result{uri, err}
	}()
	cancel()
	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) {
			t.Errorf("error = %v; want context.Canceled", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fetchViewURI did not return after ctx cancellation")
	}
}

// --- subscribe-level (ports of the createNicoliveSource Deno tests) ---

func fetchResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body))}
}

// blockingBody never delivers bytes and unblocks on the fetch ctx, like
// the abortedRejection helper in the Deno tests.
type blockingBody struct{ ctx context.Context }

func (b *blockingBody) Read(p []byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func strp(s string) *string { return &s }
func nump(n int64) *int64   { return &n }

func TestNicoliveSubscribeUnsupportedTarget(t *testing.T) {
	// A channel with no nicolive mapping in the configuration.
	source := NewNicoliveSource(func(Target) (string, bool) { return "", false })
	ch := source.Subscribe(context.Background(), Target{ID: 3273601024, NetworkID: 0x7fe0, ServiceID: 0x0400})
	if ch != nil {
		t.Fatal("subscribe should return nil for an unsupported target")
	}
}

func TestNicoliveWatchPageToNDGRFlow(t *testing.T) {
	const viewURI = "https://mpn.example/api/view/v4/abc"
	watchHTML := watchPageOf(t, map[string]any{
		"site":    map[string]any{"relive": map[string]any{"webSocketUrl": "wss://watch.example/session"}},
		"program": map[string]any{"status": "ON_AIR"},
	})

	var mu sync.Mutex
	var requested []string
	viewNow := ndgrLengthDelimitedOf(
		ndgrSegmentEntryOf("https://mpn.example/segment/1"),
		ndgrNextEntryOf(1718240416),
	)
	segment := ndgrLengthDelimitedOf(
		ndgrChatMessageOf("m1", 1718240401, nil, strp("こんにちは"), nil),
		// duplicate id: suppressed by the seen set
		ndgrChatMessageOf("m1", 1718240401, nil, strp("こんにちは"), nil),
		ndgrChatMessageOf("m2", 1718240402, nump(250_000_000), strp("named"), strp("テスト太郎")),
	)
	fetch := func(ctx context.Context, url string) (*http.Response, error) {
		mu.Lock()
		requested = append(requested, url)
		mu.Unlock()
		switch url {
		case "https://live.nicovideo.jp/watch/ch2646436":
			return fetchResponse(200, []byte(watchHTML)), nil
		case viewURI + "?at=now":
			return fetchResponse(200, viewNow), nil
		case viewURI + "?at=1718240416":
			// the second View round stays pending until aborted
			<-ctx.Done()
			return nil, ctx.Err()
		case "https://mpn.example/segment/1":
			return fetchResponse(200, segment), nil
		}
		return fetchResponse(404, []byte("not found")), nil
	}

	var dialed []*fakeConn
	dial := func(ctx context.Context, url string) (wsConn, error) {
		if url != "wss://watch.example/session" {
			t.Errorf("dial url = %q", url)
		}
		conn := newFakeConn(watchSessionScript(viewURI))
		dialed = append(dialed, conn)
		return conn, nil
	}

	source := NewNicoliveSource(
		func(Target) (string, bool) { return "ch2646436", true },
		WithNicoliveFetch(fetch),
		WithNicoliveDial(dial),
	)

	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	ch := source.Subscribe(ctx, Target{ID: 3273601024, NetworkID: 0x7fe0, ServiceID: 0x0400})
	if ch == nil {
		t.Fatal("subscribe returned nil for a supported target")
	}

	got := []SourceComment{recvComment(t, ch), recvComment(t, ch)}
	want := []SourceComment{
		{ID: "m1", Source: "nicolive", At: 1718240401000, Text: "こんにちは"},
		{ID: "m2", Source: "nicolive", At: 1718240402250, Text: "named", Author: "テスト太郎"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("comments = %+v; want %+v", got, want)
	}

	cancel()
	if rest := drainComments(t, ch); len(rest) != 0 {
		t.Errorf("unexpected extra comments %+v", rest)
	}

	if len(dialed) != 1 {
		t.Fatalf("watch sessions = %d; want 1", len(dialed))
	}
	if frames := dialed[0].sentFrames(); len(frames) == 0 {
		t.Error("no startWatching was sent")
	}
	if !dialed[0].isClosed() {
		t.Error("watch session was not closed")
	}
	mu.Lock()
	hasNow, hasNext := false, false
	for _, url := range requested {
		if url == viewURI+"?at=now" {
			hasNow = true
		}
		if url == viewURI+"?at=1718240416" {
			hasNext = true
		}
	}
	mu.Unlock()
	if !hasNow || !hasNext {
		t.Errorf("view fetches = %v; want both ?at=now and ?at=<next>", requested)
	}
	assertNoGoroutineLeak(t, baseline)
}

func TestNicoliveOffAirWaitsWithoutComments(t *testing.T) {
	watchHTML := watchPageOf(t, map[string]any{
		"site":    map[string]any{"relive": map[string]any{"webSocketUrl": ""}},
		"program": map[string]any{"status": "ENDED"},
	})

	var mu sync.Mutex
	watchFetches := 0
	fetch := func(ctx context.Context, url string) (*http.Response, error) {
		mu.Lock()
		watchFetches++
		mu.Unlock()
		return fetchResponse(200, []byte(watchHTML)), nil
	}
	dial := func(ctx context.Context, url string) (wsConn, error) {
		t.Error("must not connect a watch session while off-air")
		return nil, errors.New("must not connect")
	}

	source := NewNicoliveSource(
		func(Target) (string, bool) { return "ch2646436", true },
		WithNicoliveFetch(fetch),
		WithNicoliveDial(dial),
		WithNicoliveOffAirDelay(10*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	ch := source.Subscribe(ctx, Target{ID: 3273601024, NetworkID: 0x7fe0, ServiceID: 0x0400})
	if ch == nil {
		t.Fatal("subscribe returned nil for a supported target")
	}

	// Let it re-check the program a few times, then cut it off.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := watchFetches
		mu.Unlock()
		if n >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if got := drainComments(t, ch); len(got) != 0 {
		t.Errorf("off-air stream delivered %+v", got)
	}
	mu.Lock()
	n := watchFetches
	mu.Unlock()
	if n < 2 {
		t.Errorf("watch page fetches = %d; want the off-air recheck to repeat", n)
	}
}

func TestNicoliveResolvesChannelPerSubscribe(t *testing.T) {
	var mu sync.Mutex
	var resolved []int64
	resolve := func(target Target) (string, bool) {
		mu.Lock()
		resolved = append(resolved, target.ID)
		mu.Unlock()
		if target.ID == 1 {
			return "ch999", true
		}
		return "", false
	}
	fetch := func(ctx context.Context, url string) (*http.Response, error) {
		return fetchResponse(404, []byte("x")), nil
	}
	source := NewNicoliveSource(
		resolve,
		WithNicoliveFetch(fetch),
		WithNicoliveRetryDelay(10*time.Millisecond),
	)

	if ch := source.Subscribe(context.Background(), Target{ID: 2}); ch != nil {
		t.Error("subscribe should return nil when the resolver has no mapping")
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := source.Subscribe(ctx, Target{ID: 1})
	if ch == nil {
		t.Fatal("subscribe returned nil for a supported target")
	}
	cancel()
	drainComments(t, ch)

	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(resolved, []int64{2, 1}) {
		t.Errorf("resolved targets = %v; want [2 1]", resolved)
	}
}

func TestNicoliveSegmentFailureIsNotFatal(t *testing.T) {
	const viewURI = "https://mpn.example/api/view/v4/abc"
	watchHTML := watchPageOf(t, map[string]any{
		"site":    map[string]any{"relive": map[string]any{"webSocketUrl": "wss://watch.example/session"}},
		"program": map[string]any{"status": "ON_AIR"},
	})

	var mu sync.Mutex
	requested := map[string]int{}
	fetch := func(ctx context.Context, url string) (*http.Response, error) {
		mu.Lock()
		requested[url]++
		mu.Unlock()
		switch url {
		case "https://live.nicovideo.jp/watch/ch2646436":
			return fetchResponse(200, []byte(watchHTML)), nil
		case viewURI + "?at=now":
			// a segment that will fail + the next cursor
			return fetchResponse(200, ndgrLengthDelimitedOf(
				ndgrSegmentEntryOf("https://mpn.example/segment/broken"),
				ndgrNextEntryOf(1718240416),
			)), nil
		case "https://mpn.example/segment/broken":
			return fetchResponse(500, []byte("boom")), nil
		case viewURI + "?at=1718240416":
			return fetchResponse(200, ndgrLengthDelimitedOf(
				ndgrSegmentEntryOf("https://mpn.example/segment/good"),
				ndgrNextEntryOf(1718240476),
			)), nil
		case "https://mpn.example/segment/good":
			return fetchResponse(200, ndgrLengthDelimitedOf(
				ndgrChatMessageOf("m1", 1718240401, nil, strp("ok"), nil),
			)), nil
		case viewURI + "?at=1718240476":
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return fetchResponse(404, []byte("not found")), nil
	}

	source := NewNicoliveSource(
		func(Target) (string, bool) { return "ch2646436", true },
		WithNicoliveFetch(fetch),
		WithNicoliveDial(func(ctx context.Context, url string) (wsConn, error) {
			return newFakeConn(watchSessionScript(viewURI)), nil
		}),
	)

	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	ch := source.Subscribe(ctx, Target{ID: 3273601024, NetworkID: 0x7fe0, ServiceID: 0x0400})
	if got := recvComment(t, ch); got.ID != "m1" || got.Text != "ok" {
		t.Errorf("comment = %+v; want m1/ok", got)
	}
	cancel()
	drainComments(t, ch)
	assertNoGoroutineLeak(t, baseline)
}

func TestNicoliveIdleWatchdogRestartsStream(t *testing.T) {
	const viewURI = "https://mpn.example/api/view/v4/abc"
	watchHTML := watchPageOf(t, map[string]any{
		"site":    map[string]any{"relive": map[string]any{"webSocketUrl": "wss://watch.example/session"}},
		"program": map[string]any{"status": "ON_AIR"},
	})

	var mu sync.Mutex
	viewNowFetches := 0
	fetch := func(ctx context.Context, url string) (*http.Response, error) {
		switch url {
		case "https://live.nicovideo.jp/watch/ch2646436":
			return fetchResponse(200, []byte(watchHTML)), nil
		case viewURI + "?at=now":
			mu.Lock()
			viewNowFetches++
			round := viewNowFetches
			mu.Unlock()
			if round == 1 {
				// no traffic at all: the watchdog must cut this round
				return &http.Response{StatusCode: 200, Body: io.NopCloser(&blockingBody{ctx: ctx})}, nil
			}
			return fetchResponse(200, ndgrLengthDelimitedOf(
				ndgrSegmentEntryOf("https://mpn.example/segment/1"),
				ndgrNextEntryOf(1718240416),
			)), nil
		case "https://mpn.example/segment/1":
			return fetchResponse(200, ndgrLengthDelimitedOf(
				ndgrChatMessageOf("m1", 1718240401, nil, strp("after restart"), nil),
			)), nil
		case viewURI + "?at=1718240416":
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return fetchResponse(404, []byte("not found")), nil
	}

	source := NewNicoliveSource(
		func(Target) (string, bool) { return "ch2646436", true },
		WithNicoliveFetch(fetch),
		WithNicoliveDial(func(ctx context.Context, url string) (wsConn, error) {
			return newFakeConn(watchSessionScript(viewURI)), nil
		}),
		WithNicoliveRetryDelay(10*time.Millisecond),
		WithNicoliveIdleTimeout(40*time.Millisecond),
	)

	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	ch := source.Subscribe(ctx, Target{ID: 3273601024, NetworkID: 0x7fe0, ServiceID: 0x0400})
	if got := recvComment(t, ch); got.ID != "m1" || got.Text != "after restart" {
		t.Errorf("comment = %+v; want m1 after the watchdog restart", got)
	}
	cancel()
	drainComments(t, ch)

	mu.Lock()
	n := viewNowFetches
	mu.Unlock()
	if n < 2 {
		t.Errorf("view ?at=now fetches = %d; want the watchdog to restart the stream", n)
	}
	assertNoGoroutineLeak(t, baseline)
}

// slowEntryReader delivers one entry per interval, keeping the idle
// watchdog fed while the View stream stays open.
type slowEntryReader struct {
	ctx      context.Context
	entries  [][]byte
	interval time.Duration
}

func (r *slowEntryReader) Read(p []byte) (int, error) {
	if len(r.entries) == 0 {
		return 0, io.EOF
	}
	select {
	case <-time.After(r.interval):
		n := copy(p, r.entries[0])
		r.entries = r.entries[1:]
		return n, nil
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	}
}

func TestNicoliveIdleWatchdogExtendedByEntries(t *testing.T) {
	const viewURI = "https://mpn.example/api/view/v4/abc"
	// Five next cursors delivered every 40ms (200ms total) with an 80ms
	// idle timeout: the stream only survives if every entry extends the
	// watchdog, and the final reconnect then uses the last cursor. Each
	// delivered chunk is one length-delimited message, like a live View
	// stream feeding one entry per flush.
	entries := make([][]byte, 5)
	for i := range entries {
		entries[i] = ndgrLengthDelimitedOf(ndgrNextEntryOf(int64(1718240400 + i + 1)))
	}
	lastCursor := viewURI + "?at=1718240405"

	var mu sync.Mutex
	requested := []string{}
	fetch := func(ctx context.Context, url string) (*http.Response, error) {
		mu.Lock()
		requested = append(requested, url)
		mu.Unlock()
		switch url {
		case viewURI + "?at=now":
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(&slowEntryReader{ctx: ctx, entries: entries, interval: 40 * time.Millisecond}),
			}, nil
		case lastCursor:
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return fetchResponse(404, []byte("not found")), nil
	}

	cfg := &nicoliveConfig{Fetch: fetch, IdleTimeout: 80 * time.Millisecond}

	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan SourceComment, 128)
	done := make(chan error, 1)
	go func() { done <- ndgrComments(ctx, cfg, viewURI, out) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		sawLast := false
		for _, url := range requested {
			if url == lastCursor {
				sawLast = true
			}
		}
		mu.Unlock()
		if sawLast {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("the view stream never reached the final cursor")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("ndgrComments returned %v; want nil on cancellation", err)
	}
	assertNoGoroutineLeak(t, baseline)
}

func TestSeenSetEviction(t *testing.T) {
	s := newSeenSet()
	for i := 0; i < 2000; i++ {
		s.remember(fmt.Sprintf("id-%d", i))
	}
	if s.has("id-0") != true {
		t.Fatal("below the 2000 cap nothing should be evicted")
	}
	// Crossing the cap evicts the oldest half down to 1000.
	s.remember("id-2000")
	if s.has("id-0") {
		t.Fatal("oldest entries should be evicted past the cap")
	}
	if !s.has("id-1500") || !s.has("id-2000") {
		t.Fatal("recent entries must survive eviction")
	}
	// A re-added id after eviction dedups again.
	s.remember("id-0")
	if !s.has("id-0") {
		t.Fatal("re-added id must be tracked again")
	}
}

func TestConcurrentSegmentDedup(t *testing.T) {
	// Two segment URIs in ONE view round, both delivering the same message
	// id: the real pumpSegment goroutines race on the shared seen set and
	// the duplicate must be delivered exactly once (the -race-covered
	// production path, not a test-local loop).
	const viewURI = "https://mpn.example/api/view/v4/abc"
	watchHTML := watchPageOf(t, map[string]any{
		"site":    map[string]any{"relive": map[string]any{"webSocketUrl": "wss://watch.example/session"}},
		"program": map[string]any{"status": "ON_AIR"},
	})
	viewNow := ndgrLengthDelimitedOf(
		ndgrSegmentEntryOf("https://mpn.example/segment/a"),
		ndgrSegmentEntryOf("https://mpn.example/segment/b"),
		ndgrNextEntryOf(1718240416),
	)
	// Both segments carry the shared id; each adds one unique id.
	segmentA := ndgrLengthDelimitedOf(
		ndgrChatMessageOf("m1", 1718240401, nil, strp(">>1"), nil),
		ndgrChatMessageOf("a1", 1718240402, nil, strp("a"), nil),
	)
	segmentB := ndgrLengthDelimitedOf(
		ndgrChatMessageOf("m1", 1718240401, nil, strp(">>1"), nil),
		ndgrChatMessageOf("b1", 1718240403, nil, strp("b"), nil),
	)
	fetch := func(ctx context.Context, url string) (*http.Response, error) {
		switch url {
		case "https://live.nicovideo.jp/watch/ch1":
			return fetchResponse(200, []byte(watchHTML)), nil
		case viewURI + "?at=now":
			return fetchResponse(200, viewNow), nil
		case viewURI + "?at=1718240416":
			<-ctx.Done()
			return nil, ctx.Err()
		case "https://mpn.example/segment/a":
			return fetchResponse(200, segmentA), nil
		case "https://mpn.example/segment/b":
			return fetchResponse(200, segmentB), nil
		}
		return fetchResponse(404, []byte("not found")), nil
	}
	dial := func(ctx context.Context, url string) (wsConn, error) {
		return newFakeConn(watchSessionScript(viewURI)), nil
	}
	source := NewNicoliveSource(
		func(Target) (string, bool) { return "ch1", true },
		WithNicoliveFetch(fetch), WithNicoliveDial(dial),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := source.Subscribe(ctx, Target{Channel: "gr001"})
	if ch == nil {
		t.Fatal("subscribe returned nil")
	}

	deadline := time.After(5 * time.Second)
	ids := map[string]bool{}
	for len(ids) < 3 {
		select {
		case c, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed after %v", ids)
			}
			if ids[c.ID] {
				t.Fatalf("duplicate delivery of %q through the real pump path", c.ID)
			}
			ids[c.ID] = true
		case <-deadline:
			t.Fatalf("timed out with %v", ids)
		}
	}
}
