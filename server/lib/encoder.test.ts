import { assertEquals } from "@std/assert";
import {
  buildVideoEncoderArgs,
  buildVideoFilter,
  encoderChain,
  qualitySettings,
  v4l2ConstantQualityEnabled,
} from "./encoder.ts";

Deno.test("buildVideoEncoderArgs: libx264 は CRF と低遅延プリセット", () => {
  const args = buildVideoEncoderArgs("libx264", "1080p");
  assertEquals(args.includes("libx264"), true);
  // superfast は ultrafast と同じビットレートで 1 dB 以上良い (docs/transcode-quality.md)。
  assertEquals(args[args.indexOf("-preset") + 1], "superfast");
  assertEquals(args[args.indexOf("-tune") + 1], "zerolatency");
  assertEquals(args[args.indexOf("-crf") + 1], "27");
  assertEquals(args.includes("-b:v"), false);
});

Deno.test("buildVideoEncoderArgs: HW は CQ 無効なら従来と同じ引数", () => {
  // 後方互換の約束なので、順序まで含めて完全一致で固定する。
  assertEquals(buildVideoEncoderArgs("h264_v4l2m2m", "1080p"), [
    "-c:v",
    "h264_v4l2m2m",
    "-b:v",
    "4500k",
    "-g",
    "15",
  ]);
  assertEquals(buildVideoEncoderArgs("hevc_v4l2m2m", "720p"), [
    "-c:v",
    "hevc_v4l2m2m",
    "-b:v",
    "2000k",
    "-g",
    "15",
  ]);
});

Deno.test("buildVideoEncoderArgs: libx264 は CQ フラグを無視する", () => {
  assertEquals(
    buildVideoEncoderArgs("libx264", "1080p", true),
    buildVideoEncoderArgs("libx264", "1080p"),
  );
});

Deno.test("buildVideoEncoderArgs: HW の CQ は -qp と解像度別の値", () => {
  const expected = {
    h264_v4l2m2m: { "1080p": "28", "720p": "29", "480p": "29" },
    hevc_v4l2m2m: { "1080p": "25", "720p": "26", "480p": "26" },
  } as const;
  for (const encoder of ["h264_v4l2m2m", "hevc_v4l2m2m"] as const) {
    for (const quality of ["1080p", "720p", "480p"] as const) {
      const args = buildVideoEncoderArgs(encoder, quality, true);
      assertEquals(args[args.indexOf("-qp") + 1], expected[encoder][quality]);
      // CQ ではレート制御が切れるので -b:v を渡さない。
      assertEquals(args.includes("-b:v"), false);
      assertEquals(args[args.indexOf("-g") + 1], "15");
    }
  }
});

Deno.test("buildVideoEncoderArgs: B フレームは HEVC の CQ だけ", () => {
  assertEquals(
    buildVideoEncoderArgs("hevc_v4l2m2m", "1080p", true).slice(-2),
    ["-bf", "2"],
  );
  // c161 は -bf を受け付けても B を出力しないので付けない。
  assertEquals(
    buildVideoEncoderArgs("h264_v4l2m2m", "1080p", true).includes("-bf"),
    false,
  );
  assertEquals(
    buildVideoEncoderArgs("hevc_v4l2m2m", "1080p", false).includes("-bf"),
    false,
  );
});

Deno.test("buildVideoEncoderArgs: SAR はエンコーダ側のパッチが書くので bsf を付けない", () => {
  for (
    const encoder of ["h264_v4l2m2m", "hevc_v4l2m2m", "libx264"] as const
  ) {
    assertEquals(
      buildVideoEncoderArgs(encoder, "1080p", true).includes("-bsf:v"),
      false,
    );
  }
});

Deno.test("v4l2ConstantQualityEnabled: V4L2_CQ=1 のときだけ有効", () => {
  const original = Deno.env.get("V4L2_CQ");
  try {
    Deno.env.delete("V4L2_CQ");
    assertEquals(v4l2ConstantQualityEnabled(), false);
    Deno.env.set("V4L2_CQ", "0");
    assertEquals(v4l2ConstantQualityEnabled(), false);
    Deno.env.set("V4L2_CQ", "1");
    assertEquals(v4l2ConstantQualityEnabled(), true);
  } finally {
    if (original === undefined) {
      Deno.env.delete("V4L2_CQ");
    } else {
      Deno.env.set("V4L2_CQ", original);
    }
  }
});

Deno.test("buildVideoFilter: HW だけ yuv420p を強制する", () => {
  assertEquals(
    buildVideoFilter("h264_v4l2m2m", "1080p"),
    "yadif=mode=0:parity=-1:deint=1,scale=-2:1080,format=yuv420p",
  );
  assertEquals(
    buildVideoFilter("hevc_v4l2m2m", "720p"),
    "yadif=mode=0:parity=-1:deint=1,scale=-2:720,format=yuv420p",
  );
  assertEquals(
    buildVideoFilter("libx264", "1080p"),
    "yadif=mode=0:parity=-1:deint=1,scale=-2:1080",
  );
});

Deno.test("encoderChain: HEVC 希望時のみ先頭、必ず x264 で終わる", () => {
  assertEquals(encoderChain(true), [
    "hevc_v4l2m2m",
    "h264_v4l2m2m",
    "libx264",
  ]);
  assertEquals(encoderChain(false), ["h264_v4l2m2m", "libx264"]);
});

Deno.test("qualitySettings: 画質ごとの拡縮・CRF・QP", () => {
  assertEquals(qualitySettings, {
    "480p": {
      scale: "-2:480",
      bitrate: "1000k",
      crf: 27,
      qp: { h264_v4l2m2m: 29, hevc_v4l2m2m: 26 },
    },
    "720p": {
      scale: "-2:720",
      bitrate: "2000k",
      crf: 27,
      qp: { h264_v4l2m2m: 29, hevc_v4l2m2m: 26 },
    },
    "1080p": {
      scale: "-2:1080",
      bitrate: "4500k",
      crf: 27,
      qp: { h264_v4l2m2m: 28, hevc_v4l2m2m: 25 },
    },
  });
});
