import { Hono } from "hono";
import {
  buildVideoEncoderArgs,
  buildVideoFilter,
  encoderChain,
  type EncoderName,
  pipeStderr,
  probeVideoEncoder,
  qualitySettings,
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

type Attempt = {
  ffmpeg: Deno.ChildProcess;
  tsreadex: Deno.ChildProcess | null;
  reader: ReadableStreamDefaultReader<Uint8Array>;
  first: Uint8Array;
  stop: (reason: string) => void;
};

type StartOptions = {
  encoder: EncoderName;
  quality: Quality;
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
        qualitySettings[opts.quality].bitrate,
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

/**
 * mirakc から取り、tsreadex・ffmpeg を繋いで最初のチャンクまで待つ。
 * 最初のチャンクが出なければ (ffmpeg 即終了 / デバイス占有 / タイムアウト)
 * 後始末して null を返す。呼び出し側は次の候補へ進む。
 */
async function startAttempt(opts: StartOptions): Promise<Attempt | null> {
  const upstream = await fetch(opts.streamUrl, { signal: opts.signal });
  if (!upstream.ok || !upstream.body) {
    console.error(
      `[transcode] mirakc stream failed serviceId=${opts.serviceId} status=${upstream.status}`,
    );
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

  let stopped = false;
  const stop = (reason: string) => {
    if (stopped) {
      return;
    }
    stopped = true;
    console.error(
      `[transcode] stop serviceId=${opts.serviceId} encoder=${opts.encoder} reason=${reason}`,
    );
    for (const child of [tsreadex, ffmpeg]) {
      try {
        child?.kill("SIGTERM");
      } catch {
        // already exited
      }
    }
    setTimeout(() => {
      for (const child of [tsreadex, ffmpeg]) {
        try {
          child?.kill("SIGKILL");
        } catch {
          // already exited
        }
      }
    }, 3000);
    upstream.body?.cancel().catch(() => {});
  };

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

  const reader = ffmpeg.stdout.getReader();
  const first = await firstChunk(reader, ffmpeg, opts.signal);
  if (first === null) {
    stop("no output");
    reader.cancel().catch(() => {});
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
      attempt.stop("response body cancel");
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

  const streamUrl = new URL(
    `${mirakcApiUrl}/services/${serviceId}/stream?decode=1`,
  ).toString();

  for (const encoder of encoderChain(preferHevc)) {
    if (c.req.raw.signal.aborted) {
      break;
    }
    if (!(await probeVideoEncoder(encoder, quality))) {
      continue;
    }
    let attempt: Attempt | null = null;
    try {
      attempt = await startAttempt({
        encoder,
        quality,
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
      console.error(
        `[transcode] attempt failed serviceId=${serviceId} encoder=${encoder}`,
        e,
      );
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
