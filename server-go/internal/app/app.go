// Package app composes the mirakc integration exactly as server/main.ts
// does: the mirakc proxy and programs API, the keyword auto-recording job,
// the mirakc /events subscription, and the notification fan-out that gates
// on the stored settings' toggles.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/api"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/comments"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/httpapi"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/keyword"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/mirakc"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/settings"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/store"
)

// Deps are the composed application's inputs. The zero-ish values mirror
// main.ts: an empty MirakcURL disables the recording job, the events
// subscription and upstream fetches, while the proxy and programs routes
// stay mounted with the unset-URL 500 convention.
type Deps struct {
	Store     *store.Store
	Auth      *httpapi.Auth
	Log       *slog.Logger
	MirakcURL string
	TimeZone  string

	// KeywordInterval is the fallback run interval for the recording job.
	KeywordInterval time.Duration

	// KeywordOptions forwards job options to keyword.Start (tests shrink
	// the debounce); production leaves it empty.
	KeywordOptions []keyword.Option

	// Client for all mirakc traffic. nil → http.DefaultClient, like the
	// Deno fetch default. It must NOT carry a global timeout: the events
	// subscription holds a long-lived response.
	Client *http.Client
}

type App struct {
	// Handler is the full mounted surface behind the auth middleware —
	// the Go counterpart of main.ts's app with auth.use first.
	Handler http.Handler

	store  *store.Store
	log    *slog.Logger
	job    *keyword.Job
	cancel context.CancelFunc
}

// New composes and starts the background pieces (the recording job runs
// once at startup, like main.ts starting the job at module load). The
// events subscription and the job stop when ctx is cancelled; Stop()
// additionally halts the interval fallback.
func New(ctx context.Context, d Deps) *App {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	d.Client = client

	// main.ts clamps KEYWORD_RECORDING_INTERVAL_MINUTES to >= 1 minute;
	// the composed app enforces the same floor.
	if d.KeywordInterval <= 0 {
		d.KeywordInterval = 60 * time.Minute
	}

	var cache *mirakc.ProgramsCache
	if d.MirakcURL != "" {
		cache = mirakc.NewProgramsCache(mirakc.APIURL(d.MirakcURL), client, mirakc.DefaultProgramsCacheTTL, nil)
	}
	job := startKeywordJob(d, cache)

	var proxy http.Handler
	var programs http.Handler
	if d.MirakcURL == "" {
		proxy = mirakc.NewProxy("", mirakc.ProxyHooks{}, client)
		programs = mirakc.ProgramsHandler(programsUnavailable)
	} else {
		apiURL := mirakc.APIURL(d.MirakcURL)
		proxy = mirakc.NewProxy(d.MirakcURL, proxyHooks(d, apiURL, client), client)
		programs = mirakc.ProgramsHandler(cache.GetPrograms)
	}

	router := httpapi.NewRouter(httpapi.Deps{
		Store: d.Store,
		Auth:  d.Auth,
		KeywordRulesOnChanged: func() {
			if job != nil {
				job.Trigger()
			}
		},
		MirakcProxy: proxy,
		Programs:    programs,
		Comments: comments.NewHandler(comments.Deps{
			APIURL: apiURLOrEmpty(d.MirakcURL),
			Sources: []comments.Source{
				comments.NewNicoliveSource(resolveChannelID(d.Store, "nicolive")),
				comments.NewNxJikkyoSource(resolveChannelID(d.Store, "nx-jikkyo")),
			},
		}),
	})

	appCtx, cancel := context.WithCancel(ctx)
	var eventsDone <-chan struct{}
	if cache != nil {
		eventsDone = mirakc.SubscribeEvents(appCtx, mirakc.EventsURL(d.MirakcURL), client, 5*time.Second,
			sseHandler(d, cache, job))
	}

	return &App{
		Handler: d.Auth.Middleware(router),
		store:   d.Store,
		log:     log,
		job:     job,
		cancel: func() {
			cancel()
			if eventsDone != nil {
				drain(eventsDone)
			}
		},
	}
}

