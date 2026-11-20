# プロジェクト概要

[mirakc](https://github.com/mirakc/mirakc) 向けの Web UI。番組表閲覧、録画管理、番組検索、ライブ視聴機能を提供する。ライブ視聴はトランスコード層を mirakc-ui 内部で実行する A 方式で確定している ([issue #11](https://github.com/ansanloms/mirakc-ui/issues/11))。

## テックスタック

- **クライアント**: Vite + React 19 SPA
- **ルーティング**: TanStack Router（file-based, `client/routes/`）
- **サーバー状態**: TanStack Query + openapi-react-query（`$api`）
- **サーバー**: Go（net/http）— API プロキシ / トランスコード / 本番の静的配信（`server-go/`）
- **ランタイム**: 静的リンクした Go バイナリ（イメージに JS ランタイム無し）
- **API クライアント**: openapi-fetch + openapi-typescript による型安全な API 呼び出し
- **国際化**: i18next（日本語のみ）
- **スタイリング**: CSS モジュール + グローバル CSS 変数（ライト/ダークモード対応）

> 旧構成は Fresh 2.x（Deno ベースの Island Architecture）だった。Fresh の暗黙的な island 境界が分かりにくいこと、テストの土台が無いことを動機に、Vite + React SPA + Hono へ全面移行した。

## ディレクトリ構成

- `client/` — Vite + React SPA。
  - `routes/` — TanStack Router の file-based ルート。`routeTree.gen.ts` は生成物。
  - `components/` — Atomic Design 構成（atoms → molecules → organisms → templates）。
  - `islands/` — API 非依存の共有 UI（ColorSchemeToggle）と Watch ビュー。「island」は旧構成の名残で、現在は通常の React コンポーネント。
  - `hooks/` — 再利用する React フック（`use-debounced-value` / `use-now` / `use-program-queries` 等）。`use-*` 命名のフックはここに置く。
  - `lib/api/` — `$api`（openapi-react-query）クライアントと生成済み `schema.d.ts`。
  - `locales/` — i18next 翻訳。`assets/` — グローバル CSS。
- `server-go/` — Go アプリ。`cmd/mirakc-ui`（エントリ）、`internal/httpapi`（ルーティング・認証）、`internal/app`（合成）、`internal/mirakc`（プロキシ・programs・events）、`internal/keyword`（自動録画ジョブ）、`internal/comments`（実況 SSE とソース、`ndgr/` デコーダ）、`internal/transcode`、`internal/store`（SQLite）、`internal/settings`（検証・通知送信）、`internal/i18n`/`internal/timefmt`。

## コマンド

```bash
pnpm dev        # 開発: Go サーバ(:8000) と Vite(:5173) を並行起動。/api は Vite proxy 経由
pnpm dev:client # Vite のみ
pnpm dev:server # Go サーバのみ (cd server-go && go run ./cmd/mirakc-ui 相当)
pnpm build      # 本番ビルド（client/dist に出力）
pnpm check      # 型チェック (tsc --noEmit、CI 相当)
pnpm lint       # oxlint + oxfmt --check
pnpm fix        # oxlint --fix + oxfmt で自動修正
pnpm test       # Vitest（client のテストスイート）
pnpm generate:mirakc # client/lib/api/schema.d.ts を mirakc の OpenAPI から再生成
```

`start` / `export:data` / `generate:internal` / `test:server` の Deno タスクは廃止。本番は Go バイナリが client/dist を配信し、旧データの移行は README の旧イメージ手順を使う。

oxlint の `curly` ルールで 1 行の `if` でも波括弧が必須。`pnpm check` が失敗したら `pnpm fix` で多くは解消する。

client の JSON Schema 定数は `client/lib/api/internal-schemas.ts`（生成物・コミット対象）。`npm --prefix ./docs/api run bundle:json && npm --prefix ./docs/api run emit-server-schemas -- ../client/lib/api/internal-schemas.ts` で再生成する。CI の docs-api ジョブが drift を検査する。

依存の追加・更新は `pnpm install` で行う（lockfile をコミットする。CI は `pnpm install --frozen-lockfile`）。
