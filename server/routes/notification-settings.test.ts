import { assertEquals } from "@std/assert";
import { createNotificationSettingsRoutes } from "./notification-settings.ts";
import {
  DEFAULT_NOTIFICATION_SETTINGS,
  type NotificationSettings,
} from "../lib/notification-settings.ts";

function fakeStore(initial?: NotificationSettings) {
  let settings = initial ?? DEFAULT_NOTIFICATION_SETTINGS;
  return {
    get: () => Promise.resolve(settings),
    set: (next: NotificationSettings) => {
      settings = next;
      return Promise.resolve(settings);
    },
  };
}

function fakeSender(result: boolean) {
  const ntfy: { url: string; token: string }[] = [];
  const discord: { webhookUrl: string }[] = [];
  return {
    ntfy,
    discord,
    sendTestNtfy: (target: { url: string; token: string }) => {
      ntfy.push(target);
      return Promise.resolve(result);
    },
    sendTestDiscord: (target: { webhookUrl: string }) => {
      discord.push(target);
      return Promise.resolve(result);
    },
  };
}

Deno.test("GET /: マスク済みの設定を返す (未保存なら既定値)", async () => {
  const app = createNotificationSettingsRoutes(fakeStore(), fakeSender(true));

  const res = await app.request("/");
  assertEquals(res.status, 200);
  assertEquals(await res.json(), {
    url: "",
    tokenSet: false,
    discordWebhookUrlSet: false,
    onSchedule: false,
    onStart: false,
    onEnd: false,
    onFail: false,
    onRemove: false,
  });
});

Deno.test("GET /: シークレットが設定済みなら Set が true になる", async () => {
  const app = createNotificationSettingsRoutes(
    fakeStore({
      ...DEFAULT_NOTIFICATION_SETTINGS,
      url: "https://ntfy.sh/x",
      token: "tk",
      discordWebhookUrl: "https://discord.com/api/webhooks/123/abc",
    }),
    fakeSender(true),
  );

  const res = await app.request("/");
  assertEquals(res.status, 200);
  assertEquals(await res.json(), {
    url: "https://ntfy.sh/x",
    tokenSet: true,
    discordWebhookUrlSet: true,
    onSchedule: false,
    onStart: false,
    onEnd: false,
    onFail: false,
    onRemove: false,
  });
});

Deno.test("PUT /: 設定を保存してマスク済みの View を返す", async () => {
  const store = fakeStore();
  const app = createNotificationSettingsRoutes(store, fakeSender(true));

  const res = await app.request("/", {
    method: "PUT",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      url: " https://ntfy.sh/mirakc-rec ",
      token: "tk",
      onStart: true,
      onEnd: false,
    }),
  });
  assertEquals(res.status, 200);
  assertEquals(await res.json(), {
    url: "https://ntfy.sh/mirakc-rec",
    tokenSet: true,
    discordWebhookUrlSet: false,
    onSchedule: false,
    onStart: true,
    onEnd: false,
    onFail: false,
    onRemove: false,
  });
  // ストアには平文のまま保存される。
  assertEquals(await store.get(), {
    url: "https://ntfy.sh/mirakc-rec",
    token: "tk",
    discordWebhookUrl: "",
    onSchedule: false,
    onStart: true,
    onEnd: false,
    onFail: false,
    onRemove: false,
  });
});

Deno.test("PUT /: シークレットは未指定・空文字なら保持する", async () => {
  const initial: NotificationSettings = {
    ...DEFAULT_NOTIFICATION_SETTINGS,
    url: "https://ntfy.sh/x",
    token: "tk_old",
    discordWebhookUrl: "https://discord.com/api/webhooks/123/abc",
  };
  const store = fakeStore(initial);
  const app = createNotificationSettingsRoutes(store, fakeSender(true));

  for (
    const body of [
      { url: initial.url, onSchedule: true },
      { url: initial.url, token: "", discordWebhookUrl: "", onSchedule: true },
    ]
  ) {
    const res = await app.request("/", {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    assertEquals(res.status, 200, JSON.stringify(body));
    assertEquals(await res.json(), {
      url: initial.url,
      tokenSet: true,
      discordWebhookUrlSet: true,
      onSchedule: true,
      onStart: false,
      onEnd: false,
      onFail: false,
      onRemove: false,
    }, JSON.stringify(body));
    assertEquals((await store.get()).token, "tk_old", JSON.stringify(body));
    assertEquals(
      (await store.get()).discordWebhookUrl,
      initial.discordWebhookUrl,
      JSON.stringify(body),
    );
  }
});

Deno.test("PUT /: シークレットの非空文字は置き換え、明示的な null は空にする", async () => {
  const initial: NotificationSettings = {
    ...DEFAULT_NOTIFICATION_SETTINGS,
    url: "https://ntfy.sh/x",
    token: "tk_old",
    discordWebhookUrl: "https://discord.com/api/webhooks/123/abc",
  };

  const replacedStore = fakeStore(initial);
  const replacedApp = createNotificationSettingsRoutes(
    replacedStore,
    fakeSender(true),
  );
  const replaced = await replacedApp.request("/", {
    method: "PUT",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      url: initial.url,
      token: "tk_new",
      discordWebhookUrl: "https://discord.com/api/webhooks/456/def",
    }),
  });
  assertEquals(replaced.status, 200);
  const replacedSettings = await replacedStore.get();
  assertEquals(replacedSettings.token, "tk_new");
  assertEquals(
    replacedSettings.discordWebhookUrl,
    "https://discord.com/api/webhooks/456/def",
  );

  const clearedStore = fakeStore(initial);
  const clearedApp = createNotificationSettingsRoutes(
    clearedStore,
    fakeSender(true),
  );
  const cleared = await clearedApp.request("/", {
    method: "PUT",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      url: initial.url,
      token: null,
      discordWebhookUrl: null,
    }),
  });
  assertEquals(cleared.status, 200);
  const clearedSettings = await clearedStore.get();
  assertEquals(clearedSettings.token, "");
  assertEquals(clearedSettings.discordWebhookUrl, "");
  // 応答も Set が false に変わる。
  const clearedView = await cleared.json();
  assertEquals(clearedView.tokenSet, false);
  assertEquals(clearedView.discordWebhookUrlSet, false);
});

