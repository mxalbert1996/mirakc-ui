package settings

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/api"
)

var jst = time.FixedZone("JST", 9*60*60)

// 2026-01-02T03:04:05+09:00 (= 2026-01-01T18:04:05Z)
var startAt = time.Date(2026, 1, 1, 18, 4, 5, 0, time.UTC).UnixMilli()

func strPtr(s string) *string { return &s }

func ruleMatch(overrides KeywordRuleMatch) KeywordRuleMatch {
	base := KeywordRuleMatch{Keyword: "ニュース"}
	if overrides.Keyword != "" {
		base.Keyword = overrides.Keyword
	}
	if overrides.From != nil {
		base.From = overrides.From
	}
	if overrides.To != nil {
		base.To = overrides.To
	}
	if overrides.Channels != nil {
		base.Channels = overrides.Channels
	}
	if overrides.Genres != nil {
		base.Genres = overrides.Genres
	}
	return base
}

func baseTarget() KeywordRuleTarget {
	return KeywordRuleTarget{
		Name:      strPtr("ニュースウォッチ"),
		StartAt:   startAt,
		ChannelID: strPtr("27"),
		Genres:    []int{0, 3},
	}
}

func TestMatchesKeywordRuleName(t *testing.T) {
	r := ruleMatch(KeywordRuleMatch{})
	if !MatchesKeywordRule(r, baseTarget()) {
		t.Error("substring match on name failed")
	}
	newsRule := ruleMatch(KeywordRuleMatch{Keyword: "news"})
	newsTarget := baseTarget()
	newsTarget.Name = strPtr("NEWS TODAY")
	if !MatchesKeywordRule(newsRule, newsTarget) {
		t.Error("case-insensitive match failed")
	}
	animeRule := ruleMatch(KeywordRuleMatch{Keyword: "アニメ"})
	if MatchesKeywordRule(animeRule, baseTarget()) {
		t.Error("non-matching keyword matched")
	}
	nilName := baseTarget()
	nilName.Name = nil
	if MatchesKeywordRule(r, nilName) {
		t.Error("nil name should not match")
	}
}

func TestMatchesKeywordRulePeriod(t *testing.T) {
	at := func(y, m, d, hh, mm, ss int, zone *time.Location) *time.Time {
		tt := time.Date(y, time.Month(m), d, hh, mm, ss, 0, zone)
		return &tt
	}
	if !MatchesKeywordRule(
		ruleMatch(KeywordRuleMatch{From: at(2026, 1, 2, 0, 0, 0, jst)}),
		baseTarget(),
	) {
		t.Error("startAt within from should match")
	}
	if !MatchesKeywordRule(
		ruleMatch(KeywordRuleMatch{To: at(2026, 1, 2, 23, 59, 59, jst)}),
		baseTarget(),
	) {
		t.Error("startAt within to should match")
	}
	if MatchesKeywordRule(
		ruleMatch(KeywordRuleMatch{From: at(2026, 1, 3, 0, 0, 0, jst)}),
		baseTarget(),
	) {
		t.Error("startAt before from should not match")
	}
	if MatchesKeywordRule(
		ruleMatch(KeywordRuleMatch{To: at(2026, 1, 1, 23, 59, 59, jst)}),
		baseTarget(),
	) {
		t.Error("startAt after to should not match")
	}
	if !MatchesKeywordRule(
		ruleMatch(KeywordRuleMatch{
			From: at(2026, 1, 2, 0, 0, 0, jst),
			To:   at(2026, 1, 2, 23, 59, 59, jst),
		}),
		baseTarget(),
	) {
		t.Error("same-day range should match inclusively")
	}
	// The same calendar day in UTC excludes startAt (Jan 1 18:04Z).
	if MatchesKeywordRule(
		ruleMatch(KeywordRuleMatch{
			From: at(2026, 1, 2, 0, 0, 0, time.UTC),
			To:   at(2026, 1, 2, 23, 59, 59, time.UTC),
		}),
		baseTarget(),
	) {
		t.Error("UTC range should exclude the +09:00 startAt instant")
	}
}

