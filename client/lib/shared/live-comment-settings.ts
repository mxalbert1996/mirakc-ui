/**
 * Live-comment mapping settings: the source list and per-source channel ID
 * format checks.
 *
 * Client-side contract module. The Go server (server-go/internal/settings)
 * implements the same contract from the same docs/api OpenAPI; drift is
 * caught by the CI schema regen and the Go tests. Persistence and request
 * validation are server-side concerns — this module keeps what the mapping
 * form uses: the assignable sources and the channel ID format check per
 * source.
 *
 * 取得元 (source) ごとに ID 形式が違う:
 *   - nicolive  : ニコ生チャンネル ID "ch2646436" (watch URL 末尾)
 *   - nx-jikkyo : 実況チャンネル番号 "jk1"
 */

/** 設定で割り当てを持つ取得元 (表示順)。 */
export const LIVE_COMMENT_SOURCE_IDS = ["nicolive", "nx-jikkyo"] as const;

export type LiveCommentSourceId = (typeof LIVE_COMMENT_SOURCE_IDS)[number];

/** 取得元ごとのチャンネル ID 形式。 */
const ID_PATTERN: Record<LiveCommentSourceId, RegExp> = {
  nicolive: /^ch\d+$/,
  "nx-jikkyo": /^jk\d+$/,
};

export function isValidChannelId(source: LiveCommentSourceId, value: string): boolean {
  return ID_PATTERN[source].test(value);
}
