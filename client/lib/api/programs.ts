/**
 * 番組一覧 API (/api/programs) のクライアント。
 *
 * mirakc の GET /programs は全番組の詳細 (description / extended 等) を含む巨大な
 * ダンプ (~24MB) になるため、mirakc-ui 自身の Hono が slim 化した一覧を返す専用
 * エンドポイントを用意している (server-go の programs cache)。$api (mirakc の
 * OpenAPI から生成) には含まれないため、keyword-rules と同じく素の fetch を
 * 薄くラップする。slim 一覧に無い description / extended 等が必要な場合は
 * getProgram (GET /api/mirakc/programs/{id}) で個別に取得する。
 */
import type { components } from "./schema.d.ts";

type MirakurunProgram = components["schemas"]["MirakurunProgram"];

/**
 * 番組の slim 形式。UI の描画・キーワードマッチングに不要な重いフィールド
 * (audio / audios / description / extended / relatedItems / series / video) を
 * MirakurunProgram から除いたサブセット。除いたフィールドはいずれも任意のため
 * Program 型の props にはそのまま流せる。
 */
export type SlimProgram = Omit<
  MirakurunProgram,
  "audio" | "audios" | "description" | "extended" | "relatedItems" | "series" | "video"
>;

const BASE_PATH = "/api/programs";

async function ensureOk(res: Response): Promise<Response> {
  if (!res.ok) {
    await res.body?.cancel();
    throw new Error(`programs api failed: ${res.status}`);
  }
  return res;
}

/**
 * slim 化した番組一覧を取得する。from/to (epoch ms) を両方指定すると放送時間が
 * その窓と重複する番組だけに絞る。省略時は全件 (検索モーダルのスコープ)。
 */
export async function fetchSlimPrograms(
  window?: { from?: number; to?: number },
  fetchFn: typeof fetch = fetch,
): Promise<SlimProgram[]> {
  const params = new URLSearchParams();
  if (window?.from !== undefined && window?.to !== undefined) {
    params.set("from", String(window.from));
    params.set("to", String(window.to));
  }
  const url = params.size > 0 ? `${BASE_PATH}?${params}` : BASE_PATH;
  const res = await ensureOk(await fetchFn(url));
  return await res.json();
}

/**
 * getProgram (GET /api/mirakc/programs/{id}) のクエリ結果から単一番組を取り出す。
 *
 * mirakc の OpenAPI はこの操作の 200 応答を MirakurunProgram[] と型付けるが、
 * 実装 (mirakc 本体) は単一オブジェクトを返す。生成型と実応答の食い違いをここで
 * 吸収し、どちらの形でも動くように正規化する。
 */
export function fullProgramOf(data: MirakurunProgram[] | undefined): MirakurunProgram | undefined {
  if (Array.isArray(data)) {
    return data[0];
  }
  return data as unknown as MirakurunProgram | undefined;
}
