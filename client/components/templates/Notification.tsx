import { useState } from "react";
import { useForm } from "@tanstack/react-form";
import {
  isValidDiscordWebhookUrl,
  isValidNtfyUrl,
  NOTIFICATION_EVENT_KEYS,
} from "../../lib/shared/notification-settings.ts";
import type {
  NotificationSettings,
  NotificationSettingsInput,
} from "../../lib/api/notification-settings.ts";
import PageHeader from "../organisms/PageHeader.tsx";
import Toast from "../molecules/Toast.tsx";
import ServerCard from "../organisms/Notification/ServerCard.tsx";
import DiscordCard from "../organisms/Notification/DiscordCard.tsx";
import EventToggles from "../organisms/Notification/EventToggles.tsx";
import SaveBar from "../organisms/Notification/SaveBar.tsx";
import ColorSchemeToggle from "../../islands/ColorSchemeToggle.tsx";
import { useToast } from "../../hooks/use-toast.ts";
import { t } from "../../locales/i18n.ts";
import styles from "./Notification.module.css";

type Props = {
  /** 保存済みの設定 (GET の応答)。draft の初期値と dirty 判定の基準。 */
  settings: NotificationSettings;

  /** 保存処理中。 */
  saving: boolean;

  /** テスト送信中。 */
  testing: boolean;

  /** 設定を保存する。失敗は reject。シークレットは入力が非空のときだけ含まれる。 */
  onSave: (input: NotificationSettingsInput) => Promise<void>;

  /** draft の url / token で ntfy へテスト通知を送る。失敗は reject。 */
  onTestNtfy: (target: { url: string; token: string }) => Promise<void>;

  /** draft の webhookUrl で Discord へテスト通知を送る。失敗は reject。 */
  onTestDiscord: (target: { webhookUrl: string }) => Promise<void>;

  /** 設定ポータル (/settings) へ戻る。 */
  onBackToSettings: () => void;

  /** 番組表へ戻る。 */
  onBack: () => void;

  /** 視聴画面 (/watch) へ遷移する。 */
  onOpenWatch: () => void;
};

/**
 * 通知設定ページ。ntfy カード + Discord カード + 通知イベント + 保存バー。
 * フォーム状態は @tanstack/react-form の useForm に集約する。シークレット
 * (ntfy の token / Discord の Webhook URL) は API が保存済み値を返さないため
 * (§6.2)、draft は常に空で始め、入力が非空のときだけ PUT body に載せる。
 * 保存済みシークレットの削除はクリア予定 (tokenClear / discordClear) として
 * 別状態で持ち、保存時に明示的な null を送る。入力があれば置き換えが優先
 * される。dirty は現在値と props.settings の比較で導出するため、保存成功で
 * props.settings が更新されれば dirty は自然に消える。URL エラーは
 * 「一度でも変更したか (dirty)」を解禁条件にする。
 */
