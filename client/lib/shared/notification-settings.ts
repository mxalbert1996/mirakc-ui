/**
 * ntfy / Discord notification settings: event toggle keys and URL checks.
 *
 * Client-side contract module. The Go server (server-go/internal/settings)
 * implements the same contract from the same docs/api OpenAPI; drift is
 * caught by the CI schema regen and the Go tests. Persistence, merging and
 * request validation are server-side concerns — this module keeps what the
 * notification form uses: the toggle keys (display order) and the
 * destination URL format checks that gate the test buttons and save state.
 */

/**
 * 通知イベントのトグルキー (表示順)。
 * 録画登録 → 開始 → 終了 → 失敗 → 削除。
 */
export const NOTIFICATION_EVENT_KEYS = [
  "onSchedule",
  "onStart",
  "onEnd",
  "onFail",
  "onRemove",
] as const;

export type NotificationEventKey = (typeof NOTIFICATION_EVENT_KEYS)[number];

/**
 * ntfy のトピック URL を base とトピックに分解する。分解できなければ
 * null (isValidNtfyUrl の判定で使う)。
 */
function splitNtfyUrl(url: string): { base: string; topic: string } | null {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return null;
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return null;
  }

  const segments = parsed.pathname.split("/").filter((s) => s !== "");
  const topic = segments.pop();
  if (topic === undefined) {
    return null;
  }
  const basePath = segments.length > 0 ? `/${segments.join("/")}` : "";
  return { base: `${parsed.origin}${basePath}`, topic };
}

/** http(s) かつトピック (非空の最終 path セグメント) を持つ URL か。 */
export function isValidNtfyUrl(url: string): boolean {
  return splitNtfyUrl(url) !== null;
}

/**
 * Discord の Incoming Webhook URL か。`https` かつ host が discord(app).com 系
 * (ptb. / canary. サブドメイン含む)、path が `/api[/vN]/webhooks/{id}/{token}`
 * 形式かを緩く判定する。クライアントのフォーム検証で使うため厳密検証はせず、
 * 明らかな誤入力 (別サービスの URL・トピック欠落) の保存を防ぐ程度に留める。
 */
export function isValidDiscordWebhookUrl(url: string): boolean {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return false;
  }
  if (parsed.protocol !== "https:") {
    return false;
  }
  const host = parsed.hostname;
  const hostOk =
    host === "discord.com" ||
    host === "discordapp.com" ||
    host.endsWith(".discord.com") ||
    host.endsWith(".discordapp.com");
  if (!hostOk) {
    return false;
  }
  const path = parsed.pathname.replace(/\/$/, "");
  return /^\/api(?:\/v\d+)?\/webhooks\/\d+\/[\w-]+$/.test(path);
}
