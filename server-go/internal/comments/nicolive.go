// Nicolive (NicoNico's official live-comment service) source, port of
// server/lib/comments/sources/nicolive.ts. Unofficial NDGR receive flow:
//
//  1. watch page https://live.nicovideo.jp/watch/{channelId}: embedded-data
//     gives the watch-session WebSocket URL and the program status
//  2. watch session: send startWatching, take the NDGR View API URI from
//     the messageServer reply, then close immediately (comments do not
//     need the session alive)
//  3. View API stream (?at=now): pump segment entries in parallel
//     goroutines; reconnect with the next cursor
//
// Protocol reference (MIT): https://github.com/tsukumijima/NDGRClient
// Being unofficial, on breakage it just keeps retrying and never affects
// the viewing feature itself.
package comments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/comments/ndgr"
)

const nicoliveUserAgent = "mirakc-ui (+https://github.com/ansanloms/mirakc-ui)"

// ResolveChannel maps a Target to the source-specific channel id
// ("ch2646436" for nicolive, "jk1" for nx-jikkyo). ok=false: the target has
// no mapping and is not comment-capable via that source. The sources keep
// no table of their own; the caller injects a resolver backed by the
// jikkyo configuration (tests inject fakes).
type ResolveChannel func(Target) (string, bool)

// wsConn is the subset of *websocket.Conn used by the comment sources; the
// parameterless Close keeps test fakes simple.
type wsConn interface {
	Reader(ctx context.Context) (websocket.MessageType, io.Reader, error)
	Writer(ctx context.Context, typ websocket.MessageType) (io.WriteCloser, error)
	Close() error
}

// DialFunc dials a comment WebSocket. Tests substitute scripted fakes.
type DialFunc func(ctx context.Context, url string) (wsConn, error)

// wsAdapter adapts *websocket.Conn to wsConn. Close is CloseNow (no close
// handshake) so ctx-driven teardown stays prompt instead of waiting on the
// peer.
type wsAdapter struct{ conn *websocket.Conn }

func (a wsAdapter) Reader(ctx context.Context) (websocket.MessageType, io.Reader, error) {
	return a.conn.Reader(ctx)
}

func (a wsAdapter) Writer(ctx context.Context, typ websocket.MessageType) (io.WriteCloser, error) {
	return a.conn.Writer(ctx, typ)
}

func (a wsAdapter) Close() error { return a.conn.CloseNow() }

func dialWebSocket(ctx context.Context, url string) (wsConn, error) {
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{})
	if err != nil {
		return nil, err
	}
	return wsAdapter{conn: conn}, nil
}

// wsSend writes one text frame.
func wsSend(ctx context.Context, conn wsConn, frame string) error {
	w, err := conn.Writer(ctx, websocket.MessageText)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, frame); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

// FetchFunc fetches a URL. The default is a plain GET with the source's
// user agent; tests substitute per-URL fakes.
type FetchFunc func(ctx context.Context, url string) (*http.Response, error)

// fetchClient bounds only the time-to-first-header (a blackholed watch
// page or NDGR leg would otherwise hang the source until ctx cancel;
// Deno self-healed via undici's ~300s timeouts). Streaming bodies stay
// unbounded, like the app's other clients.
var fetchClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

func httpFetch(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", nicoliveUserAgent)
	return fetchClient.Do(req)
}

// NicoliveProgramInfo is the watch-page subset the source uses.
// "" = absent (null in the Deno original).
type NicoliveProgramInfo struct {
	WebSocketURL string
	Status       string
}

// The real page is <script id="embedded-data" data-props="...">, but tag
// name and attribute order may change, so only look for id and data-props
// within the same tag.
var (
	embeddedDataPattern         = regexp.MustCompile(`<[^>]*\bid="embedded-data"[^>]*\bdata-props="([^"]*)"`)
	embeddedDataPatternReversed = regexp.MustCompile(`<[^>]*\bdata-props="([^"]*)"[^>]*\bid="embedded-data"`)
)

// ParseEmbeddedData extracts the watch-session WebSocket URL and the
// program status from the watch page's embedded-data (data-props JSON),
// decoding HTML entities. It errors when embedded-data is missing.
func ParseEmbeddedData(html string) (NicoliveProgramInfo, error) {
	m := embeddedDataPattern.FindStringSubmatch(html)
	if m == nil {
		m = embeddedDataPatternReversed.FindStringSubmatch(html)
	}
	if m == nil {
		return NicoliveProgramInfo{}, errors.New("embedded-data not found in the watch page")
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(decodeHTMLEntities(m[1])), &props); err != nil {
		return NicoliveProgramInfo{}, err
	}
	var info NicoliveProgramInfo
	if site, ok := props["site"].(map[string]any); ok {
		if relive, ok := site["relive"].(map[string]any); ok {
			if u, ok := relive["webSocketUrl"].(string); ok && u != "" {
				info.WebSocketURL = u
			}
		}
	}
	if program, ok := props["program"].(map[string]any); ok {
		if status, ok := program["status"].(string); ok {
			info.Status = status
		}
	}
	return info, nil
}

