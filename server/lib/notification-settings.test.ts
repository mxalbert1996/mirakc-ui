import { assertEquals } from "@std/assert";
import {
  DEFAULT_NOTIFICATION_SETTINGS,
  hasEnabledDestination,
  isValidDiscordWebhookUrl,
  isValidNtfyUrl,
  mergeNotificationSettings,
  normalizeNotificationSettings,
  type NotificationSettings,
  parseNotificationSettingsInput,
  splitNtfyUrl,
  toNotificationSettingsView,
} from "./notification-settings.ts";

Deno.test("isValidNtfyUrl: http(s) + 非空トピックのみ true", () => {
  assertEquals(isValidNtfyUrl("https://ntfy.sh/mirakc-rec"), true);
  assertEquals(isValidNtfyUrl("http://ntfy.example.com/topic"), true);
  // セルフホストのサブパス配下も許容する。
  assertEquals(isValidNtfyUrl("https://example.com/ntfy/topic"), true);

  assertEquals(isValidNtfyUrl(""), false);
  assertEquals(isValidNtfyUrl("ntfy.sh/topic"), false);
  assertEquals(isValidNtfyUrl("ftp://ntfy.sh/topic"), false);
  // トピックなし。
  assertEquals(isValidNtfyUrl("https://ntfy.sh"), false);
  assertEquals(isValidNtfyUrl("https://ntfy.sh/"), false);
});

Deno.test("splitNtfyUrl: 最終セグメントを topic、残りを base に分解", () => {
  assertEquals(splitNtfyUrl("https://ntfy.sh/mirakc-rec"), {
    base: "https://ntfy.sh",
    topic: "mirakc-rec",
  });
  assertEquals(splitNtfyUrl("https://example.com/ntfy/topic"), {
    base: "https://example.com/ntfy",
    topic: "topic",
  });
  assertEquals(splitNtfyUrl("https://ntfy.sh/topic/"), {
    base: "https://ntfy.sh",
    topic: "topic",
  });
  assertEquals(splitNtfyUrl("https://ntfy.sh"), null);
  assertEquals(splitNtfyUrl("not a url"), null);
});

Deno.test("normalize: 新形状はそのまま通す", () => {
  const settings = {
    url: "https://ntfy.sh/x",
    token: "tk",
    discordWebhookUrl: "https://discord.com/api/webhooks/123/abc",
    onSchedule: true,
    onStart: true,
    onEnd: false,
    onFail: true,
    onRemove: false,
  };
  assertEquals(normalizeNotificationSettings(settings), settings);
});

Deno.test("normalize: 旧形状 (onStart/onEnd のみ) は新トグルを false 補完", () => {
  assertEquals(
    normalizeNotificationSettings({
      url: "https://ntfy.sh/x",
      token: "tk",
      onStart: true,
      onEnd: false,
    }),
    {
      url: "https://ntfy.sh/x",
      token: "tk",
      discordWebhookUrl: "",
      onSchedule: false,
      onStart: true,
      onEnd: false,
      onFail: false,
      onRemove: false,
    },
  );
});

Deno.test("normalize: Discord 追加前の旧形状は discordWebhookUrl を空で補完", () => {
  const result = normalizeNotificationSettings({
    url: "https://ntfy.sh/x",
    token: "tk",
    onSchedule: false,
    onStart: false,
    onEnd: false,
    onFail: false,
    onRemove: false,
  });
  assertEquals(result?.discordWebhookUrl, "");
});

Deno.test("normalize: 不正な形は null", () => {
  assertEquals(normalizeNotificationSettings(null), null);
  assertEquals(normalizeNotificationSettings({}), null);
  assertEquals(
    normalizeNotificationSettings({ url: 1, token: "" }),
    null,
  );
  assertEquals(
    normalizeNotificationSettings({ url: "", token: "", onStart: "yes" }),
    null,
  );
  // discordWebhookUrl が string 以外なら null。
  assertEquals(
    normalizeNotificationSettings({ url: "", token: "", discordWebhookUrl: 1 }),
    null,
  );
});

Deno.test("parse: 正常系は trim と既定値 (未指定トグル false) を適用する", () => {
  const result = parseNotificationSettingsInput({
    url: " https://ntfy.sh/mirakc-rec ",
    token: " tk_abc ",
    onStart: true,
    onEnd: false,
  });
  assertEquals(result, {
    ok: true,
    input: {
      url: "https://ntfy.sh/mirakc-rec",
      token: "tk_abc",
      discordWebhookUrl: undefined,
      onSchedule: false,
      onStart: true,
      onEnd: false,
      onFail: false,
      onRemove: false,
    },
  });
});

