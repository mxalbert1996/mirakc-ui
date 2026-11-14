import { assertEquals } from "@std/assert";
import {
  buildVideoEncoderArgs,
  buildVideoFilter,
  encoderChain,
  isV4l2Busy,
  pipeStderr,
  type ProbeAttempt,
  probeWithBusyRetry,
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

Deno.test("isV4l2Busy: デバイス占有の stderr だけを busy と判定する", () => {
  // 実際のログ (旧セッションの ffmpeg がまだ /dev/video2 を握っている)。
  const busyStderr = [
    "[hevc_v4l2m2m @ 0x7f8] VIDIOC_STREAMON failed on capture context",
    "[hevc_v4l2m2m @ 0x7f8] Error encoding a frame: Device or resource busy",
    "Task finished with error code: -16 (Device or resource busy)",
    "Conversion failed!",
  ].join("\n");
  assertEquals(isV4l2Busy(busyStderr), true);

  // 待っても直らないエラーは busy ではない (次の候補へ進む)。
  assertEquals(
    isV4l2Busy("[hevc_v4l2m2m @ 0x7f8] Unknown option 'qp'"),
    false,
  );
  assertEquals(
    isV4l2Busy(
      "[h264_v4l2m2m @ 0x7f8] Initialization failed: -38 (Function not implemented)",
    ),
    false,
  );
  // VIDIOC_STREAMON の失敗だけでは占有と断定できない。
  assertEquals(
    isV4l2Busy(
      "[h264_v4l2m2m @ 0x7f8] VIDIOC_STREAMON failed on capture context",
    ),
    false,
  );
  assertEquals(isV4l2Busy(""), false);
});

// strerror の文言は LC_MESSAGES に従うので、英語以外の配備では errno で判定する。
Deno.test("isV4l2Busy: 翻訳された stderr でも errno で判定する", () => {
  const localized =
    "Task finished with error code: -16 (Gerät oder Ressource belegt)";
  assertEquals(localized.includes("Device or resource busy"), false);
  assertEquals(isV4l2Busy(localized), true);
});

const BUSY_STDERR =
  "Task finished with error code: -16 (Device or resource busy)";

Deno.test("probeWithBusyRetry: EBUSY のあと成功すれば ok", async () => {
  const results: ProbeAttempt[] = [
    { success: false, stderr: BUSY_STDERR },
    { success: false, stderr: BUSY_STDERR },
    { success: true, stderr: "" },
  ];
  let calls = 0;
  const result = await probeWithBusyRetry(() => {
    const attempt = results[calls];
    calls++;
    return Promise.resolve(attempt);
  });
  assertEquals(result, "ok");
});

Deno.test("probeWithBusyRetry: 占有が続く限り再試行して busy", async () => {
  let calls = 0;
  const result = await probeWithBusyRetry(() => {
    calls++;
    return Promise.resolve({ success: false, stderr: BUSY_STDERR });
  });
  assertEquals(result, "busy");
  // 窓の終わりは実時間なので回数は固定しないが、再試行はしている。
  assertEquals(calls >= 2, true);
});

Deno.test("probeWithBusyRetry: busy でない失敗は再試行しない", async () => {
  let calls = 0;
  const result = await probeWithBusyRetry(() => {
    calls++;
    return Promise.resolve({
      success: false,
      stderr: "[hevc_v4l2m2m @ 0x7f8] Unknown option 'qp'",
    });
  });
  assertEquals(result, "unusable");
  assertEquals(calls, 1);
});

Deno.test("probeWithBusyRetry: 中断済みなら probe しない", async () => {
  const controller = new AbortController();
  controller.abort();
  let calls = 0;
  const result = await probeWithBusyRetry(() => {
    calls++;
    return Promise.resolve({ success: false, stderr: BUSY_STDERR });
  }, controller.signal);
  assertEquals(result, "unusable");
  assertEquals(calls, 0);
});

Deno.test("probeWithBusyRetry: 待機中の中断で打ち切る", async () => {
  const controller = new AbortController();
  const started = Date.now();
  let calls = 0;
  setTimeout(() => controller.abort(), 50);
  const result = await probeWithBusyRetry(() => {
    calls++;
    return Promise.resolve({ success: false, stderr: BUSY_STDERR });
  }, controller.signal);
  assertEquals(result, "unusable");
  assertEquals(calls, 1);
  // 100ms の再試行間隔を中断で打ち切るので、500ms の窓は待たない。
  assertEquals(Date.now() - started < 500, true);
});

Deno.test("pipeStderr: onLine は行単位で改行を除いて渡す", async () => {
  const lines: string[] = [];
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      const encoder = new TextEncoder();
      controller.enqueue(encoder.encode("a\nb"));
      controller.enqueue(encoder.encode("c\n"));
      controller.close();
    },
  });
  await pipeStderr(stream, "[test]", (line) => lines.push(line));
  assertEquals(lines, ["a", "bc"]);
});

Deno.test("pipeStderr: 改行で終わらない最終行も渡す", async () => {
  const lines: string[] = [];
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new TextEncoder().encode("done"));
      controller.close();
    },
  });
  await pipeStderr(stream, "[test]", (line) => lines.push(line));
  assertEquals(lines, ["done"]);
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
