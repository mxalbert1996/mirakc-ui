// Package ndgr decodes NDGR (NicoLive's post-2024/08 message server) wire
// responses: a minimal hand-rolled protobuf reader plus the ChunkedEntry /
// ChunkedMessage subsets needed for live comment reception. Port of
// server/lib/comments/protobuf.ts and server/lib/comments/ndgr.ts.
//
// Field-number provenance (upstream schema definitions, MIT):
//
//	https://github.com/n-air-app/nicolive-comment-protobuf
//	- proto/dwango/nicolive/chat/service/edge/payload.proto
//	  ChunkedEntry { segment=1, backward=2, previous=3, next=4 }
//	  MessageSegment { from=1, until=2, uri=3 }
//	  ChunkedEntry.ReadyForNext { at=1 }
//	  ChunkedMessage { meta=1, message=2, state=4, signal=5 }
//	  ChunkedMessage.Meta { id=1, at=2 }
//	- proto/dwango/nicolive/chat/data/message.proto
//	  NicoliveMessage { chat=1, ..., overflowed_chat=20 }
//	- proto/dwango/nicolive/chat/data/atoms.proto
//	  Chat { content=1, name=2, vpos=3, ... }
//
// Protocol-flow reference implementation (MIT):
//
//	https://github.com/tsukumijima/NDGRClient
package ndgr

// Entry is the View-API ChunkedEntry subset used for live reception.
type Entry struct {
	// SegmentURI is the Segment API URI to receive from (segment entry).
	// "" when absent.
	SegmentURI string
	// NextAt is the View-API reconnect cursor (next entry, epoch seconds).
	// 0 when absent.
	NextAt int64
}

// DecodeChunkedEntry decodes a View-API ChunkedEntry. backward/previous are
// ignored (only used for replaying past logs). Malformed input yields the
// zero Entry (the Deno original throws; callers here feed server responses
// and treat undecodable data as empty).
func DecodeChunkedEntry(b []byte) Entry {
	var entry Entry
	fields, err := Fields(b)
	if err != nil {
		return entry
	}
	for _, field := range fields {
		if field.WireType != 2 {
			continue
		}
		switch field.FieldNo {
		case 1: // segment: MessageSegment { uri = 3 }
			if sub, err := Fields(field.Bytes); err == nil {
				if uri, ok := stringOf(findField(sub, 3)); ok {
					entry.SegmentURI = uri
				}
			}
		case 4: // next: ReadyForNext { at = 1 }
			if sub, err := Fields(field.Bytes); err == nil {
				if f, ok := findField(sub, 1); ok && f.WireType == 0 {
					entry.NextAt = int64(f.Varint)
				}
			}
		}
	}
	return entry
}

// Message is the Segment-API ChunkedMessage subset; nil when the message
// carries no chat (state/signal-only).
type Message struct {
	// ID is the message ID (used for dedup).
	ID string
	// AtMs is the post time (epoch milliseconds).
	AtMs int64
	// Chat is nil for non-chat messages (gifts, system notices, ...).
	Chat *Chat
}

// Chat is the decoded body of a chat (or overflowed_chat) message.
type Chat struct {
	Content string
	Name    string // "" when absent
}

// DecodeChunkedMessage decodes a Segment-API ChunkedMessage. It returns nil
// when meta is missing or has no id/at. chat (=1) and overflowed_chat (=20)
// of NicoliveMessage are treated as comments; everything else (state,
// signal, gift, ...) yields a Message with Chat == nil. Malformed input
// yields nil (the Deno original throws).
func DecodeChunkedMessage(b []byte) *Message {
	fields, err := Fields(b)
	if err != nil {
		return nil
	}
	meta, ok := findField(fields, 1)
	if !ok || meta.WireType != 2 {
		return nil
	}
	metaFields, err := Fields(meta.Bytes)
	if err != nil {
		return nil
	}
	id, ok := stringOf(findField(metaFields, 1))
	if !ok {
		return nil
	}
	at, ok := findField(metaFields, 2)
	if !ok || at.WireType != 2 {
		return nil
	}

	var chat *Chat
	if message, ok := findField(fields, 2); ok && message.WireType == 2 {
		if messageFields, err := Fields(message.Bytes); err == nil {
			chatField, ok := findField(messageFields, 1)
			if !ok {
				chatField, ok = findField(messageFields, 20)
			}
			if ok && chatField.WireType == 2 {
				if chatFields, err := Fields(chatField.Bytes); err == nil {
					if content, ok := stringOf(findField(chatFields, 1)); ok {
						chat = &Chat{Content: content}
						if name, ok := stringOf(findField(chatFields, 2)); ok {
							chat.Name = name
						}
					}
				}
			}
		}
	}

	return &Message{ID: id, AtMs: epochMs(at.Bytes), Chat: chat}
}

// epochMs converts google.protobuf.Timestamp { seconds=1, nanos=2 } bytes to
// epoch milliseconds (mirrors epochMsOf in ndgr.ts, floor division of nanos).
func epochMs(timestamp []byte) int64 {
	fields, err := Fields(timestamp)
	if err != nil {
		return 0
	}
	var seconds, nanos int64
	if f, ok := findField(fields, 1); ok && f.WireType == 0 {
		seconds = int64(f.Varint)
	}
	if f, ok := findField(fields, 2); ok && f.WireType == 0 {
		nanos = int64(f.Varint)
	}
	return seconds*1000 + floorDiv(nanos, 1_000_000)
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// findField returns the first field with the given field number (mirrors
// findField in ndgr.ts).
func findField(fields []Field, fieldNo int32) (Field, bool) {
	for _, f := range fields {
		if f.FieldNo == fieldNo {
			return f, true
		}
	}
	return Field{}, false
}

// stringOf returns the LEN payload of a field as a string (mirrors stringOf
// in ndgr.ts: undefined when the field is absent or not wire type 2).
func stringOf(f Field, ok bool) (string, bool) {
	if !ok || f.WireType != 2 {
		return "", false
	}
	return string(f.Bytes), true
}
