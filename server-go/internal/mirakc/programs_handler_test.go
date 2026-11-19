package mirakc

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

var errGetProgramsBoom = errors.New("fetch failed to mirakc")

const (
	from = int64(1_000_000)
	to   = int64(2_000_000)
)

func slimForTest(id int64, startAt int64, duration int64) SlimProgram {
	return SlimProgram{
		ID:       id,
		EventID:  100,
		IsFree:   true,
		StartAt:  startAt,
		Duration: duration,
	}
}

func getProgramsList(t *testing.T, handler http.Handler, target string) (int, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + target)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestProgramsHandlerNoWindowReturnsAll(t *testing.T) {
	programs := []SlimProgram{slimForTest(1, 1_500_000, 100_000), slimForTest(2, 1_500_000, 100_000)}
	status, body := getProgramsList(t, ProgramsHandler(func() ([]SlimProgram, error) { return programs, nil }), "/")
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	want := `[{"id":1,"eventId":100,"isFree":true,"name":null,"startAt":1500000,"duration":100000,"networkId":0,"serviceId":0,"genres":null},{"id":2,"eventId":100,"isFree":true,"name":null,"startAt":1500000,"duration":100000,"networkId":0,"serviceId":0,"genres":null}]`
	if body != want {
		t.Errorf("body = %s, want %s", body, want)
	}
}

func TestProgramsHandlerFromToBothOrNeither(t *testing.T) {
	handler := ProgramsHandler(func() ([]SlimProgram, error) { return nil, nil })

	status, body := getProgramsList(t, handler, "/?from=1000")
	if status != http.StatusBadRequest {
		t.Errorf("only from: status = %d, want 400", status)
	}
	if body != `{"error":"from and to must be specified together"}` {
		t.Errorf("only from: body = %s, want the together error", body)
	}

	status, _ = getProgramsList(t, handler, "/?to=2000")
	if status != http.StatusBadRequest {
		t.Errorf("only to: status = %d, want 400", status)
	}
}

func TestProgramsHandlerFromToMustBeIntegers(t *testing.T) {
	handler := ProgramsHandler(func() ([]SlimProgram, error) { return nil, nil })
	for _, target := range []string{
		"/?from=abc&to=1",
		"/?from=1.5&to=2",
		"/?from=1e3&to=2",
		"/?from=&to=1",
		"/?from=+1&to=2",
		"/?from=1&to=abc",
	} {
		status, body := getProgramsList(t, handler, target)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, status)
		}
		if body != `{"error":"from and to must be integers"}` {
			t.Errorf("%s: body = %s, want the integers error", target, body)
		}
	}
}

func TestProgramsHandlerNegativeIntegersAccepted(t *testing.T) {
	status, _ := getProgramsList(t, ProgramsHandler(func() ([]SlimProgram, error) { return nil, nil }), "/?from=-123&to=-1")
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
}

func TestProgramsHandlerWindowFilter(t *testing.T) {
	programs := []SlimProgram{
		slimForTest(1, from+100, 10),          // inside
		slimForTest(2, to, 10),                // startAt === to: strict < excludes
		slimForTest(3, from-100, 100),         // startAt + duration === from: strict > excludes
		slimForTest(4, from-1_000, 500),       // entirely before
		slimForTest(5, from-100, to-from+200), // spans the whole window
	}
	handler := ProgramsHandler(func() ([]SlimProgram, error) { return programs, nil })

	status, body := getProgramsList(t, handler, "/?from=1000000&to=2000000")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	want := `[{"id":1,"eventId":100,"isFree":true,"name":null,"startAt":1000100,"duration":10,"networkId":0,"serviceId":0,"genres":null},{"id":5,"eventId":100,"isFree":true,"name":null,"startAt":999900,"duration":1000200,"networkId":0,"serviceId":0,"genres":null}]`
	if body != want {
		t.Errorf("body = %s, want ids 1 and 5 only", body)
	}
}

func TestProgramsHandlerGetProgramsFailureReturnsConvention500(t *testing.T) {
	handler := ProgramsHandler(func() ([]SlimProgram, error) { return nil, errGetProgramsBoom })

	status, body := getProgramsList(t, handler, "/")
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", status)
	}
	if body != `{"error":"MIRAKC_URL is not set"}` {
		t.Errorf("body = %s, want the convention 500 JSON", body)
	}

	// A non-MIRAKC_URL cause still answers with the same convention.
	status, body = getProgramsList(t, handler, "/?from=1&to=2")
	if status != http.StatusInternalServerError || body != `{"error":"MIRAKC_URL is not set"}` {
		t.Errorf("window query: status=%d body=%s, want the convention 500 JSON", status, body)
	}
}

func TestProgramsHandlerRejectsNonGet(t *testing.T) {
	srv := httptest.NewServer(ProgramsHandler(func() ([]SlimProgram, error) { return nil, nil }))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