export default function Notification(props: Props) {
  const { toast, show } = useToast();

  // シークレットのクリア予定は draft 値とは別の UI 状態として持つ。
  // 入力 (置き換え) があれば解除し、保存成功でリセットする。
  const [tokenClear, setTokenClear] = useState(false);
  const [discordClear, setDiscordClear] = useState(false);

  const form = useForm({
    // シークレットは保存済み値を draft に入れられないため空で始める。
    defaultValues: {
      url: props.settings.url,
      token: "",
      discordWebhookUrl: "",
      onSchedule: props.settings.onSchedule,
      onStart: props.settings.onStart,
      onEnd: props.settings.onEnd,
      onFail: props.settings.onFail,
      onRemove: props.settings.onRemove,
    },
    onSubmit: ({ value }) => {
      // シークレットは入力が非空ならその値 (置き換え)、クリア予定なら
      // 明示的な null (削除)、どちらでもなければ省略 (保持) を送る (§6.2)。
      const token = value.token.trim();
      const discordWebhookUrl = value.discordWebhookUrl.trim();
      props
        .onSave({
          url: value.url.trim(),
          onSchedule: value.onSchedule,
          onStart: value.onStart,
          onEnd: value.onEnd,
          onFail: value.onFail,
          onRemove: value.onRemove,
          ...(token !== "" ? { token } : tokenClear ? { token: null } : {}),
          ...(discordWebhookUrl !== ""
            ? { discordWebhookUrl }
            : discordClear
              ? { discordWebhookUrl: null }
              : {}),
        })
        .then(() => {
          // 保存に成功したら draft とクリア予定を未変更状態に戻す。シークレット
          // は保存済み値を受け取れないため、空 (= 未変更) へのリセットが相当。
          form.setFieldValue("token", "");
          form.setFieldValue("discordWebhookUrl", "");
          setTokenClear(false);
          setDiscordClear(false);
          show(t("notification.toast.saved"), "success");
        })
        .catch(() => show(t("notification.toast.saveFailed"), "error"));
    },
  });

  const runTest = (sent: Promise<void>) => {
    sent
      .then(() => show(t("notification.toast.testSent"), "success"))
      .catch(() => show(t("notification.toast.testFailed"), "error"));
  };

  return (
    <div className="app-root">
      <PageHeader
        icon="notifications"
        title={t("notification.title")}
        subtitle={t("notification.subtitle")}
        links={[
          {
            icon: "grid_view",
            label: t("notification.epg"),
            onClick: props.onBack,
          },
          {
            icon: "live_tv",
            label: t("watch.open"),
            onClick: props.onOpenWatch,
          },
          {
            icon: "settings",
            label: t("notification.settings"),
            onClick: props.onBackToSettings,
          },
        ]}
      >
        <ColorSchemeToggle />
      </PageHeader>

      <main className={styles.page}>
        <div className={styles.pageInner}>
          <div className={styles.pageHead}>
            <h2 className={styles.pageTitle}>{t("notification.title")}</h2>
            <p className={styles.pageLead}>{t("notification.lead")}</p>
          </div>

          <form.Subscribe selector={(s) => s.values}>
            {(values) => {
              const url = values.url.trim();
              const discordUrl = values.discordWebhookUrl.trim();
              const anyEvent = NOTIFICATION_EVENT_KEYS.some((key) => values[key]);
              const ntfyInvalid = url !== "" && !isValidNtfyUrl(url);
              const discordInvalid = discordUrl !== "" && !isValidDiscordWebhookUrl(discordUrl);
              // イベント有効なのに送信できる通知先 (ntfy / Discord) が 1 つも無い。
              // Discord の Webhook はシークレットで draft が空でも、保存済み
              // (discordWebhookUrlSet) かつクリア予定でなければ送信先として数える。
              const noDestination =
                anyEvent &&
                url === "" &&
                discordUrl === "" &&
                (!props.settings.discordWebhookUrlSet || discordClear);
              // シークレットは保存済み値が draft に入らないため、入力が非空なら
              // 変更扱いとする (空に戻せば元の値の保持で変更なし)。クリア予定も
              // 保存すべき変更なので dirty に含める。
              const dirty =
                values.url !== props.settings.url ||
                values.token.trim() !== "" ||
                values.discordWebhookUrl.trim() !== "" ||
                tokenClear ||
                discordClear ||
                NOTIFICATION_EVENT_KEYS.some((key) => values[key] !== props.settings[key]);
              // 「通知先ゼロ」は ntfy カードに 1 回だけ出す (文言で両対応を明示)。
              const ntfyError = !dirty
                ? undefined
                : ntfyInvalid
                  ? ("invalid" as const)
                  : noDestination
                    ? ("required" as const)
                    : undefined;
              const discordError = !dirty || !discordInvalid ? undefined : ("invalid" as const);

              return (
                <>
                  <ServerCard
                    url={values.url}
                    token={values.token}
                    tokenSet={props.settings.tokenSet}
                    tokenClearPending={tokenClear}
                    urlError={ntfyError}
                    testEnabled={isValidNtfyUrl(url) && !tokenClear}
                    testing={props.testing}
                    onChangeUrl={(value) => form.setFieldValue("url", value)}
                    onChangeToken={(value) => {
                      // 入力すればクリア予定を解除し、置き換えとして保存する。
                      setTokenClear(false);
                      form.setFieldValue("token", value);
                    }}
                    onToggleTokenClear={() => {
                      // クリア予定の ON で draft を空に戻し、再クリックで復元する。
                      if (!tokenClear) {
                        form.setFieldValue("token", "");
                      }
                      setTokenClear(!tokenClear);
                    }}
                    onTest={() =>
                      runTest(
                        props.onTestNtfy({
                          url: values.url.trim(),
                          token: values.token.trim(),
                        }),
                      )
                    }
                  />

                  <DiscordCard
                    webhookUrl={values.discordWebhookUrl}
                    webhookUrlSet={props.settings.discordWebhookUrlSet}
                    webhookUrlClearPending={discordClear}
                    urlError={discordError}
                    testEnabled={isValidDiscordWebhookUrl(discordUrl) && !discordClear}
                    testing={props.testing}
                    onChangeWebhookUrl={(value) => {
                      // 入力すればクリア予定を解除し、置き換えとして保存する。
                      setDiscordClear(false);
                      form.setFieldValue("discordWebhookUrl", value);
                    }}
                    onToggleWebhookClear={() => {
                      // クリア予定の ON で draft を空に戻し、再クリックで復元する。
                      if (!discordClear) {
                        form.setFieldValue("discordWebhookUrl", "");
                      }
                      setDiscordClear(!discordClear);
                    }}
                    onTest={() =>
                      runTest(
                        props.onTestDiscord({
                          webhookUrl: values.discordWebhookUrl.trim(),
                        }),
                      )
                    }
                  />

                  <EventToggles
                    values={values}
                    onToggle={(key) => form.setFieldValue(key, !values[key])}
                  />

                  <SaveBar
                    dirty={dirty}
                    saving={props.saving}
                    disabled={ntfyInvalid || discordInvalid || noDestination}
                    onSave={() => form.handleSubmit()}
                  />
                </>
              );
            }}
          </form.Subscribe>
        </div>
      </main>

      {toast !== null && (
        <Toast message={toast.message} variant={toast.variant} leaving={toast.leaving} />
      )}
    </div>
  );
}
