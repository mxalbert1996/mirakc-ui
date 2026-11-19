package transcode

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeClock advances time by each sleep, making retry windows and
// backoff cadence deterministic. onSleep, when set, replaces the
// default advance.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	sleeps  []time.Duration
	onSleep func(ctx context.Context, d time.Duration) error
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(0, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	onSleep := c.onSleep
	if onSleep == nil {
		c.now = c.now.Add(d)
	}
	c.mu.Unlock()
	if onSleep != nil {
		return onSleep(ctx, d)
	}
	return nil
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) clock() clock {
	return clock{Now: c.Now, Sleep: c.Sleep}
}

func (c *fakeClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// fakeProc simulates a child over real os.Pipes so EOF and EPIPE
// semantics match production. The body is the "process main"; onSignal
// defaults to exiting on SIGTERM/SIGKILL (bodies cooperate via exiting).
type fakeProc struct {
	name    string
	stdinR  io.ReadCloser  // the fake reads
	stdoutW io.WriteCloser // the fake writes
	stderrW io.WriteCloser // the fake writes

	cmd *fakeCmd

	mu       sync.Mutex
	sigs     []os.Signal
	writeErr error
	stdinBuf []byte

	exiting  chan struct{}
	exitOnce sync.Once
	body     func(p *fakeProc) int
	onSignal func(p *fakeProc, sig os.Signal)

	done chan struct{}
	code int
}

func (p *fakeProc) interrupt() {
	p.exitOnce.Do(func() { close(p.exiting) })
}

func (p *fakeProc) Signal(sig os.Signal) error {
	p.mu.Lock()
	p.sigs = append(p.sigs, sig)
	onSignal := p.onSignal
	p.mu.Unlock()
	if onSignal != nil {
		onSignal(p, sig)
	}
	return nil
}

func (p *fakeProc) Wait() (int, error) {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code, nil
}

func (p *fakeProc) signals() []os.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]os.Signal(nil), p.sigs...)
}

func (p *fakeProc) writeErrValue() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writeErr
}

func (p *fakeProc) writeStdout(b []byte) bool {
	_, err := p.stdoutW.Write(b)
	if err != nil {
		p.mu.Lock()
		p.writeErr = err
		p.mu.Unlock()
		return false
	}
	return true
}

// readStdin drains the fake's stdin, recording what arrived (like a
// real child consuming input). Inline: the caller decides the exit.
func (p *fakeProc) readStdin() {
	buf := make([]byte, 32*1024)
	for {
		n, err := p.stdinR.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.stdinBuf = append(p.stdinBuf, buf[:n]...)
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (p *fakeProc) stdinData() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.stdinBuf...)
}

// finish mimics process death: every fd the fake owns closes, then the
// exit status resolves.
func (p *fakeProc) finish(code int) {
	p.stdinR.Close()
	p.stdoutW.Close()
	p.stderrW.Close()
	p.mu.Lock()
	p.code = code
	p.mu.Unlock()
	close(p.done)
}

