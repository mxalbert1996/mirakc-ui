// Package transcode is the Phase-4 port of the live transcode route
// (server/routes/transcode.ts) with its quality presets
// (server/lib/quality.ts) and encoder selection (server/lib/encoder.ts):
// mirakc service stream -> tsreadex -> ffmpeg -> chunked MPEG-TS
// response, with per-encoder probing before each attempt.
package transcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// Right after a channel switch the old stream's tuner release must
	// be waited out; mirakc reports tuner shortage as 404
	// (TunerUnavailable) and 503, and release takes up to ~1.8s
	// (measured). Other statuses are returned at once.
	upstreamRetryWindow = 3 * time.Second
	upstreamRetryMin    = 100 * time.Millisecond
	upstreamRetryMax    = 500 * time.Millisecond

	// TS identifies every stream up front; the default 5MB analysis
	// measurably delays first video by ~4.5s.
	probeSize = "500000"
)

// Timing knobs. Plain vars would race: tests shorten them while other
// tests' watcher goroutines still read them under parallel-suite load,
// so every access goes through these synchronized getters/setters.
var knobsMu sync.Mutex
var knobs = struct {
	firstChunkTimeout time.Duration
	killDelay         time.Duration
	stopWait          time.Duration
}{
	firstChunkTimeout: 15 * time.Second,
	killDelay:         3 * time.Second,
	stopWait:          4 * time.Second,
}

func firstChunkTimeoutOf() time.Duration {
	knobsMu.Lock()
	defer knobsMu.Unlock()
	return knobs.firstChunkTimeout
}

func setFirstChunkTimeout(d time.Duration) {
	knobsMu.Lock()
	knobs.firstChunkTimeout = d
	knobsMu.Unlock()
}

func killDelayOf() time.Duration {
	knobsMu.Lock()
	defer knobsMu.Unlock()
	return knobs.killDelay
}

func setKillDelay(d time.Duration) {
	knobsMu.Lock()
	knobs.killDelay = d
	knobsMu.Unlock()
}

func stopWaitOf() time.Duration {
	knobsMu.Lock()
	defer knobsMu.Unlock()
	return knobs.stopWait
}

func setStopWait(d time.Duration) {
	knobsMu.Lock()
	knobs.stopWait = d
	knobsMu.Unlock()
}

// UpstreamError is a mirakc fetch failure — switching encoders cannot
// fix it, so the handler answers with the upstream status verbatim.
type UpstreamError struct{ Status int }

func (e *UpstreamError) Error() string { return "Failed to fetch stream from mirakc" }

// Deps are the handler's collaborators; nil Client/Log/Spawn/Probe fall
// back to the production implementations.
type Deps struct {
	// APIURL is the mirakc Web API base (mirakc.APIURL of MIRAKC_URL).
	// "" means MIRAKC_URL is unset.
	APIURL string
	Client *http.Client
	Log    *slog.Logger
	// V4L2CQ enables the -qp constant-quality args. Resolve once at
	// startup via V4L2ConstantQualityEnabled so probe and production
	// args can never disagree mid-process.
	V4L2CQ bool
	// Spawn and Probe are test seams; nil → real os/exec + ffmpeg.
	Spawn SpawnFunc
	Probe ProbeRunner
}

// NewHandler builds the GET /api/transcode/services/{id} handler. The
// mux pattern pins the method; the whole process tree is bound to
// r.Context(), so a client disconnect tears every child down.
func NewHandler(deps Deps) http.HandlerFunc {
	return newHandler(deps, realClock()).serveHTTP
}

type handler struct {
	apiURL         string
	client         *http.Client
	fetch          Fetcher
	log            *slog.Logger
	v4l2Cq         bool
	spawn          SpawnFunc
	probe          ProbeRunner
	clk            clock
	upstreamWindow time.Duration
}

