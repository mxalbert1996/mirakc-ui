package mirakc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func collectSSE(r io.Reader) []SseEvent {
	var events []SseEvent
	ParseSSE(r, func(ev SseEvent) { events = append(events, ev) })
	return events
}

func TestParseSSEEventAndDataPairs(t *testing.T) {
	events := collectSSE(strings.NewReader(
		"event: recording.started\n" +
			`data: {"programId":1}` + "\n" +
			"\n" +
			": keep-alive comment\n" +
			"\n" +
			"event: epg.programs-updated\n" +
			`data: {"serviceId":2}` + "\n\n"))

	want := []SseEvent{
		{Event: "recording.started", Data: `{"programId":1}`},
		{Event: "epg.programs-updated", Data: `{"serviceId":2}`},
	}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %+v, want %+v", events, want)
	}
}

func TestParseSSEMultiLineDataJoined(t *testing.T) {
	events := collectSSE(strings.NewReader("data: a\ndata: b\n\n"))
	want := []SseEvent{{Event: "", Data: "a\nb"}}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %+v, want %+v", events, want)
	}
}

func TestParseSSECRLF(t *testing.T) {
	events := collectSSE(strings.NewReader("event: recording.started\r\ndata: {\"programId\":1}\r\n\r\n"))
	want := []SseEvent{{Event: "recording.started", Data: `{"programId":1}`}}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %+v, want %+v", events, want)
	}
}

// oneByteReader hands out one byte per Read, the worst-case chunking.
type oneByteReader struct {
	s string
	i int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	p[0] = r.s[r.i]
	r.i++
	return 1, nil
}

func TestParseSSERestoresSplitChunkBoundaries(t *testing.T) {
	events := collectSSE(&oneByteReader{s: "event: recording.started\ndata: {\"programId\":42}\n\n"})
	want := []SseEvent{{Event: "recording.started", Data: `{"programId":42}`}}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %+v, want %+v", events, want)
	}
}

func TestParseSSEEventWithoutDataEmitted(t *testing.T) {
	events := collectSSE(strings.NewReader("event: epg.programs-updated\n\n"))
	want := []SseEvent{{Event: "epg.programs-updated", Data: ""}}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %+v, want %+v", events, want)
	}
}

func TestParseSSETrailingEventWithoutBlankLineDroppedAtEOF(t *testing.T) {
	// The Deno parser never flushes at EOF; mirroring that, a trailing
	// event not followed by a blank line is dropped.
	events := collectSSE(strings.NewReader("event: recording.started\ndata: {\"programId\":1}"))
	if len(events) != 0 {
		t.Errorf("events = %+v, want none", events)
	}
	events = collectSSE(strings.NewReader("event: recording.started\ndata: {\"programId\":1}\n"))
	if len(events) != 0 {
		t.Errorf("events = %+v, want none (no blank line)", events)
	}
}

func TestRecordingEventOf(t *testing.T) {
	cases := []struct {
		name    string
		ev      SseEvent
		kind    RecordingKind
		program int64
		ok      bool
	}{
		{"started", SseEvent{Event: "recording.started", Data: `{"programId":123}`}, RecordingStarted, 123, true},
		{"stopped", SseEvent{Event: "recording.stopped", Data: `{"programId":123}`}, RecordingStopped, 123, true},
		{"failed with reason", SseEvent{Event: "recording.failed", Data: `{"programId":123,"reason":{"type":"io-error"}}`}, RecordingFailed, 123, true},
		{"rescheduled", SseEvent{Event: "recording.rescheduled", Data: `{"programId":123}`}, 0, 0, false},
		{"broken data", SseEvent{Event: "recording.started", Data: "broken"}, 0, 0, false},
		{"string programId", SseEvent{Event: "recording.started", Data: `{"programId":"123"}`}, 0, 0, false},
		{"missing programId", SseEvent{Event: "recording.stopped", Data: `{}`}, 0, 0, false},
		{"non-object data", SseEvent{Event: "recording.stopped", Data: `123`}, 0, 0, false},
	}
	for _, c := range cases {
		kind, program, ok := RecordingEventOf(c.ev)
		if ok != c.ok || (ok && (kind != c.kind || program != c.program)) {
			t.Errorf("%s: RecordingEventOf = kind %v, id %d, ok %v; want kind %v, id %d, ok %v",
				c.name, kind, program, ok, c.kind, c.program, c.ok)
		}
	}
}

func TestSubscribeEventsDeliversAndReconnects(t *testing.T) {
	var connections int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&connections, 1)
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %q, want text/event-stream", r.Header.Get("Accept"))
		}
		fmt.Fprint(w, "event: recording.started\ndata: {\"programId\":1}\n\n")
	}))
	defer upstream.Close()

	received := make(chan SseEvent, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := SubscribeEvents(ctx, upstream.URL, nil, 10*time.Millisecond, func(ev SseEvent) {
		received <- ev
	})

	var first, second SseEvent
	select {
	case first = <-received:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("no event from the first connection")
	}
	select {
	case second = <-received:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("no reconnect after the server closed the stream")
	}
	if atomic.LoadInt32(&connections) < 2 {
		t.Errorf("connections = %d, want >= 2", connections)
	}
	if first != (SseEvent{Event: "recording.started", Data: `{"programId":1}`}) {
		t.Errorf("first event = %+v", first)
	}
	if second != first {
		t.Errorf("second event = %+v, want same as first", second)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("done channel never closed after cancel")
	}
}

func TestSubscribeEventsRetriesOnBadStatus(t *testing.T) {
	var connections int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&connections, 1)
		http.Error(w, "ng", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := SubscribeEvents(ctx, upstream.URL, nil, 10*time.Millisecond, func(SseEvent) {})

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&connections) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("done channel never closed after cancel")
	}
	if atomic.LoadInt32(&connections) < 2 {
		t.Errorf("connections = %d, want the loop to retry on non-2xx", connections)
	}
}

func TestSubscribeEventsOnEventPanicContinues(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: first\n\ndata: second\n\n")
	}))
	defer upstream.Close()

	received := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := SubscribeEvents(ctx, upstream.URL, nil, 10*time.Millisecond, func(ev SseEvent) {
		if ev.Data == "first" {
			panic("handler blew up")
		}
		received <- ev.Data
	})
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}()

	select {
	case got := <-received:
		if got != "second" {
			t.Errorf("received %q, want the event after the panicking one", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscription died after an onEvent panic")
	}
}

func TestSubscribeEventsCancelStopsLoopAndGoroutine(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: hello\n\n")
		// Push the event out before holding the connection open, like a
		// real SSE stream.
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(release)

	received := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := SubscribeEvents(ctx, upstream.URL, nil, 5*time.Second, func(SseEvent) {
		select {
		case received <- struct{}{}:
		default:
		}
	})

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("no event delivered")
	}

	baseline := runtime.NumGoroutine()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("done channel never closed after cancel")
	}

	// Goroutine-shutdown evidence: the subscriber goroutine must be gone
	// shortly after done closes (allow the server's connection goroutines
	// a moment to wind down too).
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > baseline {
		t.Errorf("goroutines after cancel = %d, baseline %d: the subscriber leaked", got, baseline)
	}
}