func TestMatchesKeywordRuleChannels(t *testing.T) {
	if !MatchesKeywordRule(
		ruleMatch(KeywordRuleMatch{Channels: []string{"27"}}),
		baseTarget(),
	) {
		t.Error("matching channel should match")
	}
	if MatchesKeywordRule(
		ruleMatch(KeywordRuleMatch{Channels: []string{"99"}}),
		baseTarget(),
	) {
		t.Error("non-matching channel should not match")
	}
	noChannel := baseTarget()
	noChannel.ChannelID = nil
	if MatchesKeywordRule(ruleMatch(KeywordRuleMatch{Channels: []string{"99"}}), noChannel) {
		t.Error("nil channel ID with channel filter should not match")
	}
}

func TestMatchesKeywordRuleGenres(t *testing.T) {
	if !MatchesKeywordRule(
		ruleMatch(KeywordRuleMatch{Genres: []int{0}}),
		baseTarget(),
	) {
		t.Error("genre intersection should match")
	}
	if MatchesKeywordRule(
		ruleMatch(KeywordRuleMatch{Genres: []int{7}}),
		baseTarget(),
	) {
		t.Error("disjoint genres should not match")
	}
	noGenres := baseTarget()
	noGenres.Genres = []int{}
	if MatchesKeywordRule(ruleMatch(KeywordRuleMatch{Genres: []int{0}}), noGenres) {
		t.Error("empty target genres with filter should not match")
	}
}

func TestParseKeywordRuleInputTrimsAndDefaults(t *testing.T) {
	in, err := ParseKeywordRuleInput([]byte(`{"keyword":"  サッカー  "}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := KeywordRuleInput{
		Keyword:  "サッカー",
		Channels: []string{},
		Genres:   []int{},
		Enabled:  true,
	}
	if !reflect.DeepEqual(in, want) {
		t.Fatalf("got %+v, want %+v", in, want)
	}
}

func TestParseKeywordRuleInputAllFields(t *testing.T) {
	in, err := ParseKeywordRuleInput([]byte(`{
		"keyword":"ドラマ",
		"from":"2026-01-01T00:00:00+09:00",
		"to":"2026-01-31T23:59:59+09:00",
		"channels":["27","25"],
		"genres":[3,7],
		"enabled":false
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if in.Keyword != "ドラマ" || !reflect.DeepEqual(in.Channels, []string{"27", "25"}) ||
		!reflect.DeepEqual(in.Genres, []int{3, 7}) || in.Enabled {
		t.Fatalf("got %+v", in)
	}
	if in.From == nil || !in.From.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, jst)) {
		t.Fatalf("from = %v", in.From)
	}
	if in.To == nil || !in.To.Equal(time.Date(2026, 1, 31, 23, 59, 59, 0, jst)) {
		t.Fatalf("to = %v", in.To)
	}
}

func TestParseKeywordRuleInputInvalid(t *testing.T) {
	invalid := []string{
		`null`,
		`{}`,
		`{"keyword":""}`,
		`{"keyword":"   "}`,
		`{"keyword":1}`,
		`{"keyword":"x","from":"2026/01/01"}`,
		`{"keyword":"x","to":"abc"}`,
		`{"keyword":"x","from":"2026-01-01"}`,
		`{"keyword":"x","from":"2026-01-02T00:00:00+09:00","to":"2026-01-01T23:59:59+09:00"}`,
		`{"keyword":"x","channels":[1]}`,
		`{"keyword":"x","channels":"x"}`,
		`{"keyword":"x","genres":[16]}`,
		`{"keyword":"x","genres":[-1]}`,
		`{"keyword":"x","enabled":"yes"}`,
	}
	for _, body := range invalid {
		if _, err := ParseKeywordRuleInput([]byte(body)); err == nil {
			t.Errorf("ParseKeywordRuleInput(%s) succeeded, want error", body)
		}
	}

	if _, err := ParseKeywordRuleInput([]byte(
		`{"keyword":"x","from":"2026-01-01T00:00:00+09:00","to":"2026-01-01T23:59:59+09:00"}`,
	)); err != nil {
		t.Errorf("same-day from/to should parse: %v", err)
	}
}

func TestParseKeywordRuleInputErrorMessages(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"empty keyword", `{"keyword":"   "}`, "keyword must be a non-empty string"},
		{"missing keyword", `{}`, `Instance does not have required property "keyword".`},
		{"keyword type", `{"keyword":1}`, `Property "keyword" does not match schema.`},
		{"unknown key", `{"keyword":"x","unknown":1}`, `Property "unknown" does not match additional properties schema.`},
		{"leap second from", `{"keyword":"x","from":"2026-01-01T23:59:60Z"}`, "from must be an RFC 3339 date-time"},
		{"out-of-range offset to", `{"keyword":"x","to":"2026-01-01T00:00:00+99:00"}`, "to must be an RFC 3339 date-time"},
		{"from after to", `{"keyword":"x","from":"2026-01-02T00:00:00+09:00","to":"2026-01-01T23:59:59+09:00"}`, "from must not be after to"},
		{"not JSON", `not json`, "invalid JSON body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseKeywordRuleInput([]byte(tc.body))
			if err == nil {
				t.Fatalf("want error %q", tc.want)
			}
			if err.Error() != tc.want {
				t.Fatalf("error = %q, want %q", err, tc.want)
			}
		})
	}
}

