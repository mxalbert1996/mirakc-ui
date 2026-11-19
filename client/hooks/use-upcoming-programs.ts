import { nowEpochMs, startOfHourEpochMs } from "../lib/datetime.ts";
import { UPCOMING_WINDOW_DAYS } from "../lib/keyword-preview.ts";
import { useSlimPrograms } from "./use-slim-programs.ts";

/**
 * 「今後 N 日間」(N = UPCOMING_WINDOW_DAYS) のプレビュー対象を slim 一覧から
 * 組み立てるための窓。起点はキーが分の刻みで変わらないよう「時」で切り捨てる。
 * 窓の終端は anchor ≤ now の切り捨て分 (最大 59 分) を headroom として 1 時間分
 * だけ先へ張る — buildUpcoming は startAt < now + N 日 で判定するため、この分が
 * 無いと末尾の番組が落ちる。日跨ぎの放送中番組のため 12 時間分だけ過去に張る
 * (buildUpcoming が改めて正確に絞るため過剰取得で問題ない)。
 */
export function useUpcomingPrograms() {
  const anchor = startOfHourEpochMs(nowEpochMs());
  return useSlimPrograms({
    from: anchor - 12 * 3600_000,
    to: anchor + (UPCOMING_WINDOW_DAYS * 24 + 1) * 3600_000,
  });
}
