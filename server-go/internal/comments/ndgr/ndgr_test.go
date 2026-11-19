// Port of server/lib/comments/ndgr.test.ts plus the segment+next / empty
// entry coverage requested for the Go port.
package ndgr

import (
	"reflect"
	"testing"
)

func TestDecodeChunkedEntrySegment(t *testing.T) {
	got := DecodeChunkedEntry(segmentEntryOf("https://example.com/segment/1"))
	want := Entry{SegmentURI: "https://example.com/segment/1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

func TestDecodeChunkedEntryNext(t *testing.T) {
	got := DecodeChunkedEntry(nextEntryOf(1718240400))
	want := Entry{NextAt: 1718240400}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

func TestDecodeChunkedEntrySegmentAndNext(t *testing.T) {
	got := DecodeChunkedEntry(cat(
		segmentEntryOf("https://example.com/segment/1"),
		nextEntryOf(1718240400),
	))
	want := Entry{SegmentURI: "https://example.com/segment/1", NextAt: 1718240400}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

func TestDecodeChunkedEntryNeither(t *testing.T) {
	for name, bytes := range map[string][]byte{
		"empty":         {},
		"unknown field": lenField(5, stringField(1, "x")),
	} {
		if got := DecodeChunkedEntry(bytes); got != (Entry{}) {
			t.Errorf("%s: got %+v; want zero Entry", name, got)
		}
	}
}

func TestDecodeChunkedEntryIgnoresBackwardAndPrevious(t *testing.T) {
	// backward (2) / previous (3): past-log replay, unused for live viewing.
	got := DecodeChunkedEntry(cat(
		lenField(2, stringField(1, "ignored")),
		lenField(3, stringField(1, "ignored")),
	))
	if got != (Entry{}) {
		t.Errorf("got %+v; want zero Entry", got)
	}
}

func str(s string) *string { return &s }
func num(n int64) *int64   { return &n }

func TestDecodeChunkedMessageChat(t *testing.T) {
	got := DecodeChunkedMessage(chatMessageOf(chatMessageOptions{
		id:        "msg-1",
		atSeconds: 1718240400,
		atNanos:   num(500_000_000),
		content:   str("こんにちは"),
	}))
	want := &Message{ID: "msg-1", AtMs: 1718240400_500, Chat: &Chat{Content: "こんにちは"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

func TestDecodeChunkedMessageNamedChat(t *testing.T) {
	got := DecodeChunkedMessage(chatMessageOf(chatMessageOptions{
		id:        "msg-2",
		atSeconds: 1718240401,
		content:   str("named"),
		name:      str("テスト太郎"),
	}))
	want := &Message{ID: "msg-2", AtMs: 1718240401_000, Chat: &Chat{Content: "named", Name: "テスト太郎"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

func TestDecodeChunkedMessageOverflowedChat(t *testing.T) {
	// overflowed_chat (field 20) is treated as chat too.
	got := DecodeChunkedMessage(chatMessageOf(chatMessageOptions{
		id:          "msg-3",
		atSeconds:   1718240402,
		content:     str("overflowed"),
		chatFieldNo: 20,
	}))
	want := &Message{ID: "msg-3", AtMs: 1718240402_000, Chat: &Chat{Content: "overflowed"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

func TestDecodeChunkedMessageStateHasNoChat(t *testing.T) {
	// ChunkedMessage { meta(1), state(4): ... } — no message field.
	got := DecodeChunkedMessage(cat(
		chatMessageOf(chatMessageOptions{id: "msg-4", atSeconds: 1718240403}),
		lenField(4, varintField(1, 1)),
	))
	want := &Message{ID: "msg-4", AtMs: 1718240403_000}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

func TestDecodeChunkedMessageNoMeta(t *testing.T) {
	got := DecodeChunkedMessage(lenField(4, varintField(1, 1)))
	if got != nil {
		t.Errorf("got %+v; want nil", got)
	}
}
