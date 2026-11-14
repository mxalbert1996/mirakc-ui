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

/** V4L2 m2m の 2 種 (画質の指定方法が libx264 と異なる)。 */
type V4l2EncoderName = Exclude<EncoderName, "libx264">;

/** libx264 は解像度に依存しない CRF 1 本で足りる (docs/transcode-quality.md)。 */
const X264_CRF = 27;

/**
 * 画質プリセットごとのエンコーダ設定。数値は 5 本の実ストリームで測定した
 * 平均ビットレートが旧ビットレート設定 (bitrate) とほぼ一致する点を選んである
 * (docs/transcode-quality.md)。
 *
 * - crf: libx264 は常に CRF。ultrafast は同じビットレートで 1 dB 以上損する
 *   ため superfast を使う。
 * - qp: V4L2 m2m の constant QP。`-qp` を実装しているのは AVE 用パッチ
 *   (docker/ffmpeg-patches 0004) だけなので、使用は V4L2_CQ=1 で明示させる。
 * - bitrate: CQ を使わないときのフォールバック (従来の target bitrate)。
 */
export const qualitySettings: Record<
  Quality,
  {
    scale: string;
    bitrate: string;
    crf: number;
    qp: Record<V4l2EncoderName, number>;
  }
> = {
  "480p": {
    scale: "-2:480",
    bitrate: "1000k",
    crf: X264_CRF,
    qp: { h264_v4l2m2m: 29, hevc_v4l2m2m: 26 },
  },
  "720p": {
    scale: "-2:720",
    bitrate: "2000k",
    crf: X264_CRF,
    qp: { h264_v4l2m2m: 29, hevc_v4l2m2m: 26 },
  },
  "1080p": {
    scale: "-2:1080",
    bitrate: "4500k",
    crf: X264_CRF,
    qp: { h264_v4l2m2m: 28, hevc_v4l2m2m: 25 },
  },
};

/**
 * `-qp` (constant QP) は AVE 用パッチ (docker/ffmpeg-patches 0004) だけが実装
 * しており、他の V4L2 ドライバでは初期化時に ENOSYS で落ちて probe が失敗する。
 * ffmpeg のエンコーダ名からは AVE かを判別できない (h264_v4l2m2m は汎用ラッパ
 * 名で、実体は AVE の H.264 エンコーダ) ため、有効化は環境変数で明示させる
 * (既定は従来どおりビットレート制御)。
 */
export function v4l2ConstantQualityEnabled(): boolean {
  return Deno.env.get("V4L2_CQ") === "1";
}

/**
 * 映像エンコーダ引数。GOP は 15 フレーム (30fps で 0.5 秒) に固定する。
 * MSE (mpegts.js) はキーフレームからしか復号を始められないため、GOP が短い
 * ほど初回表示と遅延追従のシークが速い。
 *
 * libx264 は `-sc_threshold 0` でシーン変化によるキーフレームの割り込みを
 * 止める。GOP が既に 0.5 秒なので画質上の利点はなく、IDR 増加によるビット
 * レートのスパイク (Pi の CPU エンコードでコマ落ち要因) だけを避けられる。
 * v4l2m2m 系はこのプライベートオプションを受け付けないため付けない。
 *
 * CQ (`-qp`) のときは `-b:v` を渡さない (レート制御が切れるため無視される)。
 * B フレームは HEVC だけが実際に使う — H.264 は `-bf` を受け付けても B を
 * 出力しないため、付けても意味がない。
 */
export function buildVideoEncoderArgs(
  encoder: EncoderName,
  quality: Quality,
  v4l2Cq = false,
): string[] {
  const { bitrate, crf, qp } = qualitySettings[quality];
  if (encoder === "libx264") {
    return [
      "-c:v",
      "libx264",
      "-preset",
      "superfast",
      "-tune",
      "zerolatency",
      "-crf",
      String(crf),
      "-g",
      "15",
      "-sc_threshold",
      "0",
    ];
  }
  if (!v4l2Cq) {
    return ["-c:v", encoder, "-b:v", bitrate, "-g", "15"];
  }
  const args = ["-c:v", encoder, "-qp", String(qp[encoder]), "-g", "15"];
  if (encoder === "hevc_v4l2m2m") {
    args.push("-bf", "2");
  }
  return args;
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
 * `v4l2Cq` は呼び出し側が 1 回だけ解決した値を渡す — 本番の ffmpeg と同じ
 * 引数で probe するため、ここで環境変数を読み直さない。
 * 所要時間は 1080p で HW 約 80ms / x264 約 40ms。
 */
export async function probeVideoEncoder(
  encoder: EncoderName,
  quality: Quality,
  v4l2Cq: boolean,
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
        ...buildVideoEncoderArgs(encoder, quality, v4l2Cq),
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
