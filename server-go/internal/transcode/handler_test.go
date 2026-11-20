package transcode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Port of server/routes/transcode.test.ts (fetchUpstream) plus the Go
// orchestration hardening made possible by the Spawn/Probe seams.

const streamURL = "http://mirakc:40772/api/services/3274101064/stream?decode=1"

func fakeFetcher(statuses ...int) (func(context.Context, string) (*http.Response, error), *[]string) {
	var calls []string
	var n atomic.Int64
	fetch := func(ctx context.Context, url string) (*http.Response, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		calls = append(calls, url)
		i := int(n.Add(1)) - 1
		status := statuses[len(statuses)-1]
		if i < len(statuses) {
			status = statuses[i]
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("upstream")),
			Request:    &http.Request{},
		}, nil
	}
	return fetch, &calls
}

func TestFetchUpstream200NoRetry(t *testing.T) {
	fetch, calls := fakeFetcher(200)
	res, err := fetchUpstream(context.Background(), fetch, streamURL, 250*time.Millisecond, newFakeClock().clock())
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || len(*calls) != 1 || (*calls)[0] != streamURL {
		t.Fatalf("status=%d calls=%q", res.StatusCode, *calls)
	}
}

func TestFetchUpstream404Then200(t *testing.T) {
	fetch, calls := fakeFetcher(404, 404, 200)
	res, err := fetchUpstream(context.Background(), fetch, streamURL, 2*time.Second, newFakeClock().clock())
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || len(*calls) != 3 {
		t.Fatalf("status=%d calls=%d", res.StatusCode, len(*calls))
	}
}

func TestFetchUpstream503Then200(t *testing.T) {
	fetch, calls := fakeFetcher(503, 200)
	res, err := fetchUpstream(context.Background(), fetch, streamURL, time.Second, newFakeClock().clock())
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || len(*calls) != 2 {
		t.Fatalf("status=%d calls=%d", res.StatusCode, len(*calls))
	}
}

func TestFetchUpstream404UntilDeadlineReturnsLast(t *testing.T) {
	clk := newFakeClock()
	fetch, calls := fakeFetcher(404)
	res, err := fetchUpstream(context.Background(), fetch, streamURL, 3*time.Second, clk.clock())
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("status=%d, want the last 404", res.StatusCode)
	}
	if len(*calls) < 2 {
		t.Fatalf("calls=%d, want retries", len(*calls))
	}
	// 100ms ×1.5 growth capped at 500ms; the final sleep clamps to the
	// remaining window exactly like the Deno Math.min(wait, remaining).
	sleeps := clk.recorded()
	want := []time.Duration{100 * time.Millisecond, 150 * time.Millisecond, 225 * time.Millisecond, 337500000}
	for i, w := range want {
		if sleeps[i] != w {
			t.Fatalf("sleeps[%d] = %v, want %v (full: %v)", i, sleeps[i], w, sleeps)
		}
	}
	for _, d := range sleeps[4 : len(sleeps)-1] {
		if d != 500*time.Millisecond {
			t.Fatalf("sleep %v between the 4th and the last, want the 500ms cap", d)
		}
	}
	if last := sleeps[len(sleeps)-1]; last <= 0 || last > 500*time.Millisecond {
		t.Fatalf("last sleep = %v, want the remaining window (0, 500ms]", last)
	}
}

func TestFetchUpstream500NoRetry(t *testing.T) {
	fetch, calls := fakeFetcher(500)
	res, err := fetchUpstream(context.Background(), fetch, streamURL, 250*time.Millisecond, newFakeClock().clock())
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 500 || len(*calls) != 1 {
		t.Fatalf("status=%d calls=%d", res.StatusCode, len(*calls))
	}
}

func TestFetchUpstreamAbortedPropagatesFetchError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fetch, calls := fakeFetcher(404)
	_, err := fetchUpstream(ctx, fetch, streamURL, 250*time.Millisecond, newFakeClock().clock())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("calls=%d, want 0", len(*calls))
	}
}

func TestFetchUpstreamAbortDuringWaitReturnsLastResponse(t *testing.T) {
	clk := newFakeClock()
	ctx, cancel := context.WithCancel(context.Background())
	clk.onSleep = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}
	fetch, calls := fakeFetcher(404)
	res, err := fetchUpstream(ctx, fetch, streamURL, 500*time.Millisecond, clk.clock())
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 404 || len(*calls) != 1 {
		t.Fatalf("status=%d calls=%d, want the last response with no refetch", res.StatusCode, len(*calls))
	}
}

