package settings

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/mxalbert1996/mirakc-ui/server-go/internal/api"
)

func TestIsValidChannelID(t *testing.T) {
	cases := []struct {
		source api.LiveCommentAssignmentSource
		value  string
		want   bool
	}{
		{api.Nicolive, "ch2646436", true},
		{api.Nicolive, "jk1", false},
		{api.NxJikkyo, "jk1", true},
		{api.NxJikkyo, "jk211", true},
		{api.NxJikkyo, "ch2646436", false},
		{api.Nicolive, "", false},
		{api.NxJikkyo, "jk", false},
	}
	for _, tc := range cases {
		if got := IsValidChannelID(tc.source, tc.value); got != tc.want {
			t.Errorf("IsValidChannelID(%q, %q) = %v, want %v", tc.source, tc.value, got, tc.want)
		}
	}
}

func TestParseLiveCommentMappingInputTrims(t *testing.T) {
	in, err := ParseLiveCommentMappingInput([]byte(`{
		"channel":" 27 ",
		"assignments":[
			{"source":"nicolive","channelId":" ch2646436 "},
			{"source":"nx-jikkyo","channelId":"jk1"}
		]
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := LiveCommentMappingInput{
		Channel: "27",
		Assignments: []api.LiveCommentAssignment{
			{Source: api.Nicolive, ChannelId: "ch2646436"},
			{Source: api.NxJikkyo, ChannelId: "jk1"},
		},
		Enabled: true,
	}
	if !reflect.DeepEqual(in, want) {
		t.Fatalf("got %+v", in)
	}
}

func TestParseLiveCommentMappingInputDefaults(t *testing.T) {
	in, err := ParseLiveCommentMappingInput([]byte(`{"channel":"BS15_0"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := LiveCommentMappingInput{Channel: "BS15_0", Assignments: []api.LiveCommentAssignment{}, Enabled: true}
	if !reflect.DeepEqual(in, want) {
		t.Fatalf("got %+v", in)
	}
}

func TestParseLiveCommentMappingInputKeepsEnabled(t *testing.T) {
	in, err := ParseLiveCommentMappingInput([]byte(
		`{"channel":"27","assignments":[{"source":"nx-jikkyo","channelId":"jk1"}],"enabled":false}`,
	))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if in.Enabled {
		t.Fatal("enabled should stay false")
	}
}

func TestParseLiveCommentMappingInputChannelRequired(t *testing.T) {
	for _, body := range []string{`{}`, `{"channel":""}`, `{"channel":"   "}`} {
		if _, err := ParseLiveCommentMappingInput([]byte(body)); err == nil {
			t.Errorf("ParseLiveCommentMappingInput(%s) succeeded, want error", body)
		}
	}
	_, err := ParseLiveCommentMappingInput([]byte(`{"channel":""}`))
	if err == nil || err.Error() != "channel must be a non-empty string" {
		t.Fatalf("error = %v, want %q", err, "channel must be a non-empty string")
	}
}

func TestParseLiveCommentMappingInputErrorMessages(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"nicolive with jk id",
			`{"channel":"27","assignments":[{"source":"nicolive","channelId":"jk1"}]}`,
			"invalid channel id for nicolive: jk1",
		},
		{
			"nx-jikkyo with ch id",
			`{"channel":"27","assignments":[{"source":"nx-jikkyo","channelId":"ch2646436"}]}`,
			"invalid channel id for nx-jikkyo: ch2646436",
		},
		{
			"duplicate",
			`{"channel":"27","assignments":[{"source":"nx-jikkyo","channelId":"jk1"},{"source":"nx-jikkyo","channelId":"jk1"}]}`,
			"duplicate assignment for nx-jikkyo: jk1",
		},
		{
			"unknown source",
			`{"channel":"27","assignments":[{"source":"unknown","channelId":"ch1"}]}`,
			`Property "assignments" does not match schema.`,
		},
		{
			"not JSON",
			`nope`,
			"invalid JSON body",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseLiveCommentMappingInput([]byte(tc.body))
			if err == nil {
				t.Fatalf("want error %q", tc.want)
			}
			if err.Error() != tc.want {
				t.Fatalf("error = %q, want %q", err, tc.want)
			}
		})
	}
}

func TestParseLiveCommentMappingInputCrossSourceSameID(t *testing.T) {
	in, err := ParseLiveCommentMappingInput([]byte(`{
		"channel":"27",
		"assignments":[
			{"source":"nicolive","channelId":"ch1"},
			{"source":"nx-jikkyo","channelId":"jk1"}
		]
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(in.Assignments) != 2 {
		t.Fatalf("got %d assignments", len(in.Assignments))
	}
}

func TestIsLiveCommentMapping(t *testing.T) {
	valid := `{"id":"0f9a1b2c-3d4e-5f60-7182-93a4b5c6d7e8","channel":"27","assignments":[{"source":"nx-jikkyo","channelId":"jk1"}],"enabled":true,"createdAt":1767225600000}`
	if !IsLiveCommentMapping([]byte(valid)) {
		t.Error("valid mapping rejected")
	}
	var decoded any
	if err := json.Unmarshal([]byte(valid), &decoded); err != nil {
		t.Fatal(err)
	}
	if !IsLiveCommentMapping(decoded) {
		t.Error("decoded map value rejected")
	}
	if !IsLiveCommentMapping([]byte(`{"id":"0f9a1b2c-3d4e-5f60-7182-93a4b5c6d7e8","channel":"27","assignments":[{"source":"nx-jikkyo","channelId":"jk1"}],"enabled":true,"createdAt":1767225600000,"legacy":1}`)) {
		t.Error("legacy key should be tolerated on read-back")
	}

	broken := []string{
		`{"channel":"27","assignments":[],"enabled":true,"createdAt":0}`,                   // id missing
		`{"nicolive":[],"nx-jikkyo":[]}`,                                                   // old singleton shape
		`{"id":"not-a-uuid","channel":"27","assignments":[],"enabled":true,"createdAt":0}`, // id not a uuid
		`{"id":"0f9a1b2c-3d4e-5f60-7182-93a4b5c6d7e8","channel":1,"assignments":[],"enabled":true,"createdAt":0}`,
	}
	for _, body := range broken {
		if IsLiveCommentMapping([]byte(body)) {
			t.Errorf("IsLiveCommentMapping(%s) = true, want false", body)
		}
	}
}

func TestLiveCommentMappingJSONRoundTrip(t *testing.T) {
	mapping := api.LiveCommentMapping{
		Id:          openapi_types.UUID(uuid.MustParse("0f9a1b2c-3d4e-5f60-7182-93a4b5c6d7e8")),
		Channel:     "27",
		Assignments: []api.LiveCommentAssignment{{Source: api.NxJikkyo, ChannelId: "jk1"}},
		Enabled:     true,
		CreatedAt:   1767225600000,
	}
	b, err := json.Marshal(mapping)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id":          "0f9a1b2c-3d4e-5f60-7182-93a4b5c6d7e8",
		"channel":     "27",
		"assignments": []any{map[string]any{"source": "nx-jikkyo", "channelId": "jk1"}},
		"enabled":     true,
		"createdAt":   float64(1767225600000),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire shape = %s", b)
	}
}
