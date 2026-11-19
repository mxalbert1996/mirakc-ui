package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/comments"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/httpapi"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/keyword"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/store"
)

// fakeMirakc stands in for the mirakc backend: canned /programs and
// /services, capture of recording-schedule requests, and a scriptable
// SSE /events stream.
type fakeMirakc struct {
	mu              sync.Mutex
	programs        string
	services        string
	schedules       []string
	programsFetched int
	programFetches  int
	eventsCh        chan string
}

func newFakeMirakc() *fakeMirakc {
	return &fakeMirakc{eventsCh: make(chan string, 10)}
}

func (f *fakeMirakc) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/programs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.programsFetched++
		p := f.programs
		f.mu.Unlock()
		w.Write([]byte(p))
	})
	mux.HandleFunc("/api/services", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		s := f.services
		f.mu.Unlock()
		w.Write([]byte(s))
	})
	mux.HandleFunc("/api/programs/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.programFetches++
		f.mu.Unlock()
		w.Write([]byte(`{"id":` + r.PathValue("id") + `,"name":"夜のニュース","startAt":1795103802213,"duration":1800,"networkId":1,"serviceId":2}`))
	})
	mux.HandleFunc("/api/services/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":` + r.PathValue("id") + `,"networkId":4,"serviceId":141,"name":"日テレ","channel":{"channel":"gr026"}}`))
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		flusher.Flush()
		for {
			select {
			case ev := <-f.eventsCh:
				w.Write([]byte(ev))
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("GET /api/recording/schedules", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("POST /api/recording/schedules", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.schedules = append(f.schedules, string(body))
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"program":{"id":101,"name":"夜のニュース","startAt":1795103802213,"duration":1800,"networkId":1,"serviceId":2}}`))
	})
	return mux
}

func (f *fakeMirakc) pushEvent(ev string) { f.eventsCh <- ev }

func (f *fakeMirakc) scheduleRequests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.schedules...)
}

func (f *fakeMirakc) programsFetchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.programsFetched
}

func (f *fakeMirakc) programFetchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.programFetches
}

// fakeNtfy captures the JSON-publish body SendNtfy posts to the base URL.
type fakeNtfy struct {
	mu     sync.Mutex
	bodies []string
}

func (f *fakeNtfy) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(body))
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
}

func (f *fakeNtfy) notifications() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.bodies...)
}

func newTestApp(t *testing.T, d Deps) (*App, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	a := New(ctx, d)
	ts := httptest.NewServer(a.Handler)
	t.Cleanup(func() { ts.Close(); a.Stop() })
	return a, ts
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "app.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seedNotification(t *testing.T, st *store.Store, url string, toggles string) {
	t.Helper()
	raw := `{"url":"` + url + `","token":"","discordWebhookUrl":"",` + toggles + `}`
	if err := st.SetSingleton(store.ColNotification, []byte(raw)); err != nil {
		t.Fatal(err)
	}
}

const slimPrograms = `[{"id":101,"eventId":201,"isFree":true,"name":"夜のニュース<特別編>","startAt":%d,"duration":1800,"networkId":1,"serviceId":2,"genres":[{"lv1":0,"lv2":0,"un1":0,"un2":0}]},{"id":102,"eventId":202,"isFree":true,"name":"再放送ドラマ","startAt":%d,"duration":1800,"networkId":1,"serviceId":3,"genres":[{"lv1":5,"lv2":0,"un1":0,"un2":0}]}]`

func TestMirakcProxyAndProgramsPassThrough(t *testing.T) {
	st := newStore(t)
	fake := &fakeMirakc{
		programs: `[{"id":101,"eventId":201,"isFree":true,"name":"夜のニュース","startAt":100,"duration":50,"networkId":1,"serviceId":2,"genres":[{"lv1":0,"lv2":1,"un1":2,"un2":3}]}]`,
		services: `[{"networkId":1,"serviceId":2,"name":"BS日テレ","channel":{"channel":"ch211"}}]`,
	}
	upstream := httptest.NewServer(fake.handler())
	t.Cleanup(upstream.Close)

	_, ts := newTestApp(t, Deps{Store: st, Auth: httpapi.New(""), MirakcURL: upstream.URL, TimeZone: "Asia/Tokyo"})

	res, body := get(t, ts.URL+"/api/mirakc/services")
	if res.StatusCode != 200 || !strings.Contains(body, "BS日テレ") {
		t.Fatalf("proxy passthrough = %d %q", res.StatusCode, body)
	}

	res, body = get(t, ts.URL+"/api/programs")
	if res.StatusCode != 200 || !strings.Contains(body, "夜のニュース") || !strings.Contains(body, `"genres":[{"lv1":0`) {
		t.Fatalf("programs = %d %q", res.StatusCode, body)
	}
	if got := fake.programsFetchCount(); got != 1 {
		t.Fatalf("programs fetched %d times, want 1 (cached)", got)
	}

	res, body = get(t, ts.URL+"/api/programs?from=200&to=300")
	if res.StatusCode != 200 || strings.Contains(body, "夜のニュース") {
		t.Fatalf("window filter = %d %q", res.StatusCode, body)
	}
}

