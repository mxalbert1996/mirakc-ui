// Black-box integration tests over the full handler stack
// (Auth.Middleware(NewRouter)) against a real temporary SQLite store —
// the Go counterpart of server/main.test.ts plus the route-level Deno
// suites. Raw-path behavior (the middleware sees the request pre-routing)
// is pinned in auth_test.go; this file pins the mounted surface.
package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/store"
)

const token = "test-token-1234"

func newRouterServer(t *testing.T, auth *Auth) (*httptest.Server, *store.Store, *int32) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	var changed int32
	deps := Deps{Store: st, Auth: auth, KeywordRulesOnChanged: func() { atomic.AddInt32(&changed, 1) }}
	ts := httptest.NewServer(auth.Middleware(NewRouter(deps)))
	t.Cleanup(ts.Close)
	return ts, st, &changed
}

func call(t *testing.T, method, url string, headers map[string]string, body string) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, url, err)
	}
	res.Body.Close()
	return res, string(raw)
}

func basicCreds() map[string]string {
	return map[string]string{"Authorization": "Basic " + b64("user:"+token)}
}

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func jsonOf(t *testing.T, body string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("not a JSON object: %q: %v", body, err)
	}
	return v
}

func TestAuthOffFullSurface(t *testing.T) {
	ts, st, changed := newRouterServer(t, New(""))
	base := ts.URL

	t.Run("keyword rules CRUD round-trip", func(t *testing.T) {
		res, body := call(t, "GET", base+"/api/keyword-rules", nil, "")
		if res.StatusCode != 200 || strings.TrimSpace(body) != "[]" {
			t.Fatalf("GET empty = %d %q", res.StatusCode, body)
		}

		res, body = call(t, "POST", base+"/api/keyword-rules", nil,
			`{"keyword":"ニュース"}`)
		if res.StatusCode != 201 {
			t.Fatalf("POST = %d %q", res.StatusCode, body)
		}
		rule := jsonOf(t, body)
		if rule["keyword"] != "ニュース" || rule["enabled"] != true {
			t.Fatalf("stored rule = %v", rule)
		}
		id, _ := rule["id"].(string)
		if id == "" {
			t.Fatalf("no id: %v", rule)
		}

		res, body = call(t, "PUT", base+"/api/keyword-rules/"+id, nil,
			`{"keyword":"天気","enabled":false}`)
		if res.StatusCode != 200 {
			t.Fatalf("PUT = %d %q", res.StatusCode, body)
		}
		updated := jsonOf(t, body)
		if updated["keyword"] != "天気" || updated["enabled"] != false {
			t.Fatalf("updated rule = %v", updated)
		}

		res, _ = call(t, "PUT", base+"/api/keyword-rules/no-such-id", nil, `{}`)
		if res.StatusCode != 400 {
			t.Fatalf("PUT invalid body = %d, want 400", res.StatusCode)
		}

		res, body = call(t, "PUT", base+"/api/keyword-rules/00000000-0000-4000-8000-000000000000", nil,
			`{"keyword":"x"}`)
		if res.StatusCode != 404 || !strings.Contains(body, "rule not found") {
			t.Fatalf("PUT unknown = %d %q", res.StatusCode, body)
		}

		res, _ = call(t, "DELETE", base+"/api/keyword-rules/"+id, nil, "")
		if res.StatusCode != 204 {
			t.Fatalf("DELETE = %d", res.StatusCode)
		}
		res, body = call(t, "DELETE", base+"/api/keyword-rules/"+id, nil, "")
		if res.StatusCode != 404 || !strings.Contains(body, "rule not found") {
			t.Fatalf("DELETE again = %d %q", res.StatusCode, body)
		}

		if got := atomic.LoadInt32(changed); got != 2 {
			t.Fatalf("onChanged fired %d times, want 2 (add + update, not delete)", got)
		}
	})

	t.Run("keyword rules validation", func(t *testing.T) {
		res, body := call(t, "POST", base+"/api/keyword-rules", nil, `not json`)
		if res.StatusCode != 400 || !strings.Contains(body, "invalid JSON body") {
			t.Fatalf("bad JSON = %d %q", res.StatusCode, body)
		}
		res, body = call(t, "POST", base+"/api/keyword-rules", nil, `{"keyword":"  "}`)
		if res.StatusCode != 400 || !strings.Contains(body, "keyword must be a non-empty string") {
			t.Fatalf("blank keyword = %d %q", res.StatusCode, body)
		}
		res, body = call(t, "POST", base+"/api/keyword-rules", nil, `{"keyword":"x","genres":[99]}`)
		if res.StatusCode != 400 || !strings.Contains(body, "does not match schema") {
			t.Fatalf("genre range = %d %q", res.StatusCode, body)
		}
	})

	t.Run("live comment mappings CRUD", func(t *testing.T) {
		res, body := call(t, "POST", base+"/api/live-comment-settings", nil,
			`{"channel":"gr001","assignments":[{"source":"nicolive","channelId":"ch2641094"}]}`)
		if res.StatusCode != 201 {
			t.Fatalf("POST = %d %q", res.StatusCode, body)
		}
		mapping := jsonOf(t, body)
		id, _ := mapping["id"].(string)

		res, body = call(t, "PUT", base+"/api/live-comment-settings/"+id, nil, `{"channel":"gr002"}`)
		if res.StatusCode != 200 || jsonOf(t, body)["channel"] != "gr002" {
			t.Fatalf("PUT = %d %q", res.StatusCode, body)
		}
		res, body = call(t, "PUT", base+"/api/live-comment-settings/00000000-0000-4000-8000-000000000000", nil, `{"channel":"x"}`)
		if res.StatusCode != 404 || !strings.Contains(body, "mapping not found") {
			t.Fatalf("PUT unknown = %d %q", res.StatusCode, body)
		}
		res, _ = call(t, "DELETE", base+"/api/live-comment-settings/"+id, nil, "")
		if res.StatusCode != 204 {
			t.Fatalf("DELETE = %d", res.StatusCode)
		}
	})

	t.Run("notification settings masking and merge", func(t *testing.T) {
		res, body := call(t, "GET", base+"/api/notification-settings", nil, "")
		if res.StatusCode != 200 {
			t.Fatalf("GET = %d %q", res.StatusCode, body)
		}
		view := jsonOf(t, body)
		if view["url"] != "" || view["tokenSet"] != false || view["onSchedule"] != false {
			t.Fatalf("defaults view = %v", view)
		}
		if _, has := view["token"]; has {
			t.Fatalf("view leaks token: %v", view)
		}

		res, body = call(t, "PUT", base+"/api/notification-settings", nil,
			`{"url":"https://ntfy.sh/topic","token":"tk_secret","onSchedule":true}`)
		if res.StatusCode != 200 {
			t.Fatalf("PUT = %d %q", res.StatusCode, body)
		}
		view = jsonOf(t, body)
		if view["tokenSet"] != true || view["discordWebhookUrlSet"] != false {
			t.Fatalf("view after PUT = %v", view)
		}
		if _, has := view["token"]; has {
			t.Fatalf("PUT response leaks token: %v", view)
		}

		// The store keeps the secret even though no response shows it.
		raw, found, err := st.GetSingleton(store.ColNotification)
		if err != nil || !found || !strings.Contains(string(raw), "tk_secret") {
			t.Fatalf("stored settings = %q found=%v err=%v", raw, found, err)
		}

		// Absent/empty secret keeps the stored value; explicit null clears.
		res, body = call(t, "PUT", base+"/api/notification-settings", nil,
			`{"url":"https://ntfy.sh/topic","onSchedule":true}`)
		if res.StatusCode != 200 || jsonOf(t, body)["tokenSet"] != true {
			t.Fatalf("keep-on-absent = %d %q", res.StatusCode, body)
		}
		res, body = call(t, "PUT", base+"/api/notification-settings", nil,
			`{"url":"","token":null,"onSchedule":false}`)
		if res.StatusCode != 200 || jsonOf(t, body)["tokenSet"] != false {
			t.Fatalf("null-clear = %d %q", res.StatusCode, body)
		}

		// Destination-required fires on the MERGED value.
		res, body = call(t, "PUT", base+"/api/notification-settings", nil,
			`{"url":"","onStart":true}`)
		if res.StatusCode != 400 || !strings.Contains(body, "destination") {
			t.Fatalf("destination check = %d %q", res.StatusCode, body)
		}
	})

	t.Run("test send validation", func(t *testing.T) {
		res, body := call(t, "POST", base+"/api/notification-settings/test/ntfy", nil, `{}`)
		if res.StatusCode != 400 || !strings.Contains(body, "url must be a http(s) URL with a topic") {
			t.Fatalf("ntfy bad url = %d %q", res.StatusCode, body)
		}
		res, body = call(t, "POST", base+"/api/notification-settings/test/discord", nil,
			`{"webhookUrl":"https://example.com/hook"}`)
		if res.StatusCode != 400 || !strings.Contains(body, "webhookUrl must be a Discord webhook URL") {
			t.Fatalf("discord bad url = %d %q", res.StatusCode, body)
		}
	})

	t.Run("ticket probe and unmatched paths", func(t *testing.T) {
		res, body := call(t, "POST", base+"/api/auth/ticket", nil, "")
		if res.StatusCode != 200 || strings.TrimSpace(body) != `{"ticket":null}` {
			t.Fatalf("ticket probe = %d %q", res.StatusCode, body)
		}
		res, body = call(t, "GET", base+"/api/no-such-endpoint", nil, "")
		if res.StatusCode != 404 {
			t.Fatalf("unknown api path = %d %q", res.StatusCode, body)
		}
		res, _ = call(t, "GET", base+"/", nil, "")
		if res.StatusCode == 401 {
			t.Fatalf("auth off must never 401: %d", res.StatusCode)
		}
	})
}

