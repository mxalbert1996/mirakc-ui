import { assertEquals, assertRejects } from "@std/assert";
import { createProgramsCache } from "./programs-cache.ts";

// 並行呼び出しを 1 回の取得に畳める検証用: 解除を待つ fetch モック。
function gatedFetch() {
  let fetches = 0;
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const fetchFn = ((_input: RequestInfo | URL) => {
    fetches++;
    return gate.then(() => Response.json([]));
  }) as typeof fetch;
  return { fetches: () => fetches, fetchFn, release };
}

function instantFetch(respond: () => Response) {
  let fetches = 0;
  const fetchFn = ((_input: RequestInfo | URL) => {
    fetches++;
    return Promise.resolve(respond());
  }) as typeof fetch;
  return { fetches: () => fetches, fetchFn };
}

Deno.test("getPrograms: 並行呼び出しは 1 回の取得に畳まる", async () => {
  const { fetches, fetchFn, release } = gatedFetch();
  const cache = createProgramsCache({
    apiUrl: "http://mirakc:40772/api",
    fetchFn,
  });

  const first = cache.getPrograms();
  const second = cache.getPrograms();
  release();
  assertEquals(await first, []);
  assertEquals(await second, []);
  assertEquals(fetches(), 1);
});

Deno.test("getPrograms: 取得済みなら TTL 内は再取得しない", async () => {
  const { fetches, fetchFn } = instantFetch(() => Response.json([]));
  const cache = createProgramsCache({
    apiUrl: "http://mirakc:40772/api",
    fetchFn,
  });

  assertEquals(await cache.getPrograms(), []);
  assertEquals(await cache.getPrograms(), []);
  assertEquals(fetches(), 1);
});

Deno.test("invalidate: 取得中の発火では完了した取得をコミットしない", async () => {
  const { fetches, fetchFn, release } = gatedFetch();
  const cache = createProgramsCache({
    apiUrl: "http://mirakc:40772/api",
    fetchFn,
  });

  const first = cache.getPrograms();
  cache.invalidate();
  release();
  assertEquals(await first, []);
  assertEquals(fetches(), 1);

  // invalidate 前に開始した取得はキャッシュされない — 次回は再取得する。
  await cache.getPrograms();
  assertEquals(fetches(), 2);
});

Deno.test("invalidate: 次回の getPrograms で再取得する", async () => {
  const { fetches, fetchFn } = instantFetch(() => Response.json([]));
  const cache = createProgramsCache({
    apiUrl: "http://mirakc:40772/api",
    fetchFn,
  });

  await cache.getPrograms();
  assertEquals(fetches(), 1);

  cache.invalidate();
  await cache.getPrograms();
  assertEquals(fetches(), 2);
});

Deno.test("getPrograms: TTL の期限切れで再取得する", async () => {
  let now = 0;
  const { fetches, fetchFn } = instantFetch(() => Response.json([]));
  const cache = createProgramsCache({
    apiUrl: "http://mirakc:40772/api",
    fetchFn,
    ttlMs: 1_000,
    now: () => now,
  });

  now = 0;
  await cache.getPrograms();
  now = 500;
  await cache.getPrograms();
  assertEquals(fetches(), 1);

  now = 1_000; // fetchedAt + ttlMs → 期限切れ。
  await cache.getPrograms();
  assertEquals(fetches(), 2);
});

Deno.test("getPrograms: 重いフィールドを除いた slim 形式に変換する", async () => {
  const { fetchFn } = instantFetch(() =>
    Response.json([{
      id: 327360102706471,
      eventId: 100,
      isFree: true,
      name: "ニュースウォッチ",
      startAt: 1_000,
      duration: 30 * 60 * 1000,
      networkId: 32736,
      serviceId: 1024,
      genres: [{ lv1: 0, lv2: 0, un1: 0, un2: 0 }],
      // slim では落とす重いフィールド (MirakurunProgram の任意フィールド)。
      audio: {
        componentType: 1,
        isMain: true,
        langs: ["jpn"],
        samplingRate: 2,
      },
      audios: [],
      description: "今日の出来事",
      extended: { 番組内容: "長い拡張情報" },
      relatedItems: [],
      series: null,
      video: null,
    }])
  );
  const cache = createProgramsCache({
    apiUrl: "http://mirakc:40772/api",
    fetchFn,
  });

  assertEquals(await cache.getPrograms(), [{
    id: 327360102706471,
    eventId: 100,
    isFree: true,
    name: "ニュースウォッチ",
    startAt: 1_000,
    duration: 30 * 60 * 1000,
    networkId: 32736,
    serviceId: 1024,
    genres: [{ lv1: 0, lv2: 0, un1: 0, un2: 0 }],
  }]);
});

Deno.test("getPrograms: 非 200 は throw し、次回の呼び出しで再試行する", async () => {
  let ok = false;
  const { fetches, fetchFn } = instantFetch(() =>
    ok ? Response.json([]) : new Response("ng", { status: 500 })
  );
  const cache = createProgramsCache({
    apiUrl: "http://mirakc:40772/api",
    fetchFn,
  });

  await assertRejects(
    () => cache.getPrograms(),
    Error,
    "GET http://mirakc:40772/api/programs failed: 500",
  );
  assertEquals(fetches(), 1);

  ok = true;
  assertEquals(await cache.getPrograms(), []);
  assertEquals(fetches(), 2);
});
