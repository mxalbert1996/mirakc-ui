package keyword

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/google/uuid"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/api"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/mirakc"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/settings"
)

// startAt is 2026-01-02T03:04:05+09:00 in epoch ms.
var startAt = time.Date(2026, 1, 1, 18, 4, 5, 0, time.UTC).UnixMilli()

const servicesJSON = `[
  {"id":3273601024,"networkId":32736,"serviceId":1024,"name":"テレビA","channel":{"channel":"27","type":"GR"}},
  {"id":3273601025,"networkId":32736,"serviceId":1025,"name":"テレビA-2","channel":{"channel":"27","type":"GR"}},
  {"id":3273701032,"networkId":32737,"serviceId":1032,"name":"テレビB","channel":{"channel":"25","type":"GR"}}
]`

func slimProgram(id int64, name string, start int64, serviceID int64) mirakc.SlimProgram {
	return mirakc.SlimProgram{
		ID:        id,
		Name:      &name,
		StartAt:   start,
		Duration:  30 * 60 * 1000,
		NetworkID: 32736,
		ServiceID: serviceID,
		Genres:    []mirakc.SlimGenre{{Lv1: 0}},
	}
}

func rule(keyword string, mutate func(*api.KeywordRule)) api.KeywordRule {
	r := api.KeywordRule{
		Id:        uuid.New(),
		Keyword:   keyword,
		Channels:  []string{},
		Genres:    []int{},
		Enabled:   true,
		CreatedAt: 0,
	}
	if mutate != nil {
		mutate(&r)
	}
	return r
}

func atJST(year, month, day, hour, min, sec int) *time.Time {
	t := time.Date(year, time.Month(month), day, hour, min, sec, 0,
		time.FixedZone("+09:00", 9*60*60))
	return &t
}

type fetchCall struct {
	Method string
	Path   string
	Body   map[string]any
}

// fakeMirakc stands in for the mirakc Web API; programs come from the
// shared cache (GetPrograms), never over HTTP.
type fakeMirakc struct {
	srv         *httptest.Server
	mu          sync.Mutex
	calls       []fetchCall
	programs    []mirakc.SlimProgram
	schedules   []string
	failPostFor map[int64]bool
}

func newFakeMirakc(t *testing.T, programs []mirakc.SlimProgram, schedules []string, failPostFor []int64) *fakeMirakc {
	t.Helper()
	f := &fakeMirakc{
		programs:    programs,
		schedules:   schedules,
		failPostFor: make(map[int64]bool, len(failPostFor)),
	}
	for _, id := range failPostFor {
		f.failPostFor[id] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/services", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.Method, r.URL.Path, nil)
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, servicesJSON)
	})
	mux.HandleFunc("GET /api/recording/schedules", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.Method, r.URL.Path, nil)
		w.Header().Set("content-type", "application/json")
		body := "[]"
		if len(f.schedules) > 0 {
			body = "[" + strings.Join(f.schedules, ",") + "]"
		}
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("POST /api/recording/schedules", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Errorf("bad POST body: %v", err)
		}
		f.record(r.Method, r.URL.Path, b)
		programID := int64(b["programId"].(float64))
		if f.failPostFor[programID] {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "ng")
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, "created")
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMirakc) record(method, path string, body map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fetchCall{Method: method, Path: path, Body: body})
}

func (f *fakeMirakc) allCalls() []fetchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fetchCall{}, f.calls...)
}

func (f *fakeMirakc) postCalls() []fetchCall {
	var posts []fetchCall
	for _, c := range f.allCalls() {
		if c.Method == http.MethodPost {
			posts = append(posts, c)
		}
	}
	return posts
}

func testDeps(t *testing.T, f *fakeMirakc, rules []api.KeywordRule, notify func(settings.Notification)) Deps {
	t.Helper()
	if notify == nil {
		notify = func(settings.Notification) {}
	}
	return Deps{
		APIURL: f.srv.URL + "/api",
		Client: f.srv.Client(),
		GetPrograms: func() ([]mirakc.SlimProgram, error) {
			return f.programs, nil
		},
		ListRules: func() ([]api.KeywordRule, error) {
			return rules, nil
		},
		Notify:   notify,
		Now:      func() int64 { return startAt - 60_000 },
		TimeZone: "Asia/Tokyo",
	}
}

