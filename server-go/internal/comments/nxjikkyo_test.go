// Tests for the nx-jikkyo source, port of
// server/lib/comments/sources/nx-jikkyo.test.ts plus the Go-specific
// reconnect-dedup / cancellation / leak coverage. Shares fakeConn and the
// receive helpers from nicolive_test.go.
package comments

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
)

// nxChatScript replies to the thread command like the upstream server: a
// thread ack, then the scripted chats.
func nxChatScript(chats ...string) func(*fakeConn, string) {
	return func(c *fakeConn, frame string) {
		var frameMessages []map[string]any
		if json.Unmarshal([]byte(frame), &frameMessages) != nil {
			return
		}
		for _, m := range frameMessages {
			if _, ok := m["thread"]; ok {
				c.push(`{"thread":{"thread":"23320","resultcode":0}}`)
				for _, chat := range chats {
					c.push(chat)
				}
				return
			}
		}
	}
}

func nxChatJSON(t *testing.T, chat map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"chat": chat})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNxJikkyoSubscribeUnsupportedTarget(t *testing.T) {
	// A channel with no nx-jikkyo mapping in the configuration.
	source := NewNxJikkyoSource(func(Target) (string, bool) { return "", false })
	ch := source.Subscribe(context.Background(), Target{ID: 1, NetworkID: 7, ServiceID: 354})
	if ch != nil {
		t.Fatal("subscribe should return nil for an unsupported target")
	}
}