Deno.test("parse: シークレットは未指定・空文字を保持、null を空にする合図にする", () => {
  const draftOf = (value: unknown) => {
    const result = parseNotificationSettingsInput(value);
    if (!result.ok) {
      throw new Error(`parse failed: ${result.error}`);
    }
    return result.input;
  };

  // 未指定。
  assertEquals(draftOf({ url: "" }).token, undefined);
  // 空文字・空白のみも保持。
  assertEquals(draftOf({ url: "", token: "  " }).token, undefined);
  assertEquals(
    draftOf({ url: "", discordWebhookUrl: "" }).discordWebhookUrl,
    undefined,
  );
  // 明示的な null は空にする合図。
  assertEquals(draftOf({ url: "", token: null }).token, null);
  assertEquals(
    draftOf({ url: "", discordWebhookUrl: null }).discordWebhookUrl,
    null,
  );
});

Deno.test("parse: 不正な型は ok:false", () => {
  const invalid: unknown[] = [
    null,
    {},
    { url: 1 },
    { url: "", token: 1 },
    { url: "", token: "", onStart: "yes" },
    { url: "", token: "", onFail: 1 },
    { url: "", discordWebhookUrl: 1 },
  ];
  for (const value of invalid) {
    assertEquals(
      parseNotificationSettingsInput(value).ok,
      false,
      JSON.stringify(value),
    );
  }

  assertEquals(
    DEFAULT_NOTIFICATION_SETTINGS.onSchedule,
    false,
  );
  assertEquals(DEFAULT_NOTIFICATION_SETTINGS.discordWebhookUrl, "");
});

Deno.test("parse: url が非空なら OFF でも形式を検証する", () => {
  const result = parseNotificationSettingsInput({
    url: "not a url",
    token: "",
    onStart: false,
    onEnd: false,
  });
  assertEquals(result.ok, false);
});

Deno.test("parse: discordWebhookUrl が非空なら形式を検証する", () => {
  const result = parseNotificationSettingsInput({
    url: "",
    token: "",
    discordWebhookUrl: "https://example.com/not-a-webhook",
    onStart: false,
  });
  assertEquals(result.ok, false);
});

Deno.test("parse: Discord Webhook だけでも (ntfy 空でも) 通る", () => {
  const result = parseNotificationSettingsInput({
    url: "",
    token: "",
    discordWebhookUrl: " https://discord.com/api/webhooks/123/abc ",
    onStart: true,
  });
  assertEquals(result.ok, true);
  assertEquals(
    result.ok && result.input.discordWebhookUrl,
    "https://discord.com/api/webhooks/123/abc",
  );
});

Deno.test("isValidDiscordWebhookUrl: discord(app).com の webhook 形式のみ true", () => {
  assertEquals(
    isValidDiscordWebhookUrl("https://discord.com/api/webhooks/123/abc-DEF_0"),
    true,
  );
  assertEquals(
    isValidDiscordWebhookUrl(
      "https://discordapp.com/api/webhooks/123456789/tok",
    ),
    true,
  );
  // API バージョン付き、サブドメイン (canary / ptb)、末尾スラッシュも許容する。
  assertEquals(
    isValidDiscordWebhookUrl("https://discord.com/api/v10/webhooks/1/tok"),
    true,
  );
  assertEquals(
    isValidDiscordWebhookUrl("https://canary.discord.com/api/webhooks/1/tok/"),
    true,
  );

  assertEquals(isValidDiscordWebhookUrl(""), false);
  // http は不可 (Discord は https のみ)。
  assertEquals(
    isValidDiscordWebhookUrl("http://discord.com/api/webhooks/1/tok"),
    false,
  );
  // 別ホスト。
  assertEquals(
    isValidDiscordWebhookUrl("https://example.com/api/webhooks/1/tok"),
    false,
  );
  // token 欠落。
  assertEquals(
    isValidDiscordWebhookUrl("https://discord.com/api/webhooks/123"),
    false,
  );
  // id が数値でない。
  assertEquals(
    isValidDiscordWebhookUrl("https://discord.com/api/webhooks/abc/tok"),
    false,
  );
});

Deno.test("merge: 全イベント OFF なら url 空でも保存できる", () => {
  const draft = parseNotificationSettingsInput({
    url: "",
    token: "",
    onSchedule: false,
    onStart: false,
    onEnd: false,
    onFail: false,
    onRemove: false,
  });
  assertEquals(draft.ok, true);
  if (!draft.ok) {
    return;
  }
  assertEquals(
    mergeNotificationSettings(DEFAULT_NOTIFICATION_SETTINGS, draft.input),
    { ok: true, settings: DEFAULT_NOTIFICATION_SETTINGS },
  );
});

Deno.test("merge: いずれかのイベントが有効なら通知先が 1 つ必須", () => {
  for (
    const key of ["onSchedule", "onStart", "onEnd", "onFail", "onRemove"]
  ) {
    const draft = parseNotificationSettingsInput({ url: "", [key]: true });
    assertEquals(draft.ok, true, key);
    if (!draft.ok) {
      continue;
    }
    assertEquals(
      mergeNotificationSettings(DEFAULT_NOTIFICATION_SETTINGS, draft.input).ok,
      false,
      key,
    );
  }
});

