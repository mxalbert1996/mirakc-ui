package settings

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

type capturedRequest struct {
	method string
	url    string
	header http.Header
	body   map[string]any
}

// newCaptureServer records requests and replies with the given status.
func newCaptureServer(status int, requests *[]capturedRequest) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		json.Unmarshal(body, &parsed)
		*requests = append(*requests, capturedRequest{
			method: r.Method,
			url:    r.URL.String(),
			header: r.Header.Clone(),
			body:   parsed,
		})
		w.WriteHeader(status)
	}))
}

func TestSendNtfyJSONPublishing(t *testing.T) {
	requests := []capturedRequest{}
	server := newCaptureServer(200, &requests)
	defer server.Close()

	ok := SendNtfy(
		NtfyTarget{URL: server.URL + "/sub/mirakc", Token: ""},
		Notification{Title: "録画開始", Message: "本文", Tags: []string{"red_circle"}},
	)
	if !ok {
		t.Fatal("SendNtfy = false, want true")
	}
	if len(requests) != 1 {
		t.Fatalf("got %d requests", len(requests))
	}
	req := requests[0]
	if req.method != http.MethodPost {
		t.Errorf("method = %s", req.method)
	}
	if req.url != "/sub" {
		t.Errorf("url = %s, want the topic-stripped base", req.url)
	}
	if req.header.Get("Authorization") != "" {
		t.Error("empty token should not send Authorization")
	}
	want := map[string]any{
		"topic":   "mirakc",
		"title":   "録画開始",
		"message": "本文",
		"tags":    []any{"red_circle"},
	}
	if !reflect.DeepEqual(req.body, want) {
		t.Errorf("body = %v, want %v", req.body, want)
	}
}

func TestSendNtfyBearerToken(t *testing.T) {
	requests := []capturedRequest{}
	server := newCaptureServer(200, &requests)
	defer server.Close()

	SendNtfy(
		NtfyTarget{URL: server.URL + "/mirakc", Token: "tk_abc"},
		Notification{Title: "t", Message: "m"},
	)
	if got := requests[0].header.Get("Authorization"); got != "Bearer tk_abc" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestSendNtfyInvalidURL(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
	}))
	defer server.Close()

	// No topic and empty URL both fail without sending anything.
	for _, target := range []NtfyTarget{
		{URL: server.URL, Token: ""},
		{URL: "", Token: ""},
	} {
		if SendNtfy(target, Notification{Title: "t", Message: "m"}) {
			t.Errorf("SendNtfy(%q) = true, want false", target.URL)
		}
	}
	if count.Load() != 0 {
		t.Errorf("%d requests sent, want 0", count.Load())
	}
}

func TestSendNtfyFailures(t *testing.T) {
	requests := []capturedRequest{}
	notOK := newCaptureServer(500, &requests)
	defer notOK.Close()
	if SendNtfy(
		NtfyTarget{URL: notOK.URL + "/x", Token: ""},
		Notification{Title: "t", Message: "m"},
	) {
		t.Error("non-2xx should be false")
	}

	// Connection error (closed server).
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := closed.URL
	closed.Close()
	if SendNtfy(NtfyTarget{URL: url + "/x", Token: ""}, Notification{Title: "t", Message: "m"}) {
		t.Error("connection error should be false")
	}

	// Client timeout counts as a failure.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer slow.Close()
	savedTimeout := httpClient.Timeout
	httpClient.Timeout = 20 * time.Millisecond
	defer func() { httpClient.Timeout = savedTimeout }()
	if SendNtfy(NtfyTarget{URL: slow.URL + "/x", Token: ""}, Notification{Title: "t", Message: "m"}) {
		t.Error("timeout should be false")
	}
}