func TestAuthOnWallAndFlow(t *testing.T) {
	ts, _, _ := newRouterServer(t, New(token))
	base := ts.URL

	unauthorized := []struct{ method, path string }{
		{"GET", "/"},
		{"GET", "/index.html"},
		{"GET", "/api/keyword-rules"},
		{"GET", "/api/live-comment-settings"},
		{"GET", "/api/notification-settings"},
		{"POST", "/api/auth/ticket"},
		{"GET", "/api/comments/services/1/stream"},
		{"GET", "/api/transcode/services/1"},
		{"PUT", "/api/keyword-rules/x"},
		{"DELETE", "/api/live-comment-settings/x"},
		{"GET", "/api/no-such-endpoint"},
	}
	for _, tc := range unauthorized {
		res, body := call(t, tc.method, base+tc.path, nil, "")
		if res.StatusCode != 401 {
			t.Errorf("%s %s without creds = %d (want 401)", tc.method, tc.path, res.StatusCode)
		}
		if strings.HasPrefix(tc.path, "/api") {
			if want := `{"error":"unauthorized"}`; strings.TrimSpace(body) != want {
				t.Errorf("%s %s body = %q, want %q", tc.method, tc.path, body, want)
			}
			if res.Header.Get("WWW-Authenticate") != "" {
				t.Errorf("%s %s must not carry a Basic challenge", tc.method, tc.path)
			}
		} else if res.Header.Get("WWW-Authenticate") != `Basic realm="mirakc-ui", charset="UTF-8"` {
			t.Errorf("%s %s missing Basic challenge", tc.method, tc.path)
		}
	}

	t.Run("creds unlock the surface", func(t *testing.T) {
		for name, headers := range map[string]map[string]string{
			"basic":   basicCreds(),
			"bearer":  {"Authorization": "Bearer " + token},
			"x-token": {"X-Auth-Token": token},
		} {
			res, _ := call(t, "GET", base+"/api/keyword-rules", headers, "")
			if res.StatusCode != 200 {
				t.Errorf("%s: GET rules = %d", name, res.StatusCode)
			}
		}
	})

	t.Run("ticket mint and stream acceptance", func(t *testing.T) {
		res, body := call(t, "POST", base+"/api/auth/ticket", basicCreds(), "")
		if res.StatusCode != 200 {
			t.Fatalf("mint = %d %q", res.StatusCode, body)
		}
		ticket := jsonOf(t, body)["ticket"].(string)
		if ticket == "" {
			t.Fatalf("empty ticket: %q", body)
		}

		// A ticket authenticates stream GETs (the route itself is a
		// later phase — passing the wall shows as a non-401).
		res, _ = call(t, "GET", base+"/api/comments/services/1/stream?ticket="+ticket, nil, "")
		if res.StatusCode == 401 {
			t.Fatalf("ticket rejected on stream GET: %d", res.StatusCode)
		}

		// ...but nothing else.
		res, _ = call(t, "GET", base+"/api/keyword-rules?ticket="+ticket, nil, "")
		if res.StatusCode != 401 {
			t.Fatalf("ticket accepted off-stream: %d", res.StatusCode)
		}
		res, _ = call(t, "POST", base+"/api/auth/ticket", nil, "")
		if res.StatusCode != 401 {
			t.Fatalf("mint without creds = %d", res.StatusCode)
		}
	})
}

