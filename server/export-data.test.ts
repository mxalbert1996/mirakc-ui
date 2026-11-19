import { assertEquals } from "@std/assert";
import { type ExportData, writeExportData } from "./export-data.ts";
import { DEFAULT_NOTIFICATION_SETTINGS } from "./lib/notification-settings.ts";
import { createKv } from "./store/kv.ts";
import { createKeywordRuleStore } from "./store/keyword-rules.ts";
import { createLiveCommentMappingStore } from "./store/live-comment-settings.ts";
import { createNotificationSettingsStore } from "./store/notification-settings.ts";

Deno.test("export:data: 全コレクションを export.json に書き出し、値が round-trip する", async () => {
  const kv = createKv(":memory:");
  const dir = await Deno.makeTempDir();
  try {
    const ruleStore = createKeywordRuleStore(kv);
    const mappingStore = createLiveCommentMappingStore(kv);
    const notificationStore = createNotificationSettingsStore(kv);
    const rule = await ruleStore.add({
      keyword: "大相撲",
      channels: [],
      genres: [],
      enabled: true,
    });
    const mapping = await mappingStore.add({
      channel: "ch001",
      enabled: true,
      assignments: [{ source: "nicolive", channelId: "ch2646436" }],
    });
    const notification = await notificationStore.set({
      ...DEFAULT_NOTIFICATION_SETTINGS,
      url: "https://ntfy.sh/x",
      token: "tk",
      discordWebhookUrl: "https://discord.com/api/webhooks/123/abc",
    });

    const path = await writeExportData(kv, dir);
    assertEquals(path, `${dir}/export.json`);
    const dump: ExportData = JSON.parse(await Deno.readTextFile(path));
    assertEquals(dump.version, 1);
    assertEquals(Number.isNaN(Date.parse(dump.exportedAt)), false);
    // store の list / get と同じ値が出力される。
    assertEquals(dump.collections["keyword-rules"], [rule]);
    assertEquals(dump.collections["keyword-rules"], await ruleStore.list());
    assertEquals(dump.collections["live-comment"], [mapping]);
    assertEquals(dump.collections["live-comment"], await mappingStore.list());
    assertEquals(dump.collections.notification, notification);
    assertEquals(dump.collections.notification, await notificationStore.get());
  } finally {
    await kv.close();
    await Deno.remove(dir, { recursive: true });
  }
});

Deno.test("export:data: 未保存なら空コレクションと null、親ディレクトリも作る", async () => {
  const kv = createKv(":memory:");
  const dir = await Deno.makeTempDir();
  try {
    // 親ディレクトリが無い場所に出力する。
    const path = await writeExportData(kv, `${dir}/nested`);
    assertEquals(path, `${dir}/nested/export.json`);

    const dump = JSON.parse(await Deno.readTextFile(path));
    assertEquals(dump.version, 1);
    assertEquals(dump.collections["keyword-rules"], []);
    assertEquals(dump.collections["live-comment"], []);
    assertEquals(dump.collections.notification, null);

    // 2 回実行しても同じ形 (べき等)。
    await writeExportData(kv, `${dir}/nested`);
    const again: ExportData = JSON.parse(
      await Deno.readTextFile(`${dir}/nested/export.json`),
    );
    assertEquals(again.version, 1);
    assertEquals(again.collections.notification, null);
  } finally {
    await kv.close();
    await Deno.remove(dir, { recursive: true });
  }
});
