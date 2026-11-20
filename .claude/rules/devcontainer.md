# 開発環境 (devcontainer)

`.devcontainer/` に VS Code / `devcontainer` CLI 用の構成を配置。単一サービス構成:

- `workspace` — Debian trixie ベース。VS Code / CLI の接続先であり、開発スタック（Go API サーバ + Vite）もこの中で動かす。`onCreateCommand.sh` が mise で Node 26・Go（`server-go/go.mod` と同じバージョン）を入れ、npm で pnpm を導入する。
  - 旧構成の `app` サービス（本番イメージ内で `deno task dev` を起動していたサイドカー）は、Go ランタイムイメージが Node / Go を含まなくなったため廃止した。開発は `workspace` 内で `pnpm dev` を実行する。

## セットアップ

1. `cp .devcontainer/.env.example .devcontainer/.env` し、`MIRAKC_URL` を設定。
2. 起動:
   - VS Code: "Reopen in Container"
   - CLI: `devcontainer up --workspace-folder .`
3. `workspace` コンテナ内で `pnpm install` を実行（初回は `onCreateCommand.sh` 完了後）。
4. `pnpm dev` で開発スタック起動後、`http://localhost:5173/` で UI、`http://localhost:5173/api/mirakc/version` でバックエンド疎通を確認（`/api` は Vite proxy 経由で Go サーバ(:8000) に届く）。

## 注意事項

- **DooD**: `docker-outside-of-docker` feature を使用し、ホストの `/var/run/docker.sock` をバインド。`postStartCommand.sh` でソケット権限を調整している。
- **`docs/api` の依存**: `docs/api` は npm 管理。`npm --prefix ./docs/api ci` で依存を入れる。Node は mise 経由で入るため `workspace` 内で実行する。
- **Node のバージョン**: ルートの `.node-version` と `docs/api/.node-version` を揃える。上げる際は両方（と Dockerfile の node ベースイメージタグ）を同時に更新する。
- **設定系データ**: 開発中の Go サーバは `./data`（gitignore 済み）に SQLite を書く。本番運用とは別管理。
- **ホスト側で `pnpm install` / `pnpm fix` を実行しない**: コンテナ内の Node とホストの Node で `node_modules` の内容（特に git 解決する `webworkify-webpack`）が食い違う可能性があるため、依存操作は `workspace` コンテナ内で行うのが安全。
