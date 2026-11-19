package store

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const happyExport = `{
  "version": 1,
  "exportedAt": "2026-11-01T00:00:00.000Z",
  "collections": {
    "keyword-rules": [
      {
        "id": "11111111-1111-4111-8111-111111111111",
        "keyword": "news",
        "from": "2026-01-01T00:00:00+09:00",
        "to": "2026-01-31T23:59:59+09:00",
        "channels": ["27"],
        "genres": [3],
        "enabled": true,
        "createdAt": 1000
      },
      {
        "id": "22222222-2222-4222-8222-222222222222",
        "keyword": "drama",
        "channels": [],
        "genres": [],
        "enabled": false,
        "createdAt": 2000
      }
    ],
    "live-comment": [
      {
        "id": "33333333-3333-4333-8333-333333333333",
        "channel": "27",
        "assignments": [{"source": "nx-jikkyo", "channelId": "jk1"}],
        "enabled": true,
        "createdAt": 1500
      }
    ],
    "notification": {
      "url": "https://ntfy.sh/mirakc-rec",
      "token": "tk_abc",
      "discordWebhookUrl": "",
      "onSchedule": true,
      "onStart": true,
      "onEnd": false,
      "onFail": false,
      "onRemove": false
    }
  }
}`

func writeExport(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "export.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write export.json: %v", err)
	}
	return path
}

func recordCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM records").Scan(&n); err != nil {
		t.Fatalf("count records: %v", err)
	}
	return n
}

func assertRenamed(t *testing.T, exportPath string) {
	t.Helper()
	if _, err := os.Stat(exportPath); !os.IsNotExist(err) {
		t.Errorf("export.json must be renamed away: %v", err)
	}
	if _, err := os.Stat(exportPath + ".imported"); err != nil {
		t.Errorf("export.json.imported missing: %v", err)
	}
}

func TestImportHappyPath(t *testing.T) {
	dir := t.TempDir()
	exportPath := writeExport(t, dir, happyExport)
	s := newTestStore(t)

	if err := Import(s, exportPath, slog.Default()); err != nil {
		t.Fatalf("Import: %v", err)
	}
	assertRenamed(t, exportPath)

	rules, err := s.List(ColKeywordRules)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("keyword rules = %d, want 2", len(rules))
	}
	if rules[0].ID != "22222222-2222-4222-8222-222222222222" || rules[0].CreatedAt != 2000 {
		t.Errorf("newest rule = %q@%d, want 2222…@2000", rules[0].ID, rules[0].CreatedAt)
	}
	if rules[1].ID != "11111111-1111-4111-8111-111111111111" || rules[1].CreatedAt != 1000 {
		t.Errorf("oldest rule = %q@%d, want 1111…@1000", rules[1].ID, rules[1].CreatedAt)
	}
	news := recordData(t, rules[1])
	if news["keyword"] != "news" || news["enabled"] != true {
		t.Errorf("imported rule fields = %v", news)
	}

	mappings, err := s.List(ColLiveComment)
	if err != nil || len(mappings) != 1 {
		t.Fatalf("live-comment mappings = %v, %v", mappings, err)
	}
	if got := recordData(t, mappings[0]); got["channel"] != "27" {
		t.Errorf("mapping channel = %v, want 27", got["channel"])
	}

	notif, found, err := s.GetSingleton(ColNotification)
	if err != nil || !found {
		t.Fatalf("notification singleton = %v, %v", found, err)
	}
	settings := recordData(t, Record{Data: notif})
	if settings["url"] != "https://ntfy.sh/mirakc-rec" || settings["onStart"] != true {
		t.Errorf("notification settings = %v", settings)
	}
}

func TestImportVersionMismatchLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	exportPath := writeExport(t, dir, strings.Replace(happyExport, `"version": 1`, `"version": 2`, 1))
	s := newTestStore(t)

	err := Import(s, exportPath, slog.Default())
	if err == nil {
		t.Fatal("Import must fail on an unsupported version")
	}
	if _, err := os.Stat(exportPath); err != nil {
		t.Errorf("export.json must stay in place: %v", err)
	}
	if _, err := os.Stat(exportPath + ".imported"); !os.IsNotExist(err) {
		t.Errorf("export.json.imported must not exist: %v", err)
	}
	if n := recordCount(t, s); n != 0 {
		t.Errorf("records = %d, want 0", n)
	}
}

func TestImportUnreadableFile(t *testing.T) {
	s := newTestStore(t)
	err := Import(s, filepath.Join(t.TempDir(), "nope.json"), slog.Default())
	if err == nil {
		t.Fatal("Import must fail on an unreadable file")
	}
}

func TestImportMalformedJSONLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	exportPath := writeExport(t, dir, "{oops")
	s := newTestStore(t)

	if err := Import(s, exportPath, slog.Default()); err == nil {
		t.Fatal("Import must fail on malformed JSON")
	}
	if _, err := os.Stat(exportPath); err != nil {
		t.Errorf("export.json must stay in place: %v", err)
	}
	if n := recordCount(t, s); n != 0 {
		t.Errorf("records = %d, want 0", n)
	}
}