func newHandler(deps Deps, clk clock) *handler {
	h := &handler{
		apiURL:         deps.APIURL,
		client:         deps.Client,
		log:            deps.Log,
		v4l2Cq:         deps.V4L2CQ,
		spawn:          deps.Spawn,
		probe:          deps.Probe,
		clk:            clk,
		upstreamWindow: upstreamRetryWindow,
	}
	if h.client == nil {
		h.client = http.DefaultClient
	}
	if h.log == nil {
		h.log = slog.Default()
	}
	if h.spawn == nil {
		h.spawn = realSpawn
	}
	if h.probe == nil {
		h.probe = h.realProbe
	}
	h.fetch = func(ctx context.Context, url string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		return h.client.Do(req)
	}
	return h
}

type attemptOpts struct {
	encoder         EncoderName
	quality         Quality
	audioTrackIndex int
	rawMode         bool
	streamURL       string
	serviceId       string
}

func (h *handler) serveHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	serviceId := r.PathValue("id")

	// The Deno source builds `new URL(`${mirakcApiUrl}/services/...`)`
	// here, and with MIRAKC_URL unset the relative string throws out of
	// the handler before any encoder is probed. Mirror that fail-fast
	// with the Go port's established unset-URL 500 convention
	// (mirakc.NewProxy).
	if h.apiURL == "" {
		h.log.Error("[transcode] MIRAKC_URL is not set")
		writeJSONError(w, http.StatusInternalServerError, "MIRAKC_URL is not set")
		return
	}
	streamURL := h.apiURL + "/services/" + url.PathEscape(serviceId) + "/stream?decode=1"

	// Integer Number() clamped to >= 0, anything else → 0 (the exact
	// Deno parse).
	audioTrackIndex := parseAudioTrack(r.URL.Query().Get("audioTrack"))
	quality := NormalizeQuality(r.URL.Query().Get("quality"), DefaultQuality)
	rawMode := r.URL.Query().Get("raw") == "1"
	preferHevc := r.URL.Query().Get("codec") == "hevc"

	// AVE's h264_v4l2m2m and hevc_v4l2m2m share one /dev/video*: after
	// any v4l2 probe returns busy, skip the remaining v4l2m2m candidates
	// and fall to libx264.
	v4l2Busy := false
	for _, encoder := range EncoderChain(preferHevc) {
		if ctx.Err() != nil {
			break
		}
		if v4l2Busy && encoder != EncoderLibx264 {
			continue
		}
		switch h.probeVideoEncoder(ctx, encoder, quality) {
		case ProbeBusy:
			v4l2Busy = true
			continue
		case ProbeUnusable:
			continue
		}
		a, err := h.startAttempt(ctx, attemptOpts{
			encoder:         encoder,
			quality:         quality,
			audioTrackIndex: audioTrackIndex,
			rawMode:         rawMode,
			streamURL:       streamURL,
			serviceId:       serviceId,
		})
		if err != nil {
			var upstream *UpstreamError
			if errors.As(err, &upstream) {
				writePlain(w, upstream.Status, upstream.Error())
				return
			}
			if ctx.Err() != nil {
				h.log.Error(fmt.Sprintf(
					"[transcode] attempt failed (aborted) serviceId=%s encoder=%s error=%v",
					serviceId, encoder, err))
			} else {
				h.log.Error(fmt.Sprintf(
					"[transcode] attempt failed serviceId=%s encoder=%s: %v",
					serviceId, encoder, err))
			}
			continue
		}
		if a == nil {
			continue
		}
		h.log.Error(fmt.Sprintf(
			"[transcode] serviceId=%s encoder=%s codec=%s raw=%t quality=%s audioTrack=%d",
			serviceId, encoder, codecName(preferHevc), rawMode, quality, audioTrackIndex))
		h.streamResponse(ctx, w, a)
		return
	}

	writePlain(w, http.StatusServiceUnavailable, "No usable video encoder found")
}

func codecName(preferHevc bool) string {
	if preferHevc {
		return "hevc"
	}
	return "h264"
}

// Fetcher performs one upstream HTTP request (the http.Client.Do shape).
type Fetcher func(ctx context.Context, url string) (*http.Response, error)

// FetchUpstream fetches the mirakc stream URL, retrying only 404/503
// (tuner exhaustion) with the pinned cadence and window.
func FetchUpstream(ctx context.Context, fetch Fetcher, url string) (*http.Response, error) {
	return fetchUpstream(ctx, fetch, url, upstreamRetryWindow, realClock())
}