func TestFetchUpstreamClosesRetriedBody(t *testing.T) {
	closed := make(chan struct{})
	n := 0
	fetch := func(context.Context, string) (*http.Response, error) {
		n++
		if n == 1 {
			return &http.Response{
				StatusCode: 404,
				Header:     http.Header{},
				Body:       &closeTrackingBody{onClose: func() { close(closed) }},
				Request:    &http.Request{},
			}, nil
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    &http.Request{},
		}, nil
	}
	res, err := fetchUpstream(context.Background(), fetch, streamURL, time.Second, newFakeClock().clock())
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("the retried response body was not closed")
	}
}

func TestFetchUpstreamSuccessBodyUnread(t *testing.T) {
	fetch := func(context.Context, string) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("stream-body")),
			Request:    &http.Request{},
		}, nil
	}
	res, err := fetchUpstream(context.Background(), fetch, streamURL, 250*time.Millisecond, newFakeClock().clock())
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if string(body) != "stream-body" {
		t.Fatalf("body = %q", body)
	}
}

type closeTrackingBody struct {
	onClose func()
	done    bool
}

func (b *closeTrackingBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *closeTrackingBody) Close() error {
	if !b.done {
		b.done = true
		b.onClose()
	}
	return nil
}

// The Deno parse: integer Number() clamped to >= 0, everything else 0.
func TestParseAudioTrack(t *testing.T) {
	cases := []struct {
		param string
		want  int
	}{
		{"", 0},
		{"0", 0},
		{"1", 1},
		{"-2", 0},
		{"3.5", 0},
		{"abc", 0},
		{" 2 ", 2},
		{"1e1", 10},
		{"0x2", 2},
		{"999", 999},
	}
	for _, c := range cases {
		if got := parseAudioTrack(c.param); got != c.want {
			t.Errorf("parseAudioTrack(%q) = %d, want %d", c.param, got, c.want)
		}
	}
}

func TestFfmpegAndTsreadexArgsPinned(t *testing.T) {
	h := newHandler(Deps{}, realClock())
	got := h.ffmpegArgs(attemptOpts{
		encoder:         EncoderLibx264,
		quality:         Quality1080p,
		audioTrackIndex: 1,
	})
	want := []string{
		"-fflags", "nobuffer",
		"-analyzeduration", "0",
		"-probesize", "500000",
		"-i", "pipe:0",
		"-map", "0:v:0",
		"-map", "0:a:1?",
		"-map", "0:d?",
		"-map", "-0:#0x12",
		"-ignore_unknown",
		"-max_delay", "250000",
		"-max_interleave_delta", "500000",
		"-vf", "yadif=mode=0:parity=-1:deint=1,scale=-2:'min(ih,1080)'",
		"-c:v", "libx264", "-preset", "superfast", "-tune", "zerolatency", "-crf", "27", "-g", "15", "-sc_threshold", "0",
		"-c:a", "aac",
		"-b:a", "192k",
		"-c:d", "copy",
		"-f", "mpegts",
		"pipe:1",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ffmpegArgs = %q, want %q", got, want)
	}
	if !slices.Equal(tsreadexArgs, []string{"-n", "-1", "-a", "13", "-b", "5", "-c", "1", "-u", "1", "-d", "13", "-"}) {
		t.Fatalf("tsreadexArgs = %q", tsreadexArgs)
	}
}

func testDeps(spawn *fakeSpawn, prober *fakeProber, apiURL string, log *slog.Logger) Deps {
	d := Deps{APIURL: apiURL, Log: log}
	if spawn != nil {
		d.Spawn = spawn.Spawn
	}
	if prober != nil {
		d.Probe = prober.Run
	}
	return d
}

