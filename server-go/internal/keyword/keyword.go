// Port of server/lib/keyword-recorder.ts: the keyword auto-recording job.
// Matches enabled rules against future unscheduled programs, registers
// mirakc recording schedules tagged with KEYWORD_RECORDING_TAG, and notifies
// once per registration.
package keyword

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/api"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/i18n"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/mirakc"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/settings"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/timefmt"
)

// KEYWORD_RECORDING_TAG identifies this job's reservations in mirakc.
const KEYWORD_RECORDING_TAG = "mirakc-ui:keyword"

// Registration is one successful schedule registration.
type Registration struct {
	ProgramID int64
	Keyword   string
}

// Deps are the collaborators of one recording pass.
type Deps struct {
	// APIURL is the mirakc Web API base (a single trailing slash is trimmed).
	APIURL string
	// Client performs the services / schedules requests; nil uses
	// http.DefaultClient.
	Client *http.Client
	// GetPrograms returns the slim program list (the programs cache shared
	// with GET /api/programs).
	GetPrograms func() ([]mirakc.SlimProgram, error)
	// ListRules returns all stored rules; the enabled filter is ours.
	ListRules func() ([]api.KeywordRule, error)
	// Notify delivers one notification. It must never fail the run (the
	// caller's contract) and is fire-and-forget.
	Notify func(settings.Notification)
	// Now returns the current epoch milliseconds; nil uses time.Now().
	Now func() int64
	// TimeZone is used for file names, notification airtime and period
	// matching.
	TimeZone string
}

// BuildContentPath builds the recording file path
// `{formatYmdHms(startAt, tz)}_{id}_{name}.m2ts`, the same shape as manual
// reservations (client/routes/program/.../$programId.tsx).
func BuildContentPath(p mirakc.SlimProgram, tz string) string {
	name := ""
	if p.Name != nil {
		name = *p.Name
	}
	return timefmt.FormatYmdHms(p.StartAt, tz) + "_" + strconv.FormatInt(p.ID, 10) +
		"_" + name + ".m2ts"
}

// service is the subset of mirakc's GET /services used to resolve a
// program's channel and to name the channel in notifications.
type service struct {
	NetworkID int64  `json:"networkId"`
	ServiceID int64  `json:"serviceId"`
	Name      string `json:"name"`
	Channel   struct {
		Channel string `json:"channel"`
	} `json:"channel"`
}

// schedule is the subset of mirakc's GET /recording/schedules used to skip
// already-scheduled programs.
type schedule struct {
	Program struct {
		ID int64 `json:"id"`
	} `json:"program"`
}

func fetchJSON[T any](client *http.Client, url string) (T, error) {
	res, err := client.Get(url)
	var zero T
	if err != nil {
		return zero, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return zero, fmt.Errorf("GET %s failed: %d", url, res.StatusCode)
	}
	var value T
	if err := json.NewDecoder(res.Body).Decode(&value); err != nil {
		return zero, err
	}
	return value, nil
}

type scheduleOption struct {
	ContentPath string `json:"contentPath"`
}

type scheduleBody struct {
	ProgramID int64          `json:"programId"`
	Options   scheduleOption `json:"options"`
	Tags      []string       `json:"tags"`
}

func postSchedule(client *http.Client, url string, body scheduleBody) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	res, err := client.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("status %d", res.StatusCode)
	}
	return nil
}

func serviceKey(networkID, serviceID int64) string {
	return strconv.FormatInt(networkID, 10) + ":" + strconv.FormatInt(serviceID, 10)
}