func fetchUpstream(ctx context.Context, fetch Fetcher, url string, window time.Duration, clk clock) (*http.Response, error) {
	deadline := clk.Now().Add(window)
	wait := upstreamRetryMin
	for {
		res, err := fetch(ctx, url)
		if err != nil {
			return nil, err
		}
		if res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusServiceUnavailable {
			return res, nil
		}
		// The retried response body (a mirakc JSON error) is never read.
		res.Body.Close()
		remaining := deadline.Sub(clk.Now())
		if ctx.Err() != nil || remaining <= 0 {
			// Deadline or abort: the last response, verbatim.
			return res, nil
		}
		_ = clk.Sleep(ctx, min(wait, remaining))
		if ctx.Err() != nil {
			return res, nil
		}
		wait = min(wait*3/2, upstreamRetryMax)
	}
}

type attempt struct {
	h        *handler
	opts     attemptOpts
	ffmpeg   Cmd
	tsreadex Cmd // nil in raw mode
	stdout   io.ReadCloser
	upstream io.ReadCloser
	first    []byte

	stopOnce sync.Once
	stopDone chan struct{}
}

// startAttempt fetches mirakc, wires tsreadex/ffmpeg, and waits for the
// first chunk. No first chunk (ffmpeg dies at once / device held /
// timeout) → teardown and nil, so the caller moves to the next
// candidate.
func (h *handler) startAttempt(ctx context.Context, opts attemptOpts) (*attempt, error) {
	upstream, err := fetchUpstream(ctx, h.fetch, opts.streamURL, h.upstreamWindow, h.clk)
	if err != nil {
		return nil, err
	}
	if !statusOK(upstream.StatusCode) {
		if ctx.Err() == nil {
			// An abort mid-retry is a client disconnect, not a mirakc failure.
			h.log.Error(fmt.Sprintf("[transcode] mirakc stream failed serviceId=%s status=%d",
				opts.serviceId, upstream.StatusCode))
		}
		upstream.Body.Close()
		return nil, &UpstreamError{Status: upstream.StatusCode}
	}

	var tsreadex Cmd
	if !opts.rawMode {
		tsreadex, err = h.spawn("tsreadex", tsreadexArgs)
		if err != nil {
			upstream.Body.Close()
			return nil, err
		}
		pumpStderr(tsreadex, "[tsreadex]", h.log)
	}
	ffmpeg, err := h.spawn("ffmpeg", h.ffmpegArgs(opts))
	if err != nil {
		if tsreadex != nil {
			_ = tsreadex.Signal(syscall.SIGKILL)
			go func() {
				_, _ = tsreadex.Wait()
				_ = tsreadex.Stdin().Close()
				_ = tsreadex.Stdout().Close()
				_ = tsreadex.Stderr().Close()
			}()
		}
		upstream.Body.Close()
		return nil, err
	}
	pumpStderr(ffmpeg, "[ffmpeg]", h.log)

	a := &attempt{
		h:        h,
		opts:     opts,
		ffmpeg:   ffmpeg,
		tsreadex: tsreadex,
		stdout:   ffmpeg.Stdout(),
		upstream: upstream.Body,
		stopDone: make(chan struct{}),
	}

	if opts.rawMode {
		go copyPipe(ctx, a, upstream.Body, ffmpeg.Stdin(), "mirakc -> ffmpeg pipe ended")
	} else {
		go copyPipe(ctx, a, upstream.Body, tsreadex.Stdin(), "mirakc -> tsreadex pipe ended")
		go copyPipe(ctx, a, tsreadex.Stdout(), ffmpeg.Stdin(), "tsreadex -> ffmpeg pipe ended")
	}

	first, ok, ferr := firstChunk(ctx, a)
	if ferr != nil {
		// Dropping this without stop would leave ffmpeg holding the device.
		stopBounded(a, "first chunk failed")
		return nil, ferr
	}
	if !ok {
		// The children's actual exit (device release) is awaited inside
		// stop before the next candidate's probe.
		stopBounded(a, "no output")
		return nil, nil
	}
	a.first = first
	return a, nil
}

