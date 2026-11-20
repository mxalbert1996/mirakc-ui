# Docker (本番)

マルチステージビルド。linux/amd64, linux/arm64 対応。GHCR への publish は手動 workflow_dispatch（`publish-docker.yml`）。main push では `docker-smoke.yml` がビルドと起動 smoke を検証する。

## ステージ構成

1. `mirakc-ui-build` — Node イメージで `pnpm install` + `pnpm build` を実行し `client/dist`（Vite ビルド成果物）を生成。クライアントはアーキ非依存の JS のため `--platform=$BUILDPLATFORM`。
2. `mirakc-ui-go-build` — Go イメージでサーバをクロスコンパイル（`CGO_ENABLED=0`、`$TARGETOS/$TARGETARCH` 向け）。`client/dist` は `go:embed` でバイナリに取り込む。
3. `tsreadex-build` — Debian trixie-slim。[tsreadex](https://github.com/xtne6f/tsreadex)（`master-240517`）をソースビルドしてバイナリ化。
4. `ffmpeg-build` — Debian trixie-slim。必要なコーデック・フィルタだけを有効化した ffmpeg をソースビルド。
5. 最終ステージ — Debian trixie-slim + tini。`mirakc-ui` Go バイナリ・`ffmpeg`・`tsreadex` を配置し、uid 900 の専用ユーザで起動する。Deno ランタイムは存在しない（ビルド時のみ Node を使用）。

## タイムゾーン

最終ステージは `TZ` をハードコードしない（中立。地域に依存させない方針）。コンテナでは **UTC** になる。`TZ` 未設定だと録画通知・録画ファイル名が UTC で出る（Go サーバが `time.LoadLocation` で解決して整形に注入する。[env.md](./env.md) の `TZ` を参照）。UI の日時表示はブラウザのローカル TZ に従うため `TZ` の影響を受けない。

本番コンテナでは `TZ` を環境変数として渡す（compose の `environment:` か `docker run -e TZ=Asia/Tokyo ...`）。Go バイナリが `time/tzdata` を埋め込むため、`Asia/Tokyo` 等の解決に追加の `tzdata` インストールは要らない。

## `$BUILDPLATFORM` の使い分け

`mirakc-ui-build` ステージは `--platform=$BUILDPLATFORM` にしている。Buildx が実行ホストのネイティブ platform を割り当てるため、

- CI (amd64 runner): `$BUILDPLATFORM` は `linux/amd64` に解決される → QEMU を介さずにビルド
- Raspberry Pi (arm64 ホスト): `$BUILDPLATFORM` は `linux/arm64` に解決される → ネイティブビルド

が両立する。過去に `--platform=linux/amd64` 固定にしていた時期があるが、QEMU エミュレーションで Vite ビルドが `EISDIR` になる問題の回避が目的だった（[fe770ad](https://github.com/ansanloms/mirakc-ui/commit/fe770ad), [c5ee238](https://github.com/ansanloms/mirakc-ui/commit/c5ee238)）。現在は `$BUILDPLATFORM` 化で両対応（[#14](https://github.com/ansanloms/mirakc-ui/pull/14)）。

## トランスコード層の設計方針

Dockerfile は A 方式（mirakc-ui 内部で ffmpeg/tsreadex を実行）前提で tsreadex / ffmpeg を同梱する。トランスコード層を外部コンテナに分離する案（C 方式）は image / publish 系統が二重化するコストが過大なため見送り、1 イメージで watch まで完結する内蔵構成で確定した（[#16](https://github.com/ansanloms/mirakc-ui/issues/16)）。