func TestUnsetMirakcURLConvention(t *testing.T) {
	st := newStore(t)
	_, ts := newTestApp(t, Deps{Store: st, Auth: httpapi.New(""), MirakcURL: ""})

	for _, p := range []string{"/api/programs", "/api/mirakc/services", "/api/mirakc/recording/schedules"} {
		res, body := get(t, ts.URL+p)
		if res.StatusCode != 500 || strings.TrimSpace(body) != `{"error":"MIRAKC_URL is not set"}` {
			t.Fatalf("GET %s = %d %q, want the unset-URL convention 500", p, res.StatusCode, body)
		}
	}
}

func TestScheduleCreatedHookNotifies(t *testing.T) {
	st := newStore(t)
	ntfy := &fakeNtfy{}
	ntfyServer := httptest.NewServer(ntfy.handler())
	t.Cleanup(ntfyServer.Close)
	seedNotification(t, st, ntfyServer.URL+"/recording", `"onSchedule":true`)

	fake := &fakeMirakc{
		services: `[{"networkId":1,"serviceId":2,"name":"BS日テレ","channel":{"channel":"ch211"}}]`,
	}
	upstream := httptest.NewServer(fake.handler())
	t.Cleanup(upstream.Close)

	_, ts := newTestApp(t, Deps{Store: st, Auth: httpapi.New(""), MirakcURL: upstream.URL, TimeZone: "Asia/Tokyo"})

	res, body := post(t, ts.URL+"/api/mirakc/recording/schedules", `{}`)
	if res.StatusCode != 201 || !strings.Contains(body, `"id":101`) {
		t.Fatalf("schedule passthrough = %d %q", res.StatusCode, body)
	}

	waitFor(t, 2*time.Second, func() bool { return len(ntfy.notifications()) > 0 })
	n := ntfy.notifications()[0]
	var payload struct {
		Topic   string `json:"topic"`
		Title   string `json:"title"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(n), &payload); err != nil {
		t.Fatalf("ntfy body: %v: %q", err, n)
	}
	if payload.Topic != "recording" {
		t.Errorf("topic = %q", payload.Topic)
	}
	if !strings.Contains(payload.Title, "夜のニュース") {
		t.Errorf("title = %q, want the scheduled title with the program name", payload.Title)
	}
	if !strings.Contains(payload.Message, "2026") {
		t.Errorf("message missing airtime: %q", payload.Message)
	}
}

func TestKeywordJobRecordsAndNotifies(t *testing.T) {
	st := newStore(t)
	ntfy := &fakeNtfy{}
	ntfyServer := httptest.NewServer(ntfy.handler())
	t.Cleanup(ntfyServer.Close)
	seedNotification(t, st, ntfyServer.URL+"/recording", `"onSchedule":true`)

	if _, err := st.Add(store.ColKeywordRules, []byte(`{"keyword":"ニュース","channels":[],"genres":[],"enabled":true}`)); err != nil {
		t.Fatal(err)
	}

	future := time.Now().Add(1 * time.Hour).UnixMilli()
	programs := fmt.Sprintf(slimPrograms, future, future)
	fake := &fakeMirakc{
		programs: programs,
		services: `[{"networkId":1,"serviceId":2,"name":"BS日テレ","channel":{"channel":"ch211"}},{"networkId":1,"serviceId":3,"name":"日本テレビ","channel":{"channel":"gr026"}}]`,
	}
	upstream := httptest.NewServer(fake.handler())
	t.Cleanup(upstream.Close)

	newTestApp(t, Deps{
		Store: st, Auth: httpapi.New(""), MirakcURL: upstream.URL, TimeZone: "Asia/Tokyo",
		KeywordInterval: time.Hour,
		KeywordOptions:  []keyword.Option{keyword.WithDebounce(time.Millisecond)},
	})

	waitFor(t, 5*time.Second, func() bool { return len(fake.scheduleRequests()) > 0 })

	reqs := fake.scheduleRequests()
	var schedule struct {
		ProgramID int64 `json:"programId"`
		Options   struct {
			ContentPath string `json:"contentPath"`
		} `json:"options"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(reqs[0]), &schedule); err != nil {
		t.Fatalf("schedule body: %v: %q", err, reqs[0])
	}
	if schedule.ProgramID != 101 {
		t.Errorf("programId = %d, want 101 (the matching program)", schedule.ProgramID)
	}
	if len(schedule.Tags) != 2 || schedule.Tags[0] != keyword.KEYWORD_RECORDING_TAG || schedule.Tags[1] != "keyword:ニュース" {
		t.Errorf("tags = %v", schedule.Tags)
	}
	if !strings.HasSuffix(schedule.Options.ContentPath, "夜のニュース<特別編>.m2ts") {
		t.Errorf("contentPath = %q", schedule.Options.ContentPath)
	}
	if len(reqs) != 1 {
		t.Errorf("scheduled %d programs, want 1 (the non-matching program skipped)", len(reqs))
	}

	waitFor(t, 2*time.Second, func() bool { return len(ntfy.notifications()) > 0 })
	n := ntfy.notifications()[0]
	if !strings.Contains(n, "ニュース") || !strings.Contains(n, "BS日テレ") {
		t.Errorf("keyword notification = %q", n)
	}
}

