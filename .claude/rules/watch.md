# ライブ視聴とトランスコード

## ページ構成

- `/watch/$serviceId`（視聴）と `/watch`（サービス未選択）。`client/routes/watch/$serviceId.tsx` / `watch/index.tsx` が URL 状態を管理し、`client/islands/Watch.tsx`（ビュー）へ props で渡す。プレイヤーは `client/components/organisms/Watch/Player.tsx`、UI 部品は `client/components/organisms/Watch/`・`molecules/Watch/`。

## トランスコード API

`server-go/internal/transcode` の `NewHandler`（`/api/transcode/services/{id}`）。パイプラインは mirakc のサービスストリーム → `tsreadex`（字幕・音声整形）→ `ffmpeg`（H.264 / AAC に再エンコード）→ MPEG-TS をチャンク応答。client 切断・子プロセス異常終了・上流切断の各経路から冪等な cleanup（SIGTERM → 3 秒後 SIGKILL）を発火する。

## 再生

- `mpegts.js`（MSE ベース、npm 依存 `1.8.0`）で `<video>` にアタッチ
- `aribb24.js`（npm の `2.0.25`）で ARIB 字幕を canvas オーバーレイ

## HW エンコーダー自動検出

`server-go/internal/transcode`。リクエストごとに `h264_v4l2m2m`（→ `hevc_v4l2m2m`）と `libx264` の順で**実エンコードテスト (probe)** を検証する（EBUSY は最初の busy から 500ms の窓内で再試行）。デバイス不在の場合は `libx264` にフォールバック。全候補失敗時は `503 No usable video encoder found`。

## デバッグログ

tsreadex / ffmpeg の stderr は slog が `[tsreadex]` / `[ffmpeg]` プレフィックス付きで JSON ログ化する。`docker logs <コンテナ>` で確認可。

## URL 状態管理

- `serviceId` はパスパラメータ（`/watch/$serviceId`）
- `audioTrack` / `quality` / `caption` は `validateSearch` による型付きクエリパラメータ
- 画質プリセット（`Quality` 型 / `qualities` / `defaultQuality` / `normalizeQuality`）は `client/lib/shared/quality.ts` が client 側の単一ソース。server 側は `server-go/internal/transcode` が同じ値を Go として持つ（ドリフトは spec と実装テストで検出）。ffmpeg の scale / bitrate 対応は `qualitySettings`
- サービス選択が「リスト操作由来」か「直リンク」かは history state（`selected`）で区別し、Player の autoplay unmute 判定に使う（`HistoryState` を `client/main.tsx` の `declare module` で拡張）

## 既知の制限

- デフォルト値（audioTrack=0 / quality=`defaultQuality` / caption=true）も URL クエリに出力される。旧構成では省略していたが、TanStack Router の search param では明示される。気になる場合は `stripSearchParams` の導入を検討する。
- アクセシビリティ（クリッカブル div 等）の棚卸しは [issue #19](https://github.com/ansanloms/mirakc-ui/issues/19)。Modal の閉じボタンと Watch/ServiceItem は button 化、Program/Table のセルは role="button" + キーボード対応（Enter/Space）に是正済み。残りは継続課題。

## 解消済みの制限

- 〜 aribb24.js 2.0.16: ブラウザウィンドウ拡大時に ARIB 字幕がほぼ表示されなくなる magnification バグがあった（[issue #15](https://github.com/ansanloms/mirakc-ui/issues/15)）。upstream の monyone/aribb24.js 2.0.17 で修正済み。本リポジトリは npm の 2.0.25 を使用。

## トランスコード方式

ffmpeg / tsreadex を mirakc-ui 内部で実行する A 方式で確定。代替案（mirakc の `post-filters` に寄せる B' 方式、専用サイドカーコンテナに分離する C 方式）は [#16](https://github.com/ansanloms/mirakc-ui/issues/16) で検討の結果いずれも見送った。
