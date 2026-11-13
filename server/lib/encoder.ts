/**
 * トランスコード用エンコーダの選択と ffmpeg 引数生成。
 *
 * `ffmpeg -encoders` の文字列一致はデバイス有無を判定できず誤検出するため
 * (Debian の ffmpeg pkg は h264_v4l2m2m をビルド済みだが /dev/video* が無いと
 * 使えない)、実エンコードテスト (probe) で検証する。AV1 のハードウェア
 * エンコーダは MPEG-TS に多重化できないため候補に含めない。
 */

import type { Quality } from "./quality.ts";

export type EncoderName = "hevc_v4l2m2m" | "h264_v4l2m2m" | "libx264";

export const qualitySettings: Record<
  Quality,
  { scale: string; bitrate: string }
> = {
  "480p": { scale: "-2:480", bitrate: "1000k" },
  "720p": { scale: "-2:720", bitrate: "2000k" },
  "1080p": { scale: "-2:1080", bitrate: "4500k" },
};

export function buildVideoEncoderArgs(
  encoder: EncoderName,
  bitrate: string,
): string[] {
  if (encoder === "libx264") {
    return [
      "-c:v",
      "libx264",
      "-preset",
      "ultrafast",
      "-tune",
      "zerolatency",
      "-b:v",
      bitrate,
      "-g",
      "15",
      "-sc_threshold",
      "0",
    ];
  }
  return [
    "-c:v",
    encoder,
    "-b:v",
    bitrate,
    "-g",
    "15",
  ];
}

/**
 * V4L2 の mem2mem エンコーダは yuv420p 以外を受け付けないため format
 * フィルターを付ける。libx264 は入力の pix_fmt をそのまま扱えるので付けない。
 *
 * HW エンコーダは SPS の VUI を書かず SAR が消えるが、docker/ffmpeg-patches の
 * パッチが入力フレームの SAR をそのまま書くので、ここで格子を固定する必要は
 * ない (1920x1080 のソースも縮小せず 1:1 のまま出る)。
 */
export function buildVideoFilter(
  encoder: EncoderName,
  quality: Quality,
): string {
  const { scale } = qualitySettings[quality];
  const deinterlace = "yadif=mode=0:parity=-1:deint=1";
  return encoder === "libx264"
    ? `${deinterlace},scale=${scale}`
    : `${deinterlace},scale=${scale},format=yuv420p`;
}

/**
 * クライアントの希望を反映した試行順。HEVC は対応端末から要求されたときだけ
 * 先頭に置き、失敗時は H.264 (HW → ソフトウェア) へ落とす。
 */
export function encoderChain(preferHevc: boolean): EncoderName[] {
  return preferHevc
    ? ["hevc_v4l2m2m", "h264_v4l2m2m", "libx264"]
    : ["h264_v4l2m2m", "libx264"];
}

/** 子プロセスの stderr を改行単位で prefix 付きログに流す。 */
export function pipeStderr(
  src: ReadableStream<Uint8Array>,
  prefix: string,
): void {
  (async () => {
    const reader = src.getReader();
    const decoder = new TextDecoder();
    let buf = "";
    while (true) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      buf += decoder.decode(value, { stream: true });
      const lines = buf.split("\n");
      buf = lines.pop() ?? "";
      for (const line of lines) {
        if (line) {
          console.error(`${prefix} ${line}`);
        }
      }
    }
    buf += decoder.decode();
    if (buf) {
      console.error(`${prefix} ${buf}`);
    }
  })().catch(() => {
    // プロセス終了時の破損ストリームは無視
  });
}

/**
 * probe 用の入力。ISDB で最も多い 1440x1080 を使う。SAR 1:1 のテストパターン
 * なので、SAR を書く ffmpeg パッチの有無はここでは判定できない。
 */
const PROBE_INPUT = "testsrc=size=1440x1080:rate=30:duration=0.34";
const PROBE_TIMEOUT_MS = 5000;

/**
 * 実際に数フレームをエンコードして使えるか確かめる。配信と同じフィルターと
 * エンコーダ引数を通すので、解像度制約やデバイス占有 (EBUSY) もここで分かる。
 * 所要時間は 1080p で HW 約 80ms / x264 約 40ms。
 */
export async function probeVideoEncoder(
  encoder: EncoderName,
  quality: Quality,
): Promise<boolean> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);
  try {
    const child = new Deno.Command("ffmpeg", {
      args: [
        "-hide_banner",
        "-v",
        "error",
        "-f",
        "lavfi",
        "-i",
        PROBE_INPUT,
        "-vf",
        buildVideoFilter(encoder, quality),
        ...buildVideoEncoderArgs(encoder, qualitySettings[quality].bitrate),
        "-frames:v",
        "10",
        "-f",
        "null",
        "-",
      ],
      stdin: "null",
      stdout: "null",
      stderr: "piped",
      signal: controller.signal,
    }).spawn();
    pipeStderr(child.stderr, `[encoder-probe ${encoder}]`);
    const status = await child.status;
    return status.success;
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
  }
}