func TestImportSkipsInvalidRecords(t *testing.T) {
	export := `{
  "version": 1,
  "exportedAt": "2026-11-01T00:00:00.000Z",
  "collections": {
    "keyword-rules": [
      {"id": "not-a-uuid", "keyword": "bad-id", "channels": [], "genres": [], "enabled": true, "createdAt": 1},
      {"id": "44444444-4444-4444-8444-444444444444", "keyword": "good", "channels": [], "genres": [], "enabled": true, "createdAt": 2},
      {"id": "55555555-5555-4555-8555-555555555555", "keyword": "unknown-field", "channels": [], "genres": [], "enabled": true, "createdAt": 3, "legacy": true}
    ],
    "live-comment": [
      {"id": "33333333-3333-4333-8333-333333333333", "channel": "27", "assignments": [{"source": "nx-jikkyo", "channelId": "jk1"}], "enabled": true, "createdAt": 4}
    ],
    "notification": {"url": 5}
  }
}`
	dir := t.TempDir()
	exportPath := writeExport(t, dir, export)
	s := newTestStore(t)

	if err := Import(s, exportPath, slog.Default()); err != nil {
		t.Fatalf("Import must continue past invalid records: %v", err)
	}
	assertRenamed(t, exportPath)

	rules, err := s.List(ColKeywordRules)
	if err != nil || len(rules) != 1 {
		t.Fatalf("keyword rules = %v, %v, want only the valid one", rules, err)
	}
	if rules[0].ID != "44444444-4444-4444-8444-444444444444" {
		t.Errorf("surviving rule = %q", rules[0].ID)
	}
	mappings, err := s.List(ColLiveComment)
	if err != nil || len(mappings) != 1 {
		t.Fatalf("live-comment mappings = %v, %v", mappings, err)
	}
	if _, found, err := s.GetSingleton(ColNotification); err != nil || found {
		t.Errorf("invalid notification must be skipped: found = %v, err = %v", found, err)
	}
}

func TestImportNotificationNullMeansNeverSaved(t *testing.T) {
	export := strings.Replace(happyExport,
		`"notification": {
      "url": "https://ntfy.sh/mirakc-rec",
      "token": "tk_abc",
      "discordWebhookUrl": "",
      "onSchedule": true,
      "onStart": true,
      "onEnd": false,
      "onFail": false,
      "onRemove": false
    }`,
		`"notification": null`, 1)
	dir := t.TempDir()
	exportPath := writeExport(t, dir, export)
	s := newTestStore(t)

	if err := Import(s, exportPath, slog.Default()); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, found, err := s.GetSingleton(ColNotification); err != nil || found {
		t.Errorf("null notification must leave the singleton unset: found = %v, err = %v", found, err)
	}
	rules, err := s.List(ColKeywordRules)
	if err != nil || len(rules) != 2 {
		t.Errorf("other collections must still import: %v, %v", rules, err)
	}
}

func TestImportIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t)

	exportPath := writeExport(t, dir, happyExport)
	if err := Import(s, exportPath, slog.Default()); err != nil {
		t.Fatalf("first Import: %v", err)
	}
	if n := recordCount(t, s); n != 4 {
		t.Fatalf("records after first import = %d, want 4", n)
	}

	exportPath = writeExport(t, dir, happyExport)
	if err := Import(s, exportPath, slog.Default()); err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if n := recordCount(t, s); n != 4 {
		t.Errorf("records after re-import = %d, want 4 (no duplicates)", n)
	}
	rules, err := s.List(ColKeywordRules)
	if err != nil || len(rules) != 2 {
		t.Errorf("keyword rules after re-import = %v, %v", rules, err)
	}
}

func TestImportIfFreshImportsWhenTableEmpty(t *testing.T) {
	dir := t.TempDir()
	writeExport(t, dir, happyExport)
	s := newTestStore(t)

	if err := ImportIfFresh(s, dir, slog.Default()); err != nil {
		t.Fatalf("ImportIfFresh: %v", err)
	}
	rules, err := s.List(ColKeywordRules)
	if err != nil || len(rules) != 2 {
		t.Fatalf("keyword rules = %v, %v", rules, err)
	}
	assertRenamed(t, filepath.Join(dir, "export.json"))
}

func TestImportIfFreshNoopWhenTableNotEmpty(t *testing.T) {
	dir := t.TempDir()
	exportPath := writeExport(t, dir, happyExport)
	s := newTestStore(t)
	if _, err := s.Add(ColKeywordRules, []byte(`{"keyword":"existing"}`)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := ImportIfFresh(s, dir, slog.Default()); err != nil {
		t.Fatalf("ImportIfFresh: %v", err)
	}
	rules, err := s.List(ColKeywordRules)
	if err != nil || len(rules) != 1 {
		t.Fatalf("existing data must be left alone: %v, %v", rules, err)
	}
	if _, err := os.Stat(exportPath); err != nil {
		t.Errorf("export.json must not be renamed on no-op: %v", err)
	}
}

func TestImportIfFreshNoopWithoutExportFile(t *testing.T) {
	s := newTestStore(t)

	if err := ImportIfFresh(s, t.TempDir(), slog.Default()); err != nil {
		t.Fatalf("ImportIfFresh: %v", err)
	}
	if n := recordCount(t, s); n != 0 {
		t.Errorf("records = %d, want 0", n)
	}
}