// Stop halts the background pieces. The events subscription ends with the
// context; the job's interval fallback needs an explicit stop.
func (a *App) Stop() {
	a.cancel()
	if a.job != nil {
		a.job.Stop()
	}
}

// apiURLOrEmpty keeps the comments 503 convention intact when MIRAKC_URL
// is unset: the handler distinguishes "" from a real API base.
func apiURLOrEmpty(mirakcURL string) string {
	if mirakcURL == "" {
		return ""
	}
	return mirakc.APIURL(mirakcURL)
}

// resolveChannelID is main.ts's resolveChannelId: enabled live-comment
// mappings whose channel matches, first assignment for this source wins.
func resolveChannelID(st *store.Store, source string) comments.ResolveChannel {
	return func(target comments.Target) (string, bool) {
		if target.Channel == "" {
			return "", false
		}
		records, err := st.List(store.ColLiveComment)
		if err != nil {
			return "", false
		}
		for _, rec := range records {
			var mapping api.LiveCommentMapping
			if json.Unmarshal(rec.Data, &mapping) != nil {
				continue
			}
			if !mapping.Enabled || mapping.Channel != target.Channel {
				continue
			}
			for _, a := range mapping.Assignments {
				if string(a.Source) == source {
					return a.ChannelId, true
				}
			}
		}
		return "", false
	}
}

// startKeywordJob mirrors main.ts: the job exists only with MIRAKC_URL,
// and shares the programs cache with GET /api/programs.
func startKeywordJob(d Deps, cache *mirakc.ProgramsCache) *keyword.Job {
	if cache == nil {
		return nil
	}
	return keyword.Start(keyword.Deps{
		APIURL:      mirakc.APIURL(d.MirakcURL),
		Client:      d.Client,
		GetPrograms: cache.GetPrograms,
		ListRules:   listRules(d.Store),
		Notify:      func(n settings.Notification) { notifyIfEnabled(d.Store, settings.EventOnSchedule, n) },
		TimeZone:    d.TimeZone,
	}, d.KeywordInterval, d.KeywordOptions...)
}

// listRules reads the stored rules back as the generated type (every
// stored record is parse-gated, so decode failures are impossible by
// construction).
func listRules(st *store.Store) func() ([]api.KeywordRule, error) {
	return func() ([]api.KeywordRule, error) {
		records, err := st.List(store.ColKeywordRules)
		if err != nil {
			return nil, err
		}
		rules := make([]api.KeywordRule, 0, len(records))
		for _, rec := range records {
			var rule api.KeywordRule
			if err := json.Unmarshal(rec.Data, &rule); err != nil {
				continue
			}
			rules = append(rules, rule)
		}
		return rules, nil
	}
}

// sseHandler is main.ts's subscribeMirakcEvents onEvent: EPG updates
// invalidate the programs cache and re-run the keyword job; recording
// events gate on their toggle first (no pointless program fetch), then
// notify.
func sseHandler(d Deps, cache *mirakc.ProgramsCache, job *keyword.Job) func(mirakc.SseEvent) {
	return func(ev mirakc.SseEvent) {
		if ev.Event == "epg.programs-updated" {
			cache.Invalidate()
			if job != nil {
				job.Trigger()
			}
			return
		}
		kind, programID, ok := mirakc.RecordingEventOf(ev)
		if !ok {
			return
		}
		key := recordingToggleOf(kind)
		s := loadSettings(d.Store)
		if !settings.HasEnabledDestination(s, key) {
			return
		}
		mirakc.NotifyProgramEvent(mirakc.NotifyDeps{
			APIURL:   mirakc.APIURL(d.MirakcURL),
			Client:   d.Client,
			TimeZone: d.TimeZone,
			Notify: func(n settings.Notification) {
				notifyIfEnabled(d.Store, key, n)
			},
		}, recordingEventKeyOf(kind), programID, nil)
	}
}