func (h *handler) ffmpegArgs(opts attemptOpts) []string {
	args := []string{
		"-fflags", "nobuffer",
		"-analyzeduration", "0",
		"-probesize", probeSize,
		"-i", "pipe:0",
		"-map", "0:v:0",
		"-map", fmt.Sprintf("0:a:%d?", opts.audioTrackIndex),
		"-map", "0:d?",
		// The ARIB EPG carousel (PID 0x12) is never played and its
		// drifting timestamps stall muxing (measured 17.1s → 2.3s to
		// first video). Subtitles and ID3 (0:d?) stay.
		"-map", "-0:#0x12",
		"-ignore_unknown",
		"-max_delay", "250000",
		"-vf", BuildVideoFilter(opts.encoder, opts.quality),
	}
	args = append(args, BuildVideoEncoderArgs(opts.encoder, opts.quality, h.v4l2Cq)...)
	return append(args,
		"-c:a", "aac",
		"-b:a", "192k",
		"-c:d", "copy",
		"-f", "mpegts",
		"pipe:1",
	)
}

// tsreadexArgs stabilize the stream and shape ARIB subtitles (raw mode
// skips it entirely).
var tsreadexArgs = []string{"-n", "-1", "-a", "13", "-b", "5", "-c", "5", "-u", "1", "-d", "13", "-"}

func (a *attempt) children() []Cmd {
	if a.tsreadex == nil {
		return []Cmd{a.ffmpeg}
	}
	return []Cmd{a.tsreadex, a.ffmpeg}
}

// stop tears the attempt down and resolves only after both children
// have actually exited — the V4L2 device frees when ffmpeg dies, and
// the next candidate's probe must not race it. Reentrant: every caller
// shares the single teardown.
//
// The ffmpeg stdout reader closes FIRST: an ffmpeg stalled writing to a
// full pipe ignores SIGTERM but dies on EPIPE (measured 3s → ~120ms on
// real hardware). Then SIGTERM both children, SIGKILL after killDelay,
// and cancel the upstream body.
func (a *attempt) stop(reason string) {
	a.stopOnce.Do(func() {
		a.h.log.Error(fmt.Sprintf("[transcode] stop serviceId=%s encoder=%s reason=%s",
			a.opts.serviceId, a.opts.encoder, reason))
		_ = a.stdout.Close()
		for _, child := range a.children() {
			_ = child.Signal(syscall.SIGTERM)
		}
		kill := time.AfterFunc(killDelayOf(), func() {
			for _, child := range a.children() {
				_ = child.Signal(syscall.SIGKILL)
			}
		})
		if a.upstream != nil {
			_ = a.upstream.Close()
		}
		children := a.children()
		go func() {
			for _, child := range children {
				_, _ = child.Wait()
			}
			kill.Stop()
			// Deterministic fd hygiene: close every parent-held pipe end.
			// The copy pumps close the write ends, and stop closed ffmpeg's
			// stdout above; what is left is tsreadex's stdout read end (and
			// the stderr ends), otherwise left to the *os.File finalizer.
			for _, child := range children {
				_ = child.Stdin().Close()
				_ = child.Stdout().Close()
				_ = child.Stderr().Close()
			}
			close(a.stopDone)
		}()
	})
	<-a.stopDone
}

// stopBounded waits for stop with the stopWait cap so an unreapable
// child can never wedge the request.
func stopBounded(a *attempt, reason string) {
	done := make(chan struct{})
	go func() {
		a.stop(reason)
		close(done)
	}()
	t := time.NewTimer(stopWaitOf())
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}

// copyPipe streams src into the child stdin and closes it on completion
// (EOF delivery — pipeTo's default close). Any failure stops the
// attempt, like the pipeTo catch in the Deno source.
func copyPipe(ctx context.Context, a *attempt, src io.Reader, dst io.WriteCloser, reason string) {
	err := copyCtx(ctx, dst, src)
	_ = dst.Close()
	if err != nil {
		a.stop(reason)
	}
}

