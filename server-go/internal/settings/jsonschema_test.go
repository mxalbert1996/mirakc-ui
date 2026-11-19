package settings

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The expected strings below were captured by running the actual
// @cfworker/json-schema@4.1.1 validators (the Deno server's validator) on
// these exact inputs, so the port's 400 error bodies stay byte-identical.
func TestKeywordRuleInputSchemaErrors(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"null", `null`, `Instance type "null" is invalid. Expected "object".`},
		{"string", `"rule"`, `Instance type "string" is invalid. Expected "object".`},
		{"array", `[]`, `Instance type "array" is invalid. Expected "object".`},
		{"boolean", `true`, `Instance type "boolean" is invalid. Expected "object".`},
		{"empty object", `{}`, `Instance does not have required property "keyword".`},
		{"keyword number", `{"keyword":1}`, `Property "keyword" does not match schema.`},
		{"from slash format", `{"keyword":"x","from":"2026/01/01"}`, `Property "from" does not match schema.`},
		{"from date only", `{"keyword":"x","from":"2026-01-01"}`, `Property "from" does not match schema.`},
		{"from impossible date", `{"keyword":"x","from":"2026-02-30T00:00:00Z"}`, `Property "from" does not match schema.`},
		{"from empty string", `{"keyword":"x","from":""}`, `Property "from" does not match schema.`},
		{"from null", `{"keyword":"x","from":null}`, `Property "from" does not match schema.`},
		{"to garbage", `{"keyword":"x","to":"abc"}`, `Property "to" does not match schema.`},
		{"channels number item", `{"keyword":"x","channels":[1]}`, `Property "channels" does not match schema.`},
		{"channels string", `{"keyword":"x","channels":"x"}`, `Property "channels" does not match schema.`},
		{"channels null", `{"keyword":"x","channels":null}`, `Property "channels" does not match schema.`},
		{"genres above max", `{"keyword":"x","genres":[16]}`, `Property "genres" does not match schema.`},
		{"genres below min", `{"keyword":"x","genres":[-1]}`, `Property "genres" does not match schema.`},
		{"genres fractional", `{"keyword":"x","genres":[1.5]}`, `Property "genres" does not match schema.`},
		{"enabled string", `{"keyword":"x","enabled":"yes"}`, `Property "enabled" does not match schema.`},
		{"unknown key", `{"keyword":"x","unknownKey":1}`, `Property "unknownKey" does not match additional properties schema.`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := decodeJSONValue([]byte(tc.value))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if msg, ok := validateAgainst(keywordRuleInputSchema, value); ok {
				t.Fatalf("expected error %q, got valid", tc.want)
			} else if msg != tc.want {
				t.Fatalf("error = %q, want %q", msg, tc.want)
			}
		})
	}
}

func TestLiveCommentMappingInputSchemaErrors(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"empty object", `{}`, `Instance does not have required property "channel".`},
		{"channel number", `{"channel":1}`, `Property "channel" does not match schema.`},
		{"assignments not array", `{"channel":"27","assignments":"x"}`, `Property "assignments" does not match schema.`},
		{"assignment unknown source", `{"channel":"27","assignments":[{"source":"unknown","channelId":"ch1"}]}`, `Property "assignments" does not match schema.`},
		{"assignment missing channelId", `{"channel":"27","assignments":[{"source":"nicolive"}]}`, `Property "assignments" does not match schema.`},
		{"assignment channelId number", `{"channel":"27","assignments":[{"source":"nicolive","channelId":1}]}`, `Property "assignments" does not match schema.`},
		{"enabled string", `{"channel":"27","enabled":"yes"}`, `Property "enabled" does not match schema.`},
		{"unknown key", `{"channel":"27","extra":1}`, `Property "extra" does not match additional properties schema.`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := decodeJSONValue([]byte(tc.value))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if msg, ok := validateAgainst(liveCommentMappingInputSchema, value); ok {
				t.Fatalf("expected error %q, got valid", tc.want)
			} else if msg != tc.want {
				t.Fatalf("error = %q, want %q", msg, tc.want)
			}
		})
	}
}

func TestNotificationSettingsSchemaErrors(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"null", `null`, `Instance type "null" is invalid. Expected "object".`},
		{"empty object", `{}`, `Instance does not have required property "url".`},
		{"url number", `{"url":1}`, `Property "url" does not match schema.`},
		{"token number", `{"url":"","token":1}`, `Property "token" does not match schema.`},
		{"onStart string", `{"url":"","token":"","onStart":"yes"}`, `Property "onStart" does not match schema.`},
		{"onFail number", `{"url":"","token":"","onFail":1}`, `Property "onFail" does not match schema.`},
		{"discordWebhookUrl number", `{"url":"","discordWebhookUrl":1}`, `Property "discordWebhookUrl" does not match schema.`},
		{"unknown key", `{"url":"","extra":true}`, `Property "extra" does not match additional properties schema.`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := decodeJSONValue([]byte(tc.value))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if msg, ok := validateAgainst(notificationSettingsInputSchema, value); ok {
				t.Fatalf("expected error %q, got valid", tc.want)
			} else if msg != tc.want {
				t.Fatalf("error = %q, want %q", msg, tc.want)
			}
		})
	}
}

