package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/settings"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/store"
	"github.com/mxalbert1996/mirakc-ui/server-go/web"
)

// Deps are the router's collaborators. The zero-value hooks are valid
// no-ops (the recording job plugs in at Phase 2).
type Deps struct {
	Store *store.Store
	Auth  *Auth
	// KeywordRulesOnChanged fires after a successful rule add/update
	// (never on delete — removing a rule cancels nothing that exists).
	KeywordRulesOnChanged func()
	// MirakcProxy serves /api/mirakc/* (mirakc.NewProxy implements the
	// unset-URL 500 convention itself). nil → the paths fall through to
	// the SPA/404 handler.
	MirakcProxy http.Handler
	// Programs serves GET /api/programs. nil likewise.
	Programs http.Handler
}

// testNotification is the ntfy/Discord test-send payload
// (server/locales/ja-JP/notification.ts "test").
var testNotification = settings.Notification{
	Title:   "テスト通知",
	Message: "mirakc-ui からのテスト通知です。",
	Tags:    []string{"bell"},
}

var okBody = struct {
	Ok bool `json:"ok"`
}{Ok: true}

// NewRouter builds the full route table. The caller must wrap the result
// with Deps.Auth.Middleware so authentication covers every route (API,
// streams, static/SPA) — the same mount-order contract main.ts has.
func NewRouter(d Deps) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/auth/ticket", d.Auth.TicketHandler)

	mountCRUD(mux, d, crudSpec[settings.KeywordRuleInput]{
		base:      "/api/keyword-rules",
		col:       store.ColKeywordRules,
		parse:     settings.ParseKeywordRuleInput,
		notFound:  "rule not found",
		onChanged: d.KeywordRulesOnChanged,
	})
	mountCRUD(mux, d, crudSpec[settings.LiveCommentMappingInput]{
		base:     "/api/live-comment-settings",
		col:      store.ColLiveComment,
		parse:    settings.ParseLiveCommentMappingInput,
		notFound: "mapping not found",
	})

	mountNotificationSettings(mux, d)

	if d.MirakcProxy != nil {
		mux.Handle("/api/mirakc", d.MirakcProxy)
		mux.Handle("/api/mirakc/", d.MirakcProxy)
	}
	if d.Programs != nil {
		mux.Handle("/api/programs", d.Programs)
	}

	mux.Handle("/", web.Handler())
	return mux
}

type crudSpec[I any] struct {
	base      string
	col       string
	parse     func([]byte) (I, error)
	notFound  string
	onChanged func()
}

