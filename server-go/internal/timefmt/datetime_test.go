package timefmt

// The production tzdata import lives in cmd/mirakc-ui; tests run in their
// own binary, so embed the database here too.
import (
	"testing"
	"time"
	_ "time/tzdata"
)

// Date.UTC(2026, 0, 1, 18, 4, 5) == 2026-01-02T03:04:05+09:00.
var epochMs = time.Date(2026, 1, 1, 18, 4, 5, 0, time.UTC).UnixMilli()

func TestFormatYmdHms(t *testing.T) {
	if got := FormatYmdHms(epochMs, "Asia/Tokyo"); got != "20260102030405" {
		t.Errorf("FormatYmdHms = %q, want %q", got, "20260102030405")
	}
}

func TestFormatDisplayDateTime(t *testing.T) {
	if got := FormatDisplayDateTime(epochMs, "Asia/Tokyo"); got != "2026/01/02 03:04" {
		t.Errorf("FormatDisplayDateTime = %q, want %q", got, "2026/01/02 03:04")
	}
}

func TestFormatDisplayTime(t *testing.T) {
	if got := FormatDisplayTime(epochMs, "Asia/Tokyo"); got != "03:04" {
		t.Errorf("FormatDisplayTime = %q, want %q", got, "03:04")
	}
}

func TestFixedOffsetZones(t *testing.T) {
	// The instant in a -05:00 offset is 2026-01-01T13:04:05.
	if got := FormatYmdHms(epochMs, "-05:00"); got != "20260101130405" {
		t.Errorf("FormatYmdHms(-05:00) = %q, want %q", got, "20260101130405")
	}
	if got := FormatDisplayDateTime(epochMs, "-05:00"); got != "2026/01/01 13:04" {
		t.Errorf("FormatDisplayDateTime(-05:00) = %q, want %q", got, "2026/01/01 13:04")
	}
	if got := FormatDisplayTime(epochMs, "-05:00"); got != "13:04" {
		t.Errorf("FormatDisplayTime(-05:00) = %q, want %q", got, "13:04")
	}
	// The hhmm form is a distinct zone: -0530 means -05:30.
	if got := FormatYmdHms(epochMs, "-0530"); got == FormatYmdHms(epochMs, "-05:00") {
		t.Errorf("FormatYmdHms(-0530) should differ from -05:00 at this instant")
	}
	if got, want := FormatYmdHms(epochMs, "+09:00"), FormatYmdHms(epochMs, "+09"); got != want {
		t.Errorf("FormatYmdHms(+09:00) = %q, want same as +09 (%q)", got, want)
	}
}

func TestOtherNamedZone(t *testing.T) {
	// 2026-01-01T18:04:05Z is 13:04 in New York (EST, UTC-5).
	if got := FormatDisplayDateTime(epochMs, "America/New_York"); got != "2026/01/01 13:04" {
		t.Errorf("FormatDisplayDateTime(America/New_York) = %q, want %q", got, "2026/01/01 13:04")
	}
}

func TestUnresolvableZoneFallsBackToUTC(t *testing.T) {
	if got := FormatDisplayDateTime(epochMs, "Not/AZone"); got != "2026/01/01 18:04" {
		t.Errorf("FormatDisplayDateTime(Not/AZone) = %q, want UTC fallback %q", got, "2026/01/01 18:04")
	}
}

func TestIndependentOfProcessTZ(t *testing.T) {
	// The zone argument decides the output; the process TZ must not leak in.
	t.Setenv("TZ", "America/New_York")
	if got := FormatYmdHms(epochMs, "Asia/Tokyo"); got != "20260102030405" {
		t.Errorf("FormatYmdHms with TZ=America/New_York = %q, want %q", got, "20260102030405")
	}
	if got := FormatDisplayTime(epochMs, "Asia/Tokyo"); got != "03:04" {
		t.Errorf("FormatDisplayTime with TZ=America/New_York = %q, want %q", got, "03:04")
	}
}
