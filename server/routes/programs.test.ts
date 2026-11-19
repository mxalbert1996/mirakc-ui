import { assertEquals } from "@std/assert";
import { createProgramsRoutes } from "./programs.ts";
import type { SlimProgram } from "../lib/programs-cache.ts";

const FROM = 1_000_000;
const TO = 2_000_000;

function slim(overrides: Partial<SlimProgram> = {}): SlimProgram {
  return {
    id: 1,
    eventId: 100,
    isFree: true,
    name: "ニュース",
    startAt: 1_500_000,
    duration: 100_000,
    networkId: 32736,
    serviceId: 1024,
    genres: [],
    ...overrides,
  };
}

Deno.test("GET /: from/to 無しでは全件を返す", async () => {
  const programs = [slim(), slim({ id: 2 })];
  const app = createProgramsRoutes({
    getPrograms: () => Promise.resolve(programs),
  });

  const res = await app.request("/");
  assertEquals(res.status, 200);
  assertEquals(await res.json(), programs);
});

Deno.test("GET /: from/to は両方指定するか両方省略する", async () => {
  const app = createProgramsRoutes({
    getPrograms: () => Promise.resolve([]),
  });

  const onlyFrom = await app.request(`/?from=${FROM}`);
  assertEquals(onlyFrom.status, 400);
  assertEquals(await onlyFrom.json(), {
    error: "from and to must be specified together",
  });

  const onlyTo = await app.request(`/?to=${TO}`);
  assertEquals(onlyTo.status, 400);
});

Deno.test("GET /: from/to は整数 (epoch ms) でなければ 400", async () => {
  const app = createProgramsRoutes({
    getPrograms: () => Promise.resolve([]),
  });

  assertEquals((await app.request("/?from=abc&to=1")).status, 400);
  assertEquals((await app.request("/?from=1.5&to=2")).status, 400);
  // 空文字も指定済みと扱い整数違反で 400。
  assertEquals((await app.request("/?from=&to=1")).status, 400);
});

Deno.test("GET /: from/to と放送時間が重複する番組だけを返す", async () => {
  const inside = slim({ id: 1, startAt: FROM + 100, duration: 10 });
  // startAt === to は重複なし (判定が startAt < to の厳密比較)。
  const startsAtTo = slim({ id: 2, startAt: TO, duration: 10 });
  // startAt + duration === from も重複なし (判定が > from の厳密比較)。
  const endsAtFrom = slim({ id: 3, startAt: FROM - 100, duration: 100 });
  const before = slim({ id: 4, startAt: FROM - 1_000, duration: 500 });
  // 窓をまたぐ長時間番組は重複とみなす。
  const spans = slim({ id: 5, startAt: FROM - 100, duration: TO - FROM + 200 });

  const app = createProgramsRoutes({
    getPrograms: () =>
      Promise.resolve([inside, startsAtTo, endsAtFrom, before, spans]),
  });

  const res = await app.request(`/?from=${FROM}&to=${TO}`);
  assertEquals(res.status, 200);
  assertEquals(await res.json(), [inside, spans]);
});

Deno.test("GET /: getPrograms の失敗は mirakc プロキシと同じ 500 JSON", async () => {
  const app = createProgramsRoutes({
    getPrograms: () => Promise.reject(new Error("MIRAKC_URL is not set")),
  });

  const res = await app.request("/");
  assertEquals(res.status, 500);
  assertEquals(await res.json(), { error: "MIRAKC_URL is not set" });
});

Deno.test("GET /: 失敗原因が何であれ同じ 500 JSON を返す", async () => {
  // mirakc 到達不能など未設定以外の失敗でも、応答の規約は崩さない。
  const app = createProgramsRoutes({
    getPrograms: () => Promise.reject(new Error("fetch failed to mirakc")),
  });

  const res = await app.request("/");
  assertEquals(res.status, 500);
  assertEquals(await res.json(), { error: "MIRAKC_URL is not set" });
});
