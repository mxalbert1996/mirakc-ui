import { $api } from "../lib/api/client.ts";

/**
 * 番組表ページ (番組表レイアウト・詳細モーダル・検索モーダル) が共通で使う読み取り
 * クエリ。サービス・録画予約の 2 本をまとめて返す。各ルートで個別に呼んでも
 * TanStack Query が同一 queryKey でキャッシュ共有するため、ネットワークは 1 回で済む。
 * 番組は全量ダンプ (GET /programs, ~24MB) を避けるためここには含めず、各ルートで
 * slim 一覧 (useSlimPrograms) か ID 指定の個別取得 (GET /programs/{id}) を使う。
 */
export function useProgramQueries() {
  const services = $api.useQuery("get", "/services");
  const recordingSchedules = $api.useQuery("get", "/recording/schedules");
  return { services, recordingSchedules };
}
