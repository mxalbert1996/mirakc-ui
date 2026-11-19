// Package httpapi hosts the opt-in HTTP authentication (AUTH_TOKEN) and the
// one-time stream tickets, ported from server/lib/auth.ts. Auth.Middleware
// must wrap every route (API, streams, static/SPA) before the router.
package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ticketTTL     = 60 * time.Second
	ticketMaxUses = 5
	ticketEntropy = 16 // bytes; 128-bit
)

// Paths where ?ticket= is accepted (stream GETs only — EventSource and
// mpegts.js cannot set request headers).
var ticketPathPrefixes = []string{"/api/comments/", "/api/transcode/"}

// Warn exactly once per process when AUTH_TOKEN exists but is blank.
var (
	blankTokenWarned atomic.Bool
	warnf            = func(msg string) { log.Print(msg) }
)

type ticketEntry struct {
	expiresAt time.Time
	usesLeft  int
}

// Auth is the HTTP authentication layer. An empty or whitespace-only token
// disables authentication (fail-open; reverse proxies handle auth upstream).
type Auth struct {
	enabled     bool
	tokenDigest [sha256.Size]byte
	now         func() time.Time

	// tickets maps hex(SHA-256(ticket)) to its state; the plaintext ticket is
	// never stored or byte-compared. Deno relied on the single-threaded event
	// loop for an atomic consume; here every access is one mutex-guarded
	// critical section.
	mu      sync.Mutex
	tickets map[string]ticketEntry
}

// Option customizes Auth construction.
type Option func(*Auth)

// WithNow overrides the clock used for ticket TTL bookkeeping (tests).
func WithNow(now func() time.Time) Option {
	return func(a *Auth) { a.now = now }
}

// New creates Auth from a token value. Empty and whitespace-only tokens
// disable authentication (deliberate fail-open: a misconfigured AUTH_TOKEN
// must not 401 every request). Whitespace only feeds the blank check — the
// token itself is matched verbatim, as in the Deno implementation.
func New(token string, opts ...Option) *Auth {
	a := &Auth{
		enabled: strings.TrimSpace(token) != "",
		now:     time.Now,
		tickets: make(map[string]ticketEntry),
	}
	a.tokenDigest = sha256.Sum256([]byte(token))
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// FromEnv creates Auth from AUTH_TOKEN. Unset → disabled. Blank/whitespace
// only → also disabled, with a one-time process-wide warning.
func FromEnv(opts ...Option) *Auth {
	raw, ok := os.LookupEnv("AUTH_TOKEN")
	if ok && strings.TrimSpace(raw) == "" && blankTokenWarned.CompareAndSwap(false, true) {
		warnf("[auth] AUTH_TOKEN is blank; authentication disabled (fail-open)")
	}
	return New(raw, opts...)
}

// Middleware authenticates every route (API, streams, static/SPA). It runs
// before routing, so the path checks below see the raw request path. Header
// credentials win first; a one-time ticket is only accepted as a fallback on
// stream GETs.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.enabled {
			next.ServeHTTP(w, r)
			return
		}
		if a.headerCredentialOk(r) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && isStreamPath(r.URL.Path) {
			if ticket := r.URL.Query().Get("ticket"); ticket != "" && a.consumeTicket(ticket) {
				next.ServeHTTP(w, r)
				return
			}
		}
		// /api/* answers JSON; everything else gets a plain-text 401 with a
		// Basic challenge so browsers show their native login dialog.
		if strings.HasPrefix(r.URL.Path, "/api") {
			writeJSON(w, http.StatusUnauthorized, errorBody{Error: "unauthorized"})
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
		w.Header().Set("WWW-Authenticate", `Basic realm="mirakc-ui", charset="UTF-8"`)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	})
}

