import { Hono } from "hono";
import {
  isValidDiscordWebhookUrl,
  isValidNtfyUrl,
  mergeNotificationSettings,
  type NotificationSettings,
  parseNotificationSettingsInput,
  toNotificationSettingsView,
} from "../lib/notification-settings.ts";

/**
 * ルートが必要とするストア操作。NotificationSettingsStore のサブセット
 * (テストでフェイクに差し替えられるよう構造的に定義する)。
 */
export type NotificationSettingsStoreLike = {
  get(): Promise<NotificationSettings>;
  set(settings: NotificationSettings): Promise<NotificationSettings>;
};

/** ntfy へのテスト送信の宛先。 */
export type NtfyTestTarget = { url: string; token: string };

/** Discord へのテスト送信の宛先。 */
export type DiscordTestTarget = { webhookUrl: string };

/** テスト送信の実体 (sendNtfy / sendDiscord のラッパー) を注入する。 */
export type NotificationTestSender = {
  sendTestNtfy(target: NtfyTestTarget): Promise<boolean>;
  sendTestDiscord(target: DiscordTestTarget): Promise<boolean>;
};

/**
 * 通知設定の API。`/api/notification-settings` にマウントする。
 *
 * - GET  /              設定のマスク済み View (未保存なら既定値)。token と
 *                       discordWebhookUrl は書き込み専用のため、値のかわりに
 *                       tokenSet / discordWebhookUrlSet だけを返す
 * - PUT  /              設定の保存。url とトグルは上書きし、シークレットは
 *                       未指定・空文字なら現行値を保持・非空なら置き換え・
 *                       明示的な null なら空にする (現行値とのマージはこの
 *                       ルートが store へアクセスできる立場で行う)
 * - POST /test/ntfy     ntfy へのテスト送信。body は保存前の draft (url / token)。
 *                       失敗は 502
 * - POST /test/discord  Discord へのテスト送信。body は保存前の draft (webhookUrl)。
 *                       失敗は 502
 */
export function createNotificationSettingsRoutes(
  store: NotificationSettingsStoreLike,
  sender: NotificationTestSender,
): Hono {
  const app = new Hono();

  app.get("/", async (c) => {
    return c.json(toNotificationSettingsView(await store.get()));
  });

  app.put("/", async (c) => {
    let parsed: ReturnType<typeof parseNotificationSettingsInput>;
    try {
      parsed = parseNotificationSettingsInput(await c.req.json());
    } catch {
      parsed = { ok: false, error: "invalid JSON body" };
    }
    if (!parsed.ok) {
      return c.json({ error: parsed.error }, 400);
    }
    // シークレットは書き込み専用 (未指定・空文字は保持) のため、保存値を
    // マージしてから書き戻す。
    const merged = mergeNotificationSettings(await store.get(), parsed.input);
    if (!merged.ok) {
      return c.json({ error: merged.error }, 400);
    }
    return c.json(toNotificationSettingsView(await store.set(merged.settings)));
  });

  app.post("/test/ntfy", async (c) => {
    let body: Record<string, unknown>;
    try {
      body = await c.req.json();
    } catch {
      return c.json({ error: "invalid JSON body" }, 400);
    }
    const url = typeof body.url === "string" ? body.url.trim() : "";
    const token = typeof body.token === "string" ? body.token.trim() : "";
    if (!isValidNtfyUrl(url)) {
      return c.json({ error: "url must be a http(s) URL with a topic" }, 400);
    }

    const ok = await sender.sendTestNtfy({ url, token });
    if (!ok) {
      return c.json({ error: "failed to send test notification" }, 502);
    }
    return c.json({ ok: true });
  });

  app.post("/test/discord", async (c) => {
    let body: Record<string, unknown>;
    try {
      body = await c.req.json();
    } catch {
      return c.json({ error: "invalid JSON body" }, 400);
    }
    const webhookUrl = typeof body.webhookUrl === "string"
      ? body.webhookUrl.trim()
      : "";
    if (!isValidDiscordWebhookUrl(webhookUrl)) {
      return c.json({ error: "webhookUrl must be a Discord webhook URL" }, 400);
    }

    const ok = await sender.sendTestDiscord({ webhookUrl });
    if (!ok) {
      return c.json({ error: "failed to send test notification" }, 502);
    }
    return c.json({ ok: true });
  });

  return app;
}
