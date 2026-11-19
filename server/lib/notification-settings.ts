/**
 * ntfy / Discord 通知設定の型・バリデーション・URL ヘルパ。
 *
 * Deno API に依存しない純粋モジュールで、server (API・通知送信) と
 * client (フォーム検証・テストボタン活性判定) の両方から runtime import
 * される (server/lib/keyword-rules.ts と同じ共有パターン)。永続化は
 * server/store/notification-settings.ts (Deno KV) が担う。
 */
import type { FromSchema } from "json-schema-to-ts";
import { internalSchemas } from "./api/internal-schemas.ts";
import { matchesSchema, schemaErrorOf } from "./api/validate.ts";

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
 * 保存 API の入力 (docs/api の OpenAPI から生成した component schema が単一
 * ソース)。シークレット 2 項目 (token / discordWebhookUrl) とトグルは省略可で、
 * PUT はこの入力を現行値とマージして保存する (mergeNotificationSettings)。
 */
export type NotificationSettingsInput = FromSchema<
  typeof internalSchemas["NotificationSettings"]
>;

/**
 * 通知設定 (ストア内部形式)。入力スキーマを基に、書き込み専用のシークレット
 * 2 項目 (token / discordWebhookUrl) とトグルを必須の平文に狭めたもの。API の
 * 応答はマスク済みの NotificationSettingsView に変換して返すため、平文の
 * シークレットが API 面に出ることは無い。トグルは通知先で共有し、ON の
 * イベントを設定済みの全通知先へ送る。
 */
export type NotificationSettings =
  & Omit<
    NotificationSettingsInput,
    "token" | "discordWebhookUrl" | NotificationEventKey
  >
  & {
    token: string;
    discordWebhookUrl: string;
    onSchedule: boolean;
    onStart: boolean;
    onEnd: boolean;
    onFail: boolean;
    onRemove: boolean;
  };

/**
 * 通知設定の API 応答形式 (マスク済み)。シークレット 2 項目は値を出さず、
 * 設定済みかどうか (tokenSet / discordWebhookUrlSet) だけを返す。
 */
export type NotificationSettingsView = FromSchema<
  typeof internalSchemas["NotificationSettingsView"]
>;

/** ストア内の設定を API 応答用のマスク済み View に変換する。 */
export function toNotificationSettingsView(
  settings: NotificationSettings,
): NotificationSettingsView {
  const { token: _token, discordWebhookUrl: _discordWebhookUrl, ...rest } =
    settings;
  return {
    ...rest,
    tokenSet: settings.token !== "",
    discordWebhookUrlSet: settings.discordWebhookUrl !== "",
  };
}

/** 未保存時の既定値。通知はすべて無効。 */
export const DEFAULT_NOTIFICATION_SETTINGS: NotificationSettings = {
  url: "",
  token: "",
  discordWebhookUrl: "",
  onSchedule: false,
  onStart: false,
  onEnd: false,
  onFail: false,
  onRemove: false,
};

/**
 * KV 等から読んだ値を正規化する。トグル追加前 (onStart / onEnd のみ) や
 * Discord 追加前 (discordWebhookUrl 無し) の旧形状は既定値で補完し、保存済み
 * 設定を壊さない。形が不正なら null。
 */
export function normalizeNotificationSettings(
  value: unknown,
): NotificationSettings | null {
  if (typeof value !== "object" || value === null) {
    return null;
  }
  const settings = value as Record<string, unknown>;
  if (
    typeof settings.url !== "string" || typeof settings.token !== "string"
  ) {
    return null;
  }
  if (
    settings.discordWebhookUrl !== undefined &&
    typeof settings.discordWebhookUrl !== "string"
  ) {
    return null;
  }
  for (const key of NOTIFICATION_EVENT_KEYS) {
    if (settings[key] !== undefined && typeof settings[key] !== "boolean") {
      return null;
    }
  }
  return {
    ...DEFAULT_NOTIFICATION_SETTINGS,
    url: settings.url,
    token: settings.token,
    discordWebhookUrl: typeof settings.discordWebhookUrl === "string"
      ? settings.discordWebhookUrl
      : "",
    ...Object.fromEntries(
      NOTIFICATION_EVENT_KEYS
        .filter((key) => typeof settings[key] === "boolean")
        .map((key) => [key, settings[key]]),
    ),
  };
}

/**
 * ntfy のトピック URL を base とトピックに分解する (JSON publishing は
 * base へ POST し、body にトピックを載せるため)。分解できなければ null。
 */
export function splitNtfyUrl(
  url: string,
): { base: string; topic: string } | null {
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
 * 形式かを緩く判定する。投稿は POST するだけなので厳密検証はせず、明らかな
 * 誤入力 (別サービスの URL・トピック欠落) の保存を防ぐ程度に留める。
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
  const hostOk = host === "discord.com" || host === "discordapp.com" ||
    host.endsWith(".discord.com") || host.endsWith(".discordapp.com");
  if (!hostOk) {
    return false;
  }
  const path = parsed.pathname.replace(/\/$/, "");
  return /^\/api(?:\/v\d+)?\/webhooks\/\d+\/[\w-]+$/.test(path);
}