// copyCtx copies src→dst but abandons the copy when ctx is done, so an
// aborted pipe copy never lingers in the handler (the fd teardown in
// stop unblocks the inner copy).
func copyCtx(ctx context.Context, dst io.Writer, src io.Reader) error {
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(dst, src)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// firstChunk races the first stdout byte against process exit, the
// 15s first-chunk cap and request abort. No byte in time → ok=false
// (the caller stops the attempt and moves on); a read error before
// any byte propagates (the caller stops with "first chunk failed").
func firstChunk(ctx context.Context, a *attempt) (first []byte, ok bool, err error) {
	type readResult struct {
		n   int
		err error
	}
	buf := make([]byte, 32*1024)
	readCh := make(chan readResult, 1)
	go func() {
		n, rerr := a.stdout.Read(buf)
		readCh <- readResult{n, rerr}
	}()
	exitCh := make(chan struct{}, 1)
	go func() {
		_, _ = a.ffmpeg.Wait()
		exitCh <- struct{}{}
	}()
	t := time.NewTimer(firstChunkTimeoutOf())
	defer t.Stop()
	for {
		select {
		case r := <-readCh:
			if r.n > 0 {
				return buf[:r.n], true, nil
			}
			if r.err == nil {
				// (0, nil) never occurs on a pipe read; treat it as an
				// error rather than re-selecting without a live reader.
				return nil, false, errors.New("transcode: empty read from ffmpeg stdout")
			}
			if errors.Is(r.err, io.EOF) {
				return nil, false, nil
			}
			return nil, false, r.err
		case <-exitCh:
			return nil, false, nil
		case <-t.C:
			return nil, false, nil
		case <-ctx.Done():
			return nil, false, nil
		}
	}
}

// streamResponse writes the 200 MPEG-TS response: the first chunk, then
// the rest of the ffmpeg stdout until it ends. A write failure (client
// disconnect) stops the attempt; a read end is always the stop teardown
// closing the pipe, whose reason was already logged by the initiator.
func (h *handler) streamResponse(ctx context.Context, w http.ResponseWriter, a *attempt) {
	// Exit watchers stop with the exit reason, like the .then on the
	// status promises in the Deno source.
	go func() {
		code, _ := a.ffmpeg.Wait()
		a.stop(fmt.Sprintf("ffmpeg exited code=%d", code))
	}()
	if a.tsreadex != nil {
		go func() {
			code, _ := a.tsreadex.Wait()
			a.stop(fmt.Sprintf("tsreadex exited code=%d", code))
		}()
	}
	// The whole tree is bound to the request context: a client
	// disconnect stops the children (net/http cancels r.Context() —
	// the property the Deno version needs an explicit listener for).
	go func() {
		<-ctx.Done()
		a.stop("client abort")
	}()

	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if _, werr := w.Write(a.first); werr != nil {
		a.stop("response body cancel")
		return
	}
	buf := make([]byte, 32*1024)
	for {
		n, rerr := a.stdout.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				a.stop("response body cancel")
				return
			}
		}
		if rerr != nil {
			return
		}
	}
}

// parseAudioTrack mirrors the Deno parse exactly: Number(param) when it
// is an integer, clamped to >= 0; anything else (absent included) → 0.
func parseAudioTrack(param string) int {
	n, ok := jsNumber(param)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
		return 0
	}
	return int(math.Max(0, n))
}

// jsNumber converts like the JS Number() constructor for the inputs a
// query string can produce: whitespace-tolerant, hex-prefixed, with
// exponents (0b/0o binary/octal prefixes are the one divergence —
// unreachable from the app's own client, which always sends plain
// integers). ok=false is the JS NaN case.
func jsNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		v, err := strconv.ParseUint(s[2:], 16, 64)
		if err != nil {
			return 0, false
		}
		return float64(v), true
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// statusOK mirrors the Deno Response.ok range (200–299).
func statusOK(code int) bool {
	return code >= 200 && code < 300
}

// writePlain responds text/plain; charset=utf-8 without a trailing
// newline (http.Error would append one; the Deno bodies have none).
func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// writeJSONError mirrors the unset-URL 500 convention of
// mirakc.NewProxy.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{message})
}
