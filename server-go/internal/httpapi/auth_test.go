package httpapi

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "secret-token"

const wantWWWAuthenticate = `Basic realm="mirakc-ui", charset="UTF-8"`

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			if err := os.Setenv(key, old); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func basicAuth(user, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}

// newTestServer mirrors the Deno testApp: the middleware wraps the mux, with
// a ticket route, two stream routes, one API route and a static catch-all.
func newTestServer(t *testing.T, a *Auth) *httptest.Server {
	t.Helper()
	okJSON := func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/ticket", a.TicketHandler)
	mux.HandleFunc("GET /api/comments/services/1/stream", okJSON)
	mux.HandleFunc("GET /api/transcode/services/1", okJSON)
	mux.HandleFunc("GET /api/programs", okJSON)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("static"))
	})
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	return srv
}

func request(t *testing.T, srv *httptest.Server, method, path string, headers map[string]string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header, string(body)
}

func mintTicket(t *testing.T, srv *httptest.Server, headers map[string]string) string {
	t.Helper()
	status, _, body := request(t, srv, http.MethodPost, "/api/auth/ticket", headers)
	if status != http.StatusOK {
		t.Fatalf("mint: want 200, got %d (%s)", status, body)
	}
	var res struct {
		Ticket *string `json:"ticket"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("mint: bad JSON %q: %v", body, err)
	}
	if res.Ticket == nil || *res.Ticket == "" {
		t.Fatalf("mint: want a non-empty ticket, got %q", body)
	}
	return *res.Ticket
}

func TestDisabledByDefaultAllPathsOpen(t *testing.T) {
	unsetEnv(t, "AUTH_TOKEN")
	srv := newTestServer(t, FromEnv())

	for _, path := range []string{"/api/programs", "/api/comments/services/1/stream", "/"} {
		status, _, _ := request(t, srv, http.MethodGet, path, nil)
		if status != http.StatusOK {
			t.Errorf("%s: want 200, got %d", path, status)
		}
	}
}

func TestDisabledTicketProbeReturnsNull(t *testing.T) {
	srv := newTestServer(t, New(""))

	status, _, body := request(t, srv, http.MethodPost, "/api/auth/ticket", nil)
	if status != http.StatusOK {
		t.Fatalf("want 200, got %d", status)
	}
	if body != `{"ticket":null}` {
		t.Fatalf("want %q, got %q", `{"ticket":null}`, body)
	}
}

func TestBlankTokenTreatedAsUnset(t *testing.T) {
	srv := newTestServer(t, New("   "))

	status, _, _ := request(t, srv, http.MethodGet, "/api/programs", nil)
	if status != http.StatusOK {
		t.Errorf("programs: want 200, got %d", status)
	}
	status, _, body := request(t, srv, http.MethodPost, "/api/auth/ticket", nil)
	if status != http.StatusOK || body != `{"ticket":null}` {
		t.Errorf("ticket probe: want 200 {\"ticket\":null}, got %d %q", status, body)
	}
}

func TestFromEnvBlankWarnsOnceAndStaysDisabled(t *testing.T) {
	// White-box reset so this assertion is order-independent; no other test
	// triggers the blank-token warning.
	blankTokenWarned.Store(false)
	var warnings []string
	origWarnf := warnf
	warnf = func(msg string) { warnings = append(warnings, msg) }
	t.Cleanup(func() { warnf = origWarnf })

	t.Setenv("AUTH_TOKEN", "   ")
	a := FromEnv()
	FromEnv()

	const want = "[auth] AUTH_TOKEN is blank; authentication disabled (fail-open)"
	if len(warnings) != 1 || warnings[0] != want {
		t.Fatalf("want exactly one warning %q, got %v", want, warnings)
	}
	srv := newTestServer(t, a)
	if status, _, _ := request(t, srv, http.MethodGet, "/api/programs", nil); status != http.StatusOK {
		t.Errorf("programs: want 200 with auth disabled, got %d", status)
	}
}

func TestFromEnvSetTokenEnablesAuth(t *testing.T) {
	t.Setenv("AUTH_TOKEN", testToken)
	srv := newTestServer(t, FromEnv())

	if status, _, _ := request(t, srv, http.MethodGet, "/api/programs", nil); status != http.StatusUnauthorized {
		t.Errorf("no credential: want 401, got %d", status)
	}
	status, _, _ := request(t, srv, http.MethodGet, "/api/programs",
		map[string]string{"Authorization": "Bearer " + testToken})
	if status != http.StatusOK {
		t.Errorf("bearer: want 200, got %d", status)
	}
}

func TestAcceptedHeaderForms(t *testing.T) {
	srv := newTestServer(t, New(testToken))

	cases := map[string]map[string]string{
		"bearer":       {"Authorization": "Bearer " + testToken},
		"bearer-lower": {"Authorization": "bearer " + testToken},
		"x-auth-token": {"X-Auth-Token": testToken},
		"basic":        {"Authorization": basicAuth("mirakc", testToken)},
		"wrong-user":   {"Authorization": basicAuth("wrong-user", testToken)},
		"basic-lower":  {"Authorization": "basic " + base64.StdEncoding.EncodeToString([]byte("mirakc:"+testToken))},
		"both-headers": {"Authorization": "Bearer wrong", "X-Auth-Token": testToken},
	}
	for name, headers := range cases {
		status, _, _ := request(t, srv, http.MethodGet, "/api/programs", headers)
		if status != http.StatusOK {
			t.Errorf("%s: want 200, got %d", name, status)
		}
	}
}

func TestWrongCredentialsRejected(t *testing.T) {
	srv := newTestServer(t, New(testToken))

	cases := map[string]map[string]string{
		"bearer-wrong":     {"Authorization": "Bearer wrong"},
		"x-auth-wrong":     {"X-Auth-Token": "wrong"},
		"basic-wrong-pass": {"Authorization": basicAuth("mirakc", "wrong")},
		"basic-empty-pass": {"Authorization": basicAuth("mirakc", "")},
		"bearer-no-value":  {"Authorization": "Bearer"},
		"bearer-empty":     {"Authorization": "Bearer "},
		"unknown-scheme":   {"Authorization": "Digest abcdef"},
		"none":             nil,
	}
	for name, headers := range cases {
		status, hdr, body := request(t, srv, http.MethodGet, "/api/programs", headers)
		if status != http.StatusUnauthorized {
			t.Errorf("%s: want 401, got %d", name, status)
			continue
		}
		if body != `{"error":"unauthorized"}` {
			t.Errorf("%s: want unauthorized JSON, got %q", name, body)
		}
		if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s: want application/json, got %q", name, ct)
		}
	}
}

func TestMalformedBase64BasicRejected(t *testing.T) {
	srv := newTestServer(t, New(testToken))

	status, _, _ := request(t, srv, http.MethodGet, "/api/programs",
		map[string]string{"Authorization": "Basic !!!not-base64!!!"})
	if status != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", status)
	}
}

func TestBasicWithoutColonTreatedAsPassword(t *testing.T) {
	// Deliberate leniency (mirrors the Deno auth.ts): a decoded Basic value
	// without a colon is treated as the password itself. Pinned here so a
	// refactor cannot silently change it.
	srv := newTestServer(t, New(testToken))

	status, _, _ := request(t, srv, http.MethodGet, "/api/programs",
		map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(testToken))})
	if status != http.StatusOK {
		t.Fatalf("want 200, got %d", status)
	}
}

func TestUnauthorizedResponseShapesByPath(t *testing.T) {
	srv := newTestServer(t, New(testToken))

	status, hdr, body := request(t, srv, http.MethodGet, "/api/programs", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("api: want 401, got %d", status)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("api: want application/json, got %q", ct)
	}
	if body != `{"error":"unauthorized"}` {
		t.Errorf("api: want unauthorized JSON, got %q", body)
	}
	if hdr.Get("WWW-Authenticate") != "" {
		t.Errorf("api: unexpected WWW-Authenticate %q", hdr.Get("WWW-Authenticate"))
	}

	status, hdr, body = request(t, srv, http.MethodGet, "/", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("static: want 401, got %d", status)
	}
	if hdr.Get("WWW-Authenticate") != wantWWWAuthenticate {
		t.Errorf("static: want WWW-Authenticate %q, got %q", wantWWWAuthenticate, hdr.Get("WWW-Authenticate"))
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("static: want text/plain, got %q", ct)
	}
	if body != "unauthorized" {
		t.Errorf("static: want %q, got %q", "unauthorized", body)
	}
}

func TestStaticPathPassesWithBasic(t *testing.T) {
	srv := newTestServer(t, New(testToken))

	status, _, body := request(t, srv, http.MethodGet, "/",
		map[string]string{"Authorization": basicAuth("mirakc", testToken)})
	if status != http.StatusOK || body != "static" {
		t.Fatalf("want 200 static, got %d %q", status, body)
	}
}

func TestTicketMintRequiresHeaderAuth(t *testing.T) {
	srv := newTestServer(t, New(testToken))

	status, _, _ := request(t, srv, http.MethodPost, "/api/auth/ticket", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("mint without credential: want 401, got %d", status)
	}

	bearer := map[string]string{"Authorization": "Bearer " + testToken}
	ticket := mintTicket(t, srv, bearer)

	// A ticket must not mint another ticket (tickets are only accepted on
	// stream GETs, never on this route).
	status, _, _ = request(t, srv, http.MethodPost, "/api/auth/ticket?ticket="+ticket, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("remint via ticket: want 401, got %d", status)
	}
}

func TestTicketWorksOnBothStreamPaths(t *testing.T) {
	srv := newTestServer(t, New(testToken))
	bearer := map[string]string{"Authorization": "Bearer " + testToken}
	ticket := mintTicket(t, srv, bearer)

	for _, path := range []string{
		"/api/comments/services/1/stream",
		"/api/transcode/services/1",
	} {
		status, _, body := request(t, srv, http.MethodGet, path+"?ticket="+ticket, nil)
		if status != http.StatusOK || body != `{"ok":true}` {
			t.Errorf("%s: want 200 ok, got %d %q", path, status, body)
		}
	}
}

func TestTicketRejectedOutsideStreamGet(t *testing.T) {
	srv := newTestServer(t, New(testToken))
	bearer := map[string]string{"Authorization": "Bearer " + testToken}
	ticket := mintTicket(t, srv, bearer)

	status, _, _ := request(t, srv, http.MethodGet, "/api/programs?ticket="+ticket, nil)
	if status != http.StatusUnauthorized {
		t.Errorf("non-stream path: want 401, got %d", status)
	}

	status, _, _ = request(t, srv, http.MethodPost, "/api/transcode/services/1?ticket="+ticket, nil)
	if status != http.StatusUnauthorized {
		t.Errorf("stream path via POST: want 401, got %d", status)
	}
}

func TestInvalidTicketRejected(t *testing.T) {
	srv := newTestServer(t, New(testToken))

	status, _, _ := request(t, srv, http.MethodGet,
		"/api/comments/services/1/stream?ticket=not-a-ticket", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", status)
	}
}

func TestTicketExpiresAfterTTL(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	now := base
	a := New(testToken, WithNow(func() time.Time { return now }))
	srv := newTestServer(t, a)
	bearer := map[string]string{"Authorization": "Bearer " + testToken}
	ticket := mintTicket(t, srv, bearer)

	now = base.Add(30 * time.Second)
	status, _, _ := request(t, srv, http.MethodGet,
		"/api/comments/services/1/stream?ticket="+ticket, nil)
	if status != http.StatusOK {
		t.Fatalf("within TTL: want 200, got %d", status)
	}

	now = base.Add(61 * time.Second)
	status, _, _ = request(t, srv, http.MethodGet,
		"/api/comments/services/1/stream?ticket="+ticket, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("after TTL: want 401, got %d", status)
	}
}

func TestTicketUseCap(t *testing.T) {
	srv := newTestServer(t, New(testToken))
	bearer := map[string]string{"Authorization": "Bearer " + testToken}
	ticket := mintTicket(t, srv, bearer)

	path := "/api/comments/services/1/stream?ticket=" + ticket
	for i := 0; i < ticketMaxUses; i++ {
		if status, _, _ := request(t, srv, http.MethodGet, path, nil); status != http.StatusOK {
			t.Fatalf("use %d: want 200, got %d", i+1, status)
		}
	}
	if status, _, _ := request(t, srv, http.MethodGet, path, nil); status != http.StatusUnauthorized {
		t.Fatalf("use %d: want 401, got %d", ticketMaxUses+1, status)
	}
}

func TestTicketPruningBoundsMapSize(t *testing.T) {
	base := time.Unix(0, 0)
	now := base
	a := New(testToken, WithNow(func() time.Time { return now }))
	srv := newTestServer(t, a)
	bearer := map[string]string{"Authorization": "Bearer " + testToken}

	for i := 0; i < 5; i++ {
		mintTicket(t, srv, bearer)
	}
	if size := ticketMapSize(a); size != 5 {
		t.Fatalf("after 5 mints: want map size 5, got %d", size)
	}

	// Expired entries are pruned lazily on issue.
	now = base.Add(61 * time.Second)
	mintTicket(t, srv, bearer)
	if size := ticketMapSize(a); size != 1 {
		t.Fatalf("after TTL + one mint: want map size 1, got %d", size)
	}
}

func ticketMapSize(a *Auth) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.tickets)
}

func TestConcurrentTicketConsumeIsAtomic(t *testing.T) {
	// Frozen clock so the TTL cannot interfere; every attempt must resolve
	// purely through the mutex-guarded consume.
	a := New(testToken, WithNow(func() time.Time { return time.Unix(0, 0) }))
	srv := newTestServer(t, a)
	bearer := map[string]string{"Authorization": "Bearer " + testToken}
	ticket := mintTicket(t, srv, bearer)

	const attempts = 50
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := srv.Client().Get(srv.URL + "/api/comments/services/1/stream?ticket=" + ticket)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			switch res.StatusCode {
			case http.StatusOK:
				successes.Add(1)
			case http.StatusUnauthorized:
			default:
				t.Errorf("want 200 or 401, got %d", res.StatusCode)
			}
		}()
	}
	wg.Wait()

	if got := successes.Load(); got != ticketMaxUses {
		t.Fatalf("concurrent uses: want exactly %d successes, got %d", ticketMaxUses, got)
	}
}

func TestRawPathVariantsStillRequireAuth(t *testing.T) {
	// Raw-socket probes mirroring the security review: the middleware runs
	// before routing, so whatever path form arrives must still be
	// authenticated. How ServeMux later cleans/redirects non-canonical paths
	// is a wiring concern covered by the router integration tests.
	var reached int32
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reached, 1)
	})
	srv := httptest.NewServer(New(testToken).Middleware(next))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	// Probed on the wire: "//api/programs" keeps its double slash, so it
	// gets the plain-text shape; "/api%2Fprograms" decodes to
	// "/api/programs" and gets the JSON shape.
	cases := []struct {
		target         string
		wantWWWAuthent bool
	}{
		{"//api/programs", true},
		{"/API/programs", true},
		{"/api%2Fprograms", false},
	}
	for _, tc := range cases {
		conn, err := net.DialTimeout("tcp", host, 5*time.Second)
		if err != nil {
			t.Fatalf("%s: %v", tc.target, err)
		}
		fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
			tc.target, host)
		res, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.target, err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		conn.Close()

		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: want 401, got %d", tc.target, res.StatusCode)
		}
		if tc.wantWWWAuthent && res.Header.Get("WWW-Authenticate") != wantWWWAuthenticate {
			t.Errorf("%s: want WWW-Authenticate %q, got %q", tc.target,
				wantWWWAuthenticate, res.Header.Get("WWW-Authenticate"))
		}
		if !tc.wantWWWAuthent && res.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s: want JSON 401 shape, got Content-Type %q", tc.target,
				res.Header.Get("Content-Type"))
		}
	}
	if reached != 0 {
		t.Error("a probe reached the handler without credentials")
	}
}
