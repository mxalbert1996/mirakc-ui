import { Hono } from "hono";
import {
  buildVideoEncoderArgs,
  buildVideoFilter,
  encoderChain,
  type EncoderName,
  pipeStderr,
  probeVideoEncoder,
  v4l2ConstantQualityEnabled,
} from "../lib/encoder.ts";
import { normalizeQuality, type Quality } from "../lib/quality.ts";
import { mirakcApiUrlOf } from "../lib/mirakc.ts";

/**
 * ライブ視聴のトランスコード配信 (A 方式, #11)。mirakc のサービスストリーム →
 * tsreadex (字幕・音声整形) → ffmpeg (再エンコード) → MPEG-TS をチャンク応答
 * する。`app.route("/api/transcode", transcode)` でマウントする。
 *
 * エンコーダは probe で選ぶ。`codec=hevc` を付けたクライアント (HEVC を再生
 * できると自己申告した端末) にだけ HEVC を先頭に置き、失敗時は H.264 の
 * HW → ソフトウェアへ落とす。probe 成功後にデバイスを奪われる競合に備えて、
 * 実際に最初のチャンクが出るまでを試行とみなし、失敗したら次の候補へ進む。
 */
export const transcode = new Hono();

/** ffmpeg が起動して最初の出力を返すまでの上限。 */
const FIRST_CHUNK_TIMEOUT_MS = 15000;

/** 停止処理の完了待ちの上限。SIGKILL しても回収されないプロセスで固まらないため。 */
const STOP_WAIT_MS = 4000;

/**
 * 切り替え直後は旧ストリームのチューナー解放を待つ必要がある。mirakc は
 * チューナー不足を 404 (TunerUnavailable) で返すので、404 はサービス不在
 * だけでなく「まだ解放されていない」も意味する (実測で解放まで最大 ~1.8 秒)。
 */
const UPSTREAM_RETRY_WINDOW_MS = 3000;
const UPSTREAM_RETRY_MIN_MS = 100;
const UPSTREAM_RETRY_MAX_MS = 500;

/** `promise` が ms 以内に解決しなければ諦める (待ち続けてリクエストを止めない)。 */
function withTimeout(promise: Promise<void>, ms: number): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<void>((resolve) => {
    timer = setTimeout(() => resolve(), ms);
  });
  return Promise.race([promise.catch(() => {}), timeout]).finally(() =>
    clearTimeout(timer)
  );
}

type Attempt = {
  ffmpeg: Deno.ChildProcess;
  tsreadex: Deno.ChildProcess | null;
  reader: ReadableStreamDefaultReader<Uint8Array>;
  first: Uint8Array;
  stop: (reason: string) => Promise<void>;
};

type StartOptions = {
  encoder: EncoderName;
  quality: Quality;
  v4l2Cq: boolean;
  audioTrackIndex: number;
  rawMode: boolean;
  streamUrl: string;
  serviceId: string;
  signal: AbortSignal;
};

function spawnFfmpeg(opts: StartOptions): Deno.ChildProcess {
  const child = new Deno.Command("ffmpeg", {
    args: [
      "-fflags",
      "nobuffer",
      "-analyzeduration",
      "0",
      "-i",
      "pipe:0",
      "-map",
      "0:v:0",
      "-map",
      `0:a:${opts.audioTrackIndex}?`,
      "-map",
      "0:d?",
      "-ignore_unknown",
      "-max_delay",
      "250000",
      "-vf",
      buildVideoFilter(opts.encoder, opts.quality),
      ...buildVideoEncoderArgs(
        opts.encoder,
        opts.quality,
        opts.v4l2Cq,
      ),
      "-c:a",
      "aac",
      "-b:a",
      "192k",
      "-c:d",
      "copy",
      "-f",
      "mpegts",
      "pipe:1",
    ],
    stdin: "piped",
    stdout: "piped",
    stderr: "piped",
  }).spawn();
  pipeStderr(child.stderr, "[ffmpeg]");
  return child;
}

