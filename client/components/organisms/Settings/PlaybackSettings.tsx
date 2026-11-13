import { useState } from "react";
import {
  canPlayHevc,
  isHevcPreferred,
  setHevcPreferred,
} from "../../../lib/hevc-preference.ts";
import Icon from "../../atoms/Icon.tsx";
import ToggleSwitch from "../../atoms/ToggleSwitch.tsx";
import { t } from "../../../locales/i18n.ts";
import styles from "./PlaybackSettings.module.css";

/**
 * 再生設定のセクション。HEVC を再生できるかはブラウザ次第なので、設定は
 * ブラウザ内 (localStorage) に保存し、非対応ブラウザではトグルを無効にする。
 */
export default function PlaybackSettings() {
  const [supported] = useState(canPlayHevc);
  const [preferred, setPreferred] = useState(isHevcPreferred);
  // 非対応ブラウザでは保存値が残っていても実効は H.264 なので、オフとして見せる。
  const enabled = supported && preferred;

  const toggle = () => {
    const next = !preferred;
    setPreferred(next);
    setHevcPreferred(next);
  };

  return (
    <section className={styles.section}>
      <h3 className={styles.sectionTitle}>{t("settings.playback.title")}</h3>

      <div className={styles.card}>
        <div className={styles.row}>
          <span className={styles.rowIcon}>
            <Icon size={14}>high_quality</Icon>
          </span>
          <span className={styles.rowText}>
            <span className={styles.rowName}>
              {t("settings.playback.hevc.label")}
            </span>
            <span
              className={`${styles.rowDescription} ${
                supported ? "" : styles.rowDescriptionMuted
              }`}
            >
              {supported
                ? t("settings.playback.hevc.description")
                : t("settings.playback.hevc.unsupported")}
            </span>
          </span>
          <span
            className={`${styles.state} ${enabled ? styles.stateOn : ""}`}
            aria-hidden="true"
          >
            {enabled
              ? t("settings.playback.hevc.on")
              : t("settings.playback.hevc.off")}
          </span>
          <ToggleSwitch
            checked={enabled}
            disabled={!supported}
            label={t("settings.playback.hevc.label")}
            onToggle={toggle}
          />
        </div>
      </div>
    </section>
  );
}
