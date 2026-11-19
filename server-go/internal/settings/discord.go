// Port of server/lib/discord.ts: Discord Incoming Webhook delivery. Just a
// JSON POST of an embed to the webhook URL; no bot, token or gateway needed.
// https://discord.com/developers/docs/resources/webhook#execute-webhook
package settings

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
)

// DiscordTarget is a Discord destination: an Incoming Webhook URL.
type DiscordTarget struct {
	WebhookURL string
}

// Discord embed limits; exceeding them fails the request with 400, so
// over-length titles and descriptions are truncated.
// https://discord.com/developers/docs/resources/message#embed-object-embed-limits
const (
	embedTitleMax       = 256
	embedDescriptionMax = 4096
)

func truncateEmbedText(s string, max int) string {
	if ucs2Length(s) <= max {
		return s
	}
	var b strings.Builder
	units := 0
	for _, r := range s {
		w := 2
		if r <= 0xFFFF {
			w = 1
		}
		if units+w > max-1 {
			break
		}
		b.WriteRune(r)
		units += w
	}
	b.WriteString("…")
	return b.String()
}

type discordEmbed struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Color       *int   `json:"color,omitempty"`
}

type discordWebhookBody struct {
	Embeds []discordEmbed `json:"embeds"`
}

// SendDiscord posts a notification as an embed and reports whether it was
// delivered. An invalid URL or any delivery failure returns false without
// error (a failed notification must never take down the caller). The webhook
// responds 204 No Content on success; any 2xx counts.
func SendDiscord(target DiscordTarget, n Notification) bool {
	if !IsValidDiscordWebhookURL(target.WebhookURL) {
		log.Printf("[discord] invalid webhook url: %s", target.WebhookURL)
		return false
	}

	embed := discordEmbed{Title: truncateEmbedText(n.Title, embedTitleMax)}
	if n.Message != "" {
		embed.Description = truncateEmbedText(n.Message, embedDescriptionMax)
	}
	if n.Color != nil {
		embed.Color = n.Color
	}
	body, err := json.Marshal(discordWebhookBody{Embeds: []discordEmbed{embed}})
	if err != nil {
		log.Printf("[discord] publish failed: %v", err)
		return false
	}

	req, err := http.NewRequest(http.MethodPost, target.WebhookURL, bytes.NewReader(body))
	if err != nil {
		log.Printf("[discord] publish failed: %v", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[discord] publish failed: %v", err)
		return false
	}
	defer res.Body.Close()
	io.Copy(io.Discard, res.Body)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		log.Printf("[discord] publish failed: %s", res.Status)
		return false
	}
	return true
}