func TestUnmatchedAPIPathFallsToSPA(t *testing.T) {
	// Deno parity: the SPA fallback catches unknown GETs under /api too
	// (Hono's app.get("/*", index) outranks no route). Without an embedded
	// client that manifests as the web handler's 404 — same as the Deno
	// server without a built client/dist.
	ts, _, _ := newRouterServer(t, New(""))
	res, _ := call(t, "GET", ts.URL+"/api/definitely-not-mounted", nil, "")
	if res.StatusCode != 404 {
		t.Fatalf("unknown api = %d, want 404", res.StatusCode)
	}
	res, _ = call(t, "POST", ts.URL+"/api/definitely-not-mounted", nil, "")
	if res.StatusCode != 404 {
		t.Fatalf("unknown api POST = %d, want 404", res.StatusCode)
	}
}

// Oracle review round-1 pins: these routing behaviors were mis-recorded as
// "known divergences" — all three are actually Deno-parity. With a client
// embedded the web handler serves the SPA exactly like Hono's GET /*;
// in this tree (no embedded client) they manifest as its 404.
func TestRoutingParityPins(t *testing.T) {
	ts, _, _ := newRouterServer(t, New(""))
	base := ts.URL

	res, _ := call(t, "DELETE", base+"/api/keyword-rules", nil, "")
	if res.StatusCode != 404 {
		t.Fatalf("wrong-method on collection = %d, want 404 (Deno parity, never 405)", res.StatusCode)
	}
	res, _ = call(t, "GET", base+"/api/auth/ticket", nil, "")
	if res.StatusCode != 404 {
		t.Fatalf("GET ticket endpoint = %d, want 404 (SPA parity)", res.StatusCode)
	}
	res, _ = call(t, "GET", base+"/api/keyword-rules/", nil, "")
	if res.StatusCode != 404 {
		t.Fatalf("trailing-slash collection = %d, want 404 (SPA parity)", res.StatusCode)
	}
}