func assertRegistrations(t *testing.T, got []Registration, want []Registration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("registered = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("registered = %+v, want %+v", got, want)
		}
	}
}

func TestBuildContentPath(t *testing.T) {
	p := slimProgram(327360102706471, "ニュースウォッチ", startAt, 1024)
	got := BuildContentPath(p, "Asia/Tokyo")
	want := "20260102030405_327360102706471_ニュースウォッチ.m2ts"
	if got != want {
		t.Errorf("BuildContentPath = %q, want %q", got, want)
	}
}

func TestBuildContentPathNullName(t *testing.T) {
	p := slimProgram(1, "x", startAt, 1024)
	p.Name = nil
	got := BuildContentPath(p, "Asia/Tokyo")
	want := "20260102030405_1_.m2ts"
	if got != want {
		t.Errorf("BuildContentPath = %q, want %q", got, want)
	}
}

func TestRunKeywordRecordingNoEnabledRules(t *testing.T) {
	f := newFakeMirakc(t, []mirakc.SlimProgram{slimProgram(1, "ニュース", startAt, 1024)}, nil, nil)
	deps := testDeps(t, f, []api.KeywordRule{
		rule("ニュース", func(r *api.KeywordRule) { r.Enabled = false }),
	}, nil)

	registered, err := RunKeywordRecording(deps)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	assertRegistrations(t, registered, nil)
	if calls := f.allCalls(); len(calls) != 0 {
		t.Errorf("expected zero upstream fetches, got %+v", calls)
	}
}

func TestRunKeywordRecordingRegistersAndNotifies(t *testing.T) {
	target := slimProgram(327360102706471, "ニュースウォッチ", startAt, 1024)
	other := slimProgram(2, "アニメまつり", startAt, 1024)
	f := newFakeMirakc(t, []mirakc.SlimProgram{target, other}, nil, nil)
	var notifications []settings.Notification
	deps := testDeps(t, f, []api.KeywordRule{rule("ニュース", nil)},
		func(n settings.Notification) { notifications = append(notifications, n) })

	registered, err := RunKeywordRecording(deps)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	assertRegistrations(t, registered, []Registration{{ProgramID: 327360102706471, Keyword: "ニュース"}})

	posts := f.postCalls()
	if len(posts) != 1 {
		t.Fatalf("POST count = %d, want 1", len(posts))
	}
	if posts[0].Path != "/api/recording/schedules" {
		t.Errorf("POST path = %q", posts[0].Path)
	}
	wantBody := map[string]any{
		"programId": float64(327360102706471),
		"options": map[string]any{
			"contentPath": "20260102030405_327360102706471_ニュースウォッチ.m2ts",
		},
		"tags": []any{KEYWORD_RECORDING_TAG, "keyword:ニュース"},
	}
	if !jsonEqual(posts[0].Body, wantBody) {
		t.Errorf("POST body = %v, want %v", posts[0].Body, wantBody)
	}

	if len(notifications) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifications))
	}
	n := notifications[0]
	if n.Title != "録画登録: ニュースウォッチ" {
		t.Errorf("title = %q", n.Title)
	}
	// The ja-JP literals for notification.keyword.message /
	// notification.recording.airtime, plus the service name; null lines
	// dropped, joined with \n (start 03:04:05 + 30min → 03:34).
	wantMessage := "キーワード「ニュース」に一致する番組を予約しました。\n" +
		"テレビA\n2026/01/02 03:04 〜 03:34"
	if n.Message != wantMessage {
		t.Errorf("message = %q, want %q", n.Message, wantMessage)
	}
	if len(n.Tags) != 1 || n.Tags[0] != "calendar" {
		t.Errorf("tags = %v, want [calendar]", n.Tags)
	}
}

