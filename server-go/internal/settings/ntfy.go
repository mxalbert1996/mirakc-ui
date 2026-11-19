// Port of server/lib/ntfy.ts: ntfy notification delivery. Japanese titles
// cannot go into HTTP headers (latin-1 constraint), so the topic URL is split
// into a base and topic and the notification is JSON-published to the base.
// https://docs.ntfy.sh/publish/#publish-as-json
package settings

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

// httpClient is used for outbound notification requests (both senders).
var httpClient = &http.Client{Timeout: 5 * time.Second}

// NtfyTarget is an ntfy destination: a topic URL and an optional access
// token (empty means no Authorization header).
type NtfyTarget struct {
	URL   string
	Token string
}

// Notification is the destination-agnostic payload accepted by both senders.
type Notification struct {
	Title   string
	Message string
	// Tags are ntfy tags (emoji shortcodes etc.); Discord ignores them.
	Tags []string
	// Color is the Discord embed color (decimal); ntfy ignores it. Nil omits
	// it (0 is a valid Discord color, hence the pointer).
	Color *int
}

type ntfyPublishBody struct {
	Topic   string   `json:"topic"`
	Title   string   `json:"title"`
	Message string   `json:"message"`
	Tags    []string `json:"tags,omitempty"`
}

// SendNtfy publishes a notification and reports whether it was delivered.
// An invalid URL or any delivery failure returns false without error (a
// failed notification must never take down the caller).
func SendNtfy(target NtfyTarget, n Notification) bool {
	base, topic, ok := SplitNtfyURL(target.URL)
	if !ok {
		log.Printf("[ntfy] invalid ntfy url: %s", target.URL)
		return false
	}

	body, err := json.Marshal(ntfyPublishBody{
		Topic:   topic,
		Title:   n.Title,
		Message: n.Message,
		Tags:    n.Tags,
	})
	if err != nil {
		log.Printf("[ntfy] publish failed: %v", err)
		return false
	}

	req, err := http.NewRequest(http.MethodPost, base, bytes.NewReader(body))
	if err != nil {
		log.Printf("[ntfy] publish failed: %v", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	if target.Token != "" {
		req.Header.Set("Authorization", "Bearer "+target.Token)
	}

	res, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[ntfy] publish failed: %v", err)
		return false
	}
	defer res.Body.Close()
	io.Copy(io.Discard, res.Body)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		log.Printf("[ntfy] publish failed: %s", res.Status)
		return false
	}
	return true
}
