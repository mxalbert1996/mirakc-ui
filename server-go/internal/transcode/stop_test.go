package transcode

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Unit tests for the stop/firstChunk semantics (the attempt lifecycle
// that the Deno tests could not exercise without real binaries).

func newUnitAttempt(proc *fakeProc) *attempt {
	h := newHandler(Deps{}, realClock())
	h.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return &attempt{
		h:        h,
		opts:     attemptOpts{serviceId: "test", encoder: EncoderLibx264},
		ffmpeg:   proc.cmd,
		stdout:   proc.cmd.Stdout(),
		stopDone: make(chan struct{}),
	}
}

// The crown-jewel ordering: an ffmpeg stalled writing to a full stdout
// pipe ignores SIGTERM, but closing the reader first gives it EPIPE and
// it dies on its own (measured 3s -> ~120ms on real hardware) — well
// before the SIGKILL timer.
func TestStopClosesStdoutReaderBeforeSignals(t *testing.T) {
	proc := newFakeProc("ffmpeg", func(p *fakeProc) int {
		buf := make([]byte, 64*1024)
		for {
			if _, err := p.stdoutW.Write(buf); err != nil {
				p.mu.Lock()
				p.writeErr = err
				p.mu.Unlock()
				return 0
			}
		}
	}, ignoreTermOnSignal)
	// Fill the pipe (nobody reads) so the writer is blocked inside Write.
	time.Sleep(100 * time.Millisecond)
	a := newUnitAttempt(proc)

	start := time.Now()
	a.stop("test")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("stop took %v; the EPIPE path should resolve far below the %v SIGKILL timer",
			elapsed, killDelayOf())
	}
	if err := proc.writeErrValue(); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("write error = %v, want EPIPE", err)
	}
	if sigs := proc.signals(); len(sigs) == 0 || sigs[0] != syscall.SIGTERM {
		t.Fatalf("signals = %v, want SIGTERM after the reader close", sigs)
	}
}

// A child that ignores SIGTERM until SIGKILL: stop still resolves via
// the kill timer, and the exit is awaited (the next probe can only run
// after the device is actually released).
func TestStopKillsUnresponsiveChild(t *testing.T) {
	oldKill := killDelayOf()
	setKillDelay(50 * time.Millisecond)
	t.Cleanup(func() { setKillDelay(oldKill) })

	proc := newFakeProc("ffmpeg", func(p *fakeProc) int {
		<-p.exiting
		return -1
	}, ignoreTermOnSignal)
	a := newUnitAttempt(proc)

	start := time.Now()
	a.stop("test")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stop took %v, want the kill timer to resolve it", elapsed)
	}
	if sigs := proc.signals(); !containsSignal(sigs, syscall.SIGTERM) || !containsSignal(sigs, syscall.SIGKILL) {
		t.Fatalf("signals = %v, want SIGTERM then SIGKILL", sigs)
	}
}

func containsSignal(sigs []os.Signal, want os.Signal) bool {
	for _, sig := range sigs {
		if sig == want {
			return true
		}
	}
	return false
}

// Reentrancy: concurrent stop calls share the single teardown and the
// first reason wins.
func TestStopReentrant(t *testing.T) {
	proc := newFakeProc("ffmpeg", func(p *fakeProc) int {
		<-p.exiting
		return 0
	}, nil)
	a := newUnitAttempt(proc)
	const callers = 4
	done := make(chan struct{}, callers)
	for i := 0; i < callers; i++ {
		go func() {
			a.stop("first caller wins")
			done <- struct{}{}
		}()
	}
	for i := 0; i < callers; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent stop calls did not all resolve")
		}
	}
}

// No first chunk inside the (shortened) window: ok=false.
func TestFirstChunkTimeout(t *testing.T) {
	oldTimeout := firstChunkTimeoutOf()
	setFirstChunkTimeout(50 * time.Millisecond)
	t.Cleanup(func() { setFirstChunkTimeout(oldTimeout) })

	proc := newFakeProc("ffmpeg", func(p *fakeProc) int {
		<-p.exiting
		return 0
	}, nil)
	a := newUnitAttempt(proc)
	_, ok, err := firstChunk(context.Background(), a)
	if ok || err != nil {
		t.Fatalf("ok=%v err=%v, want a silent timeout", ok, err)
	}
	a.stop("cleanup")
}

// A stdout read error before any byte propagates (the caller stops with
// "first chunk failed").
func TestFirstChunkReadErrorPropagates(t *testing.T) {
	proc := newFakeProc("ffmpeg", func(p *fakeProc) int {
		<-p.exiting
		return 0
	}, nil)
	a := newUnitAttempt(proc)
	a.stdout.Close()
	if _, ok, err := firstChunk(context.Background(), a); ok || err == nil {
		t.Fatalf("ok=%v err=%v, want the read error", ok, err)
	}
	a.stop("cleanup")
}

// Process exit before any output: ok=false (the "no output" path).
func TestFirstChunkProcessExit(t *testing.T) {
	proc := newFakeProc("ffmpeg", func(*fakeProc) int { return 1 }, nil)
	a := newUnitAttempt(proc)
	if _, ok, err := firstChunk(context.Background(), a); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want exit before output", ok, err)
	}
	a.stop("cleanup")
}

func TestStopBoundedReturnsEvenWhenChildrenNeverReap(t *testing.T) {
	// The wedge guard: a D-state child whose Wait never resolves must not
	// hold the request hostage past the stopWait cap.
	oldWait := stopWaitOf()
	setStopWait(50 * time.Millisecond)
	t.Cleanup(func() { setStopWait(oldWait) })

	proc := newFakeProc("ffmpeg", func(p *fakeProc) int {
		<-p.exiting
		<-make(chan struct{}) // never reaps
		return -1
	}, nil)
	a := newUnitAttempt(proc)

	start := time.Now()
	stopBounded(a, "test")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("stopBounded took %v, want the 50ms cap to bound it", elapsed)
	}
}

func TestFfmpegSpawnFailureReapsTsreadex(t *testing.T) {
	// ffmpeg failing to spawn after tsreadex started (disk full, ENFILE...)
	// must not strand tsreadex or the upstream body — the Deno handler
	// leaked both here; the Go port reaps them.
	upstream := upstreamServer(t, http.StatusOK)
	var spawned sync.Mutex
	var tsreadex *fakeProc
	spawn := func(name string, args []string) (Cmd, error) {
		if name == "tsreadex" {
			p := newFakeProc(name, func(p *fakeProc) int {
				<-p.exiting
				return 0
			}, ignoreTermOnSignal)
			spawned.Lock()
			tsreadex = p
			spawned.Unlock()
			return p.cmd, nil
		}
		return nil, errors.New("spawn: simulated ENFILE")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := newTestHandler(t, Deps{APIURL: upstream.URL + "/api", Log: log, Spawn: spawn, Probe: (&fakeProber{}).Run})
	srv := transcodeServer(t, h)

	res, err := http.Get(srv.URL + "/api/transcode/services/1")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 503 {
		t.Fatalf("status = %d, want 503 after every encoder attempt failed", res.StatusCode)
	}

	spawned.Lock()
	proc := tsreadex
	spawned.Unlock()
	if proc == nil {
		t.Fatal("tsreadex was never spawned")
	}
	deadline := time.After(2 * time.Second)
	select {
	case <-proc.done:
	case <-deadline:
		t.Fatal("tsreadex was not reaped after the ffmpeg spawn failure")
	}
	if sigs := proc.signals(); !containsSignal(sigs, syscall.SIGKILL) {
		t.Fatalf("signals = %v, want SIGKILL", sigs)
	}
}
