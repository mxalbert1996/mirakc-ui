// Package mirakc is the Phase-2 port of the Deno mirakc integration:
// the URL builders (server/lib/mirakc.ts), the generic proxy
// (server/routes/mirakc.ts), the slim programs cache
// (server/lib/programs-cache.ts), the programs API
// (server/routes/programs.ts), and the SSE subscription + recording
// notifications (server/lib/mirakc-events.ts).
package mirakc

import "strings"

// trimTrailingSlash drops every trailing slash, mirroring the
// `/\/+$/` replace in server/lib/mirakc.ts.
func trimTrailingSlash(rawURL string) string {
	return strings.TrimRight(rawURL, "/")
}

// APIURL builds the mirakc Web API base URL (`${MIRAKC_URL}/api`).
func APIURL(mirakcURL string) string {
	return trimTrailingSlash(mirakcURL) + "/api"
}

// EventsURL builds the mirakc SSE endpoint (`${MIRAKC_URL}/events`).
// The SSE endpoint is mounted at the root, not under /api, so it cannot be
// derived from the API URL alone — both come from the same base URL.
func EventsURL(mirakcURL string) string {
	return trimTrailingSlash(mirakcURL) + "/events"
}