// mountCRUD registers the GET/POST/PUT/DELETE collection endpoints shared
// by keyword rules and live-comment mappings.
func mountCRUD[I any](mux *http.ServeMux, d Deps, spec crudSpec[I]) {
	mux.HandleFunc("GET "+spec.base, func(w http.ResponseWriter, r *http.Request) {
		list, err := d.Store.List(spec.col)
		if err != nil {
			internalError(w, err)
			return
		}
		records := make([]json.RawMessage, 0, len(list))
		for _, rec := range list {
			records = append(records, rec.Data)
		}
		writeJSON(w, http.StatusOK, records)
	})
	mux.HandleFunc("POST "+spec.base, func(w http.ResponseWriter, r *http.Request) {
		input, err := spec.parse(readBody(r))
		if err != nil {
			badRequest(w, err)
			return
		}
		rec, err := d.Store.Add(spec.col, mustMarshal(input))
		if err != nil {
			internalError(w, err)
			return
		}
		if spec.onChanged != nil {
			spec.onChanged()
		}
		writeJSON(w, http.StatusCreated, rec.Data)
	})
	mux.HandleFunc("PUT "+spec.base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		input, err := spec.parse(readBody(r))
		if err != nil {
			badRequest(w, err)
			return
		}
		rec, found, err := d.Store.Update(spec.col, r.PathValue("id"), mustMarshal(input))
		if err != nil {
			internalError(w, err)
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, errorBody{Error: spec.notFound})
			return
		}
		if spec.onChanged != nil {
			spec.onChanged()
		}
		writeJSON(w, http.StatusOK, rec.Data)
	})
	mux.HandleFunc("DELETE "+spec.base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		removed, err := d.Store.Remove(spec.col, r.PathValue("id"))
		if err != nil {
			internalError(w, err)
			return
		}
		if !removed {
			writeJSON(w, http.StatusNotFound, errorBody{Error: spec.notFound})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func mountNotificationSettings(mux *http.ServeMux, d Deps) {
	load := func() (settings.NotificationSettings, error) {
		raw, found, err := d.Store.GetSingleton(store.ColNotification)
		if err != nil || !found {
			return settings.DefaultNotificationSettings, err
		}
		normalized, ok := settings.NormalizeNotificationSettings(raw)
		if !ok {
			return settings.DefaultNotificationSettings, nil
		}
		return normalized, nil
	}

	mux.HandleFunc("GET /api/notification-settings", func(w http.ResponseWriter, r *http.Request) {
		current, err := load()
		if err != nil {
			internalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settings.ToNotificationSettingsView(current))
	})

	mux.HandleFunc("PUT /api/notification-settings", func(w http.ResponseWriter, r *http.Request) {
		draft, err := settings.ParseNotificationSettingsInput(readBody(r))
		if err != nil {
			badRequest(w, err)
			return
		}
		current, err := load()
		if err != nil {
			internalError(w, err)
			return
		}
		merged, err := settings.MergeNotificationSettings(current, draft)
		if err != nil {
			badRequest(w, err)
			return
		}
		if err := d.Store.SetSingleton(store.ColNotification, mustMarshal(merged)); err != nil {
			internalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settings.ToNotificationSettingsView(merged))
	})

	mux.HandleFunc("POST /api/notification-settings/test/ntfy", func(w http.ResponseWriter, r *http.Request) {
		// Loose decode like the Deno route: non-string fields become "".
		body, err := bodyAsMap(readBody(r))
		if err != nil {
			badRequest(w, err)
			return
		}
		target := settings.NtfyTarget{
			URL:   trimStringField(body, "url"),
			Token: trimStringField(body, "token"),
		}
		if !settings.IsValidNtfyURL(target.URL) {
			badRequest(w, errors.New("url must be a http(s) URL with a topic"))
			return
		}
		if !settings.SendNtfy(target, testNotification) {
			writeJSON(w, http.StatusBadGateway, errorBody{Error: "failed to send test notification"})
			return
		}
		writeJSON(w, http.StatusOK, okBody)
	})

	mux.HandleFunc("POST /api/notification-settings/test/discord", func(w http.ResponseWriter, r *http.Request) {
		body, err := bodyAsMap(readBody(r))
		if err != nil {
			badRequest(w, err)
			return
		}
		webhookURL := trimStringField(body, "webhookUrl")
		if !settings.IsValidDiscordWebhookURL(webhookURL) {
			badRequest(w, errors.New("webhookUrl must be a Discord webhook URL"))
			return
		}
		if !settings.SendDiscord(settings.DiscordTarget{WebhookURL: webhookURL}, testNotification) {
			writeJSON(w, http.StatusBadGateway, errorBody{Error: "failed to send test notification"})
			return
		}
		writeJSON(w, http.StatusOK, okBody)
	})
}

// bodyAsMap decodes a loose request body. Non-object JSON or a broken
// stream yields the route-level "invalid JSON body" error.
func bodyAsMap(raw json.RawMessage) (map[string]any, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return nil, errors.New("invalid JSON body")
	}
	return body, nil
}

func trimStringField(body map[string]any, key string) string {
	s, _ := body[key].(string)
	return strings.TrimSpace(s)
}

func readBody(r *http.Request) json.RawMessage {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil
	}
	return body
}

func badRequest(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
}

func internalError(w http.ResponseWriter, _ error) {
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// writeJSON encodes without HTML escaping (JSON.stringify parity: a
// keyword may legitimately contain < or &).
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimRight(buf.Bytes(), "\n"))
}

func mustMarshal(v any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic("httpapi: marshal: " + err.Error())
	}
	// json.Encoder appends a newline; the store writes what it is given.
	return bytes.TrimRight(buf.Bytes(), "\n")
}
