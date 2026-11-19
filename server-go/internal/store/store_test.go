package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func recordData(t *testing.T, rec Record) map[string]any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal(rec.Data, &data); err != nil {
		t.Fatalf("record data is not JSON: %v", err)
	}
	return data
}

func TestOpenCreatesParentDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "test.sqlite3")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Add(ColKeywordRules, json.RawMessage(`{"keyword":"x"}`)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("database file not created: %v", err)
	}
}

func TestAddAssignsIDAndCreatedAt(t *testing.T) {
	s := newTestStore(t)
	before := time.Now().UnixMilli()

	rec, err := s.Add(ColKeywordRules, json.RawMessage(`{"keyword":"news","enabled":true}`))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	after := time.Now().UnixMilli()

	if len(rec.ID) != 36 || rec.ID[14] != '4' {
		t.Errorf("id %q is not a uuid v4", rec.ID)
	}
	if rec.CreatedAt < before || rec.CreatedAt > after {
		t.Errorf("createdAt = %d, want within [%d, %d]", rec.CreatedAt, before, after)
	}
	want := map[string]any{
		"keyword":   "news",
		"enabled":   true,
		"id":        rec.ID,
		"createdAt": float64(rec.CreatedAt),
	}
	if got := recordData(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("stored data = %v, want %v", got, want)
	}
}

func TestAddOverridesInputIDAndCreatedAt(t *testing.T) {
	s := newTestStore(t)

	rec, err := s.Add(ColKeywordRules, json.RawMessage(`{"id":"evil","createdAt":1,"keyword":"x"}`))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if rec.ID == "evil" {
		t.Error("input id must not survive")
	}
	if rec.CreatedAt == 1 {
		t.Error("input createdAt must not survive")
	}
	data := recordData(t, rec)
	if data["id"] != rec.ID || data["createdAt"] != float64(rec.CreatedAt) {
		t.Errorf("stored id/createdAt = %v/%v, want %q/%d", data["id"], data["createdAt"], rec.ID, rec.CreatedAt)
	}
	if data["keyword"] != "x" {
		t.Errorf("keyword = %v, want x", data["keyword"])
	}
}

func TestListOrdersCreatedAtDescThenIDAsc(t *testing.T) {
	s := newTestStore(t)
	ins := []struct {
		id string
		at int64
	}{
		{"zzz", 1000},
		{"aaa", 1000},
		{"mmm", 2000},
	}
	for _, r := range ins {
		if _, err := s.db.Exec(
			"INSERT INTO records (collection, id, created_at, data) VALUES (?, ?, ?, ?)",
			ColKeywordRules, r.id, r.at, "{}",
		); err != nil {
			t.Fatalf("insert %s: %v", r.id, err)
		}
	}

	list, err := s.List(ColKeywordRules)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make([]string, len(list))
	for i, rec := range list {
		got[i] = rec.ID
	}
	want := []string{"mmm", "aaa", "zzz"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}

	empty, err := s.List(ColLiveComment)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("List on empty collection = %v, want empty", empty)
	}
}

func TestUpdateKeepsIDAndCreatedAtAndReplacesFields(t *testing.T) {
	s := newTestStore(t)
	rec, err := s.Add(ColKeywordRules, json.RawMessage(`{"keyword":"old","enabled":true}`))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	upd, found, err := s.Update(ColKeywordRules, rec.ID,
		json.RawMessage(`{"keyword":"new","genres":[3],"id":"evil","createdAt":9}`))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !found {
		t.Fatal("Update: record not found")
	}
	if upd.ID != rec.ID || upd.CreatedAt != rec.CreatedAt {
		t.Errorf("id/createdAt = %q/%d, want %q/%d", upd.ID, upd.CreatedAt, rec.ID, rec.CreatedAt)
	}
	data := recordData(t, upd)
	if data["keyword"] != "new" {
		t.Errorf("keyword = %v, want new", data["keyword"])
	}
	if _, ok := data["enabled"]; ok {
		t.Error("update must fully replace, but enabled survived")
	}
	if !reflect.DeepEqual(data["genres"], []any{float64(3)}) {
		t.Errorf("genres = %v, want [3]", data["genres"])
	}

	list, err := s.List(ColKeywordRules)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || string(list[0].Data) != string(upd.Data) {
		t.Errorf("stored record = %v, want the updated one", list)
	}
}

func TestUpdateAbsentReturnsFalse(t *testing.T) {
	s := newTestStore(t)
	_, found, err := s.Update(ColKeywordRules, "missing", json.RawMessage(`{"keyword":"x"}`))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if found {
		t.Error("Update on missing id must not be found")
	}
}

func TestRemove(t *testing.T) {
	s := newTestStore(t)
	rec, err := s.Add(ColKeywordRules, json.RawMessage(`{"keyword":"x"}`))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	removed, err := s.Remove(ColKeywordRules, rec.ID)
	if err != nil || !removed {
		t.Fatalf("Remove = %v, %v, want true, nil", removed, err)
	}
	list, err := s.List(ColKeywordRules)
	if err != nil || len(list) != 0 {
		t.Fatalf("List after remove = %v, %v, want empty", list, err)
	}
	removed, err = s.Remove(ColKeywordRules, rec.ID)
	if err != nil || removed {
		t.Fatalf("second Remove = %v, %v, want false, nil", removed, err)
	}
}

func TestCollectionsAreIsolated(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Add(ColKeywordRules, json.RawMessage(`{"keyword":"x"}`)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := s.Add(ColLiveComment, json.RawMessage(`{"channel":"27"}`)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	rules, err := s.List(ColKeywordRules)
	if err != nil || len(rules) != 1 {
		t.Fatalf("List(keyword-rules) = %v, %v", rules, err)
	}
	mappings, err := s.List(ColLiveComment)
	if err != nil || len(mappings) != 1 {
		t.Fatalf("List(live-comment) = %v, %v", mappings, err)
	}
}

func TestSingleton(t *testing.T) {
	s := newTestStore(t)

	got, found, err := s.GetSingleton(ColNotification)
	if err != nil {
		t.Fatalf("GetSingleton: %v", err)
	}
	if found {
		t.Fatal("unsaved singleton must not be found")
	}
	if got != nil {
		t.Errorf("unsaved singleton data = %s, want nil", got)
	}

	first := json.RawMessage(`{"url":"https://ntfy.sh/x","onStart":true}`)
	if err := s.SetSingleton(ColNotification, first); err != nil {
		t.Fatalf("SetSingleton: %v", err)
	}
	got, found, err = s.GetSingleton(ColNotification)
	if err != nil || !found {
		t.Fatalf("GetSingleton = %v, %v, want found", found, err)
	}
	if string(got) != string(first) {
		t.Errorf("singleton data = %s, want %s", got, first)
	}

	// The set is a full overwrite, not a merge.
	if err := s.SetSingleton(ColNotification, json.RawMessage(`{"url":"https://ntfy.sh/y"}`)); err != nil {
		t.Fatalf("SetSingleton: %v", err)
	}
	got, _, err = s.GetSingleton(ColNotification)
	if err != nil {
		t.Fatalf("GetSingleton: %v", err)
	}
	data := recordData(t, Record{Data: got})
	if data["url"] != "https://ntfy.sh/y" {
		t.Errorf("url = %v, want https://ntfy.sh/y", data["url"])
	}
	if _, ok := data["onStart"]; ok {
		t.Error("onStart survived a full overwrite")
	}
}
