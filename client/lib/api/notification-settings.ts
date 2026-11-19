/**
 * ntfy 通知設定 API (/api/notification-settings) のクライアント。
 *
 * mirakc-ui 自身の API のため $api (mirakc の OpenAPI 由来) には含まれない。
 * 素の fetch を薄くラップする。docs/api の OpenAPI が単一ソースだが、§6.2 の
 * 書き込み専用シークレット変更で GET (応答) と PUT (入力) の形状が分かれた
 * ため、ここでは docs/api の契約に従う型を明示する (サーバー側はこれまで通り
 * `deno task generate:internal` 生成の JSON Schema から導く)。シークレット
 * (ntfy の token / Discord の Webhook URL) は GET が値を返さず
 * tokenSet / discordWebhookUrlSet のフラグのみで、PUT では省略か空文字なら
 * 保存済み値を保持し、非空なら置き換える。
 */

/** GET /api/notification-settings の応答。シークレットは設定済みフラグのみ。 */
export type NotificationSettings = {
  /** 通知先 ntfy のトピックまで含む URL。空文字は ntfy 通知の無効を表す。 */
  url: string;
  /** ntfy のアクセストークンが保存済みか。値そのものは API から返らない。 */
  tokenSet: boolean;
  /** Discord の Webhook URL が保存済みか。値そのものは API から返らない。 */
  discordWebhookUrlSet: boolean;
  /** 録画予約の登録 (キーワード自動録画と手動) を通知するか。 */
  onSchedule: boolean;
  /** 録画開始を通知するか。 */
  onStart: boolean;
  /** 録画終了を通知するか。 */
  onEnd: boolean;
  /** 録画失敗を通知するか。 */
  onFail: boolean;
  /** 録画予約の削除を通知するか。 */
  onRemove: boolean;
};

/**
 * PUT /api/notification-settings のリクエスト body。シークレット
 * (token / discordWebhookUrl) は書き込み専用: 省略か空文字なら保存済み値を
 * 保持、非空なら置き換え、明示的な null ならクリアする (§6.2)。フォームは
 * 「入力が非空のときだけ送る」ため null を使わない。それ以外のフィールドは
 * 完全な上書き。
 */
export type NotificationSettingsInput = {
  url: string;
  token?: string | null;
  discordWebhookUrl?: string | null;
  onSchedule: boolean;
  onStart: boolean;
  onEnd: boolean;
  onFail: boolean;
  onRemove: boolean;
};

/**
 * 未保存時の既定値 (GET が未保存で返す JSON と同じ形状)。通知はすべて無効。
 * サーバー側の DEFAULT_NOTIFICATION_SETTINGS は PUT 入力 (シークレット含む)
 * の既定で形状が異なるため、クライアントでは応答形状の既定をここに置く。
 */
export const DEFAULT_NOTIFICATION_SETTINGS: NotificationSettings = {
  url: "",
  tokenSet: false,
  discordWebhookUrlSet: false,
  onSchedule: false,
  onStart: false,
  onEnd: false,
  onFail: false,
  onRemove: false,
};

const BASE_PATH = "/api/notification-settings";

async function ensureOk(res: Response): Promise<Response> {
  if (!res.ok) {
    await res.body?.cancel();
    throw new Error(`notification-settings api failed: ${res.status}`);
  }
  return res;
}

/** 設定を取得する (未保存なら既定値が返る)。 */
export async function fetchNotificationSettings(
  fetchFn: typeof fetch = fetch,
): Promise<NotificationSettings> {
  const res = await ensureOk(await fetchFn(BASE_PATH));
  return await res.json();
}

/** 設定を保存する。シークレット未指定ならサーバーは保存済み値を保持する。 */
export async function saveNotificationSettings(
  input: NotificationSettingsInput,
  fetchFn: typeof fetch = fetch,
): Promise<NotificationSettings> {
  const res = await ensureOk(
    await fetchFn(BASE_PATH, {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(input),
    }),
  );
  return await res.json();
}

/** 入力中の url / token で ntfy へテスト通知を送る。失敗は throw。 */
export async function sendTestNtfy(
  target: { url: string; token: string },
  fetchFn: typeof fetch = fetch,
): Promise<void> {
  const res = await ensureOk(
    await fetchFn(`${BASE_PATH}/test/ntfy`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(target),
    }),
  );
  await res.body?.cancel();
}

/** 入力中の webhookUrl で Discord へテスト通知を送る。失敗は throw。 */
export async function sendTestDiscord(
  target: { webhookUrl: string },
  fetchFn: typeof fetch = fetch,
): Promise<void> {
  const res = await ensureOk(
    await fetchFn(`${BASE_PATH}/test/discord`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(target),
    }),
  );
  await res.body?.cancel();
}