Deno.test("merge: 保持した Discord Webhook があればイベント有効でも通る", () => {
  const current: NotificationSettings = {
    ...DEFAULT_NOTIFICATION_SETTINGS,
    discordWebhookUrl: "https://discord.com/api/webhooks/123/abc",
  };
  // シークレットを未指定 (保持) でイベントを有効にする保存。
  const draft = parseNotificationSettingsInput({ url: "", onStart: true });
  assertEquals(draft.ok, true);
  if (!draft.ok) {
    return;
  }
  const merged = mergeNotificationSettings(current, draft.input);
  assertEquals(merged.ok, true);
  assertEquals(
    merged.ok && merged.settings.discordWebhookUrl,
    current.discordWebhookUrl,
  );
});

Deno.test("merge: シークレットは未指定なら保持・非空なら置き換え・null なら空にする", () => {
  const current: NotificationSettings = {
    ...DEFAULT_NOTIFICATION_SETTINGS,
    url: "https://ntfy.sh/x",
    token: "tk_old",
    discordWebhookUrl: "https://discord.com/api/webhooks/123/abc",
  };

  // 未指定 (と空文字) は保持。
  for (
    const body of [{ url: current.url }, {
      url: current.url,
      token: "",
      discordWebhookUrl: "",
    }]
  ) {
    const draft = parseNotificationSettingsInput(body);
    assertEquals(draft.ok, true, JSON.stringify(body));
    if (!draft.ok) {
      continue;
    }
    const merged = mergeNotificationSettings(current, draft.input);
    assertEquals(merged.ok, true, JSON.stringify(body));
    assertEquals(
      merged.ok && merged.settings.token,
      "tk_old",
      JSON.stringify(body),
    );
    assertEquals(
      merged.ok && merged.settings.discordWebhookUrl,
      "https://discord.com/api/webhooks/123/abc",
      JSON.stringify(body),
    );
  }

  // 非空は置き換え。
  const replaced = mergeNotificationSettings(current, {
    url: current.url,
    token: "tk_new",
    discordWebhookUrl: "https://discord.com/api/webhooks/456/def",
    onSchedule: false,
    onStart: false,
    onEnd: false,
    onFail: false,
    onRemove: false,
  });
  assertEquals(replaced, {
    ok: true,
    settings: {
      ...current,
      token: "tk_new",
      discordWebhookUrl: "https://discord.com/api/webhooks/456/def",
    },
  });

  // 明示的な null は空にする。
  const cleared = mergeNotificationSettings(current, {
    url: current.url,
    token: null,
    discordWebhookUrl: null,
    onSchedule: false,
    onStart: false,
    onEnd: false,
    onFail: false,
    onRemove: false,
  });
  assertEquals(cleared, {
    ok: true,
    settings: {
      ...current,
      token: "",
      discordWebhookUrl: "",
    },
  });
});

Deno.test("toNotificationSettingsView: シークレットを出さず設定済みかどうかだけを返す", () => {
  const unset = toNotificationSettingsView(DEFAULT_NOTIFICATION_SETTINGS);
  assertEquals(unset, {
    url: "",
    tokenSet: false,
    discordWebhookUrlSet: false,
    onSchedule: false,
    onStart: false,
    onEnd: false,
    onFail: false,
    onRemove: false,
  });
  // 平文のシークレットが応答に混ざらないこと。
  assertEquals("token" in unset, false);
  assertEquals("discordWebhookUrl" in unset, false);

  const set = toNotificationSettingsView({
    ...DEFAULT_NOTIFICATION_SETTINGS,
    url: "https://ntfy.sh/x",
    token: "tk",
    discordWebhookUrl: "https://discord.com/api/webhooks/123/abc",
  });
  assertEquals(set, {
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

Deno.test("hasEnabledDestination: トグル ON かつ妥当な通知先が 1 つ以上で true", () => {
  const base: NotificationSettings = {
    ...DEFAULT_NOTIFICATION_SETTINGS,
    onFail: true,
  };
  // 通知先が無ければ false。
  assertEquals(hasEnabledDestination(base, "onFail"), false);
  // ntfy のみ。
  assertEquals(
    hasEnabledDestination({ ...base, url: "https://ntfy.sh/x" }, "onFail"),
    true,
  );
  // Discord のみ。
  assertEquals(
    hasEnabledDestination(
      { ...base, discordWebhookUrl: "https://discord.com/api/webhooks/1/tok" },
      "onFail",
    ),
    true,
  );
  // トグル OFF なら通知先があっても false。
  assertEquals(
    hasEnabledDestination({ ...base, url: "https://ntfy.sh/x" }, "onStart"),
    false,
  );
});
