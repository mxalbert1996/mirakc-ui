/**
 * ストリーム視聴用のワンタイムチケット。
 *
 * AUTH_TOKEN 有効時、EventSource / mpegts.js は Authorization ヘッダを送れない
 * ため、ストリームを開く直前に `POST /api/auth/ticket` で短命チケットを取得し、
 * URL の `ticket` クエリとして渡す (サーバ側で ~60 秒・数回上限で検証。自動再接続
 * のたびに同じ URL が再要求されるため、厳密な 1 回きりにはしない)。
 *
 * 同一生成元への既定 credentials 付き fetch なので、Basic 認証でログイン済み
 * ならブラウザが自動でヘッダを付ける。AUTH_TOKEN 無効 (既定) ならサーバは
 * `{"ticket":null}` を返し、URL は変わらない。
 */

/**
 * チケットを取得する。認証無効 (`ticket: null`)・非 200・通信失敗のいずれも
 * `null` を返す。呼び出し側は `null` ならストリーム URL を変えずに開き、失敗は
 * ストリーム自身の既存エラー経路で見せる。
 */
export async function fetchStreamTicket(): Promise<string | null> {
  try {
    const response = await fetch("/api/auth/ticket", { method: "POST" });
    if (!response.ok) {
      return null;
    }
    const body: unknown = await response.json();
    if (
      typeof body === "object" && body !== null &&
      typeof (body as { ticket?: unknown }).ticket === "string"
    ) {
      return (body as { ticket: string }).ticket;
    }
    return null;
  } catch {
    return null;
  }
}

/** URL に ticket クエリを付ける。`null` なら URL をそのまま返す (認証無効)。 */
export function withTicketParam(url: string, ticket: string | null): string {
  if (ticket === null) {
    return url;
  }
  const separator = url.includes("?") ? "&" : "?";
  return `${url}${separator}ticket=${encodeURIComponent(ticket)}`;
}
