package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/api"
)

// exportVersion is the only export.json format this importer understands.
const exportVersion = 1

// exportFile is the shape written by `deno task export:data`
// (server/export-data.ts).
type exportFile struct {
	Version     int `json:"version"`
	Collections struct {
		KeywordRules json.RawMessage `json:"keyword-rules"`
		LiveComment  json.RawMessage `json:"live-comment"`
		Notification json.RawMessage `json:"notification"`
	} `json:"collections"`
}

// Import reads an export.json, validates every record against the generated
// API types (strict decode; a record that fails is skipped with a warning),
// stores the rest, and on success renames the file to export.json.imported.
// It is idempotent: records are keyed (collection, id), so re-importing the
// same data overwrites in place instead of duplicating.
func Import(s *Store, exportPath string, log *slog.Logger) error {
	raw, err := os.ReadFile(exportPath)
	if err != nil {
		return fmt.Errorf("store: read export file: %w", err)
	}
	var file exportFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("store: parse %s: %w", exportPath, err)
	}
	if file.Version != exportVersion {
		return fmt.Errorf(
			"store: unsupported export version %d in %s (want %d)",
			file.Version, exportPath, exportVersion,
		)
	}

	type row struct {
		col       string
		id        string
		createdAt int64
		data      []byte
	}
	var rows []row
	skipped := 0

	ruleRaws, err := rawArray(file.Collections.KeywordRules)
	if err != nil {
		return fmt.Errorf("store: parse keyword-rules in %s: %w", exportPath, err)
	}
	for i, rec := range ruleRaws {
		var rule api.KeywordRule
		if err := strictDecode(rec, &rule); err != nil {
			skipped++
			log.Warn("import: skipping invalid keyword rule", "index", i, "error", err)
			continue
		}
		rows = append(rows, row{ColKeywordRules, rule.Id.String(), rule.CreatedAt, rec})
	}

	mappingRaws, err := rawArray(file.Collections.LiveComment)
	if err != nil {
		return fmt.Errorf("store: parse live-comment in %s: %w", exportPath, err)
	}
	for i, rec := range mappingRaws {
		var mapping api.LiveCommentMapping
		if err := strictDecode(rec, &mapping); err != nil {
			skipped++
			log.Warn("import: skipping invalid live-comment mapping", "index", i, "error", err)
			continue
		}
		rows = append(rows, row{ColLiveComment, mapping.Id.String(), mapping.CreatedAt, rec})
	}

	// notification is null when never saved or invalid in Deno: leave the
	// singleton unset and GET falls back to the all-off defaults.
	var notification []byte
	if raw := file.Collections.Notification; len(raw) > 0 && !isNull(raw) {
		var settings api.NotificationSettings
		if err := strictDecode(raw, &settings); err != nil {
			skipped++
			log.Warn("import: skipping invalid notification settings", "error", err)
		} else {
			notification = raw
		}
	}

	imported := len(rows)
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: import: %w", err)
	}
	defer tx.Rollback()
	for _, r := range rows {
		if _, err := tx.Exec(`
INSERT INTO records (collection, id, created_at, data) VALUES (?, ?, ?, ?)
ON CONFLICT (collection, id) DO UPDATE SET created_at = excluded.created_at, data = excluded.data`,
			r.col, r.id, r.createdAt, string(r.data)); err != nil {
			return fmt.Errorf("store: import %s/%s: %w", r.col, r.id, err)
		}
	}
	if notification != nil {
		imported++
		if _, err := tx.Exec(`
INSERT INTO records (collection, id, created_at, data) VALUES (?, ?, ?, ?)
ON CONFLICT (collection, id) DO UPDATE SET data = excluded.data`,
			ColNotification, singletonID, time.Now().UnixMilli(), string(notification)); err != nil {
			return fmt.Errorf("store: import notification: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: import: %w", err)
	}
	if err := os.Rename(exportPath, exportPath+".imported"); err != nil {
		return fmt.Errorf("store: rename export file: %w", err)
	}
	log.Info("import: finished", "path", exportPath, "imported", imported, "skipped", skipped)
	return nil
}

// ImportIfFresh is the boot migration policy (plan §5.2): when the records
// table is empty and dataDir/export.json exists, import it; otherwise
// no-op.
func ImportIfFresh(s *Store, dataDir string, log *slog.Logger) error {
	s.importMu.Lock()
	defer s.importMu.Unlock()

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM records").Scan(&count); err != nil {
		return fmt.Errorf("store: check records: %w", err)
	}
	if count > 0 {
		return nil
	}
	exportPath := filepath.Join(dataDir, "export.json")
	if _, err := os.Stat(exportPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("store: stat %s: %w", exportPath, err)
	}
	return Import(s, exportPath, log)
}

// rawArray splits a raw JSON collection value into per-record raw JSON. A
// missing or null value is an empty collection.
func rawArray(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 || isNull(raw) {
		return nil, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func isNull(raw json.RawMessage) bool {
	return string(raw) == "null"
}

// strictDecode decodes raw JSON into v, rejecting unknown fields — the
// migration-time counterpart of the read-back validation the Deno stores did.
func strictDecode(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