func TestNxJikkyoResolverSubstitutesJk(t *testing.T) {
	source := NewNxJikkyoSource(
		func(target Target) (string, bool) {
			if target.ID == 1 {
				return "jk1", true
			}
			return "", false
		},
		WithNxJikkyoDial(func(ctx context.Context, url string) (wsConn, error) {
			return newFakeConn(nil), nil
		}),
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
}

func TestNxJikkyoHandshakeAndChatNormalization(t *testing.T) {
	chats := []string{
		nxChatJSON(t, map[string]any{
			"thread": "23320", "no": 1, "date": 1781401563,
			"date_usec": 375541, "content": "a",
			"user_id": "nicolive:a:X", "mail": "184",
		}),
		// date_usec omitted (zero fields are omitted by the server spec)
		nxChatJSON(t, map[string]any{"thread": "23320", "no": 2, "date": 1781401564, "content": "b"}),
		// duplicate id (thread:no) is suppressed
		nxChatJSON(t, map[string]any{
			"thread": "23320", "no": 1, "date": 1781401563,
			"date_usec": 375541, "content": "a",
		}),
		// no content: rejected by toComment
		nxChatJSON(t, map[string]any{"thread": "23320", "no": 4, "date": 1781401566}),
		nxChatJSON(t, map[string]any{"thread": "23320", "no": 3, "date": 1781401565, "content": "c"}),
	}

	var mu sync.Mutex
	var dialed []string
	source := NewNxJikkyoSource(
		func(Target) (string, bool) { return "jk1", true },
		WithNxJikkyoDial(func(ctx context.Context, url string) (wsConn, error) {
			mu.Lock()
			dialed = append(dialed, url)
			mu.Unlock()
			return newFakeConn(nxChatScript(chats...)), nil
		}),
	)

	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	ch := source.Subscribe(ctx, Target{ID: 1})
	if ch == nil {
		t.Fatal("subscribe returned nil for a supported target")
	}

	got := []SourceComment{recvComment(t, ch), recvComment(t, ch), recvComment(t, ch)}
	want := []SourceComment{
		{ID: "23320:1", Source: "nx-jikkyo", At: 1781401563375, Text: "a"},
		{ID: "23320:2", Source: "nx-jikkyo", At: 1781401564000, Text: "b"},
		{ID: "23320:3", Source: "nx-jikkyo", At: 1781401565000, Text: "c"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("comments = %+v; want %+v", got, want)
	}
	cancel()
	if rest := drainComments(t, ch); len(rest) != 0 {
		t.Errorf("unexpected extra comments %+v", rest)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 1 {
		t.Fatalf("connections = %d; want 1", len(dialed))
	}
	const wantURL = "wss://nx-jikkyo.tsukumijima.net/api/v1/channels/jk1/ws/comment"
	if dialed[0] != wantURL {
		t.Errorf("url = %q; want %q", dialed[0], wantURL)
	}
	assertNoGoroutineLeak(t, baseline)
}

func TestNxJikkyoOpenFrameIsExact(t *testing.T) {
	if got := nxOpenFrame(-100); got != `[{"ping":{"content":"rs:0"}},{"thread":{"version":"20061206","thread":"","user_id":"","res_from":-100}},{"ping":{"content":"rf:0"}}]` {
		t.Errorf("open frame = %s", got)
	}
	if got := nxOpenFrame(-50); got != `[{"ping":{"content":"rs:0"}},{"thread":{"version":"20061206","thread":"","user_id":"","res_from":-50}},{"ping":{"content":"rf:0"}}]` {
		t.Errorf("open frame = %s", got)
	}
}

func TestNxJikkyoDedupAcrossReconnects(t *testing.T) {
	chatA := nxChatJSON(t, map[string]any{"thread": "23320", "no": 1, "date": 1781401563, "content": "a"})
	chatD := nxChatJSON(t, map[string]any{"thread": "23320", "no": 2, "date": 1781401566, "content": "d"})

	var mu sync.Mutex
	var conns []*fakeConn
	dial := func(ctx context.Context, url string) (wsConn, error) {
		mu.Lock()
		defer mu.Unlock()
		var conn *fakeConn
		if len(conns) == 0 {
			// first connection: chat a, then the server drops us
			conn = newFakeConn(func(c *fakeConn, frame string) {
				c.push(`{"thread":{"thread":"23320","resultcode":0}}`)
				c.push(chatA)
				c.Close()
			})
		} else {
			// after the reconnect, the backfill repeats chat a (suppressed
			// by the seen set surviving reconnects) and adds chat d
			conn = newFakeConn(nxChatScript(chatA, chatD))
		}
		conns = append(conns, conn)
		return conn, nil
	}

	source := NewNxJikkyoSource(
		func(Target) (string, bool) { return "jk1", true },
		WithNxJikkyoDial(dial),
		WithNxJikkyoRetryDelay(10*time.Millisecond),
	)

	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	ch := source.Subscribe(ctx, Target{ID: 1})
	if ch == nil {
		t.Fatal("subscribe returned nil for a supported target")
	}

	first := recvComment(t, ch)
	if first.ID != "23320:1" || first.Text != "a" {
		t.Errorf("first comment = %+v; want 23320:1/a", first)
	}
	second := recvComment(t, ch)
	if second.ID != "23320:2" || second.Text != "d" {
		t.Errorf("second comment = %+v; want 23320:2/d (chat a must not repeat)", second)
	}

	cancel()
	if rest := drainComments(t, ch); len(rest) != 0 {
		t.Errorf("unexpected extra comments %+v", rest)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(conns) != 2 {
		t.Fatalf("connections = %d; want the close to trigger a reconnect", len(conns))
	}
	if !conns[0].isClosed() || !conns[1].isClosed() {
		t.Error("connections were not closed on teardown")
	}
	assertNoGoroutineLeak(t, baseline)
}

func TestNxJikkyoContextCancelCleansUp(t *testing.T) {
	var mu sync.Mutex
	var conns []*fakeConn
	source := NewNxJikkyoSource(
		func(Target) (string, bool) { return "jk1", true },
		WithNxJikkyoDial(func(ctx context.Context, url string) (wsConn, error) {
			mu.Lock()
			defer mu.Unlock()
			// thread ack only: the stream stays silent
			conn := newFakeConn(func(c *fakeConn, frame string) {
				c.push(`{"thread":{"thread":"23320","resultcode":0}}`)
			})
			conns = append(conns, conn)
			return conn, nil
		}),
	)

	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	ch := source.Subscribe(ctx, Target{ID: 1})
	if ch == nil {
		t.Fatal("subscribe returned nil for a supported target")
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	if got := drainComments(t, ch); len(got) != 0 {
		t.Errorf("unexpected comments %+v", got)
	}

	mu.Lock()
	defer mu.Unlock()
	for i, conn := range conns {
		if !conn.isClosed() {
			t.Errorf("connection %d was not closed after cancellation", i)
		}
	}
	assertNoGoroutineLeak(t, baseline)
}
