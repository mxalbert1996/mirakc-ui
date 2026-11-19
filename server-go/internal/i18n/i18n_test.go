package i18n

import "testing"

// Every dotted key the server uses (grep of server/ for `t("notification.`)
// must resolve to the exact Deno string, and every leaf in the tree must be
// a string so no key can silently resolve to a non-string node.
var serverKeys = map[string]string{
	"notification.test.title":                    "テスト通知",
	"notification.test.message":                  "mirakc-ui からのテスト通知です。",
	"notification.recording.fallbackName":        "番組 ID: {{programId}}",
	"notification.recording.startedAt":           "{{datetime}} 開始",
	"notification.recording.airtime":             "{{start}} 〜 {{end}}",
	"notification.recording.started.title":       "録画開始: {{name}}",
	"notification.recording.started.message":     "録画を開始しました。",
	"notification.recording.stopped.title":       "録画終了: {{name}}",
	"notification.recording.stopped.message":     "録画を終了しました。",
	"notification.recording.failed.title":        "録画失敗: {{name}}",
	"notification.recording.failed.message":      "録画に失敗しました。",
	"notification.recording.scheduled.title":     "録画登録: {{name}}",
	"notification.recording.scheduled.message":   "録画予約を登録しました。",
	"notification.recording.unscheduled.title":   "録画削除: {{name}}",
	"notification.recording.unscheduled.message": "録画予約を削除しました。",
	"notification.keyword.title":                 "録画登録: {{name}}",
	"notification.keyword.message":               "キーワード「{{keyword}}」に一致する番組を予約しました。",
}

func TestAllServerKeysResolve(t *testing.T) {
	for key, want := range serverKeys {
		if got := T(key, nil); got != want {
			t.Errorf("T(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestTreeHasOnlyStringLeaves(t *testing.T) {
	var walk func(prefix string, node map[string]any)
	walk = func(prefix string, node map[string]any) {
		for name, child := range node {
			if sub, ok := child.(map[string]any); ok {
				walk(prefix+name+".", sub)
				continue
			}
			if _, ok := child.(string); !ok {
				t.Errorf("%s%q is a %T, want string leaf", prefix, name, child)
			}
		}
	}
	walk("", translation)
	if count := len(serverKeys); count != 17 {
		t.Errorf("server key table has %d entries, expected 17", count)
	}
}

func TestInterpolation(t *testing.T) {
	cases := []struct {
		name string
		key  string
		vars map[string]string
		want string
	}{
		{
			name: "{{name}} interpolation",
			key:  "notification.recording.started.title",
			vars: map[string]string{"name": "ニュース７"},
			want: "録画開始: ニュース７",
		},
		{
			name: "numeric value passed as string",
			key:  "notification.recording.fallbackName",
			vars: map[string]string{"programId": "123"},
			want: "番組 ID: 123",
		},
		{
			name: "two placeholders",
			key:  "notification.recording.airtime",
			vars: map[string]string{"start": "2026/01/02 03:04", "end": "04:05"},
			want: "2026/01/02 03:04 〜 04:05",
		},
		{
			name: "missing var leaves placeholder untouched",
			key:  "notification.recording.started.title",
			vars: map[string]string{},
			want: "録画開始: {{name}}",
		},
		{
			name: "nil vars leaves placeholder untouched",
			key:  "notification.recording.startedAt",
			want: "{{datetime}} 開始",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := T(tc.key, tc.vars); got != tc.want {
				t.Errorf("T(%q, %v) = %q, want %q", tc.key, tc.vars, got, tc.want)
			}
		})
	}
}

func TestUnknownKeyReturnsKeyItself(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{name: "missing leaf", key: "notification.missing.key"},
		{name: "missing top segment", key: "missing.test.title"},
		{name: "dotted-path miss mid-tree", key: "notification.test.title.deeper"},
		{name: "key resolves to a non-string node", key: "notification.recording"},
		{name: "key resolves to the root", key: "notification"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := T(tc.key, nil); got != tc.key {
				t.Errorf("T(%q) = %q, want the key itself", tc.key, got)
			}
		})
	}
}