func newTestHandler(t *testing.T, deps Deps) *handler {
	t.Helper()
	h := newHandler(deps, newFakeClock().clock())
	if h.log == nil {
		h.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return h
}

// The Deno source throws on the relative new URL("") before the encoder
// loop; the Go port answers the established unset-URL 500 convention
// and never probes.
func TestHandlerUnsetURL500BeforeProbing(t *testing.T) {
	prober := &fakeProber{}
	h := newTestHandler(t, testDeps(nil, prober, "", nil))
	srv := transcodeServer(t, h)
	res, err := http.Get(srv.URL + "/api/transcode/services/1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if string(body) != `{"error":"MIRAKC_URL is not set"}`+"\n" {
		t.Fatalf("body = %q", body)
	}
	if len(prober.probed()) != 0 {
		t.Fatalf("probes = %v, want none (fail fast)", prober.probed())
	}
}

func TestHandlerStreamsHappyPath(t *testing.T) {
	upstream := upstreamServer(t, http.StatusOK)
	spawn := &fakeSpawn{ffmpeg: ffmpegChunks("FF-CHUNK-1", "FF-CHUNK-2")}
	prober := &fakeProber{}
	h := newTestHandler(t, testDeps(spawn, prober, upstream.URL+"/api", nil))
	srv := transcodeServer(t, h)

	res, err := http.Get(srv.URL + "/api/transcode/services/3274101064")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "video/mp2t" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q", cc)
	}
	body, _ := io.ReadAll(res.Body)
	if string(body) != "FF-CHUNK-1FF-CHUNK-2" {
		t.Fatalf("body = %q", body)
	}
	if want := []string{"tsreadex", "ffmpeg"}; !slices.Equal(spawn.spawned(), want) {
		t.Errorf("spawn order = %v, want %v", spawn.spawned(), want)
	}
	// The fake prober marks the first candidate usable, so the loop
	// never reaches libx264.
	if want := []EncoderName{EncoderH264V4l2m2m}; !slices.Equal(prober.probed(), want) {
		t.Errorf("probe order = %v, want the first candidate only", prober.probed())
	}
	// tsreadex (identity) must pass the upstream payload to ffmpeg.
	lastFF := spawn.ffmpegProcs()[len(spawn.ffmpegProcs())-1]
	pollUntil(t, "ffmpeg stdin to receive the upstream payload", func() bool {
		return string(lastFF.stdinData()) == "UPSTREAM-DATA"
	})
}

func TestHandlerRawModeSkipsTsreadex(t *testing.T) {
	upstream := upstreamServer(t, http.StatusOK)
	spawn := &fakeSpawn{ffmpeg: ffmpegChunks("RAW-1")}
	h := newTestHandler(t, testDeps(spawn, &fakeProber{}, upstream.URL+"/api", nil))
	srv := transcodeServer(t, h)
	res, err := http.Get(srv.URL + "/api/transcode/services/1?raw=1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != "RAW-1" {
		t.Fatalf("status=%d body=%q", res.StatusCode, body)
	}
	if want := []string{"ffmpeg"}; !slices.Equal(spawn.spawned(), want) {
		t.Fatalf("spawn order = %v, want ffmpeg only", spawn.spawned())
	}
}

func TestHandlerNoOutputFallsToNextEncoder(t *testing.T) {
	upstream := upstreamServer(t, http.StatusOK)
	var attempts atomic.Int64
	spawn := &fakeSpawn{ffmpeg: func(p *fakeProc) int {
		if attempts.Add(1) == 1 {
			return ffmpegNoOutput()(p)
		}
		return ffmpegChunks("GOOD-OUTPUT")(p)
	}}
	prober := &fakeProber{}
	h := newTestHandler(t, testDeps(spawn, prober, upstream.URL+"/api", nil))
	srv := transcodeServer(t, h)
	res, err := http.Get(srv.URL + "/api/transcode/services/1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != "GOOD-OUTPUT" {
		t.Fatalf("status=%d body=%q", res.StatusCode, body)
	}
	if want := []EncoderName{EncoderH264V4l2m2m, EncoderLibx264}; !slices.Equal(prober.probed(), want) {
		t.Fatalf("probes = %v", prober.probed())
	}
}

func TestHandlerFfmpegExitsMidStream(t *testing.T) {
	upstream := upstreamServer(t, http.StatusOK)
	spawn := &fakeSpawn{ffmpeg: ffmpegChunks("PARTIAL")}
	h := newTestHandler(t, testDeps(spawn, &fakeProber{}, upstream.URL+"/api", nil))
	srv := transcodeServer(t, h)
	res, err := http.Get(srv.URL + "/api/transcode/services/1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != "PARTIAL" {
		t.Fatalf("status=%d body=%q", res.StatusCode, body)
	}
}

