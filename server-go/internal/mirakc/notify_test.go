package mirakc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/settings"
)

// 2026-01-01T18:04:00Z = 2026-01-02 03:04 in Asia/Tokyo.
var startAt = time.Date(2026, 1, 1, 18, 4, 0, 0, time.UTC).UnixMilli()

const airtime = "2026/01/02 03:04 〜 03:34"

type notificationRecorder struct {
	mu            sync.Mutex
	notifications []settings.Notification
}

func (rec *notificationRecorder) add(n settings.Notification) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.notifications = append(rec.notifications, n)
}

func (rec *notificationRecorder) get() []settings.Notification {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]settings.Notification(nil), rec.notifications...)
}

func int64Ptr(v int64) *int64 { return &v }
func strPtr(s string) *string { return &s }

// fakeMirakcAPI serves /api/programs/{id} and /api/services and records
// every requested path.
func fakeMirakcAPI(t *testing.T, programBody string, programOK bool, servicesStatus int, servicesBody string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	requested := []string{}
	record := func(path string) {
		mu.Lock()
		defer mu.Unlock()
		requested = append(requested, path)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/programs/", func(w http.ResponseWriter, r *http.Request) {
		record(r.URL.Path)
		if !programOK {
			http.Error(w, "ng", http.StatusNotFound)
			return
		}
		fmt.Fprint(w, programBody)
	})
	mux.HandleFunc("/api/services", func(w http.ResponseWriter, r *http.Request) {
		record(r.URL.Path)
		w.WriteHeader(servicesStatus)
		fmt.Fprint(w, servicesBody)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &requested
}

const programJSON = `{"id":123,"name":"ニュースウォッチ","startAt":` + "%d" + `,"duration":1800000,"networkId":32736,"serviceId":1024}`

const servicesJSON = `[{"id":1,"networkId":32736,"serviceId":1024,"name":"NHK総合"},{"id":2,"networkId":32737,"serviceId":1032,"name":"Eテレ"}]`

func withRecording(t *testing.T, programOK bool, servicesStatus int, fn func(deps NotifyDeps, rec *notificationRecorder)) {
	t.Helper()
	body := fmt.Sprintf(programJSON, startAt)
	srv, _ := fakeMirakcAPI(t, body, programOK, servicesStatus, servicesJSON)
	rec := &notificationRecorder{}
	deps := NotifyDeps{
		APIURL:   srv.URL + "/api",
		Client:   srv.Client(),
		TimeZone: "Asia/Tokyo",
		Notify:   rec.add,
	}
	fn(deps, rec)
}

func TestNotifyProgramEventFetchedProgramWithService(t *testing.T) {
	withRecording(t, true, http.StatusOK, func(deps NotifyDeps, rec *notificationRecorder) {
		NotifyProgramEvent(deps, EventStarted, 123, nil)
		NotifyProgramEvent(deps, EventStopped, 123, nil)

		notifications := rec.get()
		if len(notifications) != 2 {
			t.Fatalf("notifications = %d, want 2", len(notifications))
		}
		wantTitle := []string{"録画開始: ニュースウォッチ", "録画終了: ニュースウォッチ"}
		wantTag := []string{"arrow_forward", "white_check_mark"}
		wantMessage := []string{
			"録画を開始しました。\nNHK総合\n" + airtime,
			"録画を終了しました。\nNHK総合\n" + airtime,
		}
		for i, n := range notifications {
			if n.Title != wantTitle[i] {
				t.Errorf("title[%d] = %q, want %q", i, n.Title, wantTitle[i])
			}
			if n.Message != wantMessage[i] {
				t.Errorf("message[%d] = %q, want %q", i, n.Message, wantMessage[i])
			}
			if len(n.Tags) != 1 || n.Tags[0] != wantTag[i] {
				t.Errorf("tags[%d] = %v, want [%s]", i, n.Tags, wantTag[i])
			}
			if n.Color == nil || *n.Color != 0x57f287 {
				t.Errorf("color[%d] = %v, want 57f287", i, n.Color)
			}
		}
	})
}

func TestNotifyProgramEventServiceFetchFailureDropsChannelLine(t *testing.T) {
	withRecording(t, true, http.StatusInternalServerError, func(deps NotifyDeps, rec *notificationRecorder) {
		NotifyProgramEvent(deps, EventStarted, 123, nil)

		notifications := rec.get()
		if len(notifications) != 1 {
			t.Fatalf("notifications = %d, want 1", len(notifications))
		}
		want := "録画を開始しました。\n" + airtime
		if notifications[0].Message != want {
			t.Errorf("message = %q, want %q", notifications[0].Message, want)
		}
	})
}

func TestNotifyProgramEventProvidedProgramSkipsFetch(t *testing.T) {
	srv, requested := fakeMirakcAPI(t, "", false, http.StatusOK, servicesJSON)
	rec := &notificationRecorder{}
	deps := NotifyDeps{
		APIURL:   srv.URL + "/api",
		Client:   srv.Client(),
		TimeZone: "Asia/Tokyo",
		Notify:   rec.add,
	}
	program := &ProgramInfo{
		Name:      strPtr("ニュースウォッチ"),
		StartAt:   int64Ptr(startAt),
		Duration:  int64Ptr(30 * 60 * 1000),
		NetworkID: int64Ptr(32736),
		ServiceID: int64Ptr(1024),
	}
	NotifyProgramEvent(deps, EventScheduled, 123, program)

	notifications := rec.get()
	if len(notifications) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifications))
	}
	if notifications[0].Title != "録画登録: ニュースウォッチ" {
		t.Errorf("title = %q", notifications[0].Title)
	}
	want := "録画予約を登録しました。\nNHK総合\n" + airtime
	if notifications[0].Message != want {
		t.Errorf("message = %q, want %q", notifications[0].Message, want)
	}
	if notifications[0].Tags[0] != "calendar" {
		t.Errorf("tags = %v, want calendar", notifications[0].Tags)
	}
	if notifications[0].Color == nil || *notifications[0].Color != 0x5865f2 {
		t.Errorf("color = %v, want 5865f2", notifications[0].Color)
	}
	for _, path := range *requested {
		if len(path) > 0 && path[:9] == "/api/prog" {
			t.Errorf("program was provided but %s was fetched", path)
		}
	}
}

