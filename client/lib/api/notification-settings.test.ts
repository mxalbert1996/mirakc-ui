import { describe, expect, it } from "vitest";
import {
  fetchNotificationSettings,
  saveNotificationSettings,
  sendTestDiscord,
  sendTestNtfy,
} from "./notification-settings.ts";

// GET の応答 (§6.2: シークレットは値を返さず設定済みフラグのみ)。
const stored = {
  url: "https://ntfy.sh/mirakc-rec",
  tokenSet: true,
  discordWebhookUrlSet: false,
  onSchedule: true,
  onStart: true,
  onEnd: false,
  onFail: true,
  onRemove: false,
};

describe("notification-settings api client", () => {
  it("fetchNotificationSettings: GET の JSON を返す", async () => {
    const fetchFn = ((requestUrl: RequestInfo | URL) => {
      expect(String(requestUrl)).toBe("/api/notification-settings");
      return Promise.resolve(Response.json(stored));
    }) as typeof fetch;

    expect(await fetchNotificationSettings(fetchFn)).toEqual(stored);
  });

  it("saveNotificationSettings: PUT して保存結果を返す", async () => {
    const input = {
      url: stored.url,
      token: "tk_new",
      onSchedule: true,
      onStart: true,
      onEnd: false,
      onFail: true,
      onRemove: false,
    };
    const fetchFn = ((requestUrl: RequestInfo | URL, init?: RequestInit) => {
      expect(String(requestUrl)).toBe("/api/notification-settings");
      expect(init?.method).toBe("PUT");
      expect(JSON.parse(String(init?.body))).toEqual(input);
      return Promise.resolve(Response.json(stored));
    }) as typeof fetch;

    expect(await saveNotificationSettings(input, fetchFn)).toEqual(stored);
  });

  it("saveNotificationSettings: シークレット未入力なら body から省略される", async () => {
    // シークレット無しの body はそのまま送られる (サーバーは保存済み値を保持)。
    const input = {
      url: stored.url,
      onSchedule: true,
      onStart: true,
      onEnd: false,
      onFail: true,
      onRemove: false,
    };
    const fetchFn = ((_requestUrl: RequestInfo | URL, init?: RequestInit) => {
      const body = JSON.parse(String(init?.body));
      expect(body).toEqual(input);
      expect("token" in body).toBe(false);
      expect("discordWebhookUrl" in body).toBe(false);
      return Promise.resolve(Response.json(stored));
    }) as typeof fetch;

    expect(await saveNotificationSettings(input, fetchFn)).toEqual(stored);
  });

  it("saveNotificationSettings: クリア (null) もそのまま送る", async () => {
    const input = {
      url: "",
      token: null,
      onSchedule: false,
      onStart: false,
      onEnd: false,
      onFail: false,
      onRemove: false,
    };
    const fetchFn = ((requestUrl: RequestInfo | URL, init?: RequestInit) => {
      expect(String(requestUrl)).toBe("/api/notification-settings");
      expect(JSON.parse(String(init?.body))).toEqual(input);
      return Promise.resolve(Response.json(stored));
    }) as typeof fetch;

    expect(await saveNotificationSettings(input, fetchFn)).toEqual(stored);
  });

  it("sendTestNtfy: POST /test/ntfy に url/token を送る", async () => {
    const fetchFn = ((requestUrl: RequestInfo | URL, init?: RequestInit) => {
      expect(String(requestUrl)).toBe("/api/notification-settings/test/ntfy");
      expect(init?.method).toBe("POST");
      expect(JSON.parse(String(init?.body))).toEqual({
        url: stored.url,
        token: "tk_draft",
      });
      return Promise.resolve(Response.json({ ok: true }));
    }) as typeof fetch;

    await sendTestNtfy({ url: stored.url, token: "tk_draft" }, fetchFn);
  });

  it("sendTestDiscord: POST /test/discord に webhookUrl を送る", async () => {
    const webhookUrl = "https://discord.com/api/webhooks/123/abc";
    const fetchFn = ((requestUrl: RequestInfo | URL, init?: RequestInit) => {
      expect(String(requestUrl)).toBe("/api/notification-settings/test/discord");
      expect(JSON.parse(String(init?.body))).toEqual({ webhookUrl });
      return Promise.resolve(Response.json({ ok: true }));
    }) as typeof fetch;

    await sendTestDiscord({ webhookUrl }, fetchFn);
  });

  it("エラー応答は throw する", async () => {
    const fetchFn = (() => Promise.resolve(new Response("ng", { status: 502 }))) as typeof fetch;

    await expect(fetchNotificationSettings(fetchFn)).rejects.toThrow();
    await expect(
      saveNotificationSettings(
        {
          url: "https://ntfy.sh/mirakc-rec",
          onSchedule: false,
          onStart: false,
          onEnd: false,
          onFail: false,
          onRemove: false,
        },
        fetchFn,
      ),
    ).rejects.toThrow();
    await expect(sendTestNtfy({ url: "x", token: "" }, fetchFn)).rejects.toThrow();
    await expect(sendTestDiscord({ webhookUrl: "x" }, fetchFn)).rejects.toThrow();
  });
});
