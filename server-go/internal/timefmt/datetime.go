// Package timefmt is the port of server/lib/datetime.ts: date-time
// formatting for notification text and recording file names. The Deno
// version formats with Temporal in a named timezone; this port uses
// time.LoadLocation, and additionally accepts Temporal-style fixed offset
// zone IDs ("+09:00", "-0530") which time.LoadLocation rejects. An
// unresolvable zone falls back to UTC where the Deno version would throw;
// callers only pass settings-derived zones, so the difference never
// surfaces in practice.
package timefmt

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func loadZone(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	if loc, ok := fixedOffsetZone(tz); ok {
		return loc
	}
	return time.UTC
}

// fixedOffsetZone parses a Temporal-style fixed offset zone ID ("+09:00",
// "-0530", "+09"). Returns false when tz is not such an ID.
func fixedOffsetZone(tz string) (*time.Location, bool) {
	if len(tz) < 3 || (tz[0] != '+' && tz[0] != '-') {
		return nil, false
	}
	body := tz[1:]
	if i := strings.IndexByte(body, ':'); i >= 0 {
		body = body[:i] + body[i+1:]
	}
	var hourStr, minuteStr string
	switch len(body) {
	case 2:
		hourStr, minuteStr = body, "00"
	case 4:
		hourStr, minuteStr = body[:2], body[2:]
	default:
		return nil, false
	}
	hour, err := strconv.Atoi(hourStr)
	if err != nil || hour > 23 {
		return nil, false
	}
	minute, err := strconv.Atoi(minuteStr)
	if err != nil || minute > 59 {
		return nil, false
	}
	offset := hour*3600 + minute*60
	if tz[0] == '-' {
		offset = -offset
	}
	return time.FixedZone(tz, offset), true
}

func zoned(epochMs int64, tz string) time.Time {
	return time.UnixMilli(epochMs).In(loadZone(tz))
}

// FormatYmdHms returns the YYYYMMDDhhmmss used for recording file names,
// matching client/lib/datetime.ts formatYmdHms so manual reservations and
// file names stay aligned.
func FormatYmdHms(epochMs int64, tz string) string {
	z := zoned(epochMs, tz)
	return fmt.Sprintf("%d%02d%02d%02d%02d%02d",
		z.Year(), int(z.Month()), z.Day(), z.Hour(), z.Minute(), z.Second())
}

// FormatDisplayDateTime returns the human-readable YYYY/MM/DD hh:mm used
// in notification text.
func FormatDisplayDateTime(epochMs int64, tz string) string {
	z := zoned(epochMs, tz)
	return fmt.Sprintf("%d/%02d/%02d %02d:%02d",
		z.Year(), int(z.Month()), z.Day(), z.Hour(), z.Minute())
}

// FormatDisplayTime returns the hh:mm used in notification text when the
// date is clear from context, e.g. the end side of an airtime.
func FormatDisplayTime(epochMs int64, tz string) string {
	z := zoned(epochMs, tz)
	return fmt.Sprintf("%02d:%02d", z.Hour(), z.Minute())
}