type fakeCmd struct {
	p      *fakeProc
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func (c *fakeCmd) Stdin() io.WriteCloser      { return c.stdin }
func (c *fakeCmd) Stdout() io.ReadCloser      { return c.stdout }
func (c *fakeCmd) Stderr() io.ReadCloser      { return c.stderr }
func (c *fakeCmd) Signal(sig os.Signal) error { return c.p.Signal(sig) }
func (c *fakeCmd) Wait() (int, error)         { return c.p.Wait() }

func newFakeProc(name string, body func(*fakeProc) int, onSignal func(*fakeProc, os.Signal)) *fakeProc {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	p := &fakeProc{
		name:     name,
		stdinR:   stdinR,
		stdoutW:  stdoutW,
		stderrW:  stderrW,
		exiting:  make(chan struct{}),
		onSignal: onSignal,
		done:     make(chan struct{}),
	}
	p.cmd = &fakeCmd{p: p, stdin: stdinW, stdout: stdoutR, stderr: stderrR}
	if p.onSignal == nil {
		p.onSignal = func(p *fakeProc, sig os.Signal) {
			if sig == syscall.SIGTERM || sig == syscall.SIGKILL {
				p.interrupt()
			}
		}
	}
	p.body = body
	go func() {
		p.finish(p.body(p))
	}()
	return p
}

// ignoreTermOnSignal records every signal; only SIGKILL interrupts, for
// exercising the kill timer.
func ignoreTermOnSignal(p *fakeProc, sig os.Signal) {
	if sig == syscall.SIGKILL {
		p.interrupt()
	}
}

// ffmpegChunks returns an ffmpeg body that writes chunks then exits 0,
// consuming stdin in the background like a real encoder.
func ffmpegChunks(chunks ...string) func(*fakeProc) int {
	return func(p *fakeProc) int {
		go p.readStdin()
		for _, c := range chunks {
			if !p.writeStdout([]byte(c)) {
				return 1
			}
			time.Sleep(10 * time.Millisecond)
		}
		return 0
	}
}

// ffmpegNoOutput exits without writing anything (device held / dead on
// arrival).
func ffmpegNoOutput() func(*fakeProc) int {
	return func(*fakeProc) int { return 1 }
}

// tsreadexIdentity copies stdin to stdout until either end breaks,
// like the real tsreadex passing the stream through.
func tsreadexIdentity(p *fakeProc) int {
	_, _ = io.Copy(p.stdoutW, p.stdinR)
	return 0
}

// fakeSpawn routes spawn calls to scripted fake bodies and records
// order, args and procs.
type fakeSpawn struct {
	mu       sync.Mutex
	tsreadex func(*fakeProc) int
	ffmpeg   func(*fakeProc) int
	names    []string
	args     map[string][][]string
	procs    []*fakeProc
}

func (f *fakeSpawn) Spawn(name string, args []string) (Cmd, error) {
	f.mu.Lock()
	f.names = append(f.names, name)
	if f.args == nil {
		f.args = map[string][][]string{}
	}
	f.args[name] = append(f.args[name], append([]string(nil), args...))
	tsreadex, ffmpeg := f.tsreadex, f.ffmpeg
	f.mu.Unlock()
	var body func(*fakeProc) int
	switch name {
	case "tsreadex":
		if tsreadex != nil {
			body = tsreadex
		} else {
			body = tsreadexIdentity
		}
	case "ffmpeg":
		if ffmpeg == nil {
			return nil, fmt.Errorf("fakeSpawn: no ffmpeg body scripted")
		}
		body = ffmpeg
	default:
		return nil, fmt.Errorf("fakeSpawn: unknown binary %q", name)
	}
	p := newFakeProc(name, body, nil)
	f.mu.Lock()
	f.procs = append(f.procs, p)
	f.mu.Unlock()
	return p.cmd, nil
}

func (f *fakeSpawn) spawned() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.names...)
}

func (f *fakeSpawn) ffmpegArgsList() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.args["ffmpeg"]
}

func (f *fakeSpawn) ffmpegProcs() []*fakeProc {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*fakeProc
	for _, p := range f.procs {
		if p.name == "ffmpeg" {
			out = append(out, p)
		}
	}
	return out
}

// fakeProber scripts probe results per encoder; the script receives the
// probe's request context so it can block on abort.
type fakeProber struct {
	mu     sync.Mutex
	calls  []EncoderName
	argss  [][]string
	script map[EncoderName]func(context.Context) (string, error)
}

func (f *fakeProber) Run(ctx context.Context, encoder EncoderName, args []string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, encoder)
	f.argss = append(f.argss, append([]string(nil), args...))
	script := f.script[encoder]
	f.mu.Unlock()
	if script != nil {
		return script(ctx)
	}
	return "", nil
}

func (f *fakeProber) probed() []EncoderName {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]EncoderName(nil), f.calls...)
}

func (f *fakeProber) probeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeProber) args() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.argss
}

// pollUntil waits for cond or fails the test.
func pollUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// upstreamServer serves the mirakc stream endpoint: with status 200 it
// writes a payload and holds the response open like a live stream,
// ending when the client goes away; otherwise it answers the scripted
// status for every request.
func upstreamServer(t *testing.T, statuses ...int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/services/") || !strings.HasSuffix(r.URL.Path, "/stream") {
			http.NotFound(w, r)
			return
		}
		if len(statuses) == 0 || statuses[0] != http.StatusOK {
			code := http.StatusNotFound
			if len(statuses) > 0 {
				code = statuses[0]
			}
			http.Error(w, "upstream error", code)
			return
		}
		w.Header().Set("Content-Type", "video/mpegts")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "UPSTREAM-DATA")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// settleGoroutines fails the test if goroutines never return to the
// baseline (leak evidence for the disconnect/stop paths). Idle keep-alive
// pool connections are normal transport behavior, not leaks — drop them
// first.
func settleGoroutines(t *testing.T, baseline int) {
	t.Helper()
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("goroutines did not settle (baseline %d):\n%s", baseline, buf)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// transcodeServer mounts the handler the way the router will.
func transcodeServer(t *testing.T, h *handler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/transcode/services/{id}", h.serveHTTP)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