// TicketHandler serves POST /api/auth/ticket. Method routing is the wiring's
// responsibility. With auth disabled it always returns {"ticket":null} so
// clients can probe unconditionally; with auth enabled the middleware has
// already verified header credentials (tickets are not accepted on this
// route, so a ticket cannot mint another ticket).
func (a *Auth) TicketHandler(w http.ResponseWriter, r *http.Request) {
	var ticket *string
	if a.enabled {
		t := a.issueTicket()
		ticket = &t
	}
	writeJSON(w, http.StatusOK, ticketBody{Ticket: ticket})
}

type ticketBody struct {
	Ticket *string `json:"ticket"`
}

type errorBody struct {
	Error string `json:"error"`
}

func isStreamPath(path string) bool {
	for _, prefix := range ticketPathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// matchesSecret compares the SHA-256 digest of the candidate against the
// configured token's digest in constant time.
func (a *Auth) matchesSecret(candidate string) bool {
	if !a.enabled {
		return false
	}
	digest := sha256.Sum256([]byte(candidate))
	return timingSafeEqual(a.tokenDigest[:], digest[:])
}

// timingSafeEqual is a constant-time comparison. Length differences are
// folded into the accumulator instead of returning early; operands are in
// practice always equal-length digests, but we keep the Deno behavior of no
// early length bail.
func timingSafeEqual(a, b []byte) bool {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	diff := byte(len(a) ^ len(b))
	for i := 0; i < n; i++ {
		var x, y byte
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		diff |= x ^ y
	}
	return subtle.ConstantTimeByteEq(diff, 0) == 1
}

func (a *Auth) headerCredentialOk(r *http.Request) bool {
	if authorization := r.Header.Get("Authorization"); authorization != "" {
		scheme, value := authorization, ""
		if space := strings.IndexByte(authorization, ' '); space >= 0 {
			scheme, value = authorization[:space], authorization[space+1:]
		}
		switch strings.ToLower(scheme) {
		case "bearer":
			if value != "" && a.matchesSecret(value) {
				return true
			}
		case "basic":
			decoded, err := base64.StdEncoding.DecodeString(value)
			if err == nil {
				// Only the password (after the first ':') is checked; the
				// username is arbitrary. Deliberate leniency (pinned by
				// test): a decoded string without a colon is treated as the
				// password itself.
				password := string(decoded)
				if colon := strings.IndexByte(password, ':'); colon >= 0 {
					password = password[colon+1:]
				}
				if a.matchesSecret(password) {
					return true
				}
			}
		}
	}
	if xToken := r.Header.Get("X-Auth-Token"); xToken != "" && a.matchesSecret(xToken) {
		return true
	}
	return false
}

func ticketKey(ticket string) string {
	digest := sha256.Sum256([]byte(ticket))
	return hex.EncodeToString(digest[:])
}

func (a *Auth) issueTicket() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	a.pruneTicketsLocked(now)
	var raw [ticketEntropy]byte
	// crypto/rand cannot fail meaningfully; a panic is the right response.
	if _, err := rand.Read(raw[:]); err != nil {
		panic("httpapi: crypto/rand failed: " + err.Error())
	}
	ticket := base64.RawURLEncoding.EncodeToString(raw[:])
	a.tickets[ticketKey(ticket)] = ticketEntry{
		expiresAt: now.Add(ticketTTL),
		usesLeft:  ticketMaxUses,
	}
	return ticket
}

func (a *Auth) consumeTicket(candidate string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	a.pruneTicketsLocked(now)
	key := ticketKey(candidate)
	entry, ok := a.tickets[key]
	if !ok || !entry.expiresAt.After(now) {
		return false
	}
	entry.usesLeft--
	if entry.usesLeft <= 0 {
		delete(a.tickets, key)
	} else {
		a.tickets[key] = entry
	}
	return true
}

func (a *Auth) pruneTicketsLocked(now time.Time) {
	for key, entry := range a.tickets {
		if !entry.expiresAt.After(now) {
			delete(a.tickets, key)
		}
	}
}
