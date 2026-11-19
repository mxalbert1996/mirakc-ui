// SSE relay for live comments, port of server/routes/comments.ts
// (GET /api/comments/services/{id}/stream). The composite service id is
// resolved to networkId/serviceId via mirakc, every Source is subscribed,
// and comments are fanned in to the client as SSE events:
//
//	sources: subscribed source ids, once right after connect (empty = not
//	         comment-capable; the client disconnects on it)
//	comment: one SourceComment as JSON
//	ping:    keep-alive
package comments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultKeepAlive = 30 * time.Second

// maxSafeInteger mirrors Number.isSafeInteger's bound in comments.ts.
const maxSafeInteger = int64(1)<<53 - 1

// Deps are the handler's collaborators. APIURL == "" means MIRAKC_URL is
// not set; zero Client/KeepAlive/Log fall back to http.DefaultClient,
// 30s and slog.Default().
type Deps struct {
	APIURL    string
	Client    *http.Client
	Sources   []Source
	KeepAlive time.Duration
	Log       *slog.Logger
}

// NewHandler builds the SSE relay. Wire it with a Go 1.22+ ServeMux pattern:
//
//	mux.Handle("GET /api/comments/services/{id}/stream", NewHandler(deps))
//
// The service id is taken from the {id} path value, with a fallback that
// parses the .../services/{id}/stream URL shape for other routers.
func NewHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := deps.Log
		if log == nil {
			log = slog.Default()
		}
		if deps.APIURL == "" {
			writeError(w, http.StatusServiceUnavailable, "MIRAKC_URL is not set")
			return
		}
		id, ok := serviceID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid service id")
			return
		}
		target, ok := fetchTarget(r.Context(), deps, id, log)
		if !ok {
			writeError(w, http.StatusNotFound, "service not found")
			return
		}

		// r.Context() is the parent, so a client disconnect unwinds every
		// subscription (comments.ts ties one AbortController to the stream).
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// Subscribe every source before the first write, like comments.ts;
		// nil channels mean "unsupported" and are dropped.
		type subscription struct {
			id string
			ch <-chan SourceComment
		}
		var subs []subscription
		for _, src := range deps.Sources {
			if ch := src.Subscribe(ctx, target); ch != nil {
				subs = append(subs, subscription{id: src.ID(), ch: ch})
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		sse := &sseWriter{w: w}
		ids := make([]string, 0, len(subs))
		for _, s := range subs {
			ids = append(ids, s.id)
		}
		data, _ := marshalSSE(ids)
		if err := sse.event("sources", string(data)); err != nil {
			return // client gone; deferred cancel unwinds subscriptions
		}

		// One pump goroutine per source. Writes are serialized through the
		// sseWriter mutex, so cross-source order is nondeterministic while
		// per-source order is preserved — the same awaited-write fan-in as
		// comments.ts's Promise.all. A channel closing ends only that
		// source's pump (comments.ts's catch does the same there).
		var wg sync.WaitGroup
		for _, s := range subs {
			wg.Add(1)
			go func(id string, ch <-chan SourceComment) {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case c, ok := <-ch:
						if !ok {
							return
						}
						data, err := marshalSSE(c)
						if err != nil {
							log.Error("[comments] encode comment failed",
								"source", id, "error", err)
							continue
						}
						if err := sse.event("comment", string(data)); err != nil {
							cancel() // write failure = client gone
							return
						}
					}
				}
			}(s.id, s.ch)
		}

		// Keep-alive pings. The loop runs until ctx ends so this handler
		// never returns with it alive.
		pingDone := make(chan struct{})
		go func() {
			defer close(pingDone)
			keepAlive := deps.KeepAlive
			if keepAlive <= 0 {
				keepAlive = defaultKeepAlive
			}
			ticker := time.NewTicker(keepAlive)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := sse.event("ping", ""); err != nil {
						cancel()
						return
					}
				}
			}
		}()

		wg.Wait()
		cancel() // comments.ts: finally { clearInterval(keepAlive); abort() }
		<-pingDone
	}
}

// serviceID extracts the composite service id from the {id} path value, or
// from the .../services/{id}/stream shape when no path value is set.
func serviceID(r *http.Request) (int64, bool) {
	v := r.PathValue("id")
	if v == "" {
		segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(segs) < 2 || segs[len(segs)-1] != "stream" {
			return 0, false
		}
		v = segs[len(segs)-2]
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id < -maxSafeInteger || id > maxSafeInteger {
		return 0, false
	}
	return id, true
}

// mirakcService is the subset of GET {apiURL}/services/{id} that
// comments.ts reads to build the Target.
type mirakcService struct {
	NetworkID int64  `json:"networkId"`
	ServiceID int64  `json:"serviceId"`
	Name      string `json:"name"`
	Channel   *struct {
		Channel string `json:"channel"`
	} `json:"channel"`
}

// fetchTarget resolves the composite id via mirakc. comments.ts maps any
// non-OK response to 404 "service not found"; here transport and decode
// failures land there too (an unreachable mirakc cannot resolve the id).
func fetchTarget(ctx context.Context, deps Deps, id int64, log *slog.Logger) (Target, bool) {
	url := strings.TrimSuffix(deps.APIURL, "/") + "/services/" + strconv.FormatInt(id, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Error("[comments] bad mirakc api url", "url", url, "error", err)
		return Target{}, false
	}
	client := deps.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Error("[comments] mirakc fetch failed", "error", err)
		return Target{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		return Target{}, false
	}
	var svc mirakcService
	if err := json.NewDecoder(resp.Body).Decode(&svc); err != nil {
		log.Error("[comments] mirakc service decode failed", "error", err)
		return Target{}, false
	}
	target := Target{
		ID:          id,
		NetworkID:   svc.NetworkID,
		ServiceID:   svc.ServiceID,
		ServiceName: svc.Name,
	}
	if svc.Channel != nil {
		target.Channel = svc.Channel.Channel
	}
	return target, true
}

func writeError(w http.ResponseWriter, status int, msg string) {
	// Encoder adds a trailing newline; Hono's c.json does not, and the
	// rest of the Go API is newline-free too.
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(struct {
		Error string `json:"error"`
	}{msg}); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimRight(buf.Bytes(), "\n"))
}

// sseWriter serializes SSE writes so concurrent source pumps interleave
// safely, and flushes after every event (comments.ts awaited each write).
type sseWriter struct {
	mu  sync.Mutex
	w   http.ResponseWriter
	err error
}

func (s *sseWriter) event(name, data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		s.err = err
		return err
	}
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// marshalSSE encodes without HTML escaping: jikkyo replies like ">>1" are
// everyday payloads and JSON.stringify emits them raw (the same parity
// convention as the settings router).
func marshalSSE(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
