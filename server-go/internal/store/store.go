// Package store persists settings as JSON records in a generic SQLite
// table, mirroring the Deno KV collectionStore / singletonStore semantics
// (server/store/kv.ts, docs/go-rewrite-plan.md §5.1).
package store

import (
	"bytes"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	// Registers the "sqlite" database/sql driver.
	_ "modernc.org/sqlite"
)

// singletonID is the fixed records.id used by singleton collections.
const singletonID = "_singleton"

// Collection names, matching the export.json keys (server/export-data.ts).
const (
	ColKeywordRules = "keyword-rules"
	ColLiveComment  = "live-comment"
	ColNotification = "notification"
)

const schema = `
CREATE TABLE IF NOT EXISTS records (
  collection  TEXT NOT NULL,
  id          TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  data        TEXT NOT NULL,
  PRIMARY KEY (collection, id)
);
CREATE INDEX IF NOT EXISTS idx_records_collection ON records (collection, created_at DESC, id);
`

// Record is one stored collection record. Data holds the full record JSON,
// including the id and createdAt fields.
type Record struct {
	ID        string
	CreatedAt int64
	Data      json.RawMessage
}

// Store wraps the single SQLite database shared by all collections.
type Store struct {
	db       *sql.DB
	importMu sync.Mutex
}

// Open creates missing parent directories (like the Deno createKv) and opens
// the database in WAL mode with a 5s busy timeout.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create %s: %w", dir, err)
		}
	}
	dsn := path + "?_busy_timeout=5000&_journal_mode=WAL"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: initialize schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// List returns the collection's records, newest first — createdAt
// descending, ties broken by id ascending (the collectionStore default sort).
func (s *Store) List(col string) ([]Record, error) {
	rows, err := s.db.Query(
		"SELECT id, created_at, data FROM records WHERE collection = ? ORDER BY created_at DESC, id",
		col,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list %s: %w", col, err)
	}
	defer rows.Close()
	// Non-nil even when empty, so JSON marshals to [] like the Deno stores.
	records := []Record{}
	for rows.Next() {
		var rec Record
		var data string
		if err := rows.Scan(&rec.ID, &rec.CreatedAt, &data); err != nil {
			return nil, fmt.Errorf("store: list %s: %w", col, err)
		}
		rec.Data = json.RawMessage(data)
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list %s: %w", col, err)
	}
	return records, nil
}

// Add stores a new record: a fresh uuid v4 id and the current time as
// createdAt, merged over the input like `{ ...input, id, createdAt }`. Input
// fields cannot override them.
func (s *Store) Add(col string, data json.RawMessage) (Record, error) {
	return s.add(col, data, time.Now().UnixMilli())
}

func (s *Store) add(col string, data json.RawMessage, now int64) (Record, error) {
	obj, err := decodeObject(data)
	if err != nil {
		return Record{}, fmt.Errorf("store: add %s: %w", col, err)
	}
	id, err := newUUID()
	if err != nil {
		return Record{}, fmt.Errorf("store: add %s: %w", col, err)
	}
	rec, err := stampedJSON(obj, id, now)
	if err != nil {
		return Record{}, fmt.Errorf("store: add %s: %w", col, err)
	}
	if _, err := s.db.Exec(
		"INSERT INTO records (collection, id, created_at, data) VALUES (?, ?, ?, ?)",
		col, id, now, string(rec.Data),
	); err != nil {
		return Record{}, fmt.Errorf("store: add %s: %w", col, err)
	}
	return rec, nil
}

// Update replaces a record's fields, keeping its id and createdAt like the
// collectionStore `{ ...input, id, createdAt: current.createdAt }`. found is
// false when the id does not exist. The input is not validated here.
func (s *Store) Update(col, id string, data json.RawMessage) (rec Record, found bool, err error) {
	var createdAt int64
	err = s.db.QueryRow(
		"SELECT created_at FROM records WHERE collection = ? AND id = ?", col, id,
	).Scan(&createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("store: update %s: %w", col, err)
	}
	obj, err := decodeObject(data)
	if err != nil {
		return Record{}, false, fmt.Errorf("store: update %s: %w", col, err)
	}
	rec, err = stampedJSON(obj, id, createdAt)
	if err != nil {
		return Record{}, false, fmt.Errorf("store: update %s: %w", col, err)
	}
	if _, err := s.db.Exec(
		"UPDATE records SET data = ? WHERE collection = ? AND id = ?",
		string(rec.Data), col, id,
	); err != nil {
		return Record{}, false, fmt.Errorf("store: update %s: %w", col, err)
	}
	return rec, true, nil
}

// Remove deletes the record with the given id. removed is false when it does
// not exist.
func (s *Store) Remove(col, id string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM records WHERE collection = ? AND id = ?", col, id)
	if err != nil {
		return false, fmt.Errorf("store: remove %s: %w", col, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: remove %s: %w", col, err)
	}
	return n > 0, nil
}

// GetSingleton returns the singleton's stored value. found is false when it
// has never been saved.
func (s *Store) GetSingleton(col string) (json.RawMessage, bool, error) {
	var data string
	err := s.db.QueryRow(
		"SELECT data FROM records WHERE collection = ? AND id = ?", col, singletonID,
	).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: get %s: %w", col, err)
	}
	return json.RawMessage(data), true, nil
}

// SetSingleton saves the value, fully overwriting any previous one (upsert on
// the fixed _singleton id).
func (s *Store) SetSingleton(col string, data json.RawMessage) error {
	_, err := s.db.Exec(`
INSERT INTO records (collection, id, created_at, data) VALUES (?, ?, ?, ?)
ON CONFLICT (collection, id) DO UPDATE SET data = excluded.data`,
		col, singletonID, time.Now().UnixMilli(), string(data))
	if err != nil {
		return fmt.Errorf("store: set %s: %w", col, err)
	}
	return nil
}

// stampedJSON merges id and createdAt into obj (overriding any input values,
// like `{ ...input, id, createdAt }`) and marshals the full record. HTML
// escaping stays off so a keyword containing < or & round-trips exactly as
// the Deno store kept it.
func stampedJSON(obj map[string]any, id string, createdAt int64) (Record, error) {
	obj["id"] = id
	obj["createdAt"] = createdAt
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return Record{}, err
	}
	return Record{ID: id, CreatedAt: createdAt, Data: bytes.TrimRight(buf.Bytes(), "\n")}, nil
}

// decodeObject parses a record's input JSON, keeping numbers literal so
// re-marshalling preserves them exactly.
func decodeObject(data json.RawMessage) (map[string]any, error) {
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, errors.New("record must be a JSON object")
	}
	if dec.More() {
		return nil, errors.New("unexpected data after record")
	}
	return obj, nil
}

// newUUID returns a random RFC 4122 version 4 UUID.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
