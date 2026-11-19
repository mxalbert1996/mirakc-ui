import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import Notification from "./Notification.tsx";
import { DEFAULT_NOTIFICATION_SETTINGS } from "../../lib/api/notification-settings.ts";
import { t } from "../../locales/i18n.ts";

function setup(override: Partial<Parameters<typeof Notification>[0]> = {}) {
  const props = {
    settings: DEFAULT_NOTIFICATION_SETTINGS,
    saving: false,
    testing: false,
    onSave: vi.fn(() => Promise.resolve()),
    onTestNtfy: vi.fn(() => Promise.resolve()),
    onTestDiscord: vi.fn(() => Promise.resolve()),
    onBackToSettings: vi.fn(),
    onOpenWatch: vi.fn(),
    onBack: vi.fn(),
    ...override,
  };
  return { ...render(<Notification {...props} />), props };
}

function urlInput() {
  return screen.getByPlaceholderText<HTMLInputElement>(
    t("notification.server.urlPlaceholder"),
  );
}

function discordUrlInput() {
  return screen.getByPlaceholderText<HTMLInputElement>(
    t("notification.discord.urlPlaceholder"),
  );
}

function configuredTokenInput() {
  return screen.getByPlaceholderText<HTMLInputElement>(
    t("notification.server.tokenConfiguredPlaceholder"),
  );
}

function configuredDiscordUrlInput() {
  return screen.getByPlaceholderText<HTMLInputElement>(
    t("notification.discord.urlConfiguredPlaceholder"),
  );
}

function saveButton() {
  return screen.getByText(t("notification.save")).closest("button")!;
}

const WEBHOOK = "https://discord.com/api/webhooks/123456789012345678/abc";

