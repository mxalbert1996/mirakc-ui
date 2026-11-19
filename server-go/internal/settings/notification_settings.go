// Port of server/lib/notification-settings.ts: ntfy/Discord notification
// settings input parsing, stored-shape normalization, write-only secret merge
// semantics and URL validators.
package settings

import (
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/api"
)

var notificationSettingsInputSchema = &schema{
	types:    []string{"object"},
	required: []string{"url"},
	closed:   true,
	props: []schemaProp{
		{"url", &schema{types: []string{"string"}}},
		{"token", &schema{types: []string{"string", "null"}}},
		{"discordWebhookUrl", &schema{types: []string{"string", "null"}}},
		{"onSchedule", &schema{types: []string{"boolean"}}},
		{"onStart", &schema{types: []string{"boolean"}}},
		{"onEnd", &schema{types: []string{"boolean"}}},
		{"onFail", &schema{types: []string{"boolean"}}},
		{"onRemove", &schema{types: []string{"boolean"}}},
	},
}

// NotificationEventKey identifies one notification event toggle (display
// order: schedule -> start -> end -> fail -> remove).
type NotificationEventKey int

const (
	EventOnSchedule NotificationEventKey = iota
	EventOnStart
	EventOnEnd
	EventOnFail
	EventOnRemove
)

// NotificationEventKeys lists the toggles in display order.
var NotificationEventKeys = []NotificationEventKey{
	EventOnSchedule, EventOnStart, EventOnEnd, EventOnFail, EventOnRemove,
}

// NotificationSettings is the stored notification configuration with the
// write-only secrets in plain text. API responses never serialize this type;
// use ToNotificationSettingsView.
type NotificationSettings struct {
	URL               string
	Token             string
	DiscordWebhookURL string
	OnSchedule        bool
	OnStart           bool
	OnEnd             bool
	OnFail            bool
	OnRemove          bool
}

// DefaultNotificationSettings is the unsaved default (all notifications off).
var DefaultNotificationSettings = NotificationSettings{}

func (k NotificationEventKey) enabled(s NotificationSettings) bool {
	switch k {
	case EventOnSchedule:
		return s.OnSchedule
	case EventOnStart:
		return s.OnStart
	case EventOnEnd:
		return s.OnEnd
	case EventOnFail:
		return s.OnFail
	case EventOnRemove:
		return s.OnRemove
	}
	return false
}

// SecretUpdate tells MergeNotificationSettings how to treat one write-only
// secret from a PUT body.
type SecretUpdate int

const (
	SecretKeep    SecretUpdate = iota // absent or blank: keep the stored value
	SecretClear                       // explicit JSON null: clear
	SecretReplace                     // non-empty string: replace
)

// Secret is a parsed write-only secret field (token / discordWebhookUrl).
type Secret struct {
	Update SecretUpdate
	Value  string
}

// NotificationSettingsDraft is the parsed PUT body before secrets are merged
// with the stored values. Toggles are fully determined (absent = false).
type NotificationSettingsDraft struct {
	URL               string
	Token             Secret
	DiscordWebhookURL Secret
	OnSchedule        bool
	OnStart           bool
	OnEnd             bool
	OnFail            bool
	OnRemove          bool
}

// ParseNotificationSettingsInput validates a NotificationSettings PUT body
// against the OpenAPI schema and normalizes it: url is trimmed, non-empty
// url / discordWebhookUrl are format-checked even when every event is off,
// secrets become keep/clear/replace signals and toggles default to false.
// The returned error message is the client-facing 400 body.
func ParseNotificationSettingsInput(raw []byte) (NotificationSettingsDraft, error) {
	value, err := decodeJSONValue(raw)
	if err != nil {
		return NotificationSettingsDraft{}, errors.New("invalid JSON body")
	}
	if msg, ok := validateAgainst(notificationSettingsInputSchema, value); !ok {
		return NotificationSettingsDraft{}, errors.New(msg)
	}
	obj := value.(*orderedObject)

	u := strings.TrimSpace(obj.values["url"].(string))
	if u != "" && !IsValidNtfyURL(u) {
		return NotificationSettingsDraft{}, errors.New("url must be a http(s) URL with a topic")
	}
	discord := parseSecret(obj, "discordWebhookUrl")
	if discord.Update == SecretReplace && !IsValidDiscordWebhookURL(discord.Value) {
		return NotificationSettingsDraft{}, errors.New("discordWebhookUrl must be a Discord webhook URL")
	}

	return NotificationSettingsDraft{
		URL:               u,
		Token:             parseSecret(obj, "token"),
		DiscordWebhookURL: discord,
		OnSchedule:        boolOrDefault(obj, "onSchedule", false),
		OnStart:           boolOrDefault(obj, "onStart", false),
		OnEnd:             boolOrDefault(obj, "onEnd", false),
		OnFail:            boolOrDefault(obj, "onFail", false),
		OnRemove:          boolOrDefault(obj, "onRemove", false),
	}, nil
}

