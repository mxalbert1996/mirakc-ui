package mirakc

import (
	"log"
	"net/http"
	"strconv"
)

// ProgramsHandler serves GET /api/programs (createProgramsRoutes in
// server/routes/programs.ts). Without query parameters it returns the
// full cached slim list; with from/to (epoch-ms integers, both or
// neither) it returns only programs overlapping that window, using the
// same strict comparison as the client's table:
// p.startAt < to && p.startAt + p.duration > from.
func ProgramsHandler(getPrograms func() ([]SlimProgram, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		fromRaw, fromPresent := q["from"]
		toRaw, toPresent := q["to"]
		if fromPresent != toPresent {
			writeJSONError(w, http.StatusBadRequest, "from and to must be specified together")
			return
		}

		var from, to int64
		hasWindow := false
		if fromPresent && toPresent {
			var fromOK, toOK bool
			from, fromOK = parseEpochMs(fromRaw[0])
			to, toOK = parseEpochMs(toRaw[0])
			if !fromOK || !toOK {
				writeJSONError(w, http.StatusBadRequest, "from and to must be integers")
				return
			}
			hasWindow = true
		}

		programs, err := getPrograms()
		if err != nil {
			// Same convention 500 JSON as the mirakc proxy, whatever the
			// real cause; log the cause for the operator.
			log.Printf("[programs-api] getPrograms failed: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "MIRAKC_URL is not set")
			return
		}

		if !hasWindow {
			writeJSON(w, http.StatusOK, programs)
			return
		}
		overlapping := make([]SlimProgram, 0, len(programs))
		for _, p := range programs {
			if p.StartAt < to && p.StartAt+p.Duration > from {
				overlapping = append(overlapping, p)
			}
		}
		writeJSON(w, http.StatusOK, overlapping)
	})
}

// parseEpochMs accepts only an integer literal ("-123" ok; "", "1.5",
// "1e3", "abc" not), mirroring the /^-?\d+$/ check in programs.ts.
func parseEpochMs(value string) (int64, bool) {
	if value == "" {
		return 0, false
	}
	for i := 0; i < len(value); i++ {
		if value[i] == '-' {
			if i != 0 {
				return 0, false
			}
			continue
		}
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
