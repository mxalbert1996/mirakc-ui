package settings

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/api"
)

// The normalized input types serialize back to their OpenAPI wire shapes —
// the same objects the Deno parse functions returned, which the stores then
// persisted. HTML escaping stays off for JSON.stringify parity.

func (i KeywordRuleInput) MarshalJSON() ([]byte, error) {
	type wire struct {
		Keyword  string     `json:"keyword"`
		From     *time.Time `json:"from,omitempty"`
		To       *time.Time `json:"to,omitempty"`
		Channels []string   `json:"channels"`
		Genres   []int      `json:"genres"`
		Enabled  bool       `json:"enabled"`
	}
	return marshalWire(wire{
		Keyword:  i.Keyword,
		From:     i.From,
		To:       i.To,
		Channels: i.Channels,
		Genres:   i.Genres,
		Enabled:  i.Enabled,
	})
}

func (m LiveCommentMappingInput) MarshalJSON() ([]byte, error) {
	type wire struct {
		Channel     string                      `json:"channel"`
		Assignments []api.LiveCommentAssignment `json:"assignments"`
		Enabled     bool                        `json:"enabled"`
	}
	return marshalWire(wire{
		Channel:     m.Channel,
		Assignments: m.Assignments,
		Enabled:     m.Enabled,
	})
}

func (s NotificationSettings) MarshalJSON() ([]byte, error) {
	type wire struct {
		URL               string `json:"url"`
		Token             string `json:"token"`
		DiscordWebhookURL string `json:"discordWebhookUrl"`
		OnSchedule        bool   `json:"onSchedule"`
		OnStart           bool   `json:"onStart"`
		OnEnd             bool   `json:"onEnd"`
		OnFail            bool   `json:"onFail"`
		OnRemove          bool   `json:"onRemove"`
	}
	return marshalWire(wire{
		URL:               s.URL,
		Token:             s.Token,
		DiscordWebhookURL: s.DiscordWebhookURL,
		OnSchedule:        s.OnSchedule,
		OnStart:           s.OnStart,
		OnEnd:             s.OnEnd,
		OnFail:            s.OnFail,
		OnRemove:          s.OnRemove,
	})
}

func marshalWire(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
