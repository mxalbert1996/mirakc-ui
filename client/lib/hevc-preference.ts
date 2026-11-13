/**
 * HEVC トランスコードのブラウザ内設定。
 *
 * HEVC を再生できるかは端末 (ブラウザ + OS のデコーダ) 次第なので、サーバ
 * 設定ではなくブラウザごとの localStorage に持つ。既定は有効 (対応端末なら
 * HEVC を要求し、非対応端末は shouldRequestHevc が false になる)。
 */

export const HEVC_STORAGE_KEY = "mirakc-ui:hevc";

export function isHevcPreferred(): boolean {
  try {
    // 既定は有効。オフにしたときだけ "0" を保存する。
    return localStorage.getItem(HEVC_STORAGE_KEY) !== "0";
  } catch {
    return true;
  }
}

export function setHevcPreferred(enabled: boolean): void {
  try {
    if (enabled) {
      localStorage.removeItem(HEVC_STORAGE_KEY);
    } else {
      localStorage.setItem(HEVC_STORAGE_KEY, "0");
    }
  } catch {
    // localStorage 不可の環境では既定 (HEVC 有効) のまま。
  }
}

/**
 * MSE が HEVC を再生できるか。mpegts.js は MPEG-TS を hvc1 の fMP4 へ
 * transmux するため、判定も hvc1 の codec 文字列で行う。
 */
export function canPlayHevc(): boolean {
  try {
    return typeof MediaSource !== "undefined" &&
      MediaSource.isTypeSupported('video/mp4; codecs="hvc1.1.6.L93.B0"');
  } catch {
    return false;
  }
}

/** transcode API に codec=hevc を付けるか。 */
export function shouldRequestHevc(): boolean {
  return isHevcPreferred() && canPlayHevc();
}
