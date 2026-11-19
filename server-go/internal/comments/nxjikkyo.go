// NX-Jikkyo (a community successor compatible with NicoNico jikkyo)
// comment source, port of server/lib/comments/sources/nx-jikkyo.ts.
//
// Unlike the official NDGR flow, NX-Jikkyo speaks the old nico comment
// server protocol (the XMLSocket-era thread command) over a plain
// WebSocket, so ws/comment plus an empty-thread handshake receives
// without a watch session or a threadkey, logged out. Flow:
//
//  1. connect wss://nx-jikkyo.tsukumijima.net/api/v1/channels/{jk}/ws/comment
//  2. on open, send the thread command (version 20061206, thread "" =
//     current thread auto-connect, negative res_from = last N + live)
//  3. normalize chat messages and stream them
//
// Protocol references (MIT): tsukumijima/KonomiTV (LiveCommentManager.ts
// initCommentSession) and tsukumijima/NX-Jikkyo. Being unofficial, on
// breakage it just keeps retrying and never affects viewing.
package comments

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strconv"
	"time"
)

const nxJikkyoCommentWSBase = "wss://nx-jikkyo.tsukumijima.net/api/v1/channels"

// NxJikkyoOption customizes the nx-jikkyo source.
type NxJikkyoOption func(*nxJikkyoConfig)

type nxJikkyoConfig struct {
	Dial       DialFunc
	RetryDelay time.Duration
	ResFrom    int
}

// WithNxJikkyoDial substitutes the WebSocket dial (tests inject fakes).
func WithNxJikkyoDial(d DialFunc) NxJikkyoOption { return func(c *nxJikkyoConfig) { c.Dial = d } }

// WithNxJikkyoRetryDelay sets the reconnect wait (default 10s).
func WithNxJikkyoRetryDelay(d time.Duration) NxJikkyoOption {
	return func(c *nxJikkyoConfig) { c.RetryDelay = d }
}

// WithNxJikkyoResFrom sets the initial backfill count (negative = last N
// comments then live; default -100).
func WithNxJikkyoResFrom(n int) NxJikkyoOption { return func(c *nxJikkyoConfig) { c.ResFrom = n } }