func TestHandlerUpstreamErrorPassthrough(t *testing.T) {
	upstream := upstreamServer(t, http.StatusNotFound)
	h := newTestHandler(t, testDeps(nil, &fakeProber{}, upstream.URL+"/api", nil))
	srv := transcodeServer(t, h)
	res, err := http.Get(srv.URL + "/api/transcode/services/1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("status = %d, want the upstream status verbatim", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if string(body) != "Failed to fetch stream from mirakc" {
		t.Fatalf("body = %q", body)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestHandlerV4l2BusySkipsToLibx264(t *testing.T) {
	upstream := upstreamServer(t, http.StatusOK)
	spawn := &fakeSpawn{ffmpeg: ffmpegChunks("X264")}
	prober := &fakeProber{script: map[EncoderName]func(context.Context) (string, error){
		EncoderHevcV4l2m2m: func(context.Context) (string, error) { return busyStderr, errors.New("exit status 1") },
	}}
	h := newTestHandler(t, testDeps(spawn, prober, upstream.URL+"/api", nil))
	srv := transcodeServer(t, h)
	res, err := http.Get(srv.URL + "/api/transcode/services/1?codec=hevc")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != "X264" {
		t.Fatalf("status=%d body=%q", res.StatusCode, body)
	}
	// hevc probes busy (retried within the window), h264 is skipped
	// (shared /dev/video*), libx264 delivers.
	probes := prober.probed()
	if slices.Contains(probes, EncoderH264V4l2m2m) {
		t.Fatalf("probes = %v, want h264_v4l2m2m never probed", probes)
	}
	if probes[0] != EncoderHevcV4l2m2m || probes[len(probes)-1] != EncoderLibx264 || len(probes) < 3 {
		t.Fatalf("probes = %v, want hevc retried then libx264", probes)
	}
}

func TestHandlerNoUsableEncoder503(t *testing.T) {
	upstream := upstreamServer(t, http.StatusOK)
	fail := func(context.Context) (string, error) { return "init failed", errors.New("exit status 1") }
	prober := &fakeProber{script: map[EncoderName]func(context.Context) (string, error){
		EncoderH264V4l2m2m: fail,
		EncoderLibx264:     fail,
	}}
	h := newTestHandler(t, testDeps(nil, prober, upstream.URL+"/api", nil))
	srv := transcodeServer(t, h)
	res, err := http.Get(srv.URL + "/api/transcode/services/1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 503 {
		t.Fatalf("status = %d, want 503", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if string(body) != "No usable video encoder found" {
		t.Fatalf("body = %q", body)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

// A client that reads a little then cancels: the children must be torn
// down (SIGTERM/EPIPE) and no goroutine may linger.
func TestHandlerClientDisconnectStopsChildren(t *testing.T) {
	upstream := upstreamServer(t, http.StatusOK)
	stream := func(p *fakeProc) int {
		go p.readStdin()
		for i := 0; ; i++ {
			if !p.writeStdout([]byte(fmt.Sprintf("CHUNK-%06d|", i))) {
				return 0
			}
			time.Sleep(time.Millisecond)
		}
	}
	spawn := &fakeSpawn{ffmpeg: stream}
	h := newTestHandler(t, testDeps(spawn, &fakeProber{}, upstream.URL+"/api", nil))
	srv := transcodeServer(t, h)

	baseline := runtime.NumGoroutine()
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/transcode/services/1", nil)
	res, err := client.Do(req)
	if err == nil {
		buf := make([]byte, 64)
		_, _ = io.ReadFull(res.Body, buf) // deliberate early cancel
		res.Body.Close()
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("client error = %v, want the cancel", err)
	}
	settleGoroutines(t, baseline)

	ff := spawn.ffmpegProcs()[0]
	ffDone := make(chan struct{})
	go func() {
		ff.Wait()
		close(ffDone)
	}()
	select {
	case <-ffDone:
	case <-time.After(time.Second):
		t.Fatal("ffmpeg child never exited after the disconnect")
	}
	sigs := ff.signals()
	torn := errors.Is(ff.writeErrValue(), syscall.EPIPE)
	for _, sig := range sigs {
		if sig == syscall.SIGTERM || sig == syscall.SIGKILL {
			torn = true
		}
	}
	if !torn {
		t.Fatalf("child neither signaled (%v) nor EPIPEd (%v)", sigs, ff.writeErrValue())
	}
}

// The probe blocks on the request context; canceling mid-loop breaks
// out without touching the remaining candidates.
func TestHandlerAbortDuringProbeLoop(t *testing.T) {
	upstream := upstreamServer(t, http.StatusOK)
	prober := &fakeProber{script: map[EncoderName]func(context.Context) (string, error){
		EncoderH264V4l2m2m: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "aborted", ctx.Err()
		},
	}}
	h := newTestHandler(t, testDeps(nil, prober, upstream.URL+"/api", nil))
	srv := transcodeServer(t, h)

	baseline := runtime.NumGoroutine()
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/transcode/services/1", nil)
	_, _ = client.Do(req)
	settleGoroutines(t, baseline)
	if calls := prober.probeCalls(); calls != 1 {
		t.Fatalf("probe calls = %d, want exactly the first candidate", calls)
	}
}
