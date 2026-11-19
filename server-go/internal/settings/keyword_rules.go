// Port of server/lib/keyword-rules.ts: keyword auto-recording rule input
// parsing, stored-record guard and program matching.
package settings

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/api"
)

var keywordRuleInputSchema = &schema{
	types:    []string{"object"},
	required: []string{"keyword"},
	closed:   true,
	props: []schemaProp{
		{"keyword", &schema{types: []string{"string"}}},
		{"from", &schema{types: []string{"string"}, format: "date-time"}},
		{"to", &schema{types: []string{"string"}, format: "date-time"}},
		{"channels", &schema{types: []string{"array"}, items: &schema{types: []string{"string"}}}},
		{"genres", &schema{types: []string{"array"}, items: &schema{
			types: []string{"integer"}, minimum: floatPtr(0), maximum: floatPtr(15),
		}}},
		{"enabled", &schema{types: []string{"boolean"}}},
	},
}

var keywordRuleSchema = &schema{
	types:    []string{"object"},
	required: []string{"id", "keyword", "channels", "genres", "enabled", "createdAt"},
	closed:   true,
	props: []schemaProp{
		{"id", &schema{types: []string{"string"}, format: "uuid"}},
		{"keyword", &schema{types: []string{"string"}}},
		{"from", &schema{types: []string{"string"}, format: "date-time"}},
		{"to", &schema{types: []string{"string"}, format: "date-time"}},
		{"channels", &schema{types: []string{"array"}, items: &schema{types: []string{"string"}}}},
		{"genres", &schema{types: []string{"array"}, items: &schema{
			types: []string{"integer"}, minimum: floatPtr(0), maximum: floatPtr(15),
		}}},
		{"enabled", &schema{types: []string{"boolean"}}},
		{"createdAt", &schema{types: []string{"integer"}}},
	},
}

// KeywordRuleInput is the normalized body of a keyword-rule create/update
// request (KeywordRule minus the server-assigned id / createdAt, with the
// optional fields defaulted: channels / genres empty, enabled true, from / to
// nil meaning unrestricted). Convertible to KeywordRuleMatch when the field
// sets stay identical.
type KeywordRuleInput struct {
	Keyword  string
	From     *time.Time
	To       *time.Time
	Channels []string
	Genres   []int
	Enabled  bool
}

// ParseKeywordRuleInput validates a KeywordRuleInput request body against the
// OpenAPI schema and normalizes it: keyword is trimmed and required non-empty,
// from / to must be RFC 3339 with from <= to, unspecified fields get their
// defaults. The returned error message is the client-facing 400 body.
func ParseKeywordRuleInput(raw []byte) (KeywordRuleInput, error) {
	value, err := decodeJSONValue(raw)
	if err != nil {
		return KeywordRuleInput{}, errors.New("invalid JSON body")
	}
	if msg, ok := validateAgainst(keywordRuleInputSchema, value); !ok {
		return KeywordRuleInput{}, errors.New(msg)
	}
	obj := value.(*orderedObject)

	keyword := strings.TrimSpace(obj.values["keyword"].(string))
	if keyword == "" {
		return KeywordRuleInput{}, errors.New("keyword must be a non-empty string")
	}

	from, err := parseRuleTime(obj, "from")
	if err != nil {
		return KeywordRuleInput{}, err
	}
	to, err := parseRuleTime(obj, "to")
	if err != nil {
		return KeywordRuleInput{}, err
	}
	if from != nil && to != nil && from.After(*to) {
		return KeywordRuleInput{}, errors.New("from must not be after to")
	}

	return KeywordRuleInput{
		Keyword:  keyword,
		From:     from,
		To:       to,
		Channels: stringSliceOrDefault(obj, "channels"),
		Genres:   intSliceOrDefault(obj, "genres"),
		Enabled:  boolOrDefault(obj, "enabled", true),
	}, nil
}

