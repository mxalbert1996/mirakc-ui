package mirakc

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/i18n"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/settings"
	"github.com/mxalbert1996/mirakc-ui/server-go/internal/timefmt"
)

// ProgramEventKey identifies which program event a notification describes:
// the SSE recording events plus the proxy-detected schedule add/remove.
type ProgramEventKey string

const (
	EventStarted     ProgramEventKey = "started"
	EventStopped     ProgramEventKey = "stopped"
	EventFailed      ProgramEventKey = "failed"
	EventScheduled   ProgramEventKey = "scheduled"
	EventUnscheduled ProgramEventKey = "unscheduled"
)

// EventTags are ntfy tags (emoji shortcodes): metadata, not display text.
var EventTags = map[ProgramEventKey]string{
	EventStarted:     "arrow_forward",
	EventStopped:     "white_check_mark",
	EventFailed:      "x",
	EventScheduled:   "calendar",
	EventUnscheduled: "wastebasket",
}

// EventColors are Discord embed colors (decimal): green = start/stop,
// red = failure, blue = schedule add, gray = schedule remove.
var EventColors = map[ProgramEventKey]int{
	EventStarted:     0x57f287,
	EventStopped:     0x57f287,
	EventFailed:      0xed4245,
	EventScheduled:   0x5865f2,
	EventUnscheduled: 0x99aab5,
}

// NotifyDeps are NotifyProgramEvent's collaborators. Notify is the
// caller's fan-out (ntfy/Discord publishing) — this package only builds
// the notification and hands it over.
type NotifyDeps struct {
	APIURL   string
	Client   *http.Client
	TimeZone string
	Notify   func(settings.Notification)
}

// NotifyProgramEvent sends the notification for one program event
// (notifyProgramEvent in server/lib/mirakc-events.ts). The program comes
// from the argument when provided, else GET /programs/{id}; the service
// name resolves via GET /services. A program that cannot be fetched still
// notifies — avoiding missed recordings outranks completeness.
func NotifyProgramEvent(deps NotifyDeps, key ProgramEventKey, programID int64, program *ProgramInfo) {
	client := deps.Client
	if client == nil {
		client = http.DefaultClient
	}
	apiURL := strings.TrimSuffix(deps.APIURL, "/")

	if program == nil {
		if fetched, ok := fetchProgram(client, apiURL, programID); ok {
			program = fetched
		}
	}

	name := i18n.T("notification.recording.fallbackName", map[string]string{
		"programId": strconv.FormatInt(programID, 10),
	})
	if program != nil && program.Name != nil {
		name = *program.Name
	}

	var serviceName, airtime *string
	if program != nil {
		serviceName = resolveServiceName(client, apiURL, program)
		airtime = airtimeOf(program, deps.TimeZone)
	}

	lines := []string{i18n.T("notification.recording."+string(key)+".message", nil)}
	if serviceName != nil {
		lines = append(lines, *serviceName)
	}
	if airtime != nil {
		lines = append(lines, *airtime)
	}

	color := EventColors[key]
	deps.Notify(settings.Notification{
		Title: i18n.T("notification.recording."+string(key)+".title", map[string]string{
			"name": name,
		}),
		Message: strings.Join(lines, "\n"),
		Tags:    []string{EventTags[key]},
		Color:   &color,
	})
}

func fetchProgram(client *http.Client, apiURL string, programID int64) (*ProgramInfo, bool) {
	res, err := client.Get(apiURL + "/programs/" + strconv.FormatInt(programID, 10))
	if err != nil {
		log.Printf("[mirakc-events] failed to fetch program: %v", err)
		return nil, false
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, false
	}
	var program ProgramInfo
	if err := json.NewDecoder(res.Body).Decode(&program); err != nil {
		log.Printf("[mirakc-events] failed to decode program: %v", err)
		return nil, false
	}
	return &program, true
}

// resolveServiceName finds the program's service name via GET /services;
// any failure yields nil.
func resolveServiceName(client *http.Client, apiURL string, program *ProgramInfo) *string {
	if program.NetworkID == nil || program.ServiceID == nil {
		return nil
	}
	res, err := client.Get(apiURL + "/services")
	if err != nil {
		log.Printf("[mirakc-events] failed to fetch services: %v", err)
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil
	}
	var services []struct {
		NetworkID int64  `json:"networkId"`
		ServiceID int64  `json:"serviceId"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(res.Body).Decode(&services); err != nil {
		log.Printf("[mirakc-events] failed to decode services: %v", err)
		return nil
	}
	for _, service := range services {
		if service.NetworkID == *program.NetworkID && service.ServiceID == *program.ServiceID {
			name := service.Name
			return &name
		}
	}
	return nil
}

// airtimeOf renders the airtime line; nil when the program has no startAt.
// A startAt without duration uses the startedAt-only template.
func airtimeOf(program *ProgramInfo, timeZone string) *string {
	if program.StartAt == nil {
		return nil
	}
	if program.Duration == nil {
		startedAt := i18n.T("notification.recording.startedAt", map[string]string{
			"datetime": timefmt.FormatDisplayDateTime(*program.StartAt, timeZone),
		})
		return &startedAt
	}
	airtime := i18n.T("notification.recording.airtime", map[string]string{
		"start": timefmt.FormatDisplayDateTime(*program.StartAt, timeZone),
		"end":   timefmt.FormatDisplayTime(*program.StartAt+*program.Duration, timeZone),
	})
	return &airtime
}
