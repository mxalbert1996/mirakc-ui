/**
 * オプトインの HTTP 認証 (AUTH_TOKEN) とストリーム視聴用のワンタイムチケット。
 *
 * AUTH_TOKEN 未設定 (既定) は認証無効。リバースプロキシ運用ではそちらで
 * 認証を担うため、アプリ側は常にオープンにする (チケット API はそのプローブ
 * として常に存在し、`{ "ticket": null }` を返す)。
 *
 * AUTH_TOKEN 設定時はすべてのリクエスト (/api・ストリーム・静的配信・SPA
 * フォールバック) で次のいずれかの認証情報を要求する。比較は提示側も含めて
 * SHA-256 ダイジェスト同士で一定時間に行う (文字列比較のタイミングを漏らさ
 * ないため)。
 *
 *   - `Authorization: Bearer <token>`     API クライアント・client の fetch ラッパ
 *   - `Authorization: Basic base64(user:<token>)`  ブラウザのネイティブ認証
 *    ダイアログ (ユーザー名は任意、パスワードを見る)
 *   - `X-Auth-Token: <token>`             curl 向けの簡易ヘッダ
 *   - `?ticket=<t>`                        下記のワンタイムチケット。EventSource
 *     と mpegts.js はリクエストヘッダを設定できないため、ストリーム系の GET
 *     (/api/comments/ と /api/transcode/ 配下) だけ受け付ける
 *
 * チケットは POST /api/auth/ticket で発行する (発行にはヘッダによる認証が必要
 * — チケットで別のチケットを発行させないため、ミドルウェアがこの経路では
 * チケットを受け付けないことで担保される)。EventSource の自動再接続と
 * mpegts.js の同一 URL 再要求に対応するため厳密な 1 回限りとはせず、約 60 秒
 * ・最大 5 回の使用で失効する (Replay の窓を TTL と使用回数で抑える)。失効
 * チケットの掃除は発行時と検証時に惰性的に行う。
 *
 * 401 の応答は /api/* が JSON (`{ "error": "unauthorized" }`)、それ以外 (静的
 * 配信・SPA) は `WWW-Authenticate: Basic` 付きのプレーンテキスト — ブラウザが
 * ネイティブのログインダイアログを出せるようにするため。
 *
 * cookie を使わないため CSRF 面は持たない。認証は単一の共有トークンのみ
 * (ユーザーアカウントは非対象)。
 */
import { Hono } from "hono";
import type { Context, MiddlewareHandler } from "hono";

/** チケットの有効期限 (約 60 秒)。 */
const TICKET_TTL_MS = 60_000;

/** チケットの最大使用回数 (5 回で失効)。 */
const TICKET_MAX_USES = 5;

/** チケットのエントロピー (128 ビット)。 */
const TICKET_BYTES = 16;

/** チケットをクエリパラメータで受け付けるパス (ストリーム系の GET のみ)。 */
const TICKET_PATH_PREFIXES = ["/api/comments/", "/api/transcode/"];

/** 依存の差し替え (テスト用)。省略時は環境変数と Date.now を使う。 */
export type AuthDeps = {
  /** 認証トークン。undefined (既定) なら AUTH_TOKEN 環境変数、null なら常に無効。 */
  token?: string | null;
  /** 現在時刻 (ms)。チケットの TTL に関わるためテストで差し替える。 */
  now?: () => number;
};

/** main.ts がマウントする認証の部品一式。 */
export type Auth = {
  /** 全ルートの前に置く認証ミドルウェア (`app.use("/*", ...)`)。 */
  middleware: MiddlewareHandler;
  /** POST /api/auth/ticket (`app.route("/api/auth", ...)`)。 */
  routes: Hono;
};

async function sha256(value: string): Promise<Uint8Array> {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(value),
  );
  return new Uint8Array(digest);
}