// NewNxJikkyoSource builds the nx-jikkyo source. resolve maps targets to
// jikkyo channel numbers ("jk1"); ok=false makes the target unsupported.
func NewNxJikkyoSource(resolve ResolveChannel, opts ...NxJikkyoOption) Source {
	cfg := &nxJikkyoConfig{
		Dial:       dialWebSocket,
		RetryDelay: 10 * time.Second,
		ResFrom:    -100,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return &nxJikkyoSource{resolve: resolve, cfg: cfg}
}

type nxJikkyoSource struct {
	resolve ResolveChannel
	cfg     *nxJikkyoConfig
}

func (s *nxJikkyoSource) ID() string { return "nx-jikkyo" }

// Subscribe streams comments until ctx is cancelled, then closes the
// channel; nil = the target has no nx-jikkyo channel mapping. The channel
// is buffered, so a stalled consumer blocks this source's pump; cancel ctx
// to end it.
func (s *nxJikkyoSource) Subscribe(ctx context.Context, target Target) <-chan SourceComment {
	jk, ok := s.resolve(target)
	if !ok {
		return nil
	}
	out := make(chan SourceComment, 128)
	go func() {
		defer close(out)
		streamNxJikkyoComments(ctx, s.cfg, jk, out)
	}()
	return out
}

// nxOpenFrame is the ws/comment handshake: rs ping, thread command, rf
// ping — the exact frame layout the upstream server expects.
func nxOpenFrame(resFrom int) string {
	return fmt.Sprintf(
		`[{"ping":{"content":"rs:0"}},{"thread":{"version":"20061206","thread":"","user_id":"","res_from":%d}},{"ping":{"content":"rf:0"}}]`,
		resFrom,
	)
}

// streamNxJikkyoComments is the connect-retry loop; it runs until ctx is
// cancelled. The seen set survives reconnects to suppress res_from
// backfill duplicates.
func streamNxJikkyoComments(ctx context.Context, cfg *nxJikkyoConfig, jk string, out chan<- SourceComment) {
	seen := newSeenSet()
	for ctx.Err() == nil {
		runNxJikkyoSession(ctx, cfg, jk, seen, out)
		if ctx.Err() == nil {
			waitOrCancel(ctx, cfg.RetryDelay)
		}
	}
}

// runNxJikkyoSession serves one WebSocket connection until it errors or
// closes, or ctx is cancelled. seen is only touched from this goroutine
// (sessions run sequentially), so it needs no lock.
func runNxJikkyoSession(ctx context.Context, cfg *nxJikkyoConfig, jk string, seen *seenSet, out chan<- SourceComment) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, err := cfg.Dial(sctx, nxJikkyoCommentWSBase+"/"+jk+"/ws/comment")
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("[comments:nx-jikkyo] "+jk+": stream error", "error", err)
		}
		return
	}
	defer conn.Close()

	done := make(chan struct{})
	defer close(done)

	chats := make(chan map[string]any, 16)
	readErr := make(chan error, 1)
	go func() {
		for {
			_, r, err := conn.Reader(sctx)
			if err != nil {
				readErr <- err
				return
			}
			data, err := io.ReadAll(r)
			if err != nil {
				readErr <- err
				return
			}
			var message map[string]any
			if json.Unmarshal(data, &message) != nil {
				continue // malformed frame: ignored
			}
			chat, ok := message["chat"].(map[string]any)
			if !ok {
				continue // not a chat message (thread ack, ping, ...)
			}
			select {
			case chats <- chat:
			case <-done:
				return
			case <-sctx.Done():
				return
			}
		}
	}()

	if err := wsSend(sctx, conn, nxOpenFrame(cfg.ResFrom)); err != nil {
		if ctx.Err() == nil {
			slog.Error("[comments:nx-jikkyo] "+jk+": stream error", "error", err)
		}
		return
	}

	for {
		select {
		case <-sctx.Done():
			return
		case err := <-readErr:
			if ctx.Err() == nil {
				slog.Error("[comments:nx-jikkyo] "+jk+": stream error", "error", err)
			}
			return
		case chat := <-chats:
			comment, ok := nxToComment(chat)
			if !ok {
				continue // no content: rejected
			}
			if seen.has(comment.ID) {
				continue
			}
			seen.remember(comment.ID)
			select {
			case out <- comment:
			case <-sctx.Done():
				return
			}
		}
	}
}

// nxToComment normalizes a chat message; ok=false when it has no content.
// user_id is an anonymous hash, not a display name, so no author is
// emitted (matching the NDGR source's anonymous comments). Zero-valued
// fields are omitted by the server spec, so missing ones count as 0.
func nxToComment(chat map[string]any) (SourceComment, bool) {
	content, ok := chat["content"].(string)
	if !ok {
		return SourceComment{}, false
	}
	date := nxNumber(chat, "date")
	dateUsec := nxNumber(chat, "date_usec")
	no := nxNumber(chat, "no")
	thread := ""
	switch v := chat["thread"].(type) {
	case string:
		thread = v
	case float64:
		thread = strconv.FormatFloat(v, 'f', -1, 64)
	}
	return SourceComment{
		// no is a per-thread sequence; pair it with thread to avoid
		// cross-thread id collisions.
		ID:     thread + ":" + strconv.FormatInt(no, 10),
		Source: "nx-jikkyo",
		At:     date*1000 + int64(math.Floor(float64(dateUsec)/1000)),
		Text:   content,
	}, true
}

func nxNumber(chat map[string]any, key string) int64 {
	v, ok := chat[key].(float64)
	if !ok {
		return 0
	}
	return int64(v)
}
