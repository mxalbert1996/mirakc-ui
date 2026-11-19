package mirakc

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// SseEvent is one parsed server-sent event: the event name and the data
// lines joined with "\n".
type SseEvent struct {
	Event string
	Data  string
}

// ParseSSE splits a byte stream into SseEvents (parseSseStream in
// server/lib/mirakc-events.ts): events are blank-line delimited, ":" lines
// are keep-alive comments, CRLF is handled, and — mirroring the Deno
// parser — a trailing event without a final blank line is dropped at EOF.
func ParseSSE(r io.Reader, emit func(SseEvent)) {
	event := ""
	var data []string
	flush := func() {
		if event == "" && len(data) == 0 {
			return
		}
		emit(SseEvent{Event: event, Data: strings.Join(data, "\n")})
		event = ""
		data = nil
	}

	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			switch {
			case line == "":
				flush()
			case strings.HasPrefix(line, ":"):
				// keep-alive comment
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimSpace(line[len("data:"):]))
			}
		}
		if err != nil {
			return
		}
	}
}

// RecordingKind identifies the recording SSE event kinds
// (RecordingEventKind in server/lib/mirakc-events.ts).
type RecordingKind int

const (
	RecordingStarted RecordingKind = iota
	RecordingStopped
	RecordingFailed
)

// RecordingEventOf recognizes recording.started/stopped/failed events and
// extracts the program id from their `{"programId": number}` data (failed
// also carries a reason, unused here). Anything else — unknown event,
// undecodable data, or a non-number programId — yields ok=false.
func RecordingEventOf(ev SseEvent) (kind RecordingKind, programID int64, ok bool) {
	switch ev.Event {
	case "recording.started":
		kind = RecordingStarted
	case "recording.stopped":
		kind = RecordingStopped
	case "recording.failed":
		kind = RecordingFailed
	default:
		return 0, 0, false
	}
	// json.Unmarshal into any maps a JSON number to float64, which is the
	// same "typeof number" test the Deno version applies.
	var data struct {
		ProgramID any `json:"programId"`
	}
	if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
		return 0, 0, false
	}
	number, isNumber := data.ProgramID.(float64)
	if !isNumber {
		return 0, 0, false
	}
	return kind, int64(number), true
}

// SubscribeEvents subscribes to mirakc's /events SSE endpoint
// (subscribeMirakcEvents): it reconnects forever, waiting retryDelay
// between attempts, until ctx is cancelled. onEvent errors (panics) are
// logged and the subscription continues. The returned channel is closed
// once the loop exits — cancel ctx to stop.
func SubscribeEvents(ctx context.Context, eventsURL string, client *http.Client, retryDelay time.Duration, onEvent func(SseEvent)) <-chan struct{} {
	if client == nil {
		client = http.DefaultClient
	}
	if retryDelay <= 0 {
		retryDelay = 5 * time.Second
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, eventsURL, nil)
			if err != nil {
				log.Printf("[mirakc-events] failed to build request: %v", err)
			} else {
				req.Header.Set("Accept", "text/event-stream")
				if subscribeOnce(ctx, client, req, onEvent) {
					return
				}
			}
			if !sleepOrCancel(ctx, retryDelay) {
				return
			}
		}
	}()
	return done
}

// subscribeOnce runs one connection to completion; it reports true when
// the loop should stop (ctx cancelled).
func subscribeOnce(ctx context.Context, client *http.Client, req *http.Request, onEvent func(SseEvent)) bool {
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[mirakc-events] connection lost: %v", err)
		}
		return ctx.Err() != nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		log.Printf("[mirakc-events] unexpected response: %d", resp.StatusCode)
		return ctx.Err() != nil
	}
	ParseSSE(resp.Body, func(ev SseEvent) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[mirakc-events] event handler failed: %v", rec)
			}
		}()
		onEvent(ev)
	})
	return ctx.Err() != nil
}

func sleepOrCancel(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
