/**
 * main.ts のマウント検証。auth.test.ts は合成アプリでミドルウェア自体を検査
 * するため、main.ts で認証より前にルートが登録されたり serveStatic が前に
 * 移動したりしても従来は気付けなかった。ここでは本物の main.ts にリクエスト
 * を投げて、認証ミドルウェアが全ルート (/api・ストリーム・静的配信・SPA
 * フォールバック) より前に置かれていることを担保する。
 *
 * 【重要】main.ts はモジュール top で環境変数を読む (DATA_DIR の kvPath、
 * MIRAKC_URL、AUTH_TOKEN) ため、import は必ず環境変数の設定より後に行う。
 * なお `deno test --parallel` のワーカー間でプロセス環境は共有される
 * (専用ワーカーでも環境は分離されない)。そのため環境変数を書き換える
 * テストは、自分が依存する変数を必ず事前に設定・削除し finally で戻す
 * 「自己防衛」が前提になる (AUTH_TOKEN を読む auth.test.ts 側も同様に
 * withEnv で保護済み)。
 *
 * なお `app.request()` は URL を正規化するため、このテストが担保するのは
 * マウント順と経路の網羅であって生パスの挙動ではない (traversal は実ソケット
 * での実機検証に依る)。
 */
import { assertEquals } from "@std/assert";

const TOKEN = "main-mount-test-token";

Deno.test("main.ts: 認証は全ルート (/api・ストリーム・静的・SPA) より前に置かれる", async () => {
  const dir = await Deno.makeTempDir();
  const originalEnv = [
    ["DATA_DIR", Deno.env.get("DATA_DIR")],
    ["AUTH_TOKEN", Deno.env.get("AUTH_TOKEN")],
    ["MIRAKC_URL", Deno.env.get("MIRAKC_URL")],
  ] as const;
  Deno.env.set("DATA_DIR", dir);
  Deno.env.set("AUTH_TOKEN", TOKEN);
  Deno.env.delete("MIRAKC_URL");
  // import より前に環境を整えること (モジュールコメント参照)。
  const { default: app } = await import("./main.ts");
  try {
    // --- 認証なし: どの経路も 401 ---
    for (
      const path of [
        "/api/programs",
        "/api/keyword-rules",
        "/api/comments/services/1/stream",
        "/api/transcode/services/1",
      ]
    ) {
      const res = await app.request(path);
      assertEquals(res.status, 401, path);
      assertEquals(await res.json(), { error: "unauthorized" }, path);
    }
    // 静的配信・SPA はプレーンテキスト + Basic 案内 (ブラウザのネイティブ
    // 認証ダイアログ用)。
    for (const path of ["/", "/index.html"]) {
      const res = await app.request(path);
      assertEquals(res.status, 401, path);
      assertEquals(
        res.headers.get("www-authenticate"),
        'Basic realm="mirakc-ui", charset="UTF-8"',
        path,
      );
      await res.body?.cancel();
    }
    const ticketWithoutCredential = await app.request("/api/auth/ticket", {
      method: "POST",
    });
    assertEquals(ticketWithoutCredential.status, 401);
    await ticketWithoutCredential.body?.cancel();

    // --- 認証あり: チケット発行・API・静的配信が通る ---
    const headers = { authorization: `Basic ${btoa(`any-user:${TOKEN}`)}` };

    const minted = await app.request("/api/auth/ticket", {
      method: "POST",
      headers,
    });
    assertEquals(minted.status, 200);
    assertEquals(typeof (await minted.json()).ticket, "string");

    // MIRAKC_URL 未設定でも /api/programs は一貫した 500 JSON (main.ts の規約)。
    const programs = await app.request("/api/programs", { headers });
    assertEquals(programs.status, 500);
    assertEquals(await programs.json(), { error: "MIRAKC_URL is not set" });

    // 静的配信は認証より後ろにあることの証明: 認証を通り抜ければ 200
    // (client/dist がビルド済みのとき) か 404 (未ビルドのとき)。401 でなければ
    // 認証の壁を越えている。
    const page = await app.request("/", { headers });
    assertEquals(page.status !== 401, true);
    await page.body?.cancel();
  } finally {
    for (const [key, value] of originalEnv) {
      if (value === undefined) {
        Deno.env.delete(key);
      } else {
        Deno.env.set(key, value);
      }
    }
    try {
      await Deno.remove(dir, { recursive: true });
    } catch {
      // KV が遅延 open したまま残っている場合は掃除を諦める (テスト失敗にしない)。
    }
  }
});