func parseSecret(obj *orderedObject, key string) Secret {
	v, present := obj.lookup(key)
	if !present {
		return Secret{Update: SecretKeep}
	}
	switch s := v.(type) {
	case nil:
		return Secret{Update: SecretClear}
	case string:
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			return Secret{Update: SecretKeep}
		}
		return Secret{Update: SecretReplace, Value: trimmed}
	}
	return Secret{Update: SecretKeep}
}

// NormalizeNotificationSettings coerces a stored value into
// NotificationSettings, completing pre-toggle and pre-Discord stored shapes
// (missing toggles default to false, missing discordWebhookUrl to "").
// ok=false means the value is not a valid shape; callers fall back to
// DefaultNotificationSettings, mirroring the Deno store's
// normalize(value) ?? DEFAULT composition.
func NormalizeNotificationSettings(value any) (NotificationSettings, bool) {
	v, ok := coerceStoredValue(value)
	if !ok {
		return NotificationSettings{}, false
	}
	obj, ok := v.(*orderedObject)
	if !ok {
		return NotificationSettings{}, false
	}
	u, ok := obj.lookup("url")
	storedURL, isString := u.(string)
	if !isString {
		return NotificationSettings{}, false
	}
	t, ok := obj.lookup("token")
	token, isString := t.(string)
	if !isString {
		return NotificationSettings{}, false
	}

	settings := DefaultNotificationSettings
	settings.URL = storedURL
	settings.Token = token
	if d, present := obj.lookup("discordWebhookUrl"); present {
		discord, isString := d.(string)
		if !isString {
			return NotificationSettings{}, false
		}
		settings.DiscordWebhookURL = discord
	}
	for _, key := range []string{"onSchedule", "onStart", "onEnd", "onFail", "onRemove"} {
		v, present := obj.lookup(key)
		if !present {
			continue
		}
		b, isBool := v.(bool)
		if !isBool {
			return NotificationSettings{}, false
		}
		switch key {
		case "onSchedule":
			settings.OnSchedule = b
		case "onStart":
			settings.OnStart = b
		case "onEnd":
			settings.OnEnd = b
		case "onFail":
			settings.OnFail = b
		case "onRemove":
			settings.OnRemove = b
		}
	}
	return settings, true
}

// MergeNotificationSettings resolves the draft's write-only secrets against
// current (keep stored / clear on null / replace on non-empty), overwrites
// the remaining fields and requires at least one destination (ntfy url or
// Discord webhook) on the merged value when any event is enabled. The
// returned error message is the client-facing 400 body.
func MergeNotificationSettings(current NotificationSettings, draft NotificationSettingsDraft) (NotificationSettings, error) {
	settings := NotificationSettings{
		URL:               draft.URL,
		Token:             resolveSecret(current.Token, draft.Token),
		DiscordWebhookURL: resolveSecret(current.DiscordWebhookURL, draft.DiscordWebhookURL),
		OnSchedule:        draft.OnSchedule,
		OnStart:           draft.OnStart,
		OnEnd:             draft.OnEnd,
		OnFail:            draft.OnFail,
		OnRemove:          draft.OnRemove,
	}
	anyEvent := false
	for _, key := range NotificationEventKeys {
		if key.enabled(settings) {
			anyEvent = true
			break
		}
	}
	if anyEvent && settings.URL == "" && settings.DiscordWebhookURL == "" {
		return NotificationSettings{}, errors.New("a destination (ntfy url or discord webhook) is required when notification is enabled")
	}
	return settings, nil
}