/**
 * イベント `key` のトグルが有効で、かつ送信できる通知先 (妥当な ntfy URL か
 * Discord Webhook URL) が少なくとも 1 つあるか。送信側 (notifyIfEnabled・SSE
 * ハンドラ) の発火判定を 1 箇所に集約する。
 */
export function hasEnabledDestination(
  settings: NotificationSettings,
  key: NotificationEventKey,
): boolean {
  return settings[key] &&
    (isValidNtfyUrl(settings.url) ||
      isValidDiscordWebhookUrl(settings.discordWebhookUrl));
}

/**
 * PUT の入力をマージ前の形に正規化したもの (parseNotificationSettingsInput
 * の結果)。シークレット 2 項目は undefined (保存済みの値を保持) / null (空に
 * する) / 非 empty 文字列 (置き換え) の 3 択で、他はすべて値が確定している。
 */
export type NotificationSettingsDraft = {
  url: string;
  token: string | null | undefined;
  discordWebhookUrl: string | null | undefined;
  onSchedule: boolean;
  onStart: boolean;
  onEnd: boolean;
  onFail: boolean;
  onRemove: boolean;
};

export type ParseResult =
  | { ok: true; input: NotificationSettingsDraft }
  | { ok: false; error: string };

export type MergeResult =
  | { ok: true; settings: NotificationSettings }
  | { ok: false; error: string };

/** value が NotificationSettings 入力スキーマに適合するかを判定する型ガード。 */
function isNotificationSettingsInput(
  value: unknown,
): value is NotificationSettingsInput {
  return matchesSchema("NotificationSettings", value);
}

/**
 * シークレット入力を draft の合図に正規化する。未指定・空白のみは「保持」
 * (undefined)、明示的な null は「空にする」(null)、非空文字列は trim して
 * 「置き換え」の値にする。
 */
function canonicalSecret(
  value: string | null | undefined,
): string | null | undefined {
  if (value === null) {
    return null;
  }
  if (value === undefined || value.trim() === "") {
    return undefined;
  }
  return value.trim();
}

/**
 * API 入力をバリデーションして正規化する。url は trim、トグルは未指定なら
 * false、シークレットは canonicalSecret の合図に直す。各 URL は非空なら
 * (イベントがすべて無効でも) 形式を検証し、壊れた URL の保存を防ぐ。
 * 「イベントが 1 つでも有効なら通知先必須」は保持した現行値との突合が
 * 必要なため mergeNotificationSettings 側で判定する。
 */
export function parseNotificationSettingsInput(value: unknown): ParseResult {
  if (!isNotificationSettingsInput(value)) {
    return {
      ok: false,
      error: schemaErrorOf("NotificationSettings", value),
    };
  }

  const url = value.url.trim();
  if (url !== "" && !isValidNtfyUrl(url)) {
    return { ok: false, error: "url must be a http(s) URL with a topic" };
  }
  const discordWebhookUrl = canonicalSecret(value.discordWebhookUrl);
  if (
    discordWebhookUrl !== undefined && discordWebhookUrl !== null &&
    !isValidDiscordWebhookUrl(discordWebhookUrl)
  ) {
    return {
      ok: false,
      error: "discordWebhookUrl must be a Discord webhook URL",
    };
  }

  return {
    ok: true,
    input: {
      url,
      token: canonicalSecret(value.token),
      discordWebhookUrl,
      onSchedule: value.onSchedule ?? false,
      onStart: value.onStart ?? false,
      onEnd: value.onEnd ?? false,
      onFail: value.onFail ?? false,
      onRemove: value.onRemove ?? false,
    },
  };
}

/**
 * draft を現行値 current にマージして、保存する設定 (平文のまま) を組む。
 * シークレット 2 項目は保持 (undefined)・空にする (null)・置き換え (非空) を
 * 解決したうえで、「イベントが 1 つでも有効なら通知先 (ntfy の url か
 * Discord の webhook) が少なくとも 1 つ必須」をマージ後の値で判定する。
 */
export function mergeNotificationSettings(
  current: NotificationSettings,
  draft: NotificationSettingsDraft,
): MergeResult {
  const settings: NotificationSettings = {
    url: draft.url,
    token: draft.token === undefined ? current.token : draft.token ?? "",
    discordWebhookUrl: draft.discordWebhookUrl === undefined
      ? current.discordWebhookUrl
      : draft.discordWebhookUrl ?? "",
    onSchedule: draft.onSchedule,
    onStart: draft.onStart,
    onEnd: draft.onEnd,
    onFail: draft.onFail,
    onRemove: draft.onRemove,
  };
  const anyEvent = NOTIFICATION_EVENT_KEYS.some((key) => settings[key]);
  if (anyEvent && settings.url === "" && settings.discordWebhookUrl === "") {
    return {
      ok: false,
      error:
        "a destination (ntfy url or discord webhook) is required when notification is enabled",
    };
  }
  return { ok: true, settings };
}