func decodeHTMLEntities(value string) string {
	return strings.NewReplacer(
		"&quot;", `"`,
		"&#39;", "'",
		"&lt;", "<",
		"&gt;", ">",
		"&amp;", "&",
	).Replace(value)
}

// NicoliveOption customizes the nicolive source.
type NicoliveOption func(*nicoliveConfig)

type nicoliveConfig struct {
	Fetch               FetchFunc
	Dial                DialFunc
	RetryDelay          time.Duration // connect/recv failure retry wait
	OffAirDelay         time.Duration // off-air recheck wait
	IdleTimeout         time.Duration // NDGR no-traffic cutoff
	WatchSessionTimeout time.Duration // watch session reply timeout
}

// WithNicoliveFetch substitutes the HTTP fetch (tests inject per-URL
// fakes; the default is a GET with the nicolive user agent).
func WithNicoliveFetch(f FetchFunc) NicoliveOption { return func(c *nicoliveConfig) { c.Fetch = f } }

// WithNicoliveDial substitutes the WebSocket dial (tests inject fakes).
func WithNicoliveDial(d DialFunc) NicoliveOption { return func(c *nicoliveConfig) { c.Dial = d } }

// WithNicoliveRetryDelay sets the retry wait after errors (default 10s).
func WithNicoliveRetryDelay(d time.Duration) NicoliveOption {
	return func(c *nicoliveConfig) { c.RetryDelay = d }
}

// WithNicoliveOffAirDelay sets the recheck wait while off-air (default 60s).
func WithNicoliveOffAirDelay(d time.Duration) NicoliveOption {
	return func(c *nicoliveConfig) { c.OffAirDelay = d }
}

// WithNicoliveIdleTimeout sets the NDGR no-traffic cutoff (default 90s).
func WithNicoliveIdleTimeout(d time.Duration) NicoliveOption {
	return func(c *nicoliveConfig) { c.IdleTimeout = d }
}

// WithNicoliveWatchSessionTimeout sets the watch session reply timeout
// (default 15s).
func WithNicoliveWatchSessionTimeout(d time.Duration) NicoliveOption {
	return func(c *nicoliveConfig) { c.WatchSessionTimeout = d }
}

// NewNicoliveSource builds the nicolive source. resolve maps targets to
// nicolive channel ids; ok=false makes the target unsupported.
func NewNicoliveSource(resolve ResolveChannel, opts ...NicoliveOption) Source {
	cfg := &nicoliveConfig{
		Fetch:               httpFetch,
		Dial:                dialWebSocket,
		RetryDelay:          10 * time.Second,
		OffAirDelay:         60 * time.Second,
		IdleTimeout:         90 * time.Second,
		WatchSessionTimeout: 15 * time.Second,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return &nicoliveSource{resolve: resolve, cfg: cfg}
}

type nicoliveSource struct {
	resolve ResolveChannel
	cfg     *nicoliveConfig
}

func (s *nicoliveSource) ID() string { return "nicolive" }

// Subscribe streams comments until ctx is cancelled, then closes the
// channel; nil = the target has no nicolive channel mapping. The channel is
// buffered, so a stalled consumer blocks this source's pump — the same
// backpressure as the Deno port's awaited writeSSE — and the relay must
// cancel ctx on disconnect.
func (s *nicoliveSource) Subscribe(ctx context.Context, target Target) <-chan SourceComment {
	channelID, ok := s.resolve(target)
	if !ok {
		return nil
	}
	out := make(chan SourceComment, 128)
	go func() {
		defer close(out)
		streamNicoliveComments(ctx, s.cfg, channelID, out)
	}()
	return out
}

// streamNicoliveComments is the connect-retry loop; it runs until ctx is
// cancelled (streamComments in nicolive.ts).
func streamNicoliveComments(ctx context.Context, cfg *nicoliveConfig, channelID string, out chan<- SourceComment) {
	for ctx.Err() == nil {
		err := nicoliveRound(ctx, cfg, channelID, out)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Error("[comments:nicolive] "+channelID+": stream error, retrying", "error", err)
			waitOrCancel(ctx, cfg.RetryDelay)
		}
	}
}