const validRuleJSON = `{"id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","keyword":"ニュース","channels":[],"genres":[],"enabled":true,"createdAt":0}`

func TestIsKeywordRule(t *testing.T) {
	if !IsKeywordRule([]byte(validRuleJSON)) {
		t.Error("valid record rejected")
	}
	withPeriod := `{"id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","keyword":"x","from":"2026-01-01T00:00:00+09:00","to":"2026-01-31T23:59:59+09:00","channels":[],"genres":[],"enabled":true,"createdAt":0}`
	if !IsKeywordRule([]byte(withPeriod)) {
		t.Error("record with period rejected")
	}
	// Stored reads tolerate unknown keys (relaxed additionalProperties).
	withLegacy := `{"id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","keyword":"x","channels":[],"genres":[],"enabled":true,"createdAt":0,"legacyField":1}`
	if !IsKeywordRule([]byte(withLegacy)) {
		t.Error("record with legacy key rejected")
	}
	// Raw decoded values work too, not just bytes.
	var decoded any
	if err := json.Unmarshal([]byte(validRuleJSON), &decoded); err != nil {
		t.Fatal(err)
	}
	if !IsKeywordRule(decoded) {
		t.Error("decoded map value rejected")
	}
}

func TestIsKeywordRuleBroken(t *testing.T) {
	broken := []string{
		`null`,
		`"rule"`,
		`123`,
		`{}`,
		// required id / createdAt missing
		`{"keyword":"x","channels":[],"genres":[],"enabled":true}`,
		`{"id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","keyword":"x","channels":[],"genres":[],"enabled":true,"createdAt":"x"}`,
		`{"id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","keyword":"x","channels":[],"genres":[],"enabled":"yes","createdAt":0}`,
		`{"id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","keyword":"x","channels":"x","genres":[],"enabled":true,"createdAt":0}`,
		`{"id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","keyword":"x","channels":[],"genres":[16],"enabled":true,"createdAt":0}`,
		`{"id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","keyword":"x","channels":[],"genres":[-1],"enabled":true,"createdAt":0}`,
		`{"id":"r1","keyword":"x","channels":[],"genres":[],"enabled":true,"createdAt":0}`,
		`{"id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","keyword":"x","from":"2026-01-01","channels":[],"genres":[],"enabled":true,"createdAt":0}`,
	}
	for _, body := range broken {
		if IsKeywordRule([]byte(body)) {
			t.Errorf("IsKeywordRule(%s) = true, want false", body)
		}
	}
}

func TestKeywordRuleJSONRoundTrip(t *testing.T) {
	id := openapi_types.UUID(uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479"))
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, jst)
	to := time.Date(2026, 1, 31, 23, 59, 59, 0, jst)

	rule := api.KeywordRule{
		Id:       id,
		Keyword:  "ニュース",
		From:     &from,
		To:       &to,
		Channels: []string{},
		Genres:   []int{},
		Enabled:  true,
	}
	b, err := json.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id":        "f47ac10b-58cc-4372-a567-0e02b2c3d479",
		"keyword":   "ニュース",
		"from":      "2026-01-01T00:00:00+09:00",
		"to":        "2026-01-31T23:59:59+09:00",
		"channels":  []any{},
		"genres":    []any{},
		"enabled":   true,
		"createdAt": float64(0),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire shape = %s", b)
	}

	// from / to are omitempty: absent when unset.
	rule.From, rule.To = nil, nil
	b, err = json.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]any{}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["from"]; ok {
		t.Error("from should be omitted when nil")
	}
	if _, ok := got["to"]; ok {
		t.Error("to should be omitted when nil")
	}
}
