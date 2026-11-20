/**
 * Normalized live-comment shapes received from the server's SSE stream.
 *
 * Client-side contract module. The Go server (server-go) serves the same SSE
 * comment stream described by docs/api; drift is caught by the CI schema
 * regen and the Go tests. Source pluggability (how each source fetches
 * comments) is a server-side concern implemented in Go.
 */

/** コメントソースの識別子。ソースを追加したらここに足す。 */
export type CommentSourceId = "nicolive" | "nx-jikkyo";

/** ソース非依存に正規化した実況コメント 1 件 (SSE で client へ流す形)。 */
export type SourceComment = {
  /** ソース内で一意なコメント ID。 */
  id: string;
  /** 取得元ソース。 */
  source: CommentSourceId;
  /** 投稿時刻 (epoch ミリ秒)。 */
  at: number;
  /** 本文。 */
  text: string;
  /** 投稿者名。匿名コメントでは undefined。 */
  author?: string;
};
