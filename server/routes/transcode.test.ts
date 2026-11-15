import { assertEquals, assertRejects } from "@std/assert";
import { fetchUpstream } from "./transcode.ts";

const STREAM_URL =
  "http://mirakc:40772/api/services/3274101064/stream?decode=1";

/** 呼ばれた順に status を返す偽 fetcher。最後の status は繰り返す。 */
function fakeFetcher(statuses: number[]) {
  const calls: string[] = [];
  const fetcher = ((input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.signal?.aborted) {
      return Promise.reject(new DOMException("aborted", "AbortError"));
    }
    calls.push(String(input));
    const status = statuses[Math.min(calls.length - 1, statuses.length - 1)];
    return Promise.resolve(
      new Response(JSON.stringify({ code: status }), { status }),
    );
  }) as typeof fetch;
  return { calls, fetcher };
}

const never = new AbortController().signal;

Deno.test("fetchUpstream: 200 は再試行せずそのまま返す", async () => {
  const { calls, fetcher } = fakeFetcher([200]);
  const res = await fetchUpstream(STREAM_URL, never, fetcher, 250);
  assertEquals(res.status, 200);
  assertEquals(calls, [STREAM_URL]);
  await res.body?.cancel();
});

Deno.test("fetchUpstream: 404 のあと 200 なら再試行して返す", async () => {
  const { calls, fetcher } = fakeFetcher([404, 404, 200]);
  const res = await fetchUpstream(STREAM_URL, never, fetcher, 2000);
  assertEquals(res.status, 200);
  assertEquals(calls.length, 3);
  await res.body?.cancel();
});

Deno.test("fetchUpstream: 503 のあと 200 なら再試行して返す", async () => {
  const { calls, fetcher } = fakeFetcher([503, 200]);
  const res = await fetchUpstream(STREAM_URL, never, fetcher, 1000);
  assertEquals(res.status, 200);
  assertEquals(calls.length, 2);
  await res.body?.cancel();
});

Deno.test("fetchUpstream: 404 が続けば期限まで再試行して 404 を返す", async () => {
  const { calls, fetcher } = fakeFetcher([404]);
  const started = Date.now();
  const res = await fetchUpstream(STREAM_URL, never, fetcher, 250);
  assertEquals(res.status, 404);
  assertEquals(calls.length >= 2, true);
  assertEquals(Date.now() - started < 1000, true);
});

Deno.test("fetchUpstream: 500 は再試行せず即座に返す", async () => {
  const { calls, fetcher } = fakeFetcher([500]);
  const res = await fetchUpstream(STREAM_URL, never, fetcher, 250);
  assertEquals(res.status, 500);
  assertEquals(calls.length, 1);
  await res.body?.cancel();
});

Deno.test("fetchUpstream: 中断済みなら fetch の reject をそのまま返す", async () => {
  const controller = new AbortController();
  controller.abort();
  const { calls, fetcher } = fakeFetcher([404]);
  const err = await assertRejects(
    () => fetchUpstream(STREAM_URL, controller.signal, fetcher, 250),
    DOMException,
  );
  assertEquals(err.name, "AbortError");
  assertEquals(calls.length, 0);
});

Deno.test("fetchUpstream: 待機中の中断は再取得せず最後の応答を返す", async () => {
  const controller = new AbortController();
  const { calls, fetcher } = fakeFetcher([404]);
  const started = Date.now();
  setTimeout(() => controller.abort(), 50);
  const res = await fetchUpstream(STREAM_URL, controller.signal, fetcher, 500);
  assertEquals(res.status, 404);
  assertEquals(calls.length, 1);
  assertEquals(Date.now() - started < 300, true);
});

Deno.test("fetchUpstream: 再試行した応答の body は cancel する", async () => {
  let canceled = false;
  let calls = 0;
  const fetcher = (() => {
    calls++;
    if (calls === 1) {
      const body = new ReadableStream({
        cancel() {
          canceled = true;
        },
      });
      return Promise.resolve(new Response(body, { status: 404 }));
    }
    return Promise.resolve(new Response("ok", { status: 200 }));
  }) as typeof fetch;
  const res = await fetchUpstream(STREAM_URL, never, fetcher, 1000);
  assertEquals(res.status, 200);
  await new Promise((resolve) => setTimeout(resolve, 0));
  assertEquals(canceled, true);
  await res.body?.cancel();
});

Deno.test("fetchUpstream: 成功応答の body は読まずにそのまま返す", async () => {
  const fetcher = (() =>
    Promise.resolve(
      new Response("stream-body", { status: 200 }),
    )) as typeof fetch;
  const res = await fetchUpstream(STREAM_URL, never, fetcher, 250);
  assertEquals(await res.text(), "stream-body");
});