func TestRunKeywordRecordingTrailingSlashTrimmed(t *testing.T) {
	f := newFakeMirakc(t, []mirakc.SlimProgram{slimProgram(1, "ニュース", startAt, 1024)}, nil, nil)
	deps := testDeps(t, f, []api.KeywordRule{rule("ニュース", nil)}, nil)
	deps.APIURL += "/"

	if _, err := RunKeywordRecording(deps); err != nil {
		t.Fatalf("err = %v", err)
	}
	var paths []string
	for _, c := range f.allCalls() {
		paths = append(paths, c.Path)
	}
	slices.Sort(paths)
	want := []string{"/api/recording/schedules", "/api/recording/schedules", "/api/services"}
	if !slices.Equal(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

func TestRunKeywordRecordingChannelCondition(t *testing.T) {
	// networkId 32736 / serviceId 1024 resolves to channel "27".
	fMatched := newFakeMirakc(t, []mirakc.SlimProgram{slimProgram(327360102706471, "ニュースウォッチ", startAt, 1024)}, nil, nil)
	matched, err := RunKeywordRecording(testDeps(t, fMatched, []api.KeywordRule{
		rule("ニュース", func(r *api.KeywordRule) { r.Channels = []string{"27"} }),
	}, nil))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	assertRegistrations(t, matched, []Registration{{ProgramID: 327360102706471, Keyword: "ニュース"}})

	fUnmatched := newFakeMirakc(t, []mirakc.SlimProgram{slimProgram(327360102706471, "ニュースウォッチ", startAt, 1024)}, nil, nil)
	unmatched, err := RunKeywordRecording(testDeps(t, fUnmatched, []api.KeywordRule{
		rule("ニュース", func(r *api.KeywordRule) { r.Channels = []string{"25"} }),
	}, nil))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	assertRegistrations(t, unmatched, nil)
	if posts := fMatched.postCalls(); len(posts) != 1 {
		t.Errorf("POST count = %d, want 1", len(posts))
	}
}

func TestRunKeywordRecordingChannelSpansSubServices(t *testing.T) {
	// serviceId 1025 (テレビA-2) rides the same physical channel "27".
	onSubService := slimProgram(327360102506471, "ニュース速報", startAt, 1025)
	f := newFakeMirakc(t, []mirakc.SlimProgram{onSubService}, nil, nil)

	registered, err := RunKeywordRecording(testDeps(t, f, []api.KeywordRule{
		rule("ニュース", func(r *api.KeywordRule) { r.Channels = []string{"27"} }),
	}, nil))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	assertRegistrations(t, registered, []Registration{{ProgramID: 327360102506471, Keyword: "ニュース"}})
	if posts := f.postCalls(); len(posts) != 1 {
		t.Errorf("POST count = %d, want 1", len(posts))
	}
}

func TestRunKeywordRecordingGenreAndPeriod(t *testing.T) {
	fGenre := newFakeMirakc(t, []mirakc.SlimProgram{slimProgram(1, "ニュース", startAt, 1024)}, nil, nil)
	byGenre, err := RunKeywordRecording(testDeps(t, fGenre, []api.KeywordRule{
		rule("ニュース", func(r *api.KeywordRule) { r.Genres = []int{7} }),
	}, nil))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	assertRegistrations(t, byGenre, nil)

	// startAt is 2026-01-02 in Asia/Tokyo, inside the same-day window.
	fPeriod := newFakeMirakc(t, []mirakc.SlimProgram{slimProgram(1, "ニュース", startAt, 1024)}, nil, nil)
	inPeriod, err := RunKeywordRecording(testDeps(t, fPeriod, []api.KeywordRule{
		rule("ニュース", func(r *api.KeywordRule) {
			r.From = atJST(2026, 1, 2, 0, 0, 0)
			r.To = atJST(2026, 1, 2, 23, 59, 59)
		}),
	}, nil))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	assertRegistrations(t, inPeriod, []Registration{{ProgramID: 1, Keyword: "ニュース"}})
}

func TestRunKeywordRecordingSkipsPastAndScheduled(t *testing.T) {
	past := slimProgram(1, "ニュース", startAt-86_400_000, 1024)
	scheduled := slimProgram(2, "ニュース", startAt, 1024)
	fresh := slimProgram(3, "ニュース", startAt, 1024)
	f := newFakeMirakc(t,
		[]mirakc.SlimProgram{past, scheduled, fresh},
		[]string{`{"program":{"id":2},"state":"scheduled","tags":[]}`},
		nil)

	registered, err := RunKeywordRecording(testDeps(t, f, []api.KeywordRule{rule("ニュース", nil)}, nil))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	assertRegistrations(t, registered, []Registration{{ProgramID: 3, Keyword: "ニュース"}})
	if posts := f.postCalls(); len(posts) != 1 {
		t.Errorf("POST count = %d, want 1", len(posts))
	}
}

func TestRunKeywordRecordingFirstRuleWins(t *testing.T) {
	f := newFakeMirakc(t, []mirakc.SlimProgram{slimProgram(1, "ニュースウォッチ", startAt, 1024)}, nil, nil)

	registered, err := RunKeywordRecording(testDeps(t, f, []api.KeywordRule{
		rule("ニュース", nil),
		rule("ウォッチ", nil),
	}, nil))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	assertRegistrations(t, registered, []Registration{{ProgramID: 1, Keyword: "ニュース"}})
	if posts := f.postCalls(); len(posts) != 1 {
		t.Errorf("POST count = %d, want 1", len(posts))
	}
}

func TestRunKeywordRecordingScheduleFailureContinues(t *testing.T) {
	a := slimProgram(1, "ニュース", startAt, 1024)
	b := slimProgram(2, "ニュース", startAt, 1024)
	f := newFakeMirakc(t, []mirakc.SlimProgram{a, b}, nil, []int64{1})
	var notifications []settings.Notification
	deps := testDeps(t, f, []api.KeywordRule{rule("ニュース", nil)},
		func(n settings.Notification) { notifications = append(notifications, n) })

	registered, err := RunKeywordRecording(deps)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	assertRegistrations(t, registered, []Registration{{ProgramID: 2, Keyword: "ニュース"}})
	if posts := f.postCalls(); len(posts) != 2 {
		t.Errorf("POST count = %d, want 2 (the failed one is still attempted)", len(posts))
	}
	if len(notifications) != 1 {
		t.Errorf("notifications = %d, want 1", len(notifications))
	}
}

func jsonEqual(a, b any) bool {
	ra, erra := json.Marshal(a)
	rb, errb := json.Marshal(b)
	return erra == nil && errb == nil && string(ra) == string(rb)
}

// runCounter tracks recording passes via ListRules (each pass calls it
// first) and can gate the first pass to simulate a long-running one.
type runCounter struct {
	mu          sync.Mutex
	calls       int
	inflight    int
	maxInflight int
	gateFirst   bool
	gate        chan struct{}
	started     chan struct{}
}

func newRunCounter(gateFirst bool) *runCounter {
	return &runCounter{
		gateFirst: gateFirst,
		gate:      make(chan struct{}),
		started:   make(chan struct{}, 64),
	}
}

func (c *runCounter) listRules() ([]api.KeywordRule, error) {
	c.mu.Lock()
	c.calls++
	first := c.gateFirst && c.calls == 1
	c.inflight++
	if c.inflight > c.maxInflight {
		c.maxInflight = c.inflight
	}
	c.mu.Unlock()

	select {
	case c.started <- struct{}{}:
	default:
	}
	if first {
		<-c.gate
	}

	c.mu.Lock()
	c.inflight--
	c.mu.Unlock()
	return nil, nil
}

func (c *runCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *runCounter) maxConcurrent() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxInflight
}

func waitPasses(t *testing.T, c *runCounter, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-c.started:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for pass %d (count %d)", i+1, c.count())
		}
	}
}

