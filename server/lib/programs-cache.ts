/**
 * mirakc 番組一覧 (/programs) のキャッシュ。
 *
 * mirakc の GET /programs は全番組の詳細 (description / extended 等) を含む
 * 巨大なダンプ (~24MB) だが、UI の番組表とキーワード録画ジョブのどちらも
 * 必要なのは一部のフィールドだけ。そこで 1 回の取得を slim 化 (必要フィールド
 * だけの明示マッピング) して保持し、両者で共有する。EPG 更新
 * (epg.programs-updated) で invalidate し、SSE の取りこぼしに備えて TTL の
 * 期限切れでも再取得する。
 */

/** MirakurunProgram.genre (ARIB ジャンル)。slim でもそのまま持たせる。 */
type SlimGenre = { lv1: number; lv2: number; un1: number; un2: number };

/**
 * 番組の slim 形式。MirakurunProgram から UI・キーワードマッチングに不要な
 * 重いフィールド (audio / audios / description / extended / relatedItems /
 * series / video) を除いたサブセット。省略したフィールドはいずれも
 * MirakurunProgram の任意フィールドのため、構造互換性は保たれる。
 */
export type SlimProgram = {
  id: number;
  eventId: number;
  isFree: boolean;
  name?: string | null;
  startAt: number;
  duration: number;
  networkId: number;
  serviceId: number;
  genres?: SlimGenre[] | null;
};

/** GET /programs の応答のうち slim 化に読むフィールド (MirakurunProgram のサブセット)。 */
type UpstreamProgram = SlimProgram;

function toSlim(program: UpstreamProgram): SlimProgram {
  return {
    id: program.id,
    eventId: program.eventId,
    isFree: program.isFree,
    name: program.name,
    startAt: program.startAt,
    duration: program.duration,
    networkId: program.networkId,
    serviceId: program.serviceId,
    genres: program.genres,
  };
}

/** TTL の既定値 (10 分)。EPG 更新 SSE を取りこぼした場合のフォールバック。 */
const DEFAULT_TTL_MS = 10 * 60_000;

export type ProgramsCacheOptions = {
  /** mirakc の Web API のベース URL (mirakcApiUrlOf で構築)。 */
  apiUrl: string;

  fetchFn?: typeof fetch;

  /** キャッシュの有効期限 (ms)。既定 10 分。 */
  ttlMs?: number;

  /** 現在時刻 (epoch ms)。テストで固定できるよう注入可能。 */
  now?: () => number;
};

export type ProgramsCache = {
  /** 番組一覧 (slim)。未取得・期限切れなら mirakc から取得する。 */
  getPrograms(): Promise<SlimProgram[]>;

  /** キャッシュを破棄する (次回の getPrograms で再取得)。 */
  invalidate(): void;
};

export function createProgramsCache(
  options: ProgramsCacheOptions,
): ProgramsCache {
  const url = `${options.apiUrl.replace(/\/$/, "")}/programs`;
  const fetchFn = options.fetchFn ?? fetch;
  const ttlMs = options.ttlMs ?? DEFAULT_TTL_MS;
  const now = options.now ?? (() => Date.now());

  let entry: { programs: SlimProgram[]; fetchedAt: number } | null = null;
  let inflight: { promise: Promise<SlimProgram[]>; generation: number } | null =
    null;
  // invalidate() のたびに増やす世代。取得開始時の世代と一致するときだけ
  // コミットする — 取得中に invalidate されても、古いデータが TTL いっぱい
  // キャッシュに戻るのを防ぐ。
  let generation = 0;

  async function fetchPrograms(): Promise<SlimProgram[]> {
    const res = await fetchFn(url);
    if (!res.ok) {
      await res.body?.cancel();
      throw new Error(`GET ${url} failed: ${res.status}`);
    }
    return (await res.json() as UpstreamProgram[]).map(toSlim);
  }

  const load = async (): Promise<SlimProgram[]> => {
    // 並行呼び出しは同一の in-flight promise を共有する (単一フライト)。
    const gen = generation;
    const promise = fetchPrograms();
    inflight = { promise, generation: gen };
    try {
      const programs = await promise;
      if (gen === generation) {
        entry = { programs, fetchedAt: now() };
      }
      return programs;
    } catch (e) {
      // 失敗時にキャッシュを残さない — 次回の呼び出しで再試行する。
      if (gen === generation) {
        entry = null;
      }
      throw e;
    } finally {
      if (inflight?.generation === gen) {
        inflight = null;
      }
    }
  };

  return {
    getPrograms() {
      if (entry !== null && now() - entry.fetchedAt < ttlMs) {
        return Promise.resolve(entry.programs);
      }
      if (inflight !== null && inflight.generation === generation) {
        return inflight.promise;
      }
      return load();
    },
    invalidate() {
      entry = null;
      generation++;
    },
  };
}