func TestValidateSchemaNestedErrors(t *testing.T) {
	value, err := decodeJSONValue([]byte(`{"url":"","token":1}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var errs []validationError
	validateSchemaValue(value, notificationSettingsInputSchema, "#", &errs)
	if len(errs) != 2 {
		t.Fatalf("got %d errors, want 2: %+v", len(errs), errs)
	}
	if errs[0].instanceLocation != "#" || errs[0].message != `Property "token" does not match schema.` {
		t.Fatalf("first error = %+v", errs[0])
	}
	if errs[1].instanceLocation != "#/token" ||
		errs[1].message != `Instance type "number" is invalid. Expected "string", "null".` {
		t.Fatalf("second error = %+v", errs[1])
	}
}

func TestValidDateTimeFormat(t *testing.T) {
	valid := []string{
		"2026-01-01T00:00:00Z",
		"2026-01-01t00:00:00z",
		"2026-01-01 00:00:00Z",
		"2026-01-01T00:00:00+09:00",
		"2026-01-01T00:00:00+0900",
		"2026-01-01T00:00:00+09",
		"2026-01-01T00:00:00.5Z",
		"2026-01-01T23:59:60Z",      // leap second passes the format check
		"2026-01-01T00:00:00+99:00", // offsets are not range-checked
		"2024-02-29T00:00:00Z",      // leap year
	}
	invalid := []string{
		"",
		"2026-01-01",            // no time part
		"2026-01-01T00:00:00",   // no timezone
		"2026-13-01T00:00:00Z",  // month 13
		"2026-01-32T00:00:00Z",  // day 32
		"2026-02-30T00:00:00Z",  // impossible date
		"2023-02-29T00:00:00Z",  // not a leap year
		"2026-01-01T24:00:00Z",  // hour 24
		"2026-01-01T00:60:00Z",  // minute 60
		"2026-01-01T00:00:61Z",  // leap second only at 23:59
		"2026/01/01T00:00:00Z",  // wrong separators
		"2026-01-01T00:00:00Z ", // trailing garbage
		"10000-01-01T00:00:00Z", // 5-digit year
	}
	for _, s := range valid {
		if !validDateTimeFormat(s) {
			t.Errorf("validDateTimeFormat(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if validDateTimeFormat(s) {
			t.Errorf("validDateTimeFormat(%q) = true, want false", s)
		}
	}
}

func TestParseRFC3339Instant(t *testing.T) {
	if _, ok := parseRFC3339Instant("2026-01-01T00:00:00+09:00"); !ok {
		t.Error("offset form +09:00 should parse")
	}
	if _, ok := parseRFC3339Instant("2026-01-01t00:00:00z"); !ok {
		t.Error("lowercase t/z should parse")
	}
	if _, ok := parseRFC3339Instant("2026-01-01 00:00:00Z"); !ok {
		t.Error("space separator should parse")
	}
	// The schema format check tolerates these, but the parser (like Deno's
	// Temporal.Instant.from) must reject them.
	for _, s := range []string{
		"2026-01-01T23:59:60Z",      // leap second
		"2026-01-01T00:00:00+99:00", // out-of-range offset
		"2026-01-01T00:00:00",       // missing offset
		"2026-02-30T00:00:00Z",      // impossible date
	} {
		if _, ok := parseRFC3339Instant(s); ok {
			t.Errorf("parseRFC3339Instant(%q) succeeded, want failure", s)
		}
	}

	got, ok := parseRFC3339Instant("2026-01-01T18:04:05.25Z")
	if !ok {
		t.Fatal("parse failed")
	}
	want := got.UTC()
	if want.Second() != 5 || want.Nanosecond() != 250000000 {
		t.Fatalf("fraction parsed as %v", want)
	}
}

// Oracle review S1/S2 pins: the any-typed guards must see the exact leaf
// shapes every live caller produces, not just test literals.
func TestIsKeywordRuleAcceptsUseNumberDecodedMap(t *testing.T) {
	// store.decodeObject uses UseNumber, so stored-value guards meet
	// json.Number leaves. Before the coerceValue fix these hit the
	// "unknown" type path and the guard silently returned false.
	raw := []byte(`{"id":"81e5bf5c-7215-4830-bd7a-682d4c8b3636","keyword":"k","channels":[],"genres":[0],"enabled":true,"createdAt":1795103802213}`)
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	if !IsKeywordRule(m) {
		t.Fatal("IsKeywordRule rejected a UseNumber-decoded map")
	}
}

func TestNormalizeNotificationSettingsAcceptsRawMessage(t *testing.T) {
	// GetSingleton returns json.RawMessage; its dynamic type does not
	// match the []byte case of a type switch. This pin sits next to the
	// fix so a regression cannot hide behind the integration suite.
	raw := json.RawMessage(`{"url":"https://ntfy.sh/topic","token":"tk","discordWebhookUrl":"","onSchedule":true,"onStart":false,"onEnd":false,"onFail":false,"onRemove":false}`)
	s, ok := NormalizeNotificationSettings(raw)
	if !ok || s.Token != "tk" || !s.OnSchedule {
		t.Fatalf("NormalizeNotificationSettings(json.RawMessage) = %+v ok=%v", s, ok)
	}
}