func jobDeps(c *runCounter) Deps {
	return Deps{
		APIURL: "http://mirakc.invalid/api", // never fetched: no rules
		GetPrograms: func() ([]mirakc.SlimProgram, error) {
			return nil, nil
		},
		ListRules: c.listRules,
		Notify:    func(settings.Notification) {},
		TimeZone:  "Asia/Tokyo",
	}
}

func TestJobInitialRunAndDebounceCollapse(t *testing.T) {
	c := newRunCounter(false)
	j := Start(jobDeps(c), time.Minute, WithDebounce(20*time.Millisecond))
	defer j.Stop()

	waitPasses(t, c, 1)
	if got := c.count(); got != 1 {
		t.Fatalf("passes = %d after startup, want 1", got)
	}

	j.Trigger()
	j.Trigger()
	j.Trigger()
	if got := c.count(); got != 1 {
		t.Fatalf("burst triggers must collapse before the debounce elapses; passes = %d", got)
	}

	waitPasses(t, c, 1)
	if got := c.count(); got != 2 {
		t.Fatalf("passes = %d, want 2 (startup + one collapsed run)", got)
	}
}

func TestJobStopMakesTriggerNoop(t *testing.T) {
	c := newRunCounter(false)
	j := Start(jobDeps(c), time.Minute, WithDebounce(5*time.Millisecond))

	waitPasses(t, c, 1)
	j.Trigger()
	j.Stop()

	time.Sleep(30 * time.Millisecond)
	if got := c.count(); got != 1 {
		t.Fatalf("passes = %d after Stop, want 1", got)
	}

	j.Trigger()
	time.Sleep(30 * time.Millisecond)
	if got := c.count(); got != 1 {
		t.Fatalf("Trigger after Stop must be a no-op; passes = %d", got)
	}
}

