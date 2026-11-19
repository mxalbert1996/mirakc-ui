// String format validators and the strict RFC 3339 parser.
//
// validDateTimeFormat and validUUIDFormat replicate @cfworker/json-schema's
// format checks exactly (including its quirks: leap seconds at 23:59:60 are
// accepted, timezone offsets are not range-checked). parseRFC3339Instant is
// the second, stricter gate mirroring Deno's Temporal.Instant.from — values
// that pass the schema format but fail this parser produce the
// "<key> must be an RFC 3339 date-time" errors.
package settings

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var uuidFormat = regexp.MustCompile(`^(?:urn:uuid:)?[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validUUIDFormat(s string) bool {
	return uuidFormat.MatchString(s)
}

// validDateTimeFormat mirrors @cfworker's "date-time": a full date, then 't'
// or whitespace, then a time with a mandatory timezone.
func validDateTimeFormat(s string) bool {
	parts := splitOnDateTimeSeparator(s)
	if len(parts) != 2 {
		return false
	}
	return validDateParts(parts[0]) && validTimeParts(parts[1], true)
}

// splitOnDateTimeSeparator behaves like JS s.split(/t|\s/i).
func splitOnDateTimeSeparator(s string) []string {
	parts := []string{}
	cur := strings.Builder{}
	for _, r := range s {
		if r == 't' || r == 'T' || isJSSpace(r) {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	return append(parts, cur.String())
}

// isJSSpace reports whether r is in the JavaScript \s character class.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ',
		0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

var daysInMonth = [13]int{0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

func isLeapYear(y int) bool {
	return y%4 == 0 && (y%100 != 0 || y%400 == 0)
}

// validDateParts mirrors @cfworker's date(): YYYY-MM-DD with a real calendar
// date (leap-year February included).
func validDateParts(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	if !isASCIIDigits(s[0:4]) || !isASCIIDigits(s[5:7]) || !isASCIIDigits(s[8:10]) {
		return false
	}
	y, _ := strconv.Atoi(s[0:4])
	m, _ := strconv.Atoi(s[5:7])
	d, _ := strconv.Atoi(s[8:10])
	if m < 1 || m > 12 || d < 1 {
		return false
	}
	max := daysInMonth[m]
	if m == 2 && isLeapYear(y) {
		max = 29
	}
	return d <= max
}

// validTimeParts mirrors @cfworker's time(): HH:MM:SS[.frac] with an optional
// timezone ("z" or ±HH / ±HHMM / ±HH:MM). With requireTZ (the date-time
// format), the timezone is mandatory. Leap seconds are only valid at 23:59:60.
func validTimeParts(s string, requireTZ bool) bool {
	if len(s) < 8 {
		return false
	}
	if !isASCIIDigits(s[0:2]) || s[2] != ':' || !isASCIIDigits(s[3:5]) || s[5] != ':' || !isASCIIDigits(s[6:8]) {
		return false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	sec := int(s[6]-'0')*10 + int(s[7]-'0')

	rest := s[8:]
	if strings.HasPrefix(rest, ".") {
		n := asciiDigitPrefixLen(rest[1:])
		if n == 0 {
			return false
		}
		rest = rest[1+n:]
	}
	hasTZ := false
	if rest != "" {
		hasTZ = true
		if !validTimezoneSuffix(rest) {
			return false
		}
	}
	if !((h <= 23 && m <= 59 && sec <= 59) || (h == 23 && m == 59 && sec == 60)) {
		return false
	}
	return !requireTZ || hasTZ
}

// validTimezoneSuffix matches @cfworker's (z|[+-]\d\d(?::?\d\d)?)? at the end
// of a time string.
func validTimezoneSuffix(s string) bool {
	if s == "z" || s == "Z" {
		return true
	}
	if len(s) < 3 {
		return false
	}
	if s[0] != '+' && s[0] != '-' {
		return false
	}
	if !isASCIIDigits(s[1:3]) {
		return false
	}
	switch len(s) {
	case 3:
		return true
	case 5:
		return isASCIIDigits(s[3:5])
	case 6:
		return s[3] == ':' && isASCIIDigits(s[4:6])
	default:
		return false
	}
}

func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func asciiDigitPrefixLen(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}

// parseRFC3339Instant parses an RFC 3339 date-time the way Deno's
// Temporal.Instant.from does: real calendar date, seconds 0-59 (no leap
// second), separator 'T', 't' or space, and a required offset of Z/z or
// ±HH:MM / ±HHMM / ±HH with hours <= 23 and minutes <= 59.
func parseRFC3339Instant(s string) (time.Time, bool) {
	var zero time.Time
	if len(s) < 20 || !validDateParts(s[:10]) {
		return zero, false
	}
	if c := s[10]; c != 'T' && c != 't' && c != ' ' {
		return zero, false
	}
	t := s[11:]
	if len(t) < 8 || !isASCIIDigits(t[0:2]) || t[2] != ':' || !isASCIIDigits(t[3:5]) || t[5] != ':' || !isASCIIDigits(t[6:8]) {
		return zero, false
	}
	h := int(t[0]-'0')*10 + int(t[1]-'0')
	m := int(t[3]-'0')*10 + int(t[4]-'0')
	sec := int(t[6]-'0')*10 + int(t[7]-'0')
	if h > 23 || m > 59 || sec > 59 {
		return zero, false
	}
	rest := t[8:]
	nsec := 0
	if strings.HasPrefix(rest, ".") {
		n := asciiDigitPrefixLen(rest[1:])
		if n == 0 {
			return zero, false
		}
		nsec = fractionToNanos(rest[1 : 1+n])
		rest = rest[1+n:]
	}

	offset := 0
	zone := time.UTC
	switch {
	case rest == "Z" || rest == "z":
	case len(rest) == 6 && (rest[0] == '+' || rest[0] == '-') &&
		isASCIIDigits(rest[1:3]) && rest[3] == ':' && isASCIIDigits(rest[4:6]):
		oh := int(rest[1]-'0')*10 + int(rest[2]-'0')
		om := int(rest[4]-'0')*10 + int(rest[5]-'0')
		if oh > 23 || om > 59 {
			return zero, false
		}
		offset = oh*3600 + om*60
	case len(rest) == 5 && (rest[0] == '+' || rest[0] == '-') &&
		isASCIIDigits(rest[1:3]) && isASCIIDigits(rest[3:5]):
		oh := int(rest[1]-'0')*10 + int(rest[2]-'0')
		om := int(rest[3]-'0')*10 + int(rest[4]-'0')
		if oh > 23 || om > 59 {
			return zero, false
		}
		offset = oh*3600 + om*60
	case len(rest) == 3 && (rest[0] == '+' || rest[0] == '-') && isASCIIDigits(rest[1:3]):
		oh := int(rest[1]-'0')*10 + int(rest[2]-'0')
		if oh > 23 {
			return zero, false
		}
		offset = oh * 3600
	default:
		return zero, false
	}
	if rest != "" && rest != "Z" && rest != "z" {
		if rest[0] == '-' {
			offset = -offset
		}
		zone = time.FixedZone("", offset)
	}

	y, _ := strconv.Atoi(s[0:4])
	mo, _ := strconv.Atoi(s[5:7])
	d, _ := strconv.Atoi(s[8:10])
	return time.Date(y, time.Month(mo), d, h, m, sec, nsec, zone), true
}

func fractionToNanos(frac string) int {
	digits := frac
	if len(digits) > 9 {
		digits = digits[:9]
	}
	n, _ := strconv.Atoi(digits)
	for i := len(digits); i < 9; i++ {
		n *= 10
	}
	return n
}

// ucs2Length is the UTF-16 code-unit length of s (JS strings are indexed in
// UTF-16 units; Discord embed limits are specified in those).
func ucs2Length(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF || utf8.RuneLen(r) < 0 {
			n += 2
		} else {
			n++
		}
	}
	return n
}