func TestAuthCoversNewMounts(t *testing.T) {
	st := newStore(t)
	_, ts := newTestApp(t, Deps{Store: st, Auth: httpapi.New("token"), MirakcURL: ""})

	for _, p := range []string{"/api/programs", "/api/mirakc/services"} {
		res, _ := get(t, ts.URL+p)
		if res.StatusCode != 401 {
			t.Fatalf("GET %s without creds = %d, want 401", p, res.StatusCode)
		}
	}
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(body)
}

func post(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	res, err := http.Post(url, "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(raw)
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", d)
}

func TestSSEEventGatedByToggle(t *testing.T) {
	st := newStore(t)
	fake := newFakeMirakc()
	fake.services = `[{"networkId":1,"serviceId":2,"name":"BS日テレ","channel":{"channel":"ch211"}}]`
	upstream := httptest.NewServer(fake.handler())
	t.Cleanup(upstream.Close)

	// No toggles seeded: recording events must not trigger a program fetch.
	newTestApp(t, Deps{Store: st, Auth: httpapi.New(""), MirakcURL: upstream.URL, TimeZone: "Asia/Tokyo"})
	fake.pushEvent("event: recording.started\ndata: {\"programId\":101}\n\n")

	time.Sleep(300 * time.Millisecond)
	if got := fake.programFetchCount(); got != 0 {
		t.Fatalf("program fetched %d times with toggles off, want 0", got)
	}
}

func TestSSEEventNotifiesAndInvalidates(t *testing.T) {
	st := newStore(t)
	ntfy := &fakeNtfy{}
	ntfyServer := httptest.NewServer(ntfy.handler())
	t.Cleanup(ntfyServer.Close)
	seedNotification(t, st, ntfyServer.URL+"/recording", `"onSchedule":false,"onStart":true`)

	fake := newFakeMirakc()
	fake.programs = `[{"id":101,"eventId":201,"isFree":true,"name":"x","startAt":1795200000000,"duration":60,"networkId":1,"serviceId":1,"genres":[]}]`
	fake.services = `[{"networkId":1,"serviceId":2,"name":"BS日テレ","channel":{"channel":"ch211"}}]`
	upstream := httptest.NewServer(fake.handler())
	t.Cleanup(upstream.Close)

	_, ts := newTestApp(t, Deps{Store: st, Auth: httpapi.New(""), MirakcURL: upstream.URL, TimeZone: "Asia/Tokyo"})

	// Prime the programs cache, then invalidate via the EPG event.
	res, _ := get(t, ts.URL+"/api/programs")
	if res.StatusCode != 200 {
		t.Fatalf("prime = %d", res.StatusCode)
	}
	prime := fake.programsFetchCount()

	fake.pushEvent("event: epg.programs-updated\ndata: {}\n\n")
	waitFor(t, 2*time.Second, func() bool {
		res, _ := get(t, ts.URL+"/api/programs")
		return res.StatusCode == 200 && fake.programsFetchCount() > prime
	})

	// A recording event with the toggle on fetches the program and notifies.
	fake.pushEvent("event: recording.started\ndata: {\"programId\":101}\n\n")
	waitFor(t, 2*time.Second, func() bool {
		return fake.programFetchCount() == 1 && len(ntfy.notifications()) > 0
	})

	n := ntfy.notifications()[0]
	var payload struct {
		Title   string `json:"title"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(n), &payload); err != nil {
		t.Fatalf("ntfy body: %v: %q", err, n)
	}
	if !strings.Contains(payload.Title, "夜のニュース") || !strings.Contains(payload.Title, "録画開始") {
		t.Errorf("started title = %q", payload.Title)
	}
	if !strings.Contains(payload.Message, "BS日テレ") {
		t.Errorf("message missing service line: %q", payload.Message)
	}
}

func TestCommentsStreamThroughApp(t *testing.T) {
	st := newStore(t)
	fake := newFakeMirakc()
	upstream := httptest.NewServer(fake.handler())
	t.Cleanup(upstream.Close)

	_, ts := newTestApp(t, Deps{Store: st, Auth: httpapi.New(""), MirakcURL: upstream.URL, TimeZone: "Asia/Tokyo"})

	// No live-comment mappings: sources resolves to the empty list and
	// the stream ends right after the sources event (client-side
	// "not comment-capable" contract).
	status, body := getStream(t, ts.URL+"/api/comments/services/1/stream")
	if status != 200 {
		t.Fatalf("stream = %d, want 200", status)
	}
	if !strings.HasPrefix(body, "event: sources\ndata: []\n\n") {
		t.Fatalf("stream head = %q", body[:min(len(body), 60)])
	}
}

func TestCommentsStreamUnsetMIRAKC(t *testing.T) {
	st := newStore(t)
	_, ts := newTestApp(t, Deps{Store: st, Auth: httpapi.New(""), MirakcURL: ""})

	res, body := get(t, ts.URL+"/api/comments/services/1/stream")
	if res.StatusCode != 503 || strings.TrimSpace(body) != `{"error":"MIRAKC_URL is not set"}` {
		t.Fatalf("unset stream = %d %q, want the 503 convention", res.StatusCode, body)
	}
}

func TestCommentsStreamTicketAuth(t *testing.T) {
	st := newStore(t)
	fake := newFakeMirakc()
	upstream := httptest.NewServer(fake.handler())
	t.Cleanup(upstream.Close)

	_, ts := newTestApp(t, Deps{Store: st, Auth: httpapi.New("stoken"), MirakcURL: upstream.URL, TimeZone: "Asia/Tokyo"})

	res, body := get(t, ts.URL+"/api/comments/services/1/stream?ticket="+mintTicket(t, ts.URL))
	if res.StatusCode != 200 || !strings.HasPrefix(body, "event: sources\ndata: []") {
		t.Fatalf("ticketed stream = %d %q", res.StatusCode, body[:min(len(body), 40)])
	}
}

func mintTicket(t *testing.T, baseURL string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", baseURL+"/api/auth/ticket", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("u:stoken")))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var body struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Ticket == "" {
		t.Fatalf("mint = %d %q", res.StatusCode, raw)
	}
	return body.Ticket
}

func getStream(t *testing.T, url string) (status int, body string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res.StatusCode, string(raw)
}

func TestResolveChannelID(t *testing.T) {
	st := newStore(t)
	addMapping := func(channel string, enabled bool, assignments string) {
		t.Helper()
		if _, err := st.Add(store.ColLiveComment, []byte(
			`{"channel":"`+channel+`","assignments":[`+assignments+`],"enabled":`+fmt.Sprint(enabled)+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	addMapping("gr026", false, `{"source":"nicolive","channelId":"ch-disabled"}`)
	addMapping("gr999", true, `{"source":"nicolive","channelId":"ch-other"}`)
	addMapping("gr026", true, `{"source":"nx-jikkyo","channelId":"jk26"}`)
	addMapping("gr026", true, `{"source":"nicolive","channelId":"ch2646436"}`)
	// Seeded last (newest → iterated first by created_at DESC) so a
	// short-circuit regression that returns unsupported on a no-assignment
	// mapping would actually fail this test.
	time.Sleep(2 * time.Millisecond)
	addMapping("gr026", true, ``) // matching channel, no nicolive assignment

	resolve := resolveChannelID(st, "nicolive")
	got, ok := resolve(comments.Target{Channel: "gr026"})
	if !ok || got != "ch2646436" {
		t.Fatalf("nicolive = %q %v, want ch2646436 (first enabled mapping WITH an assignment for the source)", got, ok)
	}

	resolveNx := resolveChannelID(st, "nx-jikkyo")
	got, ok = resolveNx(comments.Target{Channel: "gr026"})
	if !ok || got != "jk26" {
		t.Fatalf("nx-jikkyo = %q %v, want jk26", got, ok)
	}

	if _, ok := resolve(comments.Target{Channel: "gr001"}); ok {
		t.Fatal("unmapped channel must resolve to unsupported")
	}
	if _, ok := resolve(comments.Target{Channel: ""}); ok {
		t.Fatal("channel-less target must resolve to unsupported")
	}
}
