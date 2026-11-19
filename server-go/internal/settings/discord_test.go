package settings

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const webHookURL = "https://discord.com/api/webhooks/123/abc"

// captureTransport records requests made through it and answers with canned
// responses, keeping the webhook URL a valid discord.com URL while routing
// the actual request to the test handler.
type captureTransport struct {
	handler  http.HandlerFunc
	requests []capturedRequest
}

func (ct *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	var parsed map[string]any
	json.Unmarshal(body, &parsed)
	ct.requests = append(ct.requests, capturedRequest{
		method: req.Method,
		url:    req.URL.String(),
		header: req.Header.Clone(),
		body:   parsed,
	})

	rec := httptest.NewRecorder()
	ct.handler(rec, req)
	return rec.Result(), nil
}

func withCaptureTransport(t *testing.T, status int) *captureTransport {
	t.Helper()
	ct := &captureTransport{
		handler: func(w http.ResponseWriter, r *http.Request) {
			if status != http.StatusNoContent {
				w.WriteHeader(status)
				return
			}
			w.WriteHeader(status)
		},
	}
	saved := httpClient.Transport
	httpClient.Transport = ct
	t.Cleanup(func() { httpClient.Transport = saved })
	return ct
}

func TestSendDiscordEmbed(t *testing.T) {
	ct := withCaptureTransport(t, http.StatusNoContent)

	color := 0x57f287
	ok := SendDiscord(
		DiscordTarget{WebhookURL: webHookURL},
		Notification{Title: "録画開始", Message: "本文", Tags: []string{"red_circle"}, Color: &color},
	)
	if !ok {
		t.Fatal("SendDiscord = false, want true")
	}
	if len(ct.requests) != 1 {
		t.Fatalf("got %d requests", len(ct.requests))
	}
	req := ct.requests[0]
	if req.method != http.MethodPost || req.url != webHookURL {
		t.Errorf("request = %s %s", req.method, req.url)
	}
	if ct.requests[0].header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", req.header.Get("Content-Type"))
	}
	want := map[string]any{
		"embeds": []any{map[string]any{
			"title":       "録画開始",
			"description": "本文",
			"color":       float64(0x57f287),
		}},
	}
	if !reflect.DeepEqual(req.body, want) {
		t.Errorf("body = %v, want %v", req.body, want)
	}
}

func TestSendDiscordOmitsEmptyFields(t *testing.T) {
	ct := withCaptureTransport(t, http.StatusNoContent)

	SendDiscord(DiscordTarget{WebhookURL: webHookURL}, Notification{Title: "t", Message: ""})
	want := map[string]any{
		"embeds": []any{map[string]any{"title": "t"}},
	}
	if !reflect.DeepEqual(ct.requests[0].body, want) {
		t.Errorf("body = %v, want %v", ct.requests[0].body, want)
	}
}

func TestSendDiscordTruncatesEmbedLimits(t *testing.T) {
	ct := withCaptureTransport(t, http.StatusNoContent)

	SendDiscord(
		DiscordTarget{WebhookURL: webHookURL},
		Notification{Title: strings.Repeat("あ", 300), Message: strings.Repeat("い", 5000)},
	)
	embed := ct.requests[0].body["embeds"].([]any)[0].(map[string]any)
	title := embed["title"].(string)
	description := embed["description"].(string)
	if len([]rune(title)) != 256 || !strings.HasSuffix(title, "…") {
		t.Errorf("title length = %d", len([]rune(title)))
	}
	if len([]rune(description)) != 4096 || !strings.HasSuffix(description, "…") {
		t.Errorf("description length = %d", len([]rune(description)))
	}
}

func TestSendDiscordInvalidWebhookURL(t *testing.T) {
	var count atomic.Int32
	ct := &captureTransport{
		handler: func(w http.ResponseWriter, r *http.Request) { count.Add(1) },
	}
	saved := httpClient.Transport
	httpClient.Transport = ct
	t.Cleanup(func() { httpClient.Transport = saved })

	for _, target := range []DiscordTarget{
		{WebhookURL: "https://example.com/not-a-webhook"},
		{WebhookURL: ""},
	} {
		if SendDiscord(target, Notification{Title: "t", Message: "m"}) {
			t.Errorf("SendDiscord(%q) = true, want false", target.WebhookURL)
		}
	}
	if count.Load() != 0 {
		t.Errorf("%d requests sent, want 0", count.Load())
	}
}

func TestSendDiscordFailures(t *testing.T) {
	// Webhooks answer 204 on success; any non-2xx fails.
	ct := withCaptureTransport(t, http.StatusBadRequest)
	if SendDiscord(DiscordTarget{WebhookURL: webHookURL}, Notification{Title: "t", Message: "m"}) {
		t.Error("400 response should be false")
	}
	if len(ct.requests) != 1 {
		t.Fatalf("got %d requests", len(ct.requests))
	}

	// Transport-level error (unreachable host).
	saved := httpClient.Transport
	httpClient.Transport = errorTransport{}
	t.Cleanup(func() { httpClient.Transport = saved })
	if SendDiscord(DiscordTarget{WebhookURL: webHookURL}, Notification{Title: "t", Message: "m"}) {
		t.Error("transport error should be false")
	}
}

type errorTransport struct{}

func (errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, io.ErrUnexpectedEOF
}
