/**
 * Keyword auto-recording rules: the rule type and the match predicate.
 *
 * Client-side contract module. The Go server (server-go/internal/settings)
 * implements the same contract from the same docs/api OpenAPI components;
 * drift is caught by the CI schema regen and the Go tests. Persistence and
 * request validation are server-side concerns — this module keeps what the
 * client uses: the rule type (single-sourced from the generated component
 * schema), the match target, and the match logic shared by the rule
 * management UI and the keyword preview.
 */
import type { FromSchema } from "json-schema-to-ts";
import type { internalSchemas } from "../api/internal-schemas.ts";

/**
 * キーワード自動録画のルール。docs/api の OpenAPI から生成した component
 * schema (internal-schemas.ts) を型の単一ソースとする。
 * (id / keyword / channels / genres / enabled / createdAt を持ち、
 * from / to は任意。)
 */
export type KeywordRule = FromSchema<(typeof internalSchemas)["KeywordRule"]>;

/** 一致判定の対象となる番組情報 (MirakurunProgram のサブセット)。 */
export type KeywordRuleTarget = {
  name?: string | null;

  /** 開始時刻 (epoch ms)。期間条件のローカル日付判定に使う。 */
  startAt: number;

  /** 番組が属するチャンネル (MirakurunChannel.channel)。サービスから解決する。 */
  channelId?: string;

  /** ジャンルの ARIB lv1 コード一覧。 */
  genres: number[];
};

/** RFC 3339 日時文字列を Temporal.Instant にする。不正なら null (オフセット必須)。 */
function toInstant(value: string): Temporal.Instant | null {
  try {
    return Temporal.Instant.from(value);
  } catch {
    return null;
  }
}

/**
 * 番組がルールに一致するか。enabled は見ない (編集モーダルのプレビューが
 * 下書きルールで使うため)。有効ルールだけに適用するのは呼び出し側の責務。
 *
 * - キーワード: 番組名の部分一致 (大文字小文字無視)。名前なしは不一致
 * - 期間: 開始時刻 (瞬間) が from / to の RFC 3339 日時の範囲内 (両端含む)。
 *   from / to はタイムゾーン付きの絶対時刻なので瞬間どうしで比較する
 * - channels / genres: 空は無条件、指定時は一致 (genres は交差) を要求。
 *   channels は番組の属するチャンネル (MirakurunChannel.channel) で判定するため、
 *   チャンネル配下の全サービスを横断して対象になる
 */
export function matchesKeywordRule(
  rule: Pick<KeywordRule, "keyword" | "from" | "to" | "channels" | "genres">,
  target: KeywordRuleTarget,
): boolean {
  if (!target.name?.toLowerCase().includes(rule.keyword.toLowerCase())) {
    return false;
  }

  if (rule.from !== undefined) {
    const from = toInstant(rule.from);
    if (from !== null && target.startAt < from.epochMilliseconds) {
      return false;
    }
  }
  if (rule.to !== undefined) {
    const to = toInstant(rule.to);
    if (to !== null && target.startAt > to.epochMilliseconds) {
      return false;
    }
  }

  if (rule.channels.length > 0) {
    if (target.channelId === undefined || !rule.channels.includes(target.channelId)) {
      return false;
    }
  }

  if (rule.genres.length > 0) {
    if (!target.genres.some((lv1) => rule.genres.includes(lv1))) {
      return false;
    }
  }

  return true;
}
