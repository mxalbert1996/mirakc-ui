/**
 * 保存済み設定の一括エクスポート (`deno task export:data`)。
 *
 * Deno KV の SQLite 形式は内部実装に依存するため、将来の Go サーバ
 * (docs/go-rewrite-plan.md §5.2) が取り込めるよう、全コレクションを
 * `${DATA_DIR}/export.json` に書き出す。各コレクションは store の公開 API
 * (list) と同じ正規化を通した値を出力し、Go 側の import 時検証と一致させる。
 *
 * 通知設定 (シングルトン) だけは未保存を null で表すため、store の get (未保存
 * を既定値に畳める) ではなく同じ正規化関数で生値を読む。
 *
 * 【注意】このファイルには ntfy のアクセストークンと Discord の Webhook URL
 * が平文で含まれる。出力先はユーザーが volume として管理している DATA_DIR
 * 配下だが、移行が済んだら必ず削除すること。
 */
import type { KeywordRule } from "./lib/keyword-rules.ts";
import type { LiveCommentMapping } from "./lib/live-comment-settings.ts";
import {
  normalizeNotificationSettings,
  type NotificationSettings,
} from "./lib/notification-settings.ts";
import { createKv, dataDir, type Kv } from "./store/kv.ts";
import { createKeywordRuleStore } from "./store/keyword-rules.ts";
import { createLiveCommentMappingStore } from "./store/live-comment-settings.ts";

/** エクスポート形式のバージョン。Go 側の import がこの値を見る。 */
const EXPORT_VERSION = 1;

/** 通知設定の KV キー (store/notification-settings.ts と同じ 1 つ)。 */
const NOTIFICATION_KEY = ["settings", "notification"];

/** export.json の中身。collections のキーは Go 側のレコード集合名に合わせる。 */
export type ExportData = {
  version: number;
  exportedAt: string;
  collections: {
    "keyword-rules": KeywordRule[];
    "live-comment": LiveCommentMapping[];
    "notification": NotificationSettings | null;
  };
};

/** kv の全コレクションを読み出してエクスポート用のオブジェクトを組む。 */
export async function collectExportData(kv: Kv): Promise<ExportData> {
  const keywordRules = await createKeywordRuleStore(kv).list();
  const liveComment = await createLiveCommentMappingStore(kv).list();
  // 通知設定は未保存 (と不正値) を null で表す。ストアの get は未保存を
  // 既定値に畳めるため、正規化関数で生値を読んで null を区別する。
  const raw = await kv.get(NOTIFICATION_KEY);
  const notification = raw === null
    ? null
    : normalizeNotificationSettings(raw) ?? null;
  return {
    version: EXPORT_VERSION,
    exportedAt: new Date().toISOString(),
    collections: {
      "keyword-rules": keywordRules,
      "live-comment": liveComment,
      notification,
    },
  };
}

/** エクスポートを `${dir}/export.json` に書き出す。戻り値は書き出したパス。 */
export async function writeExportData(
  kv: Kv,
  dir: string = dataDir(),
): Promise<string> {
  const dump = await collectExportData(kv);
  await Deno.mkdir(dir, { recursive: true });
  const path = `${dir}/export.json`;
  await Deno.writeTextFile(path, `${JSON.stringify(dump, null, 2)}\n`);
  return path;
}

if (import.meta.main) {
  const kv = createKv();
  try {
    console.log(`exported: ${await writeExportData(kv)}`);
  } finally {
    await kv.close();
  }
}