// nicoliveRound runs one program-info → watch-session → NDGR pass. A nil
// return means the off-air delay elapsed (recheck without retry wait).
func nicoliveRound(ctx context.Context, cfg *nicoliveConfig, channelID string, out chan<- SourceComment) error {
	info, err := fetchProgramInfo(ctx, cfg, channelID)
	if err != nil {
		return err
	}
	if info.Status != "ON_AIR" || info.WebSocketURL == "" {
		return waitOrCancel(ctx, cfg.OffAirDelay)
	}
	viewURI, err := fetchViewURI(ctx, cfg, info.WebSocketURL)
	if err != nil {
		return err
	}
	return ndgrComments(ctx, cfg, viewURI, out)
}

func fetchProgramInfo(ctx context.Context, cfg *nicoliveConfig, channelID string) (NicoliveProgramInfo, error) {
	res, err := cfg.Fetch(ctx, "https://live.nicovideo.jp/watch/"+channelID)
	if err != nil {
		return NicoliveProgramInfo{}, err
	}
	defer res.Body.Close()
	if !statusOK(res.StatusCode) {
		return NicoliveProgramInfo{}, fmt.Errorf("watch page: %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return NicoliveProgramInfo{}, err
	}
	return ParseEmbeddedData(string(body))
}

// fetchViewURI sends startWatching on the watch-session WebSocket and
// takes the NDGR View API URI from the messageServer reply, closing the
// session right after. Pings are answered with pongs; a server-sent
// error/disconnect, a network error or the timeout fails the session.
func fetchViewURI(ctx context.Context, cfg *nicoliveConfig, wsURL string) (string, error) {
	conn, err := cfg.Dial(ctx, wsURL)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	// One reader goroutine; frames are parsed there so the select loop
	// below never blocks on JSON.
	type watchEvent struct {
		typ     string
		viewURI string
	}
	events := make(chan watchEvent, 16)
	readErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			_, r, err := conn.Reader(ctx)
			if err != nil {
				readErr <- err
				return
			}
			data, err := io.ReadAll(r)
			if err != nil {
				readErr <- err
				return
			}
			var msg struct {
				Type string `json:"type"`
				Data struct {
					ViewURI string `json:"viewUri"`
				} `json:"data"`
			}
			if json.Unmarshal(data, &msg) != nil {
				continue // malformed frame: ignored
			}
			select {
			case events <- watchEvent{typ: msg.Type, viewURI: msg.Data.ViewURI}:
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	if err := wsSend(ctx, conn, `{"type":"startWatching","data":{"reconnect":false}}`); err != nil {
		return "", err
	}

	timer := time.NewTimer(cfg.WatchSessionTimeout)
	defer timer.Stop()
	for {
		select {
		case ev := <-events:
			switch {
			case ev.typ == "messageServer" && ev.viewURI != "":
				return ev.viewURI, nil
			case ev.typ == "ping":
				if err := wsSend(ctx, conn, `{"type":"pong"}`); err != nil {
					return "", err
				}
			case ev.typ == "error" || ev.typ == "disconnect":
				return "", fmt.Errorf("watch session: %s", ev.typ)
			}
		case err := <-readErr:
			return "", fmt.Errorf("watch session closed unexpectedly: %w", err)
		case <-timer.C:
			return "", errors.New("watch session timeout")
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// waitOrCancel sleeps for d, waking early on ctx cancellation.
func waitOrCancel(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func statusOK(status int) bool { return status >= 200 && status < 300 }

// seenSet is a dedup id set with FIFO eviction: past 2000 entries the
// oldest are dropped until 1000 remain (insertion-ordered Set in the Deno
// original). Not safe for concurrent use; ndgr segment pumps hold seenMu.
type seenSet struct {
	set   map[string]struct{}
	order []string
}

func newSeenSet() *seenSet { return &seenSet{set: make(map[string]struct{})} }

func (s *seenSet) has(id string) bool {
	_, ok := s.set[id]
	return ok
}

func (s *seenSet) remember(id string) {
	if s.has(id) {
		return
	}
	s.set[id] = struct{}{}
	s.order = append(s.order, id)
	if len(s.order) <= 2000 {
		return
	}
	drop := len(s.order) - 1000
	for _, v := range s.order[:drop] {
		delete(s.set, v)
	}
	s.order = append(s.order[:0], s.order[drop:]...)
}

// ndgrComments receives the NDGR View API stream, pumping segment entries
// in parallel goroutines and reconnecting with the next cursor. Errors and
// the idle timeout propagate to the retry loop; nil means ctx was
// cancelled.
func ndgrComments(ctx context.Context, cfg *nicoliveConfig, viewURI string, out chan<- SourceComment) error {
	inner, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	touch := make(chan struct{}, 1)
	s := &ndgrStream{
		inner:     inner,
		cfg:       cfg,
		viewURI:   viewURI,
		out:       out,
		segmentWg: &sync.WaitGroup{},
		seen:      newSeenSet(),
		notify: func() {
			select {
			case touch <- struct{}{}:
			default:
			}
		},
	}

	// Idle watchdog, extended on every entry/message receive.
	idleErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-inner.Done():
				return
			case <-touch:
				// activity: restart the idle window
			case <-time.After(cfg.IdleTimeout):
				select {
				case idleErr <- errors.New("NDGR stream idle timeout"):
				default:
				}
				innerCancel()
				return
			}
		}
	}()

	viewDone := make(chan error, 1)
	go func() { viewDone <- s.viewLoop() }()

	err := <-viewDone
	innerCancel() // stop the segment pumps and the watchdog
	s.segmentWg.Wait()
	if ctx.Err() != nil {
		return nil // cancelled by the consumer: normal end
	}
	select {
	case err := <-idleErr:
		return err
	default:
	}
	return err
}

// ndgrStream carries the per-NDGR-connection state shared by the view
// loop and the segment pumps.
type ndgrStream struct {
	inner     context.Context
	cfg       *nicoliveConfig
	viewURI   string
	out       chan<- SourceComment
	segmentWg *sync.WaitGroup
	seenMu    sync.Mutex
	seen      *seenSet
	notify    func()
}

func (s *ndgrStream) viewLoop() error {
	at := "now"
	for s.inner.Err() == nil {
		res, err := s.cfg.Fetch(s.inner, s.viewURI+"?at="+at)
		if err != nil {
			return err
		}
		if !statusOK(res.StatusCode) {
			res.Body.Close()
			return fmt.Errorf("NDGR view api: %d", res.StatusCode)
		}
		nextAt, haveNext, streamErr := s.readViewStream(res.Body)
		res.Body.Close()
		if streamErr != nil {
			return streamErr
		}
		if !haveNext {
			return errors.New("NDGR view stream ended without next cursor")
		}
		at = strconv.FormatInt(nextAt, 10)
	}
	return s.inner.Err()
}

func (s *ndgrStream) readViewStream(body io.Reader) (nextAt int64, haveNext bool, err error) {
	err = ndgr.LengthDelimited(body, func(b []byte) error {
		s.notify()
		if s.inner.Err() != nil {
			return s.inner.Err()
		}
		entry := ndgr.DecodeChunkedEntry(b)
		if entry.SegmentURI != "" {
			s.segmentWg.Add(1)
			go s.pumpSegment(entry.SegmentURI)
		}
		if entry.NextAt != 0 {
			nextAt = entry.NextAt
			haveNext = true
		}
		return nil
	})
	return nextAt, haveNext, err
}

// pumpSegment receives one Segment API stream in its own goroutine. A
// segment-level failure is not fatal: the next segment recovers.
func (s *ndgrStream) pumpSegment(uri string) {
	defer s.segmentWg.Done()
	res, err := s.cfg.Fetch(s.inner, uri)
	if err != nil {
		s.logSegmentFailure(uri, err)
		return
	}
	if !statusOK(res.StatusCode) {
		res.Body.Close()
		s.logSegmentFailure(uri, fmt.Errorf("NDGR segment api: %d", res.StatusCode))
		return
	}
	streamErr := ndgr.LengthDelimited(res.Body, func(b []byte) error {
		s.notify()
		if s.inner.Err() != nil {
			return s.inner.Err()
		}
		message := ndgr.DecodeChunkedMessage(b)
		if message == nil || message.Chat == nil {
			return nil // no chat (state, signal, gift, ...)
		}
		s.seenMu.Lock()
		if s.seen.has(message.ID) {
			s.seenMu.Unlock()
			return nil
		}
		s.seen.remember(message.ID)
		s.seenMu.Unlock()
		comment := SourceComment{
			ID:     message.ID,
			Source: "nicolive",
			At:     message.AtMs,
			Text:   message.Chat.Content,
			Author: message.Chat.Name,
		}
		select {
		case s.out <- comment:
			return nil
		case <-s.inner.Done():
			return s.inner.Err()
		}
	})
	res.Body.Close()
	if streamErr != nil {
		s.logSegmentFailure(uri, streamErr)
	}
}

func (s *ndgrStream) logSegmentFailure(uri string, err error) {
	if s.inner.Err() == nil {
		slog.Error("[comments:nicolive] segment stream failed", "uri", uri, "error", err)
	}
}