func TestNotifyProgramEventMissingProgramStillNotifies(t *testing.T) {
	srv, _ := fakeMirakcAPI(t, "", false, http.StatusNotFound, "")
	rec := &notificationRecorder{}
	deps := NotifyDeps{
		APIURL: srv.URL + "/api",
		Client: srv.Client(),
		Notify: rec.add,
	}

	NotifyProgramEvent(deps, EventFailed, 1, nil)
	NotifyProgramEvent(deps, EventUnscheduled, 1, nil)

	notifications := rec.get()
	if len(notifications) != 2 {
		t.Fatalf("notifications = %d, want 2", len(notifications))
	}
	if notifications[0].Title != "録画失敗: 番組 ID: 1" {
		t.Errorf("title = %q, want the fallback name", notifications[0].Title)
	}
	if notifications[0].Message != "録画に失敗しました。" {
		t.Errorf("message = %q", notifications[0].Message)
	}
	if notifications[0].Tags[0] != "x" {
		t.Errorf("tags = %v", notifications[0].Tags)
	}
	if notifications[0].Color == nil || *notifications[0].Color != 0xed4245 {
		t.Errorf("color = %v, want ed4245", notifications[0].Color)
	}
	if notifications[1].Title != "録画削除: 番組 ID: 1" {
		t.Errorf("title = %q", notifications[1].Title)
	}
	if notifications[1].Message != "録画予約を削除しました。" {
		t.Errorf("message = %q", notifications[1].Message)
	}
	if notifications[1].Tags[0] != "wastebasket" {
		t.Errorf("tags = %v", notifications[1].Tags)
	}
	if notifications[1].Color == nil || *notifications[1].Color != 0x99aab5 {
		t.Errorf("color = %v, want 99aab5", notifications[1].Color)
	}
}

func TestNotifyProgramEventStartedAtOnlyAirtime(t *testing.T) {
	srv, _ := fakeMirakcAPI(t, "", false, http.StatusOK, servicesJSON)
	rec := &notificationRecorder{}
	deps := NotifyDeps{
		APIURL:   srv.URL + "/api",
		Client:   srv.Client(),
		TimeZone: "Asia/Tokyo",
		Notify:   rec.add,
	}
	// Duration missing: the airtime line is the startedAt-only template.
	program := &ProgramInfo{
		Name:      strPtr("ニュースウォッチ"),
		StartAt:   int64Ptr(startAt),
		NetworkID: int64Ptr(32736),
		ServiceID: int64Ptr(1024),
	}
	NotifyProgramEvent(deps, EventStarted, 123, program)

	notifications := rec.get()
	if len(notifications) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifications))
	}
	want := "録画を開始しました。\nNHK総合\n2026/01/02 03:04 開始"
	if notifications[0].Message != want {
		t.Errorf("message = %q, want %q", notifications[0].Message, want)
	}
}

func TestNotifyProgramEventProgramWithoutServiceIDs(t *testing.T) {
	srv, requested := fakeMirakcAPI(t, "", false, http.StatusOK, servicesJSON)
	rec := &notificationRecorder{}
	deps := NotifyDeps{
		APIURL:   srv.URL + "/api",
		Client:   srv.Client(),
		TimeZone: "Asia/Tokyo",
		Notify:   rec.add,
	}
	program := &ProgramInfo{Name: strPtr("ニュースウォッチ"), StartAt: int64Ptr(startAt), Duration: int64Ptr(1800000)}
	NotifyProgramEvent(deps, EventStarted, 123, program)

	// No networkId/serviceId: /services is not fetched and the channel line
	// is dropped.
	for _, path := range *requested {
		if path == "/api/services" {
			t.Error("services fetched although the program has no service ids")
		}
	}
	notifications := rec.get()
	want := "録画を開始しました。\n" + airtime
	if len(notifications) != 1 || notifications[0].Message != want {
		t.Errorf("message = %q, want %q", notifications[0].Message, want)
	}
}

func TestNotifyProgramEventAPIURLTrailingSlashTrimmed(t *testing.T) {
	srv, _ := fakeMirakcAPI(t, "", false, http.StatusNotFound, "")
	rec := &notificationRecorder{}
	deps := NotifyDeps{
		APIURL: srv.URL + "/api/",
		Client: srv.Client(),
		Notify: rec.add,
	}
	NotifyProgramEvent(deps, EventStarted, 456, nil)

	notifications := rec.get()
	if len(notifications) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifications))
	}
	if notifications[0].Title != "録画開始: 番組 ID: 456" {
		t.Errorf("title = %q, want the fallback name with id 456", notifications[0].Title)
	}
}