// RunKeywordRecording performs one recording pass. Programs already started
// or already scheduled are skipped; the first matching enabled rule wins.
// Individual schedule failures are logged and the pass continues. Returns
// fast without any fetch when no rule is enabled.
func RunKeywordRecording(deps Deps) (registered []Registration, err error) {
	allRules, err := deps.ListRules()
	if err != nil {
		return nil, err
	}
	var rules []api.KeywordRule
	for _, r := range allRules {
		if r.Enabled {
			rules = append(rules, r)
		}
	}
	if len(rules) == 0 {
		return nil, nil
	}

	client := deps.Client
	if client == nil {
		client = http.DefaultClient
	}
	now := time.Now().UnixMilli()
	if deps.Now != nil {
		now = deps.Now()
	}
	apiURL := strings.TrimSuffix(deps.APIURL, "/")

	// Programs come from the shared cache; services and schedules from
	// mirakc. All three in parallel, like the Deno Promise.all.
	var (
		programs  []mirakc.SlimProgram
		services  []service
		schedules []schedule

		programsErr  error
		servicesErr  error
		schedulesErr error
		wg           sync.WaitGroup
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		programs, programsErr = deps.GetPrograms()
	}()
	go func() {
		defer wg.Done()
		services, servicesErr = fetchJSON[[]service](client, apiURL+"/services")
	}()
	go func() {
		defer wg.Done()
		schedules, schedulesErr = fetchJSON[[]schedule](client, apiURL+"/recording/schedules")
	}()
	wg.Wait()
	if programsErr != nil {
		return nil, programsErr
	}
	if servicesErr != nil {
		return nil, servicesErr
	}
	if schedulesErr != nil {
		return nil, schedulesErr
	}

	scheduledIDs := make(map[int64]bool, len(schedules))
	for _, s := range schedules {
		scheduledIDs[s.Program.ID] = true
	}
	// (networkId, serviceId) → service, for channel resolution and names.
	serviceOf := make(map[string]service, len(services))
	for _, s := range services {
		serviceOf[serviceKey(s.NetworkID, s.ServiceID)] = s
	}

	registered = []Registration{}
	for _, program := range programs {
		if program.StartAt <= now || scheduledIDs[program.ID] {
			continue
		}
		svc, hasSvc := serviceOf[serviceKey(program.NetworkID, program.ServiceID)]
		var channelID *string
		if hasSvc {
			channelID = &svc.Channel.Channel
		}
		genres := make([]int, 0, len(program.Genres))
		for _, g := range program.Genres {
			genres = append(genres, int(g.Lv1))
		}
		target := settings.KeywordRuleTarget{
			Name:      program.Name,
			StartAt:   program.StartAt,
			ChannelID: channelID,
			Genres:    genres,
		}

		matched := -1
		for i := range rules {
			if settings.MatchesKeywordRule(settings.MatchFieldsOf(rules[i]), target) {
				matched = i
				break
			}
		}
		if matched < 0 {
			continue
		}
		keyword := rules[matched].Keyword

		err := postSchedule(client, apiURL+"/recording/schedules", scheduleBody{
			ProgramID: program.ID,
			Options:   scheduleOption{ContentPath: BuildContentPath(program, deps.TimeZone)},
			Tags:      []string{KEYWORD_RECORDING_TAG, "keyword:" + keyword},
		})
		if err != nil {
			log.Printf("[keyword-recorder] failed to schedule program %d: %v", program.ID, err)
			continue
		}
		scheduledIDs[program.ID] = true
		registered = append(registered, Registration{ProgramID: program.ID, Keyword: keyword})

		name := strconv.FormatInt(program.ID, 10)
		if program.Name != nil {
			name = *program.Name
		}
		lines := []string{
			i18n.T("notification.keyword.message", map[string]string{"keyword": keyword}),
		}
		if hasSvc {
			lines = append(lines, svc.Name)
		}
		lines = append(lines, i18n.T("notification.recording.airtime", map[string]string{
			"start": timefmt.FormatDisplayDateTime(program.StartAt, deps.TimeZone),
			"end":   timefmt.FormatDisplayTime(program.StartAt+program.Duration, deps.TimeZone),
		}))
		deps.Notify(settings.Notification{
			Title:   i18n.T("notification.keyword.title", map[string]string{"name": name}),
			Message: strings.Join(lines, "\n"),
			Tags:    []string{"calendar"},
		})
	}

	return registered, nil
}

type config struct {
	debounce time.Duration
}

// Option configures Start.
type Option func(*config)

// WithDebounce overrides the trigger debounce (default 60s, matching the
// Deno job's 60_000 ms).
func WithDebounce(d time.Duration) Option {
	return func(c *config) { c.debounce = d }
}

// Job runs the keyword recording: one immediate pass at startup, a
// fallback interval, and debounced on-demand triggers (EPG update events).
type Job struct {
	deps     Deps
	interval time.Duration
	debounce time.Duration

	mu      sync.Mutex
	stopped bool
	running bool
	pending bool
	timer   *time.Timer
	done    chan struct{}
}

// Start launches the job. The trigger debounce defaults to 60 seconds;
// WithDebounce overrides it.
func Start(deps Deps, interval time.Duration, opts ...Option) *Job {
	cfg := config{debounce: 60 * time.Second}
	for _, opt := range opts {
		opt(&cfg)
	}
	j := &Job{
		deps:     deps,
		interval: interval,
		debounce: cfg.debounce,
		done:     make(chan struct{}),
	}
	j.startRun()
	go j.tickLoop()
	return j
}

func (j *Job) tickLoop() {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-j.done:
			return
		case <-ticker.C:
			j.startRun()
		}
	}
}

// startRun requests a pass. A request arriving while a pass is running
// collapses into a single rerun after completion; passes never run
// concurrently.
func (j *Job) startRun() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.stopped {
		return
	}
	if j.running {
		j.pending = true
		return
	}
	j.running = true
	go j.pass()
}

// pass executes recording passes, draining one collapsed rerun per
// iteration (the Deno run() finally-block, as a loop rather than
// recursion so the depth is structurally bounded).
func (j *Job) pass() {
	for {
		registered, err := RunKeywordRecording(j.deps)
		if err != nil {
			log.Printf("[keyword-recorder] run failed: %v", err)
		} else if len(registered) > 0 {
			log.Printf("[keyword-recorder] scheduled %d program(s)", len(registered))
		}

		j.mu.Lock()
		j.running = false
		rerun := j.pending && !j.stopped
		j.pending = false
		if rerun {
			j.running = true
		}
		j.mu.Unlock()
		if !rerun {
			return
		}
	}
}

// Trigger requests a run (EPG update events etc.). Requests arriving within
// the debounce window collapse into one pass; a request arriving during a
// pass collapses into one rerun after completion.
func (j *Job) Trigger() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.stopped {
		return
	}
	if j.timer != nil {
		j.timer.Stop()
	}
	j.timer = time.AfterFunc(j.debounce, func() {
		j.mu.Lock()
		j.timer = nil
		j.mu.Unlock()
		j.startRun()
	})
}

// Stop halts the job: no further interval, debounce or rerun passes start.
// An in-flight pass is not interrupted (same as the Deno stop()).
func (j *Job) Stop() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.stopped {
		return
	}
	j.stopped = true
	if j.timer != nil {
		j.timer.Stop()
		j.timer = nil
	}
	close(j.done)
}
