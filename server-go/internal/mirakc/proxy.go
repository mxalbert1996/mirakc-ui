package mirakc

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// ProgramInfo is the mirakc program subset used by notifications
// (ProgramInfo in server/lib/mirakc-events.ts) plus the id the proxy's
// CreatedSchedule needs. Every field but ID is optional upstream, hence
// the pointers.
type ProgramInfo struct {
	ID        int64   `json:"id"`
	Name      *string `json:"name"`
	StartAt   *int64  `json:"startAt"`
	Duration  *int64  `json:"duration"`
	NetworkID *int64  `json:"networkId"`
	ServiceID *int64  `json:"serviceId"`
}

// CreatedSchedule is the subset of the POST /recording/schedules response
// (WebRecordingSchedule) consumed by the schedule-created hook.
type CreatedSchedule struct {
	Program ProgramInfo `json:"program"`
}

// ProxyHooks detect manual schedule add/remove through the proxy. mirakc
// has no SSE event for schedule registration, so the proxy is the only
// place the UI's own operations can be observed.
type ProxyHooks struct {
	// OnScheduleCreated fires after a successful
	// POST /recording/schedules. Runs asynchronously; it never blocks or
	// changes the client response, and its failures are only logged.
	OnScheduleCreated func(CreatedSchedule)

	// OnScheduleRemoved fires after a successful
	// DELETE /recording/schedules/{program_id}.
	OnScheduleRemoved func(programID int64)
}

// proxyMount is the path prefix stripped from incoming requests; the
// handler works both mounted directly at /api/mirakc and behind
// http.StripPrefix.
const proxyMount = "/api/mirakc"

// Hop-by-hop headers, never forwarded in either direction.
var hopHeaders = map[string]bool{
	"Connection":        true,
	"Keep-Alive":        true,
	"Proxy-Connection":  true,
	"Upgrade":           true,
	"Te":                true,
	"Trailer":           true,
	"Transfer-Encoding": true,
}

// NewProxy builds the mirakc backend proxy (createMirakcProxy in
// server/routes/mirakc.ts): /api/mirakc/* forwards verbatim to
// ${MIRAKC_URL}/api/* — method, headers, query and body pass through, and
// the upstream status and headers are preserved. An empty mirakcURL
// (MIRAKC_URL unset) answers every request with the convention 500 JSON.
func NewProxy(mirakcURL string, hooks ProxyHooks, client *http.Client) http.Handler {
	if client == nil {
		client = http.DefaultClient
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mirakcURL == "" {
			writeJSONError(w, http.StatusInternalServerError, "MIRAKC_URL is not set")
			return
		}

		subPath := strings.TrimPrefix(r.URL.Path, proxyMount)
		target := APIURL(mirakcURL) + subPath
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}

		// GET/HEAD carry no body, mirroring the Deno proxy.
		var body io.Reader
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			body = r.Body
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
		if err != nil {
			log.Printf("[mirakc-proxy] failed to build upstream request: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		copyHeader(req.Header, r.Header)

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[mirakc-proxy] upstream request failed: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		ok := resp.StatusCode >= 200 && resp.StatusCode <= 299
		// Buffer only the hook-eligible schedule POST: its response is a
		// small JSON schedule. Everything else streams unmodified.
		bufferCreated := ok &&
			r.Method == http.MethodPost &&
			subPath == "/recording/schedules" &&
			hooks.OnScheduleCreated != nil

		copyHeader(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)

		if bufferCreated {
			buf, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				log.Printf("[mirakc-proxy] failed to read upstream response: %v", readErr)
			}
			if len(buf) > 0 {
				w.Write(buf)
			}
			var schedule CreatedSchedule
			if err := json.Unmarshal(buf, &schedule); err != nil {
				log.Printf("[mirakc-proxy] schedule-created hook failed: %v", err)
			} else {
				fireAsync(func() { hooks.OnScheduleCreated(schedule) })
			}
		} else if _, err := io.Copy(w, resp.Body); err != nil {
			log.Printf("[mirakc-proxy] failed to copy upstream response: %v", err)
		}

		if ok && r.Method == http.MethodDelete && hooks.OnScheduleRemoved != nil {
			if programID, digits := scheduleIDOf(subPath); digits {
				fireAsync(func() { hooks.OnScheduleRemoved(programID) })
			}
		}
	})
}

// scheduleIDOf matches ^/recording/schedules/(\d+)$.
func scheduleIDOf(subPath string) (int64, bool) {
	const prefix = "/recording/schedules/"
	if !strings.HasPrefix(subPath, prefix) {
		return 0, false
	}
	digits := subPath[len(prefix):]
	if digits == "" {
		return 0, false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// fireAsync runs a hook off the response path so it can never block or
// change the client response; hook failures are logged, never propagated.
func fireAsync(hook func()) {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[mirakc-proxy] hook panicked: %v", rec)
			}
		}()
		hook()
	}()
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		if hopHeaders[k] {
			continue
		}
		dst[k] = vv
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