// parseRuleTime extracts one of the from/to date-times. The schema already
// guarantees a string; the RFC 3339 gate here rejects values the format check
// tolerates (leap seconds, out-of-range offsets).
func parseRuleTime(obj *orderedObject, key string) (*time.Time, error) {
	v, present := obj.lookup(key)
	if !present {
		return nil, nil
	}
	t, ok := parseRFC3339Instant(v.(string))
	if !ok {
		return nil, fmt.Errorf("%s must be an RFC 3339 date-time", key)
	}
	return &t, nil
}

// IsKeywordRule reports whether a stored value satisfies the KeywordRule
// schema. Read-backs are lenient: unknown keys are tolerated (values passed
// may be raw JSON bytes or an already-decoded value).
func IsKeywordRule(value any) bool {
	v, ok := coerceStoredValue(value)
	if !ok {
		return false
	}
	var errs []validationError
	validateSchemaValue(v, relaxClosed(keywordRuleSchema), "#", &errs)
	return len(errs) == 0
}

// KeywordRuleMatch is the subset of a keyword rule used for matching; it fits
// both stored rules (via MatchFieldsOf) and parsed drafts (via struct
// conversion from KeywordRuleInput).
type KeywordRuleMatch struct {
	Keyword  string
	From     *time.Time
	To       *time.Time
	Channels []string
	Genres   []int
}

// KeywordRuleTarget is the program info matched against a rule (a subset of
// MirakurunProgram).
type KeywordRuleTarget struct {
	Name      *string // nil never matches
	StartAt   int64   // epoch milliseconds
	ChannelID *string // nil when the program has no resolvable channel
	Genres    []int   // ARIB lv1 codes
}

// MatchFieldsOf converts a stored rule record into matching fields.
func MatchFieldsOf(rule api.KeywordRule) KeywordRuleMatch {
	return KeywordRuleMatch{
		Keyword:  rule.Keyword,
		From:     rule.From,
		To:       rule.To,
		Channels: rule.Channels,
		Genres:   rule.Genres,
	}
}

// MatchesKeywordRule reports whether a program matches a rule. enabled is not
// consulted (the edit modal previews with drafts); applying only to enabled
// rules is the caller's job.
func MatchesKeywordRule(rule KeywordRuleMatch, target KeywordRuleTarget) bool {
	if target.Name == nil ||
		!strings.Contains(strings.ToLower(*target.Name), strings.ToLower(rule.Keyword)) {
		return false
	}
	if rule.From != nil && target.StartAt < rule.From.UnixMilli() {
		return false
	}
	if rule.To != nil && target.StartAt > rule.To.UnixMilli() {
		return false
	}
	if len(rule.Channels) > 0 {
		if target.ChannelID == nil || !containsString(rule.Channels, *target.ChannelID) {
			return false
		}
	}
	if len(rule.Genres) > 0 {
		if !intersectsInt(rule.Genres, target.Genres) {
			return false
		}
	}
	return true
}

func containsString(values []string, v string) bool {
	for _, s := range values {
		if s == v {
			return true
		}
	}
	return false
}

func intersectsInt(a, b []int) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func stringSliceOrDefault(obj *orderedObject, key string) []string {
	v, present := obj.lookup(key)
	if !present {
		return []string{}
	}
	arr := v.([]any)
	out := make([]string, 0, len(arr))
	for _, el := range arr {
		out = append(out, el.(string))
	}
	return out
}

func intSliceOrDefault(obj *orderedObject, key string) []int {
	v, present := obj.lookup(key)
	if !present {
		return []int{}
	}
	arr := v.([]any)
	out := make([]int, 0, len(arr))
	for _, el := range arr {
		out = append(out, int(el.(float64)))
	}
	return out
}

func boolOrDefault(obj *orderedObject, key string, def bool) bool {
	v, present := obj.lookup(key)
	if !present {
		return def
	}
	return v.(bool)
}
