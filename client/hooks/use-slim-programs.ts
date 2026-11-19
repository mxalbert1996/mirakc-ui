import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { fetchSlimPrograms } from "../lib/api/programs.ts";

/**
 * slim 化した番組一覧 (/api/programs) を読み取るクエリ。mirakc の全量ダンプ
 * (GET /programs, ~24MB) の代わりに、放送時間が窓 (from/to) と重複する番組だけを
 * 取得する。窓を省略したときは全件 (検索モーダルのスコープ)。
 *
 * queryKey に窓の端点を含めるため、呼び出し側は from/to を「時」で切り捨てた値
 * (startOfHourEpochMs) で渡すこと。さもないと再 render のたびにキーが変わり
 * 何度も再取得される。
 *
 * 窓の移動 (時刻・日付の境目) でキーが変わるため placeholderData で前の窓の
 * データを出し続ける — 取得中に画面が空白になるのを防ぐ。消費側はどちらも
 * 改めて絞り込むため、前の窓のデータが一時的に見えても問題ない。
 */
export function useSlimPrograms(window?: { from?: number; to?: number }) {
  return useQuery({
    queryKey: ["slim-programs", window?.from, window?.to],
    queryFn: () => fetchSlimPrograms(window),
    placeholderData: keepPreviousData,
  });
}
