// Port of server/lib/comments/protobuf.test.ts plus the edge-value
// coverage requested for the Go port (varint 0/127/128, >64-bit rejection,
// unsupported wire types, clean EOF).
package ndgr

import (
	"bytes"
	"io"
	"reflect"
	"testing"
)

func TestReadVarintSingleByte(t *testing.T) {
	value, next, err := readVarint([]byte{0x08}, 0)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if value != 8 || next != 1 {
		t.Errorf("value, next = %d, %d; want 8, 1", value, next)
	}
}

func TestReadVarintMultiByte(t *testing.T) {
	value, next, err := readVarint([]byte{0xac, 0x02}, 0)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if value != 300 || next != 2 {
		t.Errorf("value, next = %d, %d; want 300, 2", value, next)
	}
}

func TestReadVarintTruncated(t *testing.T) {
	if _, _, err := readVarint([]byte{0x80}, 0); err == nil {
		t.Error("want error for truncated varint")
	}
}

func TestReadVarintEdgeValues(t *testing.T) {
	for _, tc := range []struct {
		in    []byte
		value uint64
	}{{[]byte{0x00}, 0}, {[]byte{0x7f}, 127}, {[]byte{0x80, 0x01}, 128}} {
		value, next, err := readVarint(tc.in, 0)
		if err != nil {
			t.Fatalf("% x: err = %v", tc.in, err)
		}
		if value != tc.value || next != len(tc.in) {
			t.Errorf("% x: value, next = %d, %d; want %d, %d", tc.in, value, next, tc.value, len(tc.in))
		}
	}
}

func TestReadVarintMax64Bit(t *testing.T) {
	// 10-byte varint: 9 continuation bytes then the final byte at shift 63.
	in := bytes.Repeat([]byte{0x80}, 9)
	in = append(in, 0x01)
	value, next, err := readVarint(in, 0)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if value != 1<<63 || next != 10 {
		t.Errorf("value, next = %d, %d; want %d, 10", value, next, uint64(1)<<63)
	}
}

func TestReadVarintTooLong(t *testing.T) {
	// 11 bytes: the 64-bit guard rejects a varint longer than 10 bytes.
	in := bytes.Repeat([]byte{0x80}, 10)
	in = append(in, 0x01)
	if _, _, err := readVarint(in, 0); err == nil {
		t.Error("want error for >10-byte varint")
	}
}

func TestFieldsVarintAndLen(t *testing.T) {
	message := cat(varintField(1, 150), lenField(2, []byte("abc")))
	fields, err := Fields(message)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := []Field{
		{FieldNo: 1, WireType: 0, Varint: 150},
		{FieldNo: 2, WireType: 2, Bytes: []byte("abc")},
	}
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("fields = %+v; want %+v", fields, want)
	}
}

func TestFieldsSkipsI64AndI32(t *testing.T) {
	message := cat(
		[]byte{(1 << 3) | 1, 1, 2, 3, 4, 5, 6, 7, 8}, // field 1, wire type 1 (I64)
		[]byte{(2 << 3) | 5, 1, 2, 3, 4},             // field 2, wire type 5 (I32)
		varintField(3, 7),
	)
	fields, err := Fields(message)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := []Field{{FieldNo: 3, WireType: 0, Varint: 7}}
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("fields = %+v; want %+v", fields, want)
	}
}

func TestFieldsErrors(t *testing.T) {
	for name, message := range map[string][]byte{
		"unsupported wire type":      {(1 << 3) | 3},
		"truncated length-delimited": cat([]byte{(1 << 3) | 2}, varint(10), []byte{1, 2, 3}),
		"truncated field (I64 tail)": append([]byte{(1 << 3) | 1, 1, 2}, make([]byte, 3)...),
		"truncated field (I32 tail)": append([]byte{(1 << 3) | 5, 1, 2}, make([]byte, 1)...),
		"truncated varint":           {0x80},
	} {
		if _, err := Fields(message); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func collectMessages(t *testing.T, chunks ...[]byte) ([]byte, error) {
	t.Helper()
	var messages [][]byte
	err := LengthDelimited(streamOf(chunks...), func(m []byte) error {
		messages = append(messages, m)
		return nil
	})
	return bytes.Join(messages, nil), err
}

func TestLengthDelimitedMultipleMessagesPerChunk(t *testing.T) {
	stream := streamOf(lengthDelimitedOf([]byte{1, 2, 3}, []byte{4, 5}))
	var messages [][]byte
	err := LengthDelimited(stream, func(m []byte) error {
		messages = append(messages, m)
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := [][]byte{{1, 2, 3}, {4, 5}}
	if !reflect.DeepEqual(messages, want) {
		t.Errorf("messages = %v; want %v", messages, want)
	}
}

func TestLengthDelimitedAcrossChunkBoundaries(t *testing.T) {
	payload := []byte{9, 8, 7, 6, 5, 4, 3, 2, 1, 0}
	encoded := cat(varint(uint64(len(payload))), payload)
	// Split right after the length prefix and mid-body.
	chunks := [][]byte{encoded[:1], encoded[1:4], encoded[4:]}
	got, err := collectMessages(t, chunks...)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("messages = %v; want %v", got, payload)
	}
}

func TestLengthDelimitedPrefixSplitMidVarint(t *testing.T) {
	// Length 300 needs a 2-byte varint prefix; split it between the bytes.
	payload := bytes.Repeat([]byte{7}, 300)
	encoded := cat(varint(300), payload)
	chunks := [][]byte{encoded[:1], encoded[1:2], encoded[2:]}
	got, err := collectMessages(t, chunks...)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %d bytes; want %d", len(got), len(payload))
	}
}

func TestLengthDelimitedEOFMidMessage(t *testing.T) {
	stream := streamOf(cat(varint(5), []byte{1, 2}))
	err := LengthDelimited(stream, func([]byte) error { return nil })
	if err == nil || err.Error() != "stream ended in the middle of a message" {
		t.Errorf("err = %v; want stream ended in the middle of a message", err)
	}
}

func TestLengthDelimitedCleanEOF(t *testing.T) {
	for name, chunks := range map[string][][]byte{
		"empty stream":        {},
		"complete at the end": {lengthDelimitedOf([]byte{1, 2, 3})},
	} {
		if _, err := collectMessages(t, chunks...); err != nil {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestLengthDelimitedEmitErrorPropagates(t *testing.T) {
	sentinel := io.EOF
	stream := streamOf(lengthDelimitedOf([]byte{1}))
	err := LengthDelimited(stream, func([]byte) error { return sentinel })
	if err != sentinel {
		t.Errorf("err = %v; want %v", err, sentinel)
	}
}
