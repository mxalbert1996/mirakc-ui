// Command mirakc-ui is the Go application server for mirakc-ui: settings
// CRUD over SQLite, opt-in AUTH_TOKEN authentication, the embedded client,
// and the mirakc integration (proxy, programs API, keyword auto-recording,
// recording notifications). Ported from the Deno server (server/main.ts)
// per docs/go-rewrite-plan.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/app"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/httpapi"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/store"

	// The production image carries no zoneinfo; TZ-based formatting
	// (notifications, recording file names) must work regardless.
	_ "time/tzdata"
)

const defaultPort = "8000"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)

	dataDir := dataDirFromEnv()

	if len(os.Args) > 1 && os.Args[1] == "import" {
		runImport(dataDir, log)
		return
	}

	st, err := store.Open(filepath.Join(dataDir, "mirakc-ui.sqlite3"))
	if err != nil {
		log.Error("open store failed", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := store.ImportIfFresh(st, dataDir, log); err != nil {
		log.Error("export import failed", "error", err)
		os.Exit(1)
	}

	mirakcURL := os.Getenv("MIRAKC_URL")
	if mirakcURL == "" {
		// Parity with main.ts: mirakc-backed features log-and-disable.
		log.Error("MIRAKC_URL is not set; keyword recording and notifications are disabled")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	application := app.New(ctx, app.Deps{
		Store:           st,
		Auth:            httpapi.FromEnv(),
		Log:             log,
		MirakcURL:       mirakcURL,
		TimeZone:        timeZoneID(),
		KeywordInterval: keywordInterval(),
	})

	port := envOrDefault("PORT", defaultPort)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           application.Handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		log.Info("listening", "port", port, "dataDir", dataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "error", err)
			serveErr <- err
			stop()
		}
	}()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown failed", "error", err)
	}
	application.Stop()

	// A bind or serve failure must exit non-zero: restart policies treat
	// exit 0 as a clean stop.
	select {
	case err := <-serveErr:
		log.Error("server exited with failure", "error", err)
		os.Exit(1)
	default:
	}
}

// runImport is the explicit re-import subcommand: mirakc-ui import
// [export.json path], defaulting to DATA_DIR/export.json.
func runImport(dataDir string, log *slog.Logger) {
	path := filepath.Join(dataDir, "export.json")
	if len(os.Args) > 2 {
		path = os.Args[2]
	}
	st, err := store.Open(filepath.Join(dataDir, "mirakc-ui.sqlite3"))
	if err != nil {
		log.Error("open store failed", "error", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := store.Import(st, path, log); err != nil {
		log.Error("import failed", "error", err)
		os.Exit(1)
	}
	fmt.Printf("imported: %s\n", path)
}

// dataDirFromEnv matches the Deno dataDir(): trim whitespace and trailing
// slashes; empty or unset falls back to ./data.
func dataDirFromEnv() string {
	dir := strings.TrimRight(strings.TrimSpace(os.Getenv("DATA_DIR")), "/")
	if dir == "" {
		return "./data"
	}
	return dir
}

// timeZoneID mirrors main.ts's Temporal.Now.timeZoneId(): the process
// timezone (TZ env or system default). Without TZ in the container that
// is UTC, matching the Deno deployment.
func timeZoneID() string {
	if z := time.Local.String(); z != "" && z != "Local" {
		return z
	}
	return "UTC"
}

// keywordInterval mirrors main.ts's KEYWORD_RECORDING_INTERVAL_MINUTES:
// integer minutes >= 1, anything else falls back to 60.
func keywordInterval() time.Duration {
	minutes, err := strconv.Atoi(os.Getenv("KEYWORD_RECORDING_INTERVAL_MINUTES"))
	if err != nil || minutes < 1 {
		minutes = 60
	}
	return time.Duration(minutes) * time.Minute
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
