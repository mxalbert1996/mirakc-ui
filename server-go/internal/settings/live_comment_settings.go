// Port of server/lib/live-comment-settings.ts: live-comment mapping input
// parsing, stored-record guard and per-source channel ID validation.
package settings

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/api"
)

var liveCommentAssignmentSchema = &schema{
	types:    []string{"object"},
	required: []string{"source", "channelId"},
	closed:   true,
	props: []schemaProp{
		{"source", &schema{types: []string{"string"}, enum: []string{"nicolive", "nx-jikkyo"}}},
		{"channelId", &schema{types: []string{"string"}}},
	},
}

var liveCommentMappingSchema = &schema{
	types:    []string{"object"},
	required: []string{"id", "channel", "assignments", "enabled", "createdAt"},
	closed:   true,
	props: []schemaProp{
		{"id", &schema{types: []string{"string"}, format: "uuid"}},
		{"channel", &schema{types: []string{"string"}}},
		{"assignments", &schema{types: []string{"array"}, items: liveCommentAssignmentSchema}},
		{"enabled", &schema{types: []string{"boolean"}}},
		{"createdAt", &schema{types: []string{"integer"}}},
	},
}

var liveCommentMappingInputSchema = &schema{
	types:    []string{"object"},
	required: []string{"channel"},
	closed:   true,
	props: []schemaProp{
		{"channel", &schema{types: []string{"string"}}},
		{"assignments", &schema{types: []string{"array"}, items: liveCommentAssignmentSchema}},
		{"enabled", &schema{types: []string{"boolean"}}},
	},
}

// IsValidChannelID reports whether value matches the channel ID form of the
// source: nicolive is "ch" + digits, nx-jikkyo is "jk" + digits.
func IsValidChannelID(source api.LiveCommentAssignmentSource, value string) bool {
	switch source {
	case api.Nicolive:
		return matchesSourceChannelID("ch", value)
	case api.NxJikkyo:
		return matchesSourceChannelID("jk", value)
	}
	return false
}

func matchesSourceChannelID(prefix, value string) bool {
	return strings.HasPrefix(value, prefix) && isASCIIDigits(value[len(prefix):])
}

// LiveCommentMappingInput is the normalized body of a mapping create/update
// request (LiveCommentMapping minus the server-assigned id / createdAt, with
// defaults applied: assignments empty, enabled true).
type LiveCommentMappingInput struct {
	Channel     string
	Assignments []api.LiveCommentAssignment
	Enabled     bool
}

// ParseLiveCommentMappingInput validates a LiveCommentMappingInput request
// body against the OpenAPI schema and normalizes it: channel is trimmed and
// required non-empty, each assignment's channel ID must match its source's ID
// form, and (source, ID) duplicates within the entry are rejected. The
// returned error message is the client-facing 400 body.
func ParseLiveCommentMappingInput(raw []byte) (LiveCommentMappingInput, error) {
	value, err := decodeJSONValue(raw)
	if err != nil {
		return LiveCommentMappingInput{}, errors.New("invalid JSON body")
	}
	if msg, ok := validateAgainst(liveCommentMappingInputSchema, value); !ok {
		return LiveCommentMappingInput{}, errors.New(msg)
	}
	obj := value.(*orderedObject)

	channel := strings.TrimSpace(obj.values["channel"].(string))
	if channel == "" {
		return LiveCommentMappingInput{}, errors.New("channel must be a non-empty string")
	}

	assignments := []api.LiveCommentAssignment{}
	seen := map[string]bool{}
	if arr, present := obj.lookup("assignments"); present {
		for _, el := range arr.([]any) {
			assignment := el.(*orderedObject)
			source := api.LiveCommentAssignmentSource(assignment.values["source"].(string))
			id := strings.TrimSpace(assignment.values["channelId"].(string))
			// Assignments are validated regardless of enabled so paused
			// entries still hold valid IDs.
			if !IsValidChannelID(source, id) {
				return LiveCommentMappingInput{}, fmt.Errorf("invalid channel id for %s: %s", source, id)
			}
			key := string(source) + ":" + id
			if seen[key] {
				return LiveCommentMappingInput{}, fmt.Errorf("duplicate assignment for %s: %s", source, id)
			}
			seen[key] = true
			assignments = append(assignments, api.LiveCommentAssignment{Source: source, ChannelId: id})
		}
	}

	return LiveCommentMappingInput{
		Channel:     channel,
		Assignments: assignments,
		Enabled:     boolOrDefault(obj, "enabled", true),
	}, nil
}

// IsLiveCommentMapping reports whether a stored value satisfies the
// LiveCommentMapping schema. Read-backs are lenient: unknown keys are
// tolerated (values passed may be raw JSON bytes or an already-decoded value).
func IsLiveCommentMapping(value any) bool {
	v, ok := coerceStoredValue(value)
	if !ok {
		return false
	}
	var errs []validationError
	validateSchemaValue(v, relaxClosed(liveCommentMappingSchema), "#", &errs)
	return len(errs) == 0
}
