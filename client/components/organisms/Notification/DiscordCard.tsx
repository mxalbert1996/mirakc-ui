import Icon from "../../atoms/Icon.tsx";
import { t } from "../../../locales/i18n.ts";
import styles from "./DiscordCard.module.css";

type Props = {
  /** Discord の Incoming Webhook URL (入力中の draft。保存済み値はクライアントに返らない)。 */
  webhookUrl: string;

  /** Webhook URL が保存済みか。true なら「設定済み」バッジを出す。 */
  webhookUrlSet: boolean;

  /** Webhook URL を保存時に削除する予定か。true ならバッジが「削除予定」になる。 */
  webhookUrlClearPending: boolean;

  /** URL のエラー種別。undefined ならヒントを表示する。 */
  urlError?: "invalid" | "required";

  /** テスト送信ボタンの活性 (Webhook URL が有効な形式のとき)。 */
  testEnabled: boolean;

  /** テスト送信中。 */
  testing: boolean;

  onChangeWebhookUrl: (url: string) => void;

  /** クリア予定の切替。ON で draft を空にして null 送信、再クリックで復元。 */
  onToggleWebhookClear: () => void;
  onTest: () => void;
};

/** Discord 送信先カード。Incoming Webhook URL とテスト送信。 */
export default function DiscordCard(props: Props) {
  return (
    <section className={styles.card}>
      <div className={styles.head}>
        <span className={styles.headIcon}>
          <Icon size={20}>forum</Icon>
        </span>
        <div>
          <h2 className={styles.title}>{t("notification.discord.title")}</h2>
          <p className={styles.description}>
            {t("notification.discord.description")}
          </p>
        </div>
      </div>

      <div className={styles.body}>
        <div className={styles.field}>
          <div className={styles.fieldHead}>
            <span className={styles.fieldLabel}>
              {t("notification.discord.url")}
            </span>
            {props.webhookUrlSet && (
              <span
                className={props.webhookUrlClearPending
                  ? styles.setTagPending
                  : styles.setTag}
              >
                <Icon size={12}>
                  {props.webhookUrlClearPending ? "delete" : "check_circle"}
                </Icon>
                {props.webhookUrlClearPending
                  ? t("notification.discord.urlClearPending")
                  : t("notification.discord.urlConfigured")}
              </span>
            )}
            {props.webhookUrlSet && props.webhookUrl === "" && (
              <button
                type="button"
                className={styles.clearButton}
                onClick={props.onToggleWebhookClear}
              >
                {props.webhookUrlClearPending
                  ? t("notification.discord.clearCancel")
                  : t("notification.discord.clear")}
              </button>
            )}
          </div>
          <input
            type="password"
            className={`${styles.input} ${
              props.urlError !== undefined ? styles.inputBad : ""
            }`}
            value={props.webhookUrl}
            placeholder={props.webhookUrlSet
              ? t("notification.discord.urlConfiguredPlaceholder")
              : t("notification.discord.urlPlaceholder")}
            spellCheck={false}
            autoComplete="off"
            onChange={(event) => props.onChangeWebhookUrl(event.target.value)}
          />
          {props.urlError === "invalid"
            ? (
              <p className={styles.fieldError}>
                {t("notification.discord.urlInvalid")}
              </p>
            )
            : props.urlError === "required"
            ? (
              <p className={styles.fieldError}>
                {t("notification.discord.urlRequired")}
              </p>
            )
            : (
              <p className={styles.fieldHint}>
                {t("notification.discord.urlHint")}
              </p>
            )}
        </div>
      </div>

      <div className={styles.foot}>
        <button
          type="button"
          className={styles.testButton}
          disabled={!props.testEnabled || props.testing}
          onClick={props.onTest}
        >
          <Icon size={15} spin={props.testing}>
            {props.testing ? "progress_activity" : "send"}
          </Icon>
          {t("notification.discord.test")}
        </button>
      </div>
    </section>
  );
}