func TestJobTriggerDuringRunSingleRerun(t *testing.T) {
	c := newRunCounter(true)
	j := Start(jobDeps(c), time.Minute, WithDebounce(10*time.Millisecond))
	defer j.Stop()

	waitPasses(t, c, 1) // first pass is gated

	j.Trigger()
	j.Trigger()
	time.Sleep(100 * time.Millisecond) // the debounce fires while pass 1 runs
	if got := c.count(); got != 1 {
		t.Fatalf("trigger during a run must not start a concurrent pass; passes = %d", got)
	}

	close(c.gate)
	waitPasses(t, c, 1)
	if got := c.count(); got != 2 {
		t.Fatalf("passes = %d, want 2 (one rerun)", got)
	}
	time.Sleep(100 * time.Millisecond)
	if got := c.count(); got != 2 {
		t.Fatalf("exactly one rerun expected; passes = %d", got)
	}
	if c.maxConcurrent() != 1 {
		t.Fatalf("passes overlapped; max concurrent = %d", c.maxConcurrent())
	}
}

func TestJobRacingTriggersNoDoubleRun(t *testing.T) {
	c := newRunCounter(true)
	j := Start(jobDeps(c), time.Minute, WithDebounce(5*time.Millisecond))

	waitPasses(t, c, 1) // first pass is gated

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				j.Trigger()
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
	time.Sleep(50 * time.Millisecond) // the collapsed debounce fires while pass 1 is gated
	close(c.gate)

	waitPasses(t, c, 1)
	time.Sleep(100 * time.Millisecond)
	if got := c.count(); got != 2 {
		t.Fatalf("racing triggers must collapse to startup + one rerun; passes = %d", got)
	}
	j.Stop()
	if c.maxConcurrent() != 1 {
		t.Fatalf("passes overlapped; max concurrent = %d", c.maxConcurrent())
	}
}

func TestJobRequestsDuringRunCollapseToOneRerun(t *testing.T) {
	c := newRunCounter(true)
	j := &Job{deps: jobDeps(c), interval: time.Hour, debounce: time.Hour, done: make(chan struct{})}
	defer j.Stop()

	j.startRun()
	waitPasses(t, c, 1)

	j.startRun()
	j.startRun()

	close(c.gate)
	waitPasses(t, c, 1)
	if got := c.count(); got != 2 {
		t.Fatalf("passes = %d, want 2", got)
	}
	time.Sleep(50 * time.Millisecond)
	if got := c.count(); got != 2 {
		t.Fatalf("two in-flight requests must collapse to one rerun; passes = %d", got)
	}
	if c.maxConcurrent() != 1 {
		t.Fatalf("passes overlapped; max concurrent = %d", c.maxConcurrent())
	}
}

func TestJobIntervalDrivesPasses(t *testing.T) {
	c := newRunCounter(false)
	j := Start(jobDeps(c), 20*time.Millisecond, WithDebounce(time.Hour))

	waitPasses(t, c, 5) // startup + interval passes
	j.Stop()

	frozen := c.count()
	time.Sleep(80 * time.Millisecond)
	if got := c.count(); got != frozen {
		t.Fatalf("passes kept running after Stop: %d → %d", frozen, got)
	}
	if c.maxConcurrent() != 1 {
		t.Fatalf("passes overlapped; max concurrent = %d", c.maxConcurrent())
	}
}