// recordingEventKeyOf maps the SSE recording kinds onto the notification
// event keys (started/stopped/failed share their names).
func recordingEventKeyOf(kind mirakc.RecordingKind) mirakc.ProgramEventKey {
	switch kind {
	case mirakc.RecordingStarted:
		return mirakc.EventStarted
	case mirakc.RecordingStopped:
		return mirakc.EventStopped
	default:
		return mirakc.EventFailed
	}
}

// proxyHooks are main.ts's proxy hooks: manual UI schedules/removals feed
// the same program-event notifications (mirakc has no SSE event for them).
func proxyHooks(d Deps, apiURL string, client *http.Client) mirakc.ProxyHooks {
	return mirakc.ProxyHooks{
		OnScheduleCreated: func(schedule mirakc.CreatedSchedule) {
			notifyProgramEvent(d, apiURL, client, settings.EventOnSchedule, mirakc.EventScheduled, schedule.Program.ID, &schedule.Program)
		},
		OnScheduleRemoved: func(programID int64) {
			notifyProgramEvent(d, apiURL, client, settings.EventOnRemove, mirakc.EventUnscheduled, programID, nil)
		},
	}
}

func notifyProgramEvent(d Deps, apiURL string, client *http.Client, toggle settings.NotificationEventKey, key mirakc.ProgramEventKey, programID int64, program *mirakc.ProgramInfo) {
	s := loadSettings(d.Store)
	if !settings.HasEnabledDestination(s, toggle) {
		return
	}
	mirakc.NotifyProgramEvent(mirakc.NotifyDeps{
		APIURL:   apiURL,
		Client:   client,
		TimeZone: d.TimeZone,
		Notify: func(n settings.Notification) {
			notifyIfEnabled(d.Store, toggle, n)
		},
	}, key, programID, program)
}

func recordingToggleOf(kind mirakc.RecordingKind) settings.NotificationEventKey {
	switch kind {
	case mirakc.RecordingStarted:
		return settings.EventOnStart
	case mirakc.RecordingStopped:
		return settings.EventOnEnd
	default:
		return settings.EventOnFail
	}
}

// notifyIfEnabled is main.ts's notifyIfEnabled: read the settings fresh,
// gate on the event toggle, then send to every valid destination in
// parallel; any single success reports true.
func notifyIfEnabled(st *store.Store, key settings.NotificationEventKey, n settings.Notification) bool {
	s := loadSettings(st)
	if !settings.HasEnabledDestination(s, key) {
		return false
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]bool, 0, 2)
	send := func(ok bool) {
		mu.Lock()
		results = append(results, ok)
		mu.Unlock()
	}
	if settings.IsValidNtfyURL(s.URL) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			send(settings.SendNtfy(settings.NtfyTarget{URL: s.URL, Token: s.Token}, n))
		}()
	}
	if settings.IsValidDiscordWebhookURL(s.DiscordWebhookURL) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			send(settings.SendDiscord(settings.DiscordTarget{WebhookURL: s.DiscordWebhookURL}, n))
		}()
	}
	wg.Wait()
	for _, ok := range results {
		if ok {
			return true
		}
	}
	return false
}

// loadSettings reads the notification singleton with the same
// defaults-on-missing-or-invalid behavior the settings API has.
func loadSettings(st *store.Store) settings.NotificationSettings {
	raw, found, err := st.GetSingleton(store.ColNotification)
	if err != nil || !found {
		return settings.DefaultNotificationSettings
	}
	s, ok := settings.NormalizeNotificationSettings(raw)
	if !ok {
		return settings.DefaultNotificationSettings
	}
	return s
}

func programsUnavailable() ([]mirakc.SlimProgram, error) {
	return nil, errMirakcURLNotSet
}

var errMirakcURLNotSet = errors.New("MIRAKC_URL is not set")

// drain waits for the events loop to exit (bounded: the loop observes
// ctx cancellation through both its request and its retry sleep).
func drain(done <-chan struct{}) {
	deadline := time.After(2 * time.Second)
	select {
	case <-done:
	case <-deadline:
	}
}
