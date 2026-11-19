// Minimal protobuf wire-format reader for NDGR reception. Port of
// server/lib/comments/protobuf.ts. Only what NDGR decoding needs is
// implemented (see ndgr.go); no code generation from schema definitions.
// Wire format spec: https://protobuf.dev/programming-guides/encoding/
package ndgr

import (
	"errors"
	"fmt"
	"io"
)

// readVarint reads one varint from b at offset. Mirrors readVarint in
// protobuf.ts: it errors on a truncated varint and rejects varints longer
// than 10 bytes (the Deno original guards 64-bit values with bigint; Go's
// uint64 is native).
func readVarint(b []byte, offset int) (value uint64, next int, err error) {
	next = offset
	var shift uint
	for {
		if next >= len(b) {
			return 0, 0, errors.New("truncated varint")
		}
		c := b[next]
		next++
		value |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return value, next, nil
		}
		shift += 7
		if shift > 63 {
			return 0, 0, errors.New("varint too long")
		}
	}
}

// Field is one wire-format field. Varint is valid when WireType == 0 and
// Bytes when WireType == 2. I32/I64 fields are skipped by Fields and never
// appear in the result.
type Field struct {
	FieldNo  int32
	WireType int32
	Varint   uint64
	Bytes    []byte
}

// Fields enumerates the fields of a protobuf message. VARINT (0) and LEN (2)
// keep their values; I64 (1) and I32 (5) are skipped (not needed for NDGR
// decoding). Port of fieldsOf in protobuf.ts.
func Fields(message []byte) ([]Field, error) {
	var fields []Field
	offset := 0
	for offset < len(message) {
		tag, next, err := readVarint(message, offset)
		if err != nil {
			return nil, err
		}
		offset = next
		fieldNo := int32(tag >> 3)
		wireType := int32(tag & 7)
		switch wireType {
		case 0:
			v, next, err := readVarint(message, offset)
			if err != nil {
				return nil, err
			}
			offset = next
			fields = append(fields, Field{FieldNo: fieldNo, WireType: wireType, Varint: v})
		case 1:
			offset += 8
		case 2:
			length, start, err := readVarint(message, offset)
			if err != nil {
				return nil, err
			}
			if uint64(len(message)-start) < length {
				return nil, errors.New("truncated length-delimited field")
			}
			end := start + int(length)
			fields = append(fields, Field{FieldNo: fieldNo, WireType: wireType, Bytes: message[start:end]})
			offset = end
		case 5:
			offset += 4
		default:
			return nil, fmt.Errorf("unsupported wire type: %d", wireType)
		}
		if offset > len(message) {
			return nil, errors.New("truncated field")
		}
	}
	return fields, nil
}

// LengthDelimited splits a stream of varint-length-prefixed protobuf
// messages (the NDGR View/Segment API response format) into single
// messages, reassembling messages whose length prefix or body crosses a
// chunk boundary. It errors with "stream ended in the middle of a message"
// when the stream ends with a leftover partial message. Port of
// lengthDelimited in protobuf.ts (callback style instead of a generator).
func LengthDelimited(r io.Reader, emit func([]byte) error) error {
	var buffer []byte
	done := false
	chunk := make([]byte, 32*1024)
	for {
		// Extract as many complete messages as the buffer allows.
		for len(buffer) > 0 {
			length, next, err := readVarint(buffer, 0)
			if err != nil {
				break // length prefix not complete yet
			}
			if uint64(len(buffer)-next) < length {
				break // body not complete yet
			}
			end := next + int(length)
			// Copy (the Deno original uses slice()) so the emit callback
			// may retain the message.
			message := append([]byte(nil), buffer[next:end]...)
			if err := emit(message); err != nil {
				return err
			}
			buffer = buffer[end:]
		}
		if done {
			if len(buffer) > 0 {
				return errors.New("stream ended in the middle of a message")
			}
			return nil
		}
		n, err := r.Read(chunk)
		if n > 0 {
			buffer = append(buffer, chunk[:n]...)
		}
		if err == io.EOF {
			done = true
			continue
		}
		if err != nil {
			return err
		}
	}
}
