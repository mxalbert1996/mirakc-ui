package i18n

// Port of server/locales/ja-JP/translation.ts + notification.ts. Strings
// are copied verbatim from the Deno source; do not re-translate here —
// edit both sides together or only after the Deno server is retired.

var translation = map[string]any{
	"notification": map[string]any{
		"test": map[string]any{
			"title":   "テスト通知",
			"message": "mirakc-ui からのテスト通知です。",
		},
		"recording": map[string]any{
			"fallbackName": "番組 ID: {{programId}}",
			"startedAt":    "{{datetime}} 開始",
			"airtime":      "{{start}} 〜 {{end}}",
			"started": map[string]any{
				"title":   "録画開始: {{name}}",
				"message": "録画を開始しました。",
			},
			"stopped": map[string]any{
				"title":   "録画終了: {{name}}",
				"message": "録画を終了しました。",
			},
			"failed": map[string]any{
				"title":   "録画失敗: {{name}}",
				"message": "録画に失敗しました。",
			},
			"scheduled": map[string]any{
				"title":   "録画登録: {{name}}",
				"message": "録画予約を登録しました。",
			},
			"unscheduled": map[string]any{
				"title":   "録画削除: {{name}}",
				"message": "録画予約を削除しました。",
			},
		},
		"keyword": map[string]any{
			"title":   "録画登録: {{name}}",
			"message": "キーワード「{{keyword}}」に一致する番組を予約しました。",
		},
	},
}