func TestStreamTicketRejectsHead(t *testing.T) {
	ts, _, _ := newRouterServer(t, New(token))
	base := ts.URL

	res, body := call(t, "POST", base+"/api/auth/ticket", basicCreds(), "")
	ticket := jsonOf(t, body)["ticket"].(string)

	res, _ = call(t, "HEAD", base+"/api/comments/services/1/stream?ticket="+ticket, nil, "")
	if res.StatusCode != 401 {
		t.Fatalf("HEAD with ticket on stream = %d, want 401", res.StatusCode)
	}
}

func TestContentTypePins(t *testing.T) {
	ts, _, _ := newRouterServer(t, New(""))
	base := ts.URL

	res, _ := call(t, "GET", base+"/api/keyword-rules", nil, "")
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("JSON content-type = %q, want bare application/json (Hono parity)", ct)
	}

	authTS, _, _ := newRouterServer(t, New(token))
	res, _ = call(t, "GET", authTS.URL+"/", nil, "")
	if ct := res.Header.Get("Content-Type"); ct != "text/plain; charset=UTF-8" {
		t.Fatalf("plain 401 content-type = %q", ct)
	}
	if res.Header.Get("WWW-Authenticate") != `Basic realm="mirakc-ui", charset="UTF-8"` {
		t.Fatalf("Basic challenge = %q", res.Header.Get("WWW-Authenticate"))
	}
}

func TestBodySizeCap(t *testing.T) {
	// Deliberate 1MiB request cap (router.go readBody): parity with Deno's
	// own body limit, both reject; only the status differs (Deno 500).
	ts, _, _ := newRouterServer(t, New(""))
	huge := `{"keyword":"` + strings.Repeat("a", 1<<20) + `"}`
	res, body := call(t, "POST", ts.URL+"/api/keyword-rules", nil, huge)
	if res.StatusCode != 400 || !strings.Contains(body, "invalid JSON body") {
		t.Fatalf("oversized body = %d %q, want 400 invalid JSON body", res.StatusCode, body[:40])
	}
}