/** ARIB 字幕周りを整形しつつストリームを安定化する (raw モードでは使わない)。 */
function spawnTsreadex(): Deno.ChildProcess {
  const child = new Deno.Command("tsreadex", {
    args: [
      "-n",
      "-1",
      "-a",
      "13",
      "-b",
      "5",
      "-c",
      "5",
      "-u",
      "1",
      "-d",
      "13",
      "-",
    ],
    stdin: "piped",
    stdout: "piped",
    stderr: "piped",
  }).spawn();
  pipeStderr(child.stderr, "[tsreadex]");
  return child;
}

/** 上流 (mirakc) の失敗。エンコーダを替えても結果は変わらない。 */
class UpstreamError extends Error {
  constructor(readonly status: number) {
    super("Failed to fetch stream from mirakc");
  }
}

/** チューナー不足として再試行する status。他は即座に返す。 */
const UPSTREAM_RETRY_STATUSES = new Set([404, 503]);

/** signal で中断できる待機。 */
function abortableDelay(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const finish = () => {
      clearTimeout(timer);
      signal.removeEventListener("abort", finish);
      resolve();
    };
    const timer = setTimeout(finish, ms);
    signal.addEventListener("abort", finish, { once: true });
  });
}

/**
 * mirakc のストリームを取得する。404/503 (チューナー不足) の間だけ待って
 * 再試行し、200 やその他の status はそのまま返す。待ち時間は 100ms から
 * 1.5 倍ずつ増やして 500ms で頭打ちにする。期限切れ・中断時は最後の応答を
 * 返す (呼び出し側が status をそのまま応答に使う)。
 */
export async function fetchUpstream(
  url: string,
  signal: AbortSignal,
  fetcher: typeof fetch = fetch,
  windowMs = UPSTREAM_RETRY_WINDOW_MS,
): Promise<Response> {
  const deadline = Date.now() + windowMs;
  let wait = UPSTREAM_RETRY_MIN_MS;
  while (true) {
    const res = await fetcher(url, { signal });
    if (!UPSTREAM_RETRY_STATUSES.has(res.status)) {
      return res;
    }
    // 再試行する応答の body (mirakc の JSON エラー) は読まないので捨てる。
    res.body?.cancel().catch(() => {});
    const remaining = deadline - Date.now();
    if (signal.aborted || remaining <= 0) {
      return res;
    }
    await abortableDelay(Math.min(wait, remaining), signal);
    // 中断されたら次の fetch を投げない (abort 済みの signal は即座に reject する)。
    if (signal.aborted) {
      return res;
    }
    wait = Math.min(Math.round(wait * 1.5), UPSTREAM_RETRY_MAX_MS);
  }
}

/**
 * mirakc から取り、tsreadex・ffmpeg を繋いで最初のチャンクまで待つ。
 * 最初のチャンクが出なければ (ffmpeg 即終了 / デバイス占有 / タイムアウト)
 * 後始末して null を返す。呼び出し側は次の候補へ進む。
 */