function hexOf(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

/**
 * 一定時間比較。長さの差もビット和に畳めて途中で返らないようにする
 * (比較対象は実際には常に同長のダイジェストだが、長さ差を先に返す実装に
 * しないでおく)。
 */
function timingSafeEqual(a: Uint8Array, b: Uint8Array): boolean {
  const len = Math.max(a.length, b.length);
  let diff = a.length ^ b.length;
  for (let i = 0; i < len; i++) {
    const x = i < a.length ? a[i] : 0;
    const y = i < b.length ? b[i] : 0;
    diff |= x ^ y;
  }
  return diff === 0;
}

function base64UrlOf(bytes: Uint8Array): string {
  let binary = "";
  for (const b of bytes) {
    binary += String.fromCharCode(b);
  }
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(
    /=+$/,
    "",
  );
}

/** 空の AUTH_TOKEN に対する警告は 1 回だけ (起動時に近い最初の生成で出す)。 */
let warnedBlankToken = false;

/**
 * 認証を作る。token は空文字・空白のみを未設定扱い (認証無効) にする。
 * 設定ミス (`AUTH_TOKEN=``) で全リクエストを 401 にしないため。ただし環境変数が
 * 空のまま存在する場合は、静かに開いたままになることを防ぐため 1 回だけ
 * 警告する (deps.token での差し替え時は出さない)。
 */
export function createAuth(deps: AuthDeps = {}): Auth {
  const now = deps.now ?? (() => Date.now());
  let raw: string | null;
  if (deps.token === undefined) {
    const envToken = Deno.env.get("AUTH_TOKEN");
    if (envToken !== undefined && envToken.trim() === "") {
      if (!warnedBlankToken) {
        warnedBlankToken = true;
        console.warn(
          "[auth] AUTH_TOKEN は空のため認証を無効化しました (fail-open)",
        );
      }
    }
    raw = envToken ?? null;
  } else {
    raw = deps.token;
  }
  const secret = raw !== null && raw.trim() !== "" ? raw : null;

  // AUTH_TOKEN のダイジェストは最初の比較時に一度だけ計算してキャッシュする。
  let secretDigest: Uint8Array | null = null;

  /** 提示された文字列をダイジェストにして AUTH_TOKEN と一定時間比較する。 */
  async function matchesSecret(candidate: string): Promise<boolean> {
    if (secret === null) {
      return false;
    }
    secretDigest ??= await sha256(secret);
    return timingSafeEqual(secretDigest, await sha256(candidate));
  }

  /** リクエストヘッダから認証情報を取り出して検証する。 */
  async function headerCredentialOk(c: Context): Promise<boolean> {
    const authorization = c.req.header("authorization");
    if (authorization !== undefined) {
      const space = authorization.indexOf(" ");
      const scheme = space === -1
        ? authorization
        : authorization.slice(0, space);
      const value = space === -1 ? "" : authorization.slice(space + 1);
      if (scheme.toLowerCase() === "bearer") {
        if (value !== "" && await matchesSecret(value)) {
          return true;
        }
      } else if (scheme.toLowerCase() === "basic") {
        let decoded: string;
        try {
          decoded = atob(value);
        } catch {
          decoded = "";
        }
        // パスワード部分 (最初の ":" 以降) だけを見る。ユーザー名は任意。
        const colon = decoded.indexOf(":");
        const password = colon === -1 ? decoded : decoded.slice(colon + 1);
        if (await matchesSecret(password)) {
          return true;
        }
      }
    }
    const xToken = c.req.header("x-auth-token");
    if (xToken !== undefined && await matchesSecret(xToken)) {
      return true;
    }
    return false;
  }

  // チケットは発行側・提示側とも SHA-256 ダイジェストをキー/照合に使うため、
  // チケット文字列そのものを比較しない (比較のタイミングに漏れない)。
  const tickets = new Map<string, { expiresAt: number; usesLeft: number }>();

  /** 期限切れのチケットを掃除する (発行時・検証時に呼ぶ)。 */
  function pruneTickets(nowMs: number): void {
    for (const [key, entry] of tickets) {
      if (entry.expiresAt <= nowMs) {
        tickets.delete(key);
      }
    }
  }

  async function ticketKeyOf(ticket: string): Promise<string> {
    return hexOf(await sha256(ticket));
  }

  async function issueTicket(): Promise<string> {
    pruneTickets(now());
    const ticket = base64UrlOf(
      crypto.getRandomValues(new Uint8Array(TICKET_BYTES)),
    );
    tickets.set(await ticketKeyOf(ticket), {
      expiresAt: now() + TICKET_TTL_MS,
      usesLeft: TICKET_MAX_USES,
    });
    return ticket;
  }

  /** チケットを使う。有効なら 1 回消費し、使用回数を使い切れば破棄する。 */
  async function consumeTicket(candidate: string): Promise<boolean> {
    pruneTickets(now());
    const key = await ticketKeyOf(candidate);
    const entry = tickets.get(key);
    if (entry === undefined || entry.expiresAt <= now()) {
      return false;
    }
    entry.usesLeft -= 1;
    if (entry.usesLeft <= 0) {
      tickets.delete(key);
    }
    return true;
  }

  const middleware: MiddlewareHandler = async (c, next) => {
    if (secret === null) {
      return await next();
    }
    if (await headerCredentialOk(c)) {
      return await next();
    }
    // EventSource・mpegts.js はヘッダーを付けられないため、ストリーム系の
    // GET だけチケットをクエリパラメータで受け付ける。
    const ticket = c.req.query("ticket");
    const isStreamGet = c.req.method === "GET" &&
      TICKET_PATH_PREFIXES.some((prefix) => c.req.path.startsWith(prefix));
    if (isStreamGet && ticket !== undefined && await consumeTicket(ticket)) {
      return await next();
    }
    // /api/* は JSON、それ以外 (静的配信・SPA) はブラウザのネイティブ認証
    // ダイアログを誘う Basic 案内のプレーンテキストで 401 を返す。
    if (c.req.path.startsWith("/api")) {
      return c.json({ error: "unauthorized" }, 401);
    }
    return c.text("unauthorized", 401, {
      "WWW-Authenticate": 'Basic realm="mirakc-ui", charset="UTF-8"',
    });
  };

  const routes = new Hono();
  routes.post("/ticket", async (c) => {
    // 認証無効なら null を返す (クライアントは無条件でプローブできる)。
    // 有効時はミドルウェアが既にヘッダー認証を通している。チケットは
    // ストリーム系の GET でしか受け付けないため、チケットからの発行は
    // この地点に到達できない。
    if (secret === null) {
      return c.json({ ticket: null });
    }
    return c.json({ ticket: await issueTicket() });
  });

  return { middleware, routes };
}