describe("Notification template", () => {
  it("タイトル・リード文・ntfy/Discord/イベントの各カードを描画する", () => {
    setup();
    expect(screen.getAllByText(t("notification.title")).length)
      .toBeGreaterThan(0);
    expect(screen.getByText(t("notification.lead"))).toBeTruthy();
    expect(screen.getByText(t("notification.server.title"))).toBeTruthy();
    expect(screen.getByText(t("notification.discord.title"))).toBeTruthy();
    expect(screen.getByText(t("notification.events.title"))).toBeTruthy();
  });

  it("変更が無ければ保存不可、変更で dirty ピルが出る", () => {
    setup();
    expect(saveButton().disabled).toBe(true);
    expect(screen.queryByText(t("notification.dirty"))).toBeNull();

    fireEvent.change(urlInput(), {
      target: { value: "https://ntfy.sh/mirakc" },
    });
    expect(screen.getByText(t("notification.dirty"))).toBeTruthy();
    expect(saveButton().disabled).toBe(false);
  });

  it("不正な URL はエラーを出し保存できない", () => {
    setup();
    fireEvent.change(urlInput(), { target: { value: "not a url" } });
    expect(
      screen.getByText(t("notification.server.urlInvalid")),
    ).toBeTruthy();
    expect(saveButton().disabled).toBe(true);
  });

  it("イベント有効で URL 空なら必須エラーで保存できない", () => {
    setup();
    fireEvent.click(
      screen.getByRole("switch", {
        name: t("notification.events.items.onStart.label"),
      }),
    );
    expect(
      screen.getByText(t("notification.server.urlRequired")),
    ).toBeTruthy();
    expect(saveButton().disabled).toBe(true);
  });

  it("新イベント (録画失敗) のトグルだけでも URL 必須になる", () => {
    setup();
    fireEvent.click(
      screen.getByRole("switch", {
        name: t("notification.events.items.onFail.label"),
      }),
    );
    expect(
      screen.getByText(t("notification.server.urlRequired")),
    ).toBeTruthy();
  });

  it("Discord Webhook だけ入力すれば (ntfy 空でも) 保存できる", () => {
    setup();
    fireEvent.click(
      screen.getByRole("switch", {
        name: t("notification.events.items.onStart.label"),
      }),
    );
    // ntfy 空 + イベント有効 → この時点では通知先ゼロで必須エラー。
    expect(saveButton().disabled).toBe(true);

    fireEvent.change(discordUrlInput(), { target: { value: WEBHOOK } });
    // Discord を入れれば通知先が成立し、必須エラーは消えて保存できる。
    expect(screen.queryByText(t("notification.server.urlRequired"))).toBeNull();
    expect(saveButton().disabled).toBe(false);
  });

  it("不正な Discord Webhook URL はエラーを出し保存できない", () => {
    setup();
    fireEvent.change(discordUrlInput(), {
      target: { value: "https://example.com/not-a-webhook" },
    });
    expect(
      screen.getByText(t("notification.discord.urlInvalid")),
    ).toBeTruthy();
    expect(saveButton().disabled).toBe(true);
  });

  it("Discord のテスト送信が onTestDiscord に渡る", () => {
    const { props } = setup({
      settings: {
        ...DEFAULT_NOTIFICATION_SETTINGS,
        discordWebhookUrlSet: true,
        onStart: true,
      },
    });
    // 保存済みの Webhook はクライアントに返らないため、入力した値で送る。
    fireEvent.change(configuredDiscordUrlInput(), {
      target: { value: WEBHOOK },
    });
    fireEvent.click(
      screen.getByText(t("notification.discord.test")).closest("button")!,
    );
    expect(props.onTestDiscord).toHaveBeenCalledWith({ webhookUrl: WEBHOOK });
  });

  it("保存成功で onSave に trim 済みの値が渡りトーストが出る", async () => {
    const { props } = setup();
    fireEvent.change(urlInput(), {
      target: { value: " https://ntfy.sh/mirakc " },
    });
    fireEvent.click(saveButton());

    // form.handleSubmit は非同期。トースト表示を待てば onSave は呼び出し済み。
    expect(await screen.findByText(t("notification.toast.saved")))
      .toBeTruthy();
    // シークレット未入力なら PUT body から丸ごと省略される (保存済み値は保持)。
    expect(props.onSave).toHaveBeenCalledWith({
      url: "https://ntfy.sh/mirakc",
      onSchedule: false,
      onStart: false,
      onEnd: false,
      onFail: false,
      onRemove: false,
    });
  });

  it("保存失敗で失敗トーストが出る", async () => {
    setup({ onSave: vi.fn(() => Promise.reject(new Error("ng"))) });
    fireEvent.change(urlInput(), {
      target: { value: "https://ntfy.sh/mirakc" },
    });
    fireEvent.click(saveButton());
    expect(await screen.findByText(t("notification.toast.saveFailed")))
      .toBeTruthy();
  });

  it("テスト送信が onTest に渡り成功トーストが出る", async () => {
    const { props } = setup({
      settings: {
        ...DEFAULT_NOTIFICATION_SETTINGS,
        url: "https://ntfy.sh/mirakc",
        tokenSet: true,
        onStart: true,
        onEnd: true,
      },
    });
    // 保存済みのトークンはクライアントに返らないため、入力した値で送る。
    fireEvent.change(configuredTokenInput(), { target: { value: "tk" } });
    fireEvent.click(
      screen.getByText(t("notification.server.test")).closest("button")!,
    );
    expect(props.onTestNtfy).toHaveBeenCalledWith({
      url: "https://ntfy.sh/mirakc",
      token: "tk",
    });
    expect(await screen.findByText(t("notification.toast.testSent")))
      .toBeTruthy();
  });

  it("入力したシークレットは trim 済みで PUT body に載る", async () => {
    const { props } = setup({
      settings: { ...DEFAULT_NOTIFICATION_SETTINGS, tokenSet: true },
    });
    fireEvent.change(urlInput(), {
      target: { value: "https://ntfy.sh/mirakc" },
    });
    fireEvent.change(configuredTokenInput(), {
      target: { value: " tk_new " },
    });
    fireEvent.click(saveButton());
    expect(await screen.findByText(t("notification.toast.saved")))
      .toBeTruthy();
    expect(props.onSave).toHaveBeenCalledWith({
      url: "https://ntfy.sh/mirakc",
      token: "tk_new",
      onSchedule: false,
      onStart: false,
      onEnd: false,
      onFail: false,
      onRemove: false,
    });
  });

  it("保存済みシークレットは設定済みバッジと説明プレースホルダーを出す", () => {
    setup({
      settings: {
        ...DEFAULT_NOTIFICATION_SETTINGS,
        tokenSet: true,
        discordWebhookUrlSet: true,
      },
    });
    // ntfy / Discord の両カードに設定済みバッジが出る (文言は共通)。
    expect(screen.getAllByText(t("notification.server.tokenConfigured")))
      .toHaveLength(2);
    // 説明プレースホルダーも両カードで共通の文言になる。
    expect(
      screen.getAllByPlaceholderText(
        t("notification.server.tokenConfiguredPlaceholder"),
      ),
    ).toHaveLength(2);
  });

  it("未設定なら設定済みバッジもクリア操作も出ない", () => {
    setup();
    expect(screen.queryByText(t("notification.server.tokenConfigured")))
      .toBeNull();
    expect(screen.queryByText(t("notification.server.clear"))).toBeNull();
    expect(screen.queryByText(t("notification.discord.clear"))).toBeNull();
  });

  it("Discord 保存済みなら draft が空でも通知先ゼロにならない", () => {
    setup({
      settings: {
        ...DEFAULT_NOTIFICATION_SETTINGS,
        discordWebhookUrlSet: true,
      },
    });
    fireEvent.click(
      screen.getByRole("switch", {
        name: t("notification.events.items.onStart.label"),
      }),
    );
    expect(screen.queryByText(t("notification.server.urlRequired")))
      .toBeNull();
    expect(saveButton().disabled).toBe(false);
  });

  it("クリア予定のトークンは保存時に null で送られる", async () => {
    const { props } = setup({
      settings: { ...DEFAULT_NOTIFICATION_SETTINGS, tokenSet: true },
    });
    fireEvent.click(screen.getByText(t("notification.server.clear")));
    // バッジが「削除予定」に変わり、dirty になるため保存できる。
    expect(
      screen.getByText(t("notification.server.tokenClearPending")),
    ).toBeTruthy();
    expect(saveButton().disabled).toBe(false);
    fireEvent.click(saveButton());
    expect(await screen.findByText(t("notification.toast.saved")))
      .toBeTruthy();
    expect(props.onSave).toHaveBeenCalledWith({
      url: "",
      token: null,
      onSchedule: false,
      onStart: false,
      onEnd: false,
      onFail: false,
      onRemove: false,
    });
  });

  it("クリア予定の Webhook URL は保存時に null で送られる", async () => {
    const { props } = setup({
      settings: {
        ...DEFAULT_NOTIFICATION_SETTINGS,
        url: "https://ntfy.sh/mirakc",
        discordWebhookUrlSet: true,
      },
    });
    fireEvent.click(screen.getByText(t("notification.discord.clear")));
    fireEvent.click(saveButton());
    expect(await screen.findByText(t("notification.toast.saved")))
      .toBeTruthy();
    expect(props.onSave).toHaveBeenCalledWith({
      url: "https://ntfy.sh/mirakc",
      discordWebhookUrl: null,
      onSchedule: false,
      onStart: false,
      onEnd: false,
      onFail: false,
      onRemove: false,
    });
  });

  it("クリア予定でも入力すれば入力値が優先される", async () => {
    const { props } = setup({
      settings: { ...DEFAULT_NOTIFICATION_SETTINGS, tokenSet: true },
    });
    fireEvent.click(screen.getByText(t("notification.server.clear")));
    fireEvent.change(configuredTokenInput(), {
      target: { value: " tk_new " },
    });
    // 入力でクリア予定が解除され、置き換えとして保存される。
    expect(
      screen.queryByText(t("notification.server.tokenClearPending")),
    ).toBeNull();
    fireEvent.click(saveButton());
    expect(await screen.findByText(t("notification.toast.saved")))
      .toBeTruthy();
    expect(props.onSave).toHaveBeenCalledWith({
      url: "",
      token: "tk_new",
      onSchedule: false,
      onStart: false,
      onEnd: false,
      onFail: false,
      onRemove: false,
    });
  });

  it("クリア予定の取り消しでフィールドは再び省略され未変更に戻る", () => {
    setup({
      settings: { ...DEFAULT_NOTIFICATION_SETTINGS, tokenSet: true },
    });
    fireEvent.click(screen.getByText(t("notification.server.clear")));
    fireEvent.click(screen.getByText(t("notification.server.clearCancel")));
    expect(
      screen.queryByText(t("notification.server.tokenClearPending")),
    ).toBeNull();
    expect(screen.getByText(t("notification.server.tokenConfigured")))
      .toBeTruthy();
    // 他に変更が無ければ未変更のため保存は不可。
    expect(saveButton().disabled).toBe(true);
  });

  it("クリア予定中はテスト送信ボタンが無効になる", () => {
    setup({
      settings: {
        ...DEFAULT_NOTIFICATION_SETTINGS,
        url: "https://ntfy.sh/mirakc",
        tokenSet: true,
      },
    });
    const test = screen.getByText(t("notification.server.test")).closest(
      "button",
    )!;
    expect(test.disabled).toBe(false);
    fireEvent.click(screen.getByText(t("notification.server.clear")));
    expect(test.disabled).toBe(true);
    fireEvent.click(screen.getByText(t("notification.server.clearCancel")));
    expect(test.disabled).toBe(false);
  });

  it("クリア予定の Webhook は通知先として数えない", () => {
    setup({
      settings: {
        ...DEFAULT_NOTIFICATION_SETTINGS,
        discordWebhookUrlSet: true,
      },
    });
    fireEvent.click(screen.getByText(t("notification.discord.clear")));
    fireEvent.click(
      screen.getByRole("switch", {
        name: t("notification.events.items.onStart.label"),
      }),
    );
    expect(screen.getByText(t("notification.server.urlRequired")))
      .toBeTruthy();
    expect(saveButton().disabled).toBe(true);
  });

  it("クリア予定は保存成功でリセットされる", async () => {
    setup({
      settings: { ...DEFAULT_NOTIFICATION_SETTINGS, tokenSet: true },
    });
    fireEvent.click(screen.getByText(t("notification.server.clear")));
    fireEvent.click(saveButton());
    expect(await screen.findByText(t("notification.toast.saved")))
      .toBeTruthy();
    // props.settings はモックで固定のため、リセット後は「設定済み」に戻る。
    expect(screen.getByText(t("notification.server.tokenConfigured")))
      .toBeTruthy();
    expect(
      screen.queryByText(t("notification.server.tokenClearPending")),
    ).toBeNull();
  });

  it("番組表へ戻るリンクで onBack が発火する", () => {
    const { props } = setup();
    fireEvent.click(screen.getByLabelText(t("notification.epg")));
    expect(props.onBack).toHaveBeenCalledTimes(1);
  });

  it("設定へ戻るリンクで onBackToSettings が発火する", () => {
    const { props } = setup();
    fireEvent.click(screen.getByLabelText(t("notification.settings")));
    expect(props.onBackToSettings).toHaveBeenCalledTimes(1);
  });

  it("視聴画面へのリンクで onOpenWatch が発火する", () => {
    const { props } = setup();
    fireEvent.click(screen.getByLabelText(t("watch.open")));
    expect(props.onOpenWatch).toHaveBeenCalledTimes(1);
  });
});
