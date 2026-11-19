package settings

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/api"
)

func TestIsValidNtfyURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://ntfy.sh/mirakc-rec", true},
		{"http://ntfy.example.com/topic", true},
		{"https://example.com/ntfy/topic", true}, // self-hosted sub-path
		{"", false},
		{"ntfy.sh/topic", false}, // no scheme
		{"ftp://ntfy.sh/topic", false},
		{"https://ntfy.sh", false}, // no topic
		{"https://ntfy.sh/", false},
	}
	for _, tc := range cases {
		if got := IsValidNtfyURL(tc.url); got != tc.want {
			t.Errorf("IsValidNtfyURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestSplitNtfyURL(t *testing.T) {
	cases := []struct {
		url   string
		base  string
		topic string
		ok    bool
	}{
		{"https://ntfy.sh/mirakc-rec", "https://ntfy.sh", "mirakc-rec", true},
		{"https://example.com/ntfy/topic", "https://example.com/ntfy", "topic", true},
		{"https://ntfy.sh/topic/", "https://ntfy.sh", "topic", true},
		{"https://ntfy.sh", "", "", false},
		{"not a url", "", "", false},
	}
	for _, tc := range cases {
		base, topic, ok := SplitNtfyURL(tc.url)
		if ok != tc.ok || (ok && (base != tc.base || topic != tc.topic)) {
			t.Errorf("SplitNtfyURL(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.url, base, topic, ok, tc.base, tc.topic, tc.ok)
		}
	}
}

func TestIsValidDiscordWebhookURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://discord.com/api/webhooks/123/abc-DEF_0", true},
		{"https://discordapp.com/api/webhooks/123456789/tok", true},
		{"https://discord.com/api/v10/webhooks/1/tok", true},
		{"https://canary.discord.com/api/webhooks/1/tok/", true}, // trailing slash tolerated
		{"", false},
		{"http://discord.com/api/webhooks/1/tok", false}, // https only
		{"https://example.com/api/webhooks/1/tok", false},
		{"https://discord.com/api/webhooks/123", false},     // token missing
		{"https://discord.com/api/webhooks/abc/tok", false}, // id not numeric
	}
	for _, tc := range cases {
		if got := IsValidDiscordWebhookURL(tc.url); got != tc.want {
			t.Errorf("IsValidDiscordWebhookURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestNormalizeNotificationSettings(t *testing.T) {
	// New shape passes through unchanged.
	stored := NotificationSettings{
		URL:               "https://ntfy.sh/x",
		Token:             "tk",
		DiscordWebhookURL: "https://discord.com/api/webhooks/123/abc",
		OnSchedule:        true,
		OnStart:           true,
		OnEnd:             false,
		OnFail:            true,
		OnRemove:          false,
	}
	got, ok := NormalizeNotificationSettings([]byte(`{
		"url":"https://ntfy.sh/x",
		"token":"tk",
		"discordWebhookUrl":"https://discord.com/api/webhooks/123/abc",
		"onSchedule":true,
		"onStart":true,
		"onEnd":false,
		"onFail":true,
		"onRemove":false
	}`))
	if !ok || got != stored {
		t.Fatalf("got %+v, ok=%v", got, ok)
	}

	// Old shape (only onStart / onEnd) gets the newer toggles defaulted off.
	got, ok = NormalizeNotificationSettings([]byte(`{
		"url":"https://ntfy.sh/x","token":"tk","onStart":true,"onEnd":false
	}`))
	want := NotificationSettings{
		URL:     "https://ntfy.sh/x",
		Token:   "tk",
		OnStart: true,
	}
	if !ok || got != want {
		t.Fatalf("old shape: got %+v, ok=%v, want %+v", got, ok, want)
	}

	// Pre-Discord shape gets discordWebhookUrl defaulted to "".
	got, ok = NormalizeNotificationSettings([]byte(`{
		"url":"https://ntfy.sh/x","token":"tk",
		"onSchedule":false,"onStart":false,"onEnd":false,"onFail":false,"onRemove":false
	}`))
	if !ok || got.DiscordWebhookURL != "" {
		t.Fatalf("pre-discord shape: got %+v, ok=%v", got, ok)
	}

	for _, body := range []string{
		`null`,
		`{}`,
		`{"url":1,"token":""}`,
		`{"url":"","token":"","onStart":"yes"}`,
		`{"url":"","token":"","discordWebhookUrl":1}`,
		`"string"`,
	} {
		if _, ok := NormalizeNotificationSettings([]byte(body)); ok {
			t.Errorf("NormalizeNotificationSettings(%s) ok, want invalid", body)
		}
	}
}

func TestParseNotificationSettingsInputTrimsAndDefaults(t *testing.T) {
	draft, err := ParseNotificationSettingsInput([]byte(`{
		"url":" https://ntfy.sh/mirakc-rec ",
		"token":" tk_abc ",
		"onStart":true,
		"onEnd":false
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := NotificationSettingsDraft{
		URL:     "https://ntfy.sh/mirakc-rec",
		Token:   Secret{Update: SecretReplace, Value: "tk_abc"},
		OnStart: true,
	}
	if draft != want {
		t.Fatalf("got %+v, want %+v", draft, want)
	}
}

func TestParseNotificationSettingsInputSecretSignals(t *testing.T) {
	draftOf := func(body string) NotificationSettingsDraft {
		t.Helper()
		draft, err := ParseNotificationSettingsInput([]byte(body))
		if err != nil {
			t.Fatalf("parse %s: %v", body, err)
		}
		return draft
	}

	// Absent and blank (whitespace-only) secrets mean "keep".
	if got := draftOf(`{"url":""}`).Token; got.Update != SecretKeep {
		t.Errorf("absent token = %+v, want keep", got)
	}
	if got := draftOf(`{"url":"","token":"  "}`).Token; got.Update != SecretKeep {
		t.Errorf("blank token = %+v, want keep", got)
	}
	if got := draftOf(`{"url":"","discordWebhookUrl":""}`).DiscordWebhookURL; got.Update != SecretKeep {
		t.Errorf("blank discordWebhookUrl = %+v, want keep", got)
	}
	// Explicit null means "clear".
	if got := draftOf(`{"url":"","token":null}`).Token; got.Update != SecretClear {
		t.Errorf("null token = %+v, want clear", got)
	}
	if got := draftOf(`{"url":"","discordWebhookUrl":null}`).DiscordWebhookURL; got.Update != SecretClear {
		t.Errorf("null discordWebhookUrl = %+v, want clear", got)
	}
}

func TestParseNotificationSettingsInputInvalid(t *testing.T) {
	for _, body := range []string{
		`null`,
		`{}`,
		`{"url":1}`,
		`{"url":"","token":1}`,
		`{"url":"","token":"","onStart":"yes"}`,
		`{"url":"","token":"","onFail":1}`,
		`{"url":"","discordWebhookUrl":1}`,
	} {
		if _, err := ParseNotificationSettingsInput([]byte(body)); err == nil {
			t.Errorf("ParseNotificationSettingsInput(%s) succeeded, want error", body)
		}
	}
}

func TestParseNotificationSettingsInputURLValidation(t *testing.T) {
	// A non-empty url is format-checked even with every event off.
	_, err := ParseNotificationSettingsInput([]byte(
		`{"url":"not a url","token":"","onStart":false,"onEnd":false}`,
	))
	if err == nil || err.Error() != "url must be a http(s) URL with a topic" {
		t.Fatalf("error = %v", err)
	}

	// A non-empty discordWebhookUrl is format-checked.
	_, err = ParseNotificationSettingsInput([]byte(
		`{"url":"","token":"","discordWebhookUrl":"https://example.com/not-a-webhook","onStart":false}`,
	))
	if err == nil || err.Error() != "discordWebhookUrl must be a Discord webhook URL" {
		t.Fatalf("error = %v", err)
	}

	// A Discord webhook alone (ntfy url empty) passes and is trimmed.
	draft, err := ParseNotificationSettingsInput([]byte(
		`{"url":"","token":"","discordWebhookUrl":" https://discord.com/api/webhooks/123/abc ","onStart":true}`,
	))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if draft.DiscordWebhookURL != (Secret{Update: SecretReplace, Value: "https://discord.com/api/webhooks/123/abc"}) {
		t.Fatalf("discordWebhookUrl = %+v", draft.DiscordWebhookURL)
	}
}

func TestMergeNotificationSettingsAllOff(t *testing.T) {
	draft, err := ParseNotificationSettingsInput([]byte(`{
		"url":"","token":"","onSchedule":false,"onStart":false,
		"onEnd":false,"onFail":false,"onRemove":false
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	merged, err := MergeNotificationSettings(DefaultNotificationSettings, draft)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged != DefaultNotificationSettings {
		t.Fatalf("got %+v", merged)
	}
}

func TestMergeNotificationSettingsRequiresDestination(t *testing.T) {
	const want = "a destination (ntfy url or discord webhook) is required when notification is enabled"
	for _, body := range []string{
		`{"url":"","onSchedule":true}`,
		`{"url":"","onStart":true}`,
		`{"url":"","onEnd":true}`,
		`{"url":"","onFail":true}`,
		`{"url":"","onRemove":true}`,
	} {
		draft, err := ParseNotificationSettingsInput([]byte(body))
		if err != nil {
			t.Fatalf("parse %s: %v", body, err)
		}
		_, err = MergeNotificationSettings(DefaultNotificationSettings, draft)
		if err == nil || err.Error() != want {
			t.Errorf("%s: error = %v, want %q", body, err, want)
		}
	}
}

func TestMergeNotificationSettingsKeptDestination(t *testing.T) {
	current := DefaultNotificationSettings
	current.DiscordWebhookURL = "https://discord.com/api/webhooks/123/abc"

	draft, err := ParseNotificationSettingsInput([]byte(`{"url":"","onStart":true}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	merged, err := MergeNotificationSettings(current, draft)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged.DiscordWebhookURL != current.DiscordWebhookURL {
		t.Fatalf("kept discordWebhookUrl = %q", merged.DiscordWebhookURL)
	}
}

func TestMergeNotificationSettingsSecrets(t *testing.T) {
	current := NotificationSettings{
		URL:               "https://ntfy.sh/x",
		Token:             "tk_old",
		DiscordWebhookURL: "https://discord.com/api/webhooks/123/abc",
	}

	// Absent and blank (empty string) secrets keep the stored values.
	for _, body := range []string{
		`{"url":"https://ntfy.sh/x"}`,
		`{"url":"https://ntfy.sh/x","token":"","discordWebhookUrl":""}`,
	} {
		draft, err := ParseNotificationSettingsInput([]byte(body))
		if err != nil {
			t.Fatalf("parse %s: %v", body, err)
		}
		merged, err := MergeNotificationSettings(current, draft)
		if err != nil {
			t.Fatalf("merge %s: %v", body, err)
		}
		if merged.Token != "tk_old" || merged.DiscordWebhookURL != current.DiscordWebhookURL {
			t.Errorf("%s: merged = %+v", body, merged)
		}
	}

	// Non-empty secrets replace.
	merged, err := MergeNotificationSettings(current, NotificationSettingsDraft{
		URL:               current.URL,
		Token:             Secret{Update: SecretReplace, Value: "tk_new"},
		DiscordWebhookURL: Secret{Update: SecretReplace, Value: "https://discord.com/api/webhooks/456/def"},
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	want := current
	want.Token = "tk_new"
	want.DiscordWebhookURL = "https://discord.com/api/webhooks/456/def"
	if merged != want {
		t.Fatalf("got %+v, want %+v", merged, want)
	}

	// Explicit null clears.
	merged, err = MergeNotificationSettings(current, NotificationSettingsDraft{
		URL:               current.URL,
		Token:             Secret{Update: SecretClear},
		DiscordWebhookURL: Secret{Update: SecretClear},
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	want = current
	want.Token = ""
	want.DiscordWebhookURL = ""
	if merged != want {
		t.Fatalf("got %+v, want %+v", merged, want)
	}
}

func TestToNotificationSettingsView(t *testing.T) {
	view := ToNotificationSettingsView(DefaultNotificationSettings)
	wantDefault := api.NotificationSettingsView{
		Url: "", TokenSet: false, DiscordWebhookUrlSet: false,
		OnSchedule: false, OnStart: false, OnEnd: false, OnFail: false, OnRemove: false,
	}
	if view != wantDefault {
		t.Fatalf("default view = %+v", view)
	}

	// The plain-text secrets never reach the response.
	b, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["token"]; ok {
		t.Error("response contains token")
	}
	if _, ok := fields["discordWebhookUrl"]; ok {
		t.Error("response contains discordWebhookUrl")
	}

	set := ToNotificationSettingsView(NotificationSettings{
		URL:               "https://ntfy.sh/x",
		Token:             "tk",
		DiscordWebhookURL: "https://discord.com/api/webhooks/123/abc",
	})
	if !set.TokenSet || !set.DiscordWebhookUrlSet || set.Url != "https://ntfy.sh/x" {
		t.Fatalf("masked view = %+v", set)
	}
	b, err = json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	fields = map[string]any{}
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"url":                  "https://ntfy.sh/x",
		"tokenSet":             true,
		"discordWebhookUrlSet": true,
		"onSchedule":           false,
		"onStart":              false,
		"onEnd":                false,
		"onFail":               false,
		"onRemove":             false,
	}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("wire shape = %s", b)
	}
}

func TestHasEnabledDestination(t *testing.T) {
	base := DefaultNotificationSettings
	base.OnFail = true

	if HasEnabledDestination(base, EventOnFail) {
		t.Error("no destination should be false")
	}
	ntfyOnly := base
	ntfyOnly.URL = "https://ntfy.sh/x"
	if !HasEnabledDestination(ntfyOnly, EventOnFail) {
		t.Error("ntfy destination should be true")
	}
	discordOnly := base
	discordOnly.DiscordWebhookURL = "https://discord.com/api/webhooks/1/tok"
	if !HasEnabledDestination(discordOnly, EventOnFail) {
		t.Error("discord destination should be true")
	}
	if HasEnabledDestination(ntfyOnly, EventOnStart) {
		t.Error("toggle off should be false even with a destination")
	}
}
