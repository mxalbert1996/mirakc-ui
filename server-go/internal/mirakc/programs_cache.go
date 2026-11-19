package mirakc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SlimGenre is MirakurunProgram.genre (ARIB genre), kept as-is in the slim
// form.
type SlimGenre struct {
	Lv1 int64 `json:"lv1"`
	Lv2 int64 `json:"lv2"`
	Un1 int64 `json:"un1"`
	Un2 int64 `json:"un2"`
}

// SlimProgram is the program subset the UI table and the keyword-recording
// job need: the heavy optional fields of MirakurunProgram (audio, audios,
// description, extended, relatedItems, series, video) are dropped, and the
// upstream response is decoded straight into it.
type SlimProgram struct {
	ID        int64       `json:"id"`
	EventID   int64       `json:"eventId"`
	IsFree    bool        `json:"isFree"`
	Name      *string     `json:"name"`
	StartAt   int64       `json:"startAt"`
	Duration  int64       `json:"duration"`
	NetworkID int64       `json:"networkId"`
	ServiceID int64       `json:"serviceId"`
	Genres    []SlimGenre `json:"genres"`
}

// DefaultProgramsCacheTTL is the fallback for a zero ttl: EPG-update SSE
// events can be missed, so the cache must also expire on its own.
const DefaultProgramsCacheTTL = 10 * time.Minute

type cacheEntry struct {
	programs  []SlimProgram
	fetchedAt time.Time
}

type inflightFetch struct {
	done       chan struct{}
	programs   []SlimProgram
	err        error
	generation uint64
}

// ProgramsCache caches the slim program list from GET /programs
// (server/lib/programs-cache.ts). Concurrent callers share one in-flight
// fetch, entries live for the TTL, and a fetch started before Invalidate
// never commits (generation guard) so stale data cannot re-enter the
// cache. A failed fetch leaves no entry; the next call retries.
type ProgramsCache struct {
	url    string
	client *http.Client
	ttl    time.Duration
	now    func() time.Time

	mu         sync.Mutex
	entry      *cacheEntry
	inflight   *inflightFetch
	generation uint64
}

// NewProgramsCache builds a cache over the mirakc Web API base URL (the
// APIURL output). Zero ttl/now fall back to 10 minutes / time.Now.
func NewProgramsCache(apiURL string, client *http.Client, ttl time.Duration, now func() time.Time) *ProgramsCache {
	if client == nil {
		client = http.DefaultClient
	}
	if ttl <= 0 {
		ttl = DefaultProgramsCacheTTL
	}
	if now == nil {
		now = time.Now
	}
	return &ProgramsCache{
		// Mirror the Deno single-trailing-slash trim before appending.
		url:    strings.TrimSuffix(apiURL, "/") + "/programs",
		client: client,
		ttl:    ttl,
		now:    now,
	}
}

// GetPrograms returns the cached slim list, fetching it when absent or
// expired. Concurrent callers share one fetch.
func (c *ProgramsCache) GetPrograms() ([]SlimProgram, error) {
	c.mu.Lock()
	if c.entry != nil && c.now().Sub(c.entry.fetchedAt) < c.ttl {
		programs := c.entry.programs
		c.mu.Unlock()
		return programs, nil
	}
	if c.inflight != nil && c.inflight.generation == c.generation {
		fl := c.inflight
		c.mu.Unlock()
		<-fl.done
		return fl.programs, fl.err
	}

	gen := c.generation
	fl := &inflightFetch{done: make(chan struct{}), generation: gen}
	c.inflight = fl
	c.mu.Unlock()

	programs, err := c.fetchPrograms()

	c.mu.Lock()
	if c.inflight != nil && c.inflight.generation == gen {
		c.inflight = nil
	}
	// Commit only when no Invalidate() happened during the fetch —
	// otherwise the result is served to this caller but not cached.
	if gen == c.generation {
		if err == nil {
			c.entry = &cacheEntry{programs: programs, fetchedAt: c.now()}
		} else {
			c.entry = nil
		}
	}
	fl.programs = programs
	fl.err = err
	close(fl.done)
	c.mu.Unlock()

	return programs, err
}

// Invalidate drops the cached entry; the next GetPrograms refetches.
func (c *ProgramsCache) Invalidate() {
	c.mu.Lock()
	c.entry = nil
	c.generation++
	c.mu.Unlock()
}

func (c *ProgramsCache) fetchPrograms() ([]SlimProgram, error) {
	resp, err := c.client.Get(c.url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("GET %s failed: %d", c.url, resp.StatusCode)
	}
	var programs []SlimProgram
	if err := json.NewDecoder(resp.Body).Decode(&programs); err != nil {
		return nil, err
	}
	return programs, nil
}
