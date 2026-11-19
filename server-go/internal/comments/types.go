// Shared types for the live-comment relay, port of
// server/lib/comments/types.ts. Pure data + interface: no HTTP here.
package comments

import "context"

// SourceComment is one comment normalized independently of its origin
// (SourceComment in types.ts). This is the JSON shape sent to the client.
type SourceComment struct {
	ID     string `json:"id"`
	Source string `json:"source"` // "nicolive" | "nx-jikkyo"
	At     int64  `json:"at"`     // epoch ms
	Text   string `json:"text"`
	Author string `json:"author,omitempty"` // empty for anonymous comments
}

// Target identifies the channel to fetch comments for, assembled from
// mirakc service info (CommentTarget in types.ts).
type Target struct {
	ID          int64 // mirakc composite service id
	NetworkID   int64
	ServiceID   int64
	Channel     string // MirakurunChannel.channel; "" if unresolved
	ServiceName string
}

// Source is a pluggable comment origin (CommentSource in types.ts).
//
// Subscribe returns nil when the target has no mapping in this source
// (not comment-capable). Otherwise the channel stays open, delivering
// comments until ctx is cancelled. Reconnection on upstream errors is the
// source's own responsibility; closing the channel early just ends this
// source's pump in the SSE relay.
type Source interface {
	ID() string
	// Subscribe returns nil = target unsupported by this source.
	Subscribe(ctx context.Context, target Target) <-chan SourceComment
}
