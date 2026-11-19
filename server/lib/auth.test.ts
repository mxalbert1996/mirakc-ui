import { assertEquals } from "@std/assert";
import { Hono } from "hono";
import { type Auth, createAuth } from "./auth.ts";

const TOKEN = "secret-token";

async function withEnv(
  key: string,
  value: string | null,
  fn: () => Promise<void> | void,
): Promise<void> {
  const original = Deno.env.get(key);
  try {
    if (value === null) {
      Deno.env.delete(key);
    } else {
      Deno.env.set(key, value);
    }
    await fn();
  } finally {
    if (original === undefined) {
      Deno.env.delete(key);
    } else {
      Deno.env.set(key, original);
    }
  }
}

/** 認証を被せるテスト用アプリ。ミドルウェアは全ルートの前に置く。 */
function testApp(auth: Auth) {
  const app = new Hono();
  app.use("/*", auth.middleware);
  app.route("/api/auth", auth.routes);
  app.get("/api/comments/services/1/stream", (c) => c.json({ ok: true }));
  app.get("/api/transcode/services/1", (c) => c.json({ ok: true }));
  app.get("/api/programs", (c) => c.json({ ok: true }));
  app.get("/*", (c) => c.text("static"));
  return app;
}

function basic(user: string, password: string): string {
  return `Basic ${btoa(`${user}:${password}`)}`;
}

Deno.test("認証無効 (既定): AUTH_TOKEN 未設定なら全経路オープン", async () => {
  await withEnv("AUTH_TOKEN", null, async () => {
    const app = testApp(createAuth());

    assertEquals((await app.request("/api/programs")).status, 200);
    assertEquals((await app.request("/")).status, 200);
  });
});

Deno.test("認証無効 (既定): チケット API は 200 で ticket:null を返す", async () => {
  // 環境変数経由で createAuth() を呼ぶため、他モジュール (main.test.ts など)
  // が設定した AUTH_TOKEN の影響を受けないよう必ず明示的に未設定へ向ける
  // (--parallel のワーカー間でプロセス環境は共有される)。
  await withEnv("AUTH_TOKEN", null, async () => {
    const app = testApp(createAuth());

    const res = await app.request("/api/auth/ticket", { method: "POST" });
    assertEquals(res.status, 200);
    assertEquals(await res.json(), { ticket: null });
  });
});

Deno.test("認証無効: 空白のみの AUTH_TOKEN は未設定扱い", async () => {
  const app = testApp(createAuth({ token: "   " }));

  assertEquals((await app.request("/api/programs")).status, 200);
  const res = await app.request("/api/auth/ticket", { method: "POST" });
  assertEquals(res.status, 200);
  assertEquals(await res.json(), { ticket: null });
});

Deno.test("認証無効: 環境変数の AUTH_TOKEN が空なら起動時に 1 回警告する", async () => {
  // deps.token での差し替えではなく、環境変数経由で空を渡す経路の検証。
  const original = console.warn;
  const warnings: string[] = [];
  console.warn = (...args: unknown[]) => {
    warnings.push(args.join(" "));
  };
  try {
    await withEnv("AUTH_TOKEN", "   ", () => {
      createAuth();
      // 生成のたびに出るのではなく、起動時 (1 回目の生成) だけに出す。
      createAuth();
    });
    assertEquals(warnings, [
      "[auth] AUTH_TOKEN は空のため認証を無効化しました (fail-open)",
    ]);
  } finally {
    console.warn = original;
  }
});

Deno.test("認証有効: Bearer / X-Auth-Token / Basic の 3 形式を受け付ける", async () => {
  const app = testApp(createAuth({ token: TOKEN }));

  const bearer = await app.request("/api/programs", {
    headers: { authorization: `Bearer ${TOKEN}` },
  });
  assertEquals(bearer.status, 200);

  const xToken = await app.request("/api/programs", {
    headers: { "x-auth-token": TOKEN },
  });
  assertEquals(xToken.status, 200);

  const basicAuth = await app.request("/api/programs", {
    headers: { authorization: basic("mirakc", TOKEN) },
  });
  assertEquals(basicAuth.status, 200);

  // Basic のユーザー名は検証しない (パスワードだけを見る)。
  const wrongUser = await app.request("/api/programs", {
    headers: { authorization: basic("wrong-user", TOKEN) },
  });
  assertEquals(wrongUser.status, 200);
});

Deno.test("認証有効: 不正なトークンは 401 (各形式)", async () => {
  const app = testApp(createAuth({ token: TOKEN }));

  const cases: Array<Record<string, string> | undefined> = [
    { authorization: "Bearer wrong" },
    { "x-auth-token": "wrong" },
    { authorization: basic("mirakc", "wrong") },
    {},
  ];
  for (const headers of cases) {
    const res = await app.request("/api/programs", { headers });
    assertEquals(res.status, 401, JSON.stringify(headers));
    assertEquals(
      await res.json(),
      { error: "unauthorized" },
      JSON.stringify(headers),
    );
  }
});

Deno.test("認証有効: 不正な Basic の base64 でも 401 で落ちない", async () => {
  const app = testApp(createAuth({ token: TOKEN }));

  const res = await app.request("/api/programs", {
    headers: { authorization: "Basic !!!not-base64!!!" },
  });
  assertEquals(res.status, 401);
  await res.body?.cancel();
});

