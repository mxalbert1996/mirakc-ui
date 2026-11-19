import { useState } from "react";
import Icon from "../../atoms/Icon.tsx";
import { t } from "../../../locales/i18n.ts";
import styles from "./ServerCard.module.css";

type Props = {
  /** ntfy の URL (トピック込み)。 */
  url: string;

  /** アクセストークン (入力中の draft。保存済み値はクライアントに返らない)。 */
  token: string;

  /** トークンが保存済みか。true なら「設定済み」バッジを出す。 */
  tokenSet: boolean;

  /** トークンを保存時に削除する予定か。true ならバッジが「削除予定」になる。 */
  tokenClearPending: boolean;

  /** URL のエラー種別。undefined ならヒントを表示する。 */
  urlError?: "invalid" | "required";

  /** テスト送信ボタンの活性 (URL が有効な形式のとき)。 */
  testEnabled: boolean;

  /** テスト送信中。 */
  testing: boolean;

  onChangeUrl: (url: string) => void;
  onChangeToken: (token: string) => void;

  /** クリア予定の切替。ON で draft を空にして null 送信、再クリックで復元。 */
  onToggleTokenClear: () => void;
  onTest: () => void;
};

/** ntfy.sh サーバー設定カード。送信先 URL・トークン・テスト送信。 */
export default function ServerCard(props: Props) {
  // トークンの表示/非表示は純 UI 状態なのでローカルで持つ。
  const [showToken, setShowToken] = useState(false);

  return (
    <section className={styles.card}>
      <div className={styles.head}>
        <span className={styles.headIcon}>
          <Icon size={20}>dns</Icon>
        </span>
        <div>
          <h2 className={styles.title}>{t("notification.server.title")}</h2>
          <p className={styles.description}>
            {t("notification.server.description")}
          </p>
        </div>
      </div>

      <div className={styles.body}>
        <div className={styles.field}>
          <div className={styles.fieldHead}>
            <span className={styles.fieldLabel}>
              {t("notification.server.url")}
            </span>
          </div>
          <input
            type="text"
            className={`${styles.input} ${
              props.urlError !== undefined ? styles.inputBad : ""
            }`}
            value={props.url}
            placeholder={t("notification.server.urlPlaceholder")}
            spellCheck={false}
            autoComplete="off"
            onChange={(event) => props.onChangeUrl(event.target.value)}
          />
          {props.urlError === "invalid"
            ? (
              <p className={styles.fieldError}>
                {t("notification.server.urlInvalid")}
              </p>
            )
            : props.urlError === "required"
            ? (
              <p className={styles.fieldError}>
                {t("notification.server.urlRequired")}
              </p>
            )
            : (
              <p className={styles.fieldHint}>
                {t("notification.server.urlHint")}
              </p>
            )}
        </div>

        <div className={styles.field}>
          <div className={styles.fieldHead}>
            <span className={styles.fieldLabel}>
              {t("notification.server.token")}
            </span>
            <span className={styles.optTag}>
              {t("notification.server.optional")}
            </span>
            {props.tokenSet && (
              <span
                className={props.tokenClearPending
                  ? styles.setTagPending
                  : styles.setTag}
              >
                <Icon size={12}>
                  {props.tokenClearPending ? "delete" : "check_circle"}
                </Icon>
                {props.tokenClearPending
                  ? t("notification.server.tokenClearPending")
                  : t("notification.server.tokenConfigured")}
              </span>
            )}
            {props.tokenSet && props.token === "" && (
              <button
                type="button"
                className={styles.clearButton}
                onClick={props.onToggleTokenClear}
              >
                {props.tokenClearPending
                  ? t("notification.server.clearCancel")
                  : t("notification.server.clear")}
              </button>
            )}
          </div>
          <div className={styles.tokenWrap}>
            <input
              type={showToken ? "text" : "password"}
              className={`${styles.input} ${styles.tokenInput}`}
              value={props.token}
              placeholder={props.tokenSet
                ? t("notification.server.tokenConfiguredPlaceholder")
                : t("notification.server.tokenPlaceholder")}
              spellCheck={false}
              autoComplete="off"
              onChange={(event) => props.onChangeToken(event.target.value)}
            />
            <button
              type="button"
              className={styles.eyeButton}
              aria-label={showToken
                ? t("notification.server.hideToken")
                : t("notification.server.showToken")}
              onClick={() => setShowToken(!showToken)}
            >
              <Icon size={16}>
                {showToken ? "visibility_off" : "visibility"}
              </Icon>
            </button>
          </div>
          <p className={styles.fieldHint}>
            {t("notification.server.tokenHint")}
          </p>
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
          {t("notification.server.test")}
        </button>
      </div>
    </section>
  );
}