func resolveSecret(current string, secret Secret) string {
	switch secret.Update {
	case SecretKeep:
		return current
	case SecretClear:
		return ""
	default:
		return secret.Value
	}
}

// ToNotificationSettingsView masks the stored settings for API responses:
// the secrets are replaced by tokenSet / discordWebhookUrlSet flags.
func ToNotificationSettingsView(s NotificationSettings) api.NotificationSettingsView {
	return api.NotificationSettingsView{
		Url:                  s.URL,
		TokenSet:             s.Token != "",
		DiscordWebhookUrlSet: s.DiscordWebhookURL != "",
		OnSchedule:           s.OnSchedule,
		OnStart:              s.OnStart,
		OnEnd:                s.OnEnd,
		OnFail:               s.OnFail,
		OnRemove:             s.OnRemove,
	}
}

// HasEnabledDestination reports whether the event's toggle is on and at least
// one configured destination (valid ntfy URL or Discord webhook) exists.
func HasEnabledDestination(s NotificationSettings, key NotificationEventKey) bool {
	return key.enabled(s) && (IsValidNtfyURL(s.URL) || IsValidDiscordWebhookURL(s.DiscordWebhookURL))
}

// ---------------------------------------------------------------------------
// URL helpers (WHATWG URL semantics, like the Deno implementation).

// SplitNtfyURL decomposes an ntfy topic URL into its JSON-publishing base and
// the topic (the last non-empty path segment). ok=false when the URL is not
// http(s), has no host or no topic.
func SplitNtfyURL(rawURL string) (base, topic string, ok bool) {
	u, err := parseWebURL(rawURL)
	if err != nil {
		return "", "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", false
	}
	segments := nonEmptyPathSegments(u.EscapedPath())
	if len(segments) == 0 {
		return "", "", false
	}
	topic = segments[len(segments)-1]
	basePath := ""
	if len(segments) > 1 {
		basePath = "/" + strings.Join(segments[:len(segments)-1], "/")
	}
	return webOrigin(u) + basePath, topic, true
}

// IsValidNtfyURL reports whether url is an http(s) URL with a topic (a
// non-empty last path segment).
func IsValidNtfyURL(rawURL string) bool {
	_, _, ok := SplitNtfyURL(rawURL)
	return ok
}

var discordWebhookPath = regexp.MustCompile(`^/api(/v\d+)?/webhooks/\d+/[\w-]+$`)

// IsValidDiscordWebhookURL loosely checks the Discord Incoming Webhook form:
// https, a discord(app).com host (ptb. / canary. subdomains included) and a
// path of /api[/vN]/webhooks/{id}/{token}.
func IsValidDiscordWebhookURL(rawURL string) bool {
	u, err := parseWebURL(rawURL)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	hostOk := host == "discord.com" || host == "discordapp.com" ||
		strings.HasSuffix(host, ".discord.com") || strings.HasSuffix(host, ".discordapp.com")
	if !hostOk {
		return false
	}
	return discordWebhookPath.MatchString(strings.TrimSuffix(u.EscapedPath(), "/"))
}

// parseWebURL applies the WHATWG URL constructor basics that net/url alone
// does not: strips tabs/newlines and leading/trailing C0/space, requires a
// scheme and a non-empty host.
func parseWebURL(rawURL string) (*url.URL, error) {
	s := strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, rawURL)
	s = strings.TrimFunc(s, func(r rune) bool { return r <= ' ' })
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme == "" || u.Host == "" || strings.ContainsAny(u.Host, " ") {
		return nil, errors.New("invalid URL")
	}
	return u, nil
}

func webOrigin(u *url.URL) string {
	host := strings.ToLower(u.Host)
	if (u.Scheme == "https" && strings.HasSuffix(host, ":443")) ||
		(u.Scheme == "http" && strings.HasSuffix(host, ":80")) {
		host = host[:strings.LastIndex(host, ":")]
	}
	return u.Scheme + "://" + host
}

func nonEmptyPathSegments(path string) []string {
	var segments []string
	for _, seg := range strings.Split(path, "/") {
		if seg != "" {
			segments = append(segments, seg)
		}
	}
	return segments
}