async function startAttempt(opts: StartOptions): Promise<Attempt | null> {
  const upstream = await fetchUpstream(opts.streamUrl, opts.signal);
  if (!upstream.ok || !upstream.body) {
    // 中断で打ち切った場合はチューナー不足による失敗ではないので、このログは出さない。
    if (!opts.signal.aborted) {
      console.error(
        `[transcode] mirakc stream failed serviceId=${opts.serviceId} status=${upstream.status}`,
      );
    }
    // 再試行しなかった失敗応答の body も、呼び出し側が status しか使わないので閉じる。
    upstream.body?.cancel().catch(() => {});
    throw new UpstreamError(upstream.ok ? 502 : upstream.status);
  }

  const tsreadex = opts.rawMode ? null : spawnTsreadex();
  let ffmpeg: Deno.ChildProcess;
  try {
    ffmpeg = spawnFfmpeg(opts);
  } catch (e) {
    try {
      tsreadex?.kill("SIGKILL");
    } catch {
      // already exited
    }
    upstream.body.cancel().catch(() => {});
    throw e;
  }

  let stopPromise: Promise<void> | null = null;
  let reader: ReadableStreamDefaultReader<Uint8Array> | null = null;
  /**
   * 再入可能な停止処理。SIGTERM → 3 秒で SIGKILL。子プロセスが実際に終了する
   * まで resolve しない — V4L2 のデバイスは ffmpeg の終了時に解放されるため、
   * 次の試行の probe はこれを await してから行う。
   */
  const stop = (reason: string): Promise<void> => {
    if (stopPromise !== null) {
      return stopPromise;
    }
    console.error(
      `[transcode] stop serviceId=${opts.serviceId} encoder=${opts.encoder} reason=${reason}`,
    );
    // ffmpeg は stdout パイプが詰まると SIGTERM を無視して書き込みでブロックし
    // 続け、SIGKILL まで V4L2 デバイスを握る。先に reader を閉じて EPIPE で
    // 自発的に終了させる (実測 3 秒 → 約 120ms)。
    reader?.cancel().catch(() => {});
    for (const child of [tsreadex, ffmpeg]) {
      try {
        child?.kill("SIGTERM");
      } catch {
        // already exited
      }
    }
    const killTimer = setTimeout(() => {
      for (const child of [tsreadex, ffmpeg]) {
        try {
          child?.kill("SIGKILL");
        } catch {
          // already exited
        }
      }
    }, 3000);
    upstream.body?.cancel().catch(() => {});
    stopPromise = Promise.all([
      tsreadex?.status.catch(() => {}),
      ffmpeg.status.catch(() => {}),
    ]).then(() => {
      clearTimeout(killTimer);
    });
    return stopPromise;
  };

  // pipeTo の catch ハンドラは stop を先に呼び得るので、reader はここで作る。
  reader = ffmpeg.stdout.getReader();
  if (opts.rawMode) {
    upstream.body.pipeTo(ffmpeg.stdin).catch(() => {
      stop("mirakc -> ffmpeg pipe ended");
    });
  } else {
    upstream.body.pipeTo(tsreadex!.stdin).catch(() => {
      stop("mirakc -> tsreadex pipe ended");
    });
    tsreadex!.stdout.pipeTo(ffmpeg.stdin).catch(() => {
      stop("tsreadex -> ffmpeg pipe ended");
    });
  }

  let first: Uint8Array | null;
  try {
    first = await firstChunk(reader, ffmpeg, opts.signal);
  } catch (e) {
    // ここで落とすと status ハンドラが付かないまま ffmpeg がデバイスを握り続ける。
    await withTimeout(stop("first chunk failed"), STOP_WAIT_MS);
    throw e;
  }
  if (first === null) {
    // 子プロセスの終了 (= デバイス解放) を待ってから次の候補へ進む。
    await withTimeout(stop("no output"), STOP_WAIT_MS);
    return null;
  }

  return {
    ffmpeg,
    tsreadex,
    reader,
    first,
    stop,
  };
}

async function firstChunk(
  reader: ReadableStreamDefaultReader<Uint8Array>,
  ffmpeg: Deno.ChildProcess,
  signal: AbortSignal,
): Promise<Uint8Array | null> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<null>((resolve) => {
    timer = setTimeout(() => resolve(null), FIRST_CHUNK_TIMEOUT_MS);
  });
  const abort = new Promise<null>((resolve) => {
    if (signal.aborted) {
      resolve(null);
      return;
    }
    signal.addEventListener("abort", () => resolve(null), { once: true });
  });
  try {
    return await Promise.race([
      reader.read().then((result) => (result.done ? null : result.value)),
      ffmpeg.status.then(() => null),
      timeout,
      abort,
    ]);
  } finally {
    clearTimeout(timer);
  }
}

function responseBody(attempt: Attempt): ReadableStream<Uint8Array> {
  return new ReadableStream<Uint8Array>({
    async start(controller) {
      controller.enqueue(attempt.first);
      try {
        while (true) {
          const { done, value } = await attempt.reader.read();
          if (done) {
            controller.close();
            return;
          }
          controller.enqueue(value);
        }
      } catch (e) {
        try {
          controller.error(e);
        } catch {
          // 既に閉じている
        }
      } finally {
        try {
          attempt.reader.releaseLock();
        } catch {
          // already released
        }
      }
    },
    cancel() {
      return attempt.stop("response body cancel");
    },
  });
}