Deno.test("PUT /: 保持した Discord Webhook があればイベント有効でも保存できる", async () => {
  const initial: NotificationSettings = {
    ...DEFAULT_NOTIFICATION_SETTINGS,
    discordWebhookUrl: "https://discord.com/api/webhooks/123/abc",
  };
  const app = createNotificationSettingsRoutes(
    fakeStore(initial),
    fakeSender(true),
  );

  // url は空のまま onStart を有効にする。シークレット未指定 (= 保持) のため
  // 保持した Webhook が通知先として成立する。
  const res = await app.request("/", {
    method: "PUT",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ url: "", onStart: true }),
  });
  assertEquals(res.status, 200);
  assertEquals((await res.json()).discordWebhookUrlSet, true);
});

Deno.test("PUT /: 不正な入力は 400", async () => {
  const app = createNotificationSettingsRoutes(fakeStore(), fakeSender(true));

  const bodies = [
    {},
    // イベント有効なのに通知先がゼロ (現行値も空)。
    { url: "", token: "", onStart: true, onEnd: false },
    { url: "not a url", token: "", onStart: false, onEnd: false },
    {
      url: "",
      token: "",
      discordWebhookUrl: "https://example.com/x",
      onStart: false,
    },
  ];
  for (const body of bodies) {
    const res = await app.request("/", {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    assertEquals(res.status, 400, JSON.stringify(body));
    await res.body?.cancel();
  }
});

Deno.test("POST /test/ntfy: draft の url/token で ntfy テスト送信する", async () => {
  const sender = fakeSender(true);
  const app = createNotificationSettingsRoutes(fakeStore(), sender);

  const res = await app.request("/test/ntfy", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ url: "https://ntfy.sh/mirakc", token: "tk" }),
  });
  assertEquals(res.status, 200);
  assertEquals(await res.json(), { ok: true });
  assertEquals(sender.ntfy, [{ url: "https://ntfy.sh/mirakc", token: "tk" }]);
  assertEquals(sender.discord, []);
});

Deno.test("POST /test/discord: draft の webhookUrl で Discord テスト送信する", async () => {
  const sender = fakeSender(true);
  const app = createNotificationSettingsRoutes(fakeStore(), sender);

  const res = await app.request("/test/discord", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      webhookUrl: " https://discord.com/api/webhooks/123/abc ",
    }),
  });
  assertEquals(res.status, 200);
  assertEquals(await res.json(), { ok: true });
  assertEquals(sender.discord, [
    { webhookUrl: "https://discord.com/api/webhooks/123/abc" },
  ]);
  assertEquals(sender.ntfy, []);
});

Deno.test("POST /test/ntfy: 不正な URL は 400、送信失敗は 502", async () => {
  const invalid = await createNotificationSettingsRoutes(
    fakeStore(),
    fakeSender(true),
  ).request("/test/ntfy", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ url: "https://ntfy.sh/", token: "" }),
  });
  assertEquals(invalid.status, 400);
  await invalid.body?.cancel();

  const failed = await createNotificationSettingsRoutes(
    fakeStore(),
    fakeSender(false),
  ).request("/test/ntfy", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ url: "https://ntfy.sh/mirakc", token: "" }),
  });
  assertEquals(failed.status, 502);
  await failed.body?.cancel();
});

Deno.test("POST /test/discord: 不正な webhookUrl は 400、送信失敗は 502", async () => {
  const sender = fakeSender(true);
  const invalid = await createNotificationSettingsRoutes(
    fakeStore(),
    sender,
  ).request("/test/discord", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ webhookUrl: "https://example.com/not-a-webhook" }),
  });
  assertEquals(invalid.status, 400);
  await invalid.body?.cancel();
  assertEquals(sender.discord, []);

  const failed = await createNotificationSettingsRoutes(
    fakeStore(),
    fakeSender(false),
  ).request("/test/discord", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      webhookUrl: "https://discord.com/api/webhooks/123/abc",
    }),
  });
  assertEquals(failed.status, 502);
  await failed.body?.cancel();
});
