// Port of server/lib/comments/ndgr-fixtures.ts (test-only): builders that
// produce hand-crafted NDGR protobuf bytes.
package ndgr

import "io"

// varint encodes a test value (test-only counterpart of readVarint).
func varint(value uint64) []byte {
	var out []byte
	for value >= 0x80 {
		out = append(out, byte(value&0x7f)|0x80)
		value >>= 7
	}
	return append(out, byte(value))
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// lenField builds a LEN-type (wire type 2) field.
func lenField(fieldNo int32, payload []byte) []byte {
	return cat(varint(uint64(fieldNo)<<3|2), varint(uint64(len(payload))), payload)
}

// varintField builds a VARINT-type (wire type 0) field.
func varintField(fieldNo int32, value uint64) []byte {
	return cat(varint(uint64(fieldNo)<<3), varint(value))
}

// stringField builds a LEN-type field holding a string.
func stringField(fieldNo int32, value string) []byte {
	return lenField(fieldNo, []byte(value))
}

// lengthDelimitedOf prefixes each message with a varint length and
// concatenates them (the View / Segment API response format).
func lengthDelimitedOf(messages ...[]byte) []byte {
	var out []byte
	for _, m := range messages {
		out = cat(out, varint(uint64(len(m))), m)
	}
	return out
}

// segmentEntryOf builds ChunkedEntry { segment(1): MessageSegment { uri(3) } }.
func segmentEntryOf(uri string) []byte {
	return lenField(1, stringField(3, uri))
}

// nextEntryOf builds ChunkedEntry { next(4): ReadyForNext { at(1) } }.
func nextEntryOf(atSeconds int64) []byte {
	return lenField(4, varintField(1, uint64(atSeconds)))
}

type chatMessageOptions struct {
	id          string
	atSeconds   int64
	atNanos     *int64
	content     *string
	name        *string
	chatFieldNo int32 // 0 means the default 1 (overflowed_chat is 20)
}

// chatMessageOf builds ChunkedMessage { meta(1): { id(1), at(2) },
// message(2): { chat(1): { content(1), name(2)? } } }.
func chatMessageOf(o chatMessageOptions) []byte {
	var atNanos []byte
	if o.atNanos != nil {
		atNanos = varintField(2, uint64(*o.atNanos))
	}
	meta := cat(
		stringField(1, o.id),
		lenField(2, cat(varintField(1, uint64(o.atSeconds)), atNanos)),
	)
	var chat []byte
	if o.content != nil {
		var name []byte
		if o.name != nil {
			name = stringField(2, *o.name)
		}
		chat = cat(
			stringField(1, *o.content),
			name,
			varintField(3, 100), // vpos: present in real data, unused here
		)
	}
	out := lenField(1, meta)
	if chat != nil {
		fieldNo := o.chatFieldNo
		if fieldNo == 0 {
			fieldNo = 1
		}
		out = cat(out, lenField(2, lenField(fieldNo, chat)))
	}
	return out
}

// chunkReader delivers one chunk per Read, like ReadableStream in the tests
// of the Deno original.
type chunkReader struct {
	chunks [][]byte
	i      int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		return 0, io.EOF
	}
	c := r.chunks[r.i]
	r.i++
	return copy(p, c), nil
}

func streamOf(chunks ...[]byte) io.Reader {
	return &chunkReader{chunks: chunks}
}
