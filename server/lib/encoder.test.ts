import { assertEquals } from "@std/assert";
import {
  buildVideoEncoderArgs,
  buildVideoFilter,
  encoderChain,
  qualitySettings,
} from "./encoder.ts";

Deno.test("buildVideoEncoderArgs: libx264 は低遅延プリセットを含む", () => {
  const args = buildVideoEncoderArgs("libx264", "2000k");
  assertEquals(args.includes("libx264"), true);
  // -preset ultrafast / -tune zerolatency が CPU エンコードの低遅延の肝。
  assertEquals(args[args.indexOf("-preset") + 1], "ultrafast");
  assertEquals(args[args.indexOf("-tune") + 1], "zerolatency");
  assertEquals(args[args.indexOf("-b:v") + 1], "2000k");
});

Deno.test("buildVideoEncoderArgs: HW エンコーダは最小引数", () => {
  for (const encoder of ["h264_v4l2m2m", "hevc_v4l2m2m"] as const) {
    const args = buildVideoEncoderArgs(encoder, "3500k");
    assertEquals(args[args.indexOf("-c:v") + 1], encoder);
    // ソフトウェア専用の preset/tune は付けない。
    assertEquals(args.includes("-preset"), false);
    assertEquals(args.includes("-tune"), false);
    assertEquals(args[args.indexOf("-b:v") + 1], "3500k");
    assertEquals(args[args.indexOf("-g") + 1], "15");
  }
});

Deno.test("buildVideoEncoderArgs: SAR はエンコーダ側のパッチが書くので bsf を付けない", () => {
  for (
    const encoder of ["h264_v4l2m2m", "hevc_v4l2m2m", "libx264"] as const
  ) {
    assertEquals(
      buildVideoEncoderArgs(encoder, "4500k").includes("-bsf:v"),
      false,
    );
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

Deno.test("qualitySettings: 画質ごとの拡縮とビットレート", () => {
  assertEquals(qualitySettings, {
    "480p": { scale: "-2:480", bitrate: "1000k" },
    "720p": { scale: "-2:720", bitrate: "2000k" },
    "1080p": { scale: "-2:1080", bitrate: "4500k" },
  });
});