Deno.test("認証有効: Basic の base64 にコロンが無ければ全体をパスワードとして見る", async () => {
  // 意図的な緩い扱い: base64 のデコード結果にコロンが無いときは区切り無しと
  // 見なして全体をパスワードとして比較する (実際の Basic パーサーも同等に緩い)。
  // 無害な挙動のためこのテストで挙動を固定する — リファクタリングで黙って
  // 変えないこと。
  const app = testApp(createAuth({ token: TOKEN }));

  const res = await app.request("/api/programs", {
    headers: { authorization: `Basic ${btoa(TOKEN)}` },
  });
  assertEquals(res.status, 200);
  await res.body?.cancel();
});

Deno.test("認証有効: /api/* の 401 は JSON、静的パスは Basic 案内のプレーンテキスト", async () => {
  const app = testApp(createAuth({ token: TOKEN }));

  const api = await app.request("/api/programs");
  assertEquals(api.status, 401);
  assertEquals(
    api.headers.get("content-type")?.startsWith("application/json"),
    true,
  );
  assertEquals(await api.json(), { error: "unauthorized" });

  const page = await app.request("/");
  assertEquals(page.status, 401);
  assertEquals(
    page.headers.get("www-authenticate"),
    'Basic realm="mirakc-ui", charset="UTF-8"',
  );
  assertEquals(
    page.headers.get("content-type")?.startsWith("text/plain"),
    true,
  );
  await page.body?.cancel();
});

Deno.test("認証有効: 正しい Basic で静的パスも通る", async () => {
  const app = testApp(createAuth({ token: TOKEN }));

  const res = await app.request("/", {
    headers: { authorization: basic("mirakc", TOKEN) },
  });
  assertEquals(res.status, 200);
  assertEquals(await res.text(), "static");
});

Deno.test("チケット発行: ヘッダ認証が必要 (無認証・チケット経由は 401)", async () => {
  const app = testApp(createAuth({ token: TOKEN }));

  const noCredential = await app.request("/api/auth/ticket", {
    method: "POST",
  });
  assertEquals(noCredential.status, 401);
  await noCredential.body?.cancel();

  const byTicket = await app.request("/api/auth/ticket", {
    method: "POST",
    headers: { authorization: `Bearer ${TOKEN}` },
  });
  assertEquals(byTicket.status, 200);
  const { ticket } = await byTicket.json();
  assertEquals(typeof ticket, "string");
  assertEquals(ticket.length > 0, true);

  // チケットでは別のチケットを発行できない (ストリーム系 GET しか受け付けない)。
  const remint = await app.request(`/api/auth/ticket?ticket=${ticket}`, {
    method: "POST",
  });
  assertEquals(remint.status, 401);
  await remint.body?.cancel();
});

Deno.test("チケット: ストリーム系の GET 2 経路で通る", async () => {
  const app = testApp(createAuth({ token: TOKEN }));

  const minted = await app.request("/api/auth/ticket", {
    method: "POST",
    headers: { authorization: `Bearer ${TOKEN}` },
  });
  const { ticket } = await minted.json();

  const comments = await app.request(
    `/api/comments/services/1/stream?ticket=${ticket}`,
  );
  assertEquals(comments.status, 200);
  assertEquals(await comments.json(), { ok: true });

  const transcode = await app.request(
    `/api/transcode/services/1?ticket=${ticket}`,
  );
  assertEquals(transcode.status, 200);
});

Deno.test("チケット: ストリーム以外のパス・POST では使えない", async () => {
  const app = testApp(createAuth({ token: TOKEN }));

  const minted = await app.request("/api/auth/ticket", {
    method: "POST",
    headers: { authorization: `Bearer ${TOKEN}` },
  });
  const { ticket } = await minted.json();

  const otherPath = await app.request(`/api/programs?ticket=${ticket}`);
  assertEquals(otherPath.status, 401);
  await otherPath.body?.cancel();

  const post = await app.request(
    `/api/transcode/services/1?ticket=${ticket}`,
    { method: "POST" },
  );
  assertEquals(post.status, 401);
  await post.body?.cancel();
});

Deno.test("チケット: 不正な値は 401", async () => {
  const app = testApp(createAuth({ token: TOKEN }));

  const res = await app.request(
    "/api/comments/services/1/stream?ticket=not-a-ticket",
  );
  assertEquals(res.status, 401);
  await res.body?.cancel();
});

Deno.test("チケット: 約 60 秒で失効する", async () => {
  let nowMs = 0;
  const app = testApp(createAuth({ token: TOKEN, now: () => nowMs }));

  const minted = await app.request("/api/auth/ticket", {
    method: "POST",
    headers: { authorization: `Bearer ${TOKEN}` },
  });
  const { ticket } = await minted.json();

  // 期限の内側は通る。
  nowMs = 30_000;
  const within = await app.request(
    `/api/comments/services/1/stream?ticket=${ticket}`,
  );
  assertEquals(within.status, 200);

  // 期限を過ぎると使えない。
  nowMs = 61_000;
  const expired = await app.request(
    `/api/comments/services/1/stream?ticket=${ticket}`,
  );
  assertEquals(expired.status, 401);
  await expired.body?.cancel();
});

Deno.test("チケット: 最大 5 回まで有効、6 回目以降は 401", async () => {
  const app = testApp(createAuth({ token: TOKEN }));

  const minted = await app.request("/api/auth/ticket", {
    method: "POST",
    headers: { authorization: `Bearer ${TOKEN}` },
  });
  const { ticket } = await minted.json();

  for (let i = 0; i < 5; i++) {
    const res = await app.request(
      `/api/comments/services/1/stream?ticket=${ticket}`,
    );
    assertEquals(res.status, 200, `use ${i + 1}`);
    await res.body?.cancel();
  }

  const sixth = await app.request(
    `/api/comments/services/1/stream?ticket=${ticket}`,
  );
  assertEquals(sixth.status, 401);
  await sixth.body?.cancel();
});