transcode.get("/services/:id", async (c) => {
  const mirakcUrl = Deno.env.get("MIRAKC_URL") ?? "";
  const mirakcApiUrl = mirakcUrl === "" ? "" : mirakcApiUrlOf(mirakcUrl);
  const serviceId = c.req.param("id");

  const url = new URL(c.req.url);
  const audioTrackParam = url.searchParams.get("audioTrack");
  const audioTrackIndex =
    audioTrackParam !== null && Number.isInteger(Number(audioTrackParam))
      ? Math.max(0, Number(audioTrackParam))
      : 0;

  const quality = normalizeQuality(url.searchParams.get("quality"));
  const rawMode = url.searchParams.get("raw") === "1";
  const preferHevc = url.searchParams.get("codec") === "hevc";
  // probe と本番の ffmpeg で同じ画質引数を使うため、1 回だけ読む。
  const v4l2Cq = v4l2ConstantQualityEnabled();

  const streamUrl = new URL(
    `${mirakcApiUrl}/services/${serviceId}/stream?decode=1`,
  ).toString();

  // AVE の h264_v4l2m2m と hevc_v4l2m2m は同じ /dev/video* を共有するので、
  // EBUSY が出たら残りの v4l2m2m を試さず libx264 へ落とす。
  let v4l2Busy = false;
  for (const encoder of encoderChain(preferHevc)) {
    if (c.req.raw.signal.aborted) {
      break;
    }
    if (v4l2Busy && encoder !== "libx264") {
      continue;
    }
    const probe = await probeVideoEncoder(
      encoder,
      quality,
      v4l2Cq,
      c.req.raw.signal,
    );
    if (probe === "busy") {
      v4l2Busy = true;
      continue;
    }
    if (probe === "unusable") {
      continue;
    }
    let attempt: Attempt | null = null;
    try {
      attempt = await startAttempt({
        encoder,
        quality,
        v4l2Cq,
        audioTrackIndex,
        rawMode,
        streamUrl,
        serviceId,
        signal: c.req.raw.signal,
      });
    } catch (e) {
      if (e instanceof UpstreamError) {
        return new Response(e.message, {
          status: e.status,
          headers: { "Content-Type": "text/plain; charset=utf-8" },
        });
      }
      if (c.req.raw.signal.aborted) {
        // 中断と同時に起きた失敗も握り潰さず、スタックなしで残す。
        const detail = e instanceof Error ? e.message : String(e);
        console.error(
          `[transcode] attempt failed (aborted) serviceId=${serviceId} encoder=${encoder} error=${detail}`,
        );
      } else {
        console.error(
          `[transcode] attempt failed serviceId=${serviceId} encoder=${encoder}`,
          e,
        );
      }
    }
    if (attempt === null) {
      continue;
    }

    console.error(
      `[transcode] serviceId=${serviceId} encoder=${encoder} codec=${
        preferHevc ? "hevc" : "h264"
      } raw=${rawMode} quality=${quality} audioTrack=${audioTrackIndex}`,
    );
    attempt.ffmpeg.status
      .then((s) => attempt.stop(`ffmpeg exited code=${s.code}`))
      .catch(() => {});
    attempt.tsreadex?.status
      .then((s) => attempt.stop(`tsreadex exited code=${s.code}`))
      .catch(() => {});
    c.req.raw.signal.addEventListener(
      "abort",
      () => attempt.stop("client abort"),
      { once: true },
    );
    if (c.req.raw.signal.aborted) {
      attempt.stop("client abort");
    }

    return new Response(responseBody(attempt), {
      status: 200,
      headers: {
        "Content-Type": "video/mp2t",
        "Cache-Control": "no-cache",
      },
    });
  }

  return new Response("No usable video encoder found", {
    status: 503,
    headers: { "Content-Type": "text/plain; charset=utf-8" },
  });
});
