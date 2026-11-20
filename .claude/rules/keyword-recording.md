# キーワード自動録画と ntfy 通知

キーワード + 任意の期間・チャンネル・ジャンルで自動録画ルールを登録・管理し、定期ジョブが一致番組を自動予約する。録画イベント（登録・開始・終了・失敗・削除）は ntfy で通知する。UI は Claude Design のハンドオフ（キーワード録画.html / 通知設定.html）準拠。

## 自動予約ジョブ

- `server-go/internal/keyword` の `Start` が返す Job。実行トリガは 4 系統:
  - 起動時に 1 回
  - **EPG 更新**: SSE の `epg.programs-updated` で `job.trigger()`。イベントはサービス単位でバーストし、SSE 接続直後にも全サービス分のスナップショットが届くため、**debounce（既定 60 秒）で 1 回の実行に畳む**。再接続時もスナップショットで自動的に再実行されるので、切断中の EPG 更新を取りこぼさない
  - **ルールの登録・更新**: `/api/keyword-rules` の POST / PUT 成功時に `onChanged` フックで `job.trigger()`（同じ debounce 経由。削除では発火しない — 予約の取り消しはしない仕様のため再実行で増える予約が無い）
  - フォールバックの定期実行: `KEYWORD_RECORDING_INTERVAL_MINUTES`（既定 60 分）間隔
- 実行中の再要求は完了後に 1 回へ畳む（並行実行で二重予約させない）。
- mirakc の `/programs`・`/services`・`/recording/schedules` を取得し、**有効ルール**に `matchesKeywordRule` で一致する**未予約・将来**の番組を `POST /recording/schedules` で予約する。チャンネル条件は (networkId, serviceId) → サービスの `channel.channel`（= MirakurunChannel.channel）の解決を経て判定。チャンネル配下の全サービスを横断して対象になる。
- 予約には tag `mirakc-ui:keyword` と `keyword:<キーワード>` を付ける。`contentPath` は手動予約と同じ `{YYYYMMDDhhmmss}_{programId}_{番組名}.m2ts`。
- 登録時の通知はキーワード・チャンネル名・放送時間入り（`notification.keyword.*`）。予約処理自体は通知設定に関係なく実行する。

## ntfy 通知

- **設定**: `{ url, token, onSchedule, onStart, onEnd, onFail, onRemove }`。url はトピックまで含む ntfy URL（例 `https://ntfy.sh/mirakc-rec`）。イベントが 1 つでも有効なら url 必須。型・検証・トグルキー一覧 `NOTIFICATION_EVENT_KEYS` は純粋共有モジュール `client/lib/shared/notification-settings.ts`（client のフォーム検証。server 側は `server-go/internal/settings` が同じ挙動を Go で持つ）。
- **永続化**: `server-go/internal/store` の singleton（collection `notification`、SQLite `mirakc-ui.sqlite3`）。未保存・不正値は既定値（通知無効）にフォールバック。
- **API**: `/api/notification-settings` — GET（token も平文で返す。LAN 内個人アプリ前提）/ PUT（全上書き）/ POST `/test`（保存前の draft の url/token で実送信。失敗 502）。
- **送信**: `server-go/internal/settings` の `SendNtfy`。日本語タイトルはヘッダに載らないため URL を base + topic に分解して JSON publishing（base へ POST、body に topic）。token は `Authorization: Bearer`。失敗は false（throw しない）。
- **イベント検知**は 2 系統:
  - SSE: `server-go/internal/mirakc` の `SubscribeEvents` が `/events` を購読（接続先は `EventsURL` で構築。切断時 5 秒再接続）。`RecordingEventOf` が `recording.started` / `recording.stopped` / `recording.failed`（data `{programId}`）を判別する
  - プロキシフック: mirakc に予約の登録/削除を表す SSE イベントは**無い**ため、`server-go/internal/mirakc` の `NewProxy` が `POST /recording/schedules` / `DELETE /recording/schedules/{id}` の成功をフックで検知する（UI からの手動操作をカバー。キーワードジョブは mirakc 直叩きなので二重通知しない）。フックは転送応答を遅らせないよう await しない
- **通知の組み立て**: `notifyProgramEvent(deps, { key, programId, program? })`。key は `scheduled | started | stopped | failed | unscheduled`。番組名・チャンネル名・放送時間入り。`program` を渡せば `GET /programs/{id}` をスキップ（プロキシの POST 応答を流用）。
- **配線**: `server-go/internal/app` の `notifyIfEnabled`。送信のたびに SQLite から設定を読み、対応トグル + `isValidNtfyUrl` を確認するため、保存後の反映に再起動は不要。`MIRAKC_URL` 未設定ならジョブ・購読とも起動しない。
- **client**: `/settings/notification`（`templates/Notification.tsx`）。draft/dirty はテンプレート内 state（保存済み props との比較で導出）、トーストは `client/hooks/use-toast.ts`。organisms は `Notification/{ServerCard,EventToggles,SaveBar}`。`EventToggles` は `NOTIFICATION_EVENT_KEYS` を回すデータ駆動の 5 行（文言は `notification.events.items.<key>.*`）。トグルは共通 atom `atoms/ToggleSwitch.tsx`（RuleCard も使用）。

## ルールのデータモデル

`client/lib/shared/keyword-rules.ts`（client 側）と `server-go/internal/api`（Go 側）の `KeywordRule`:

- `keyword` — 番組名（name のみ、description は対象外）の部分一致。大文字小文字無視
- `from` / `to` — ローカル日付 `YYYY-MM-DD`、両端含む。未指定は無制限
- `channels` — チャンネル（`MirakurunChannel.channel`、例 `"27"` / `"BS15_0"`）の配列。空 = 全チャンネル。チャンネル単位で指定し、配下の全サービスを横断して対象にする。旧 `serviceIds`（複合 service id）形式の保存値は読み戻し時に `channels: []`（全チャンネル）へフォールバックする（Go 移行の import 検証で不正レコードはスキップされる）
- `genres` — ARIB lv1 コード (0..15) の配列。空 = 全ジャンル（交差判定）
- `enabled` — 停止中は自動予約の対象外

client 側の一致判定 `matchesKeywordRule` はここに置く（フォームの一致プレビューで使用）。入力検証 (`parseKeywordRuleInput`) は server 側のみの契約で `server-go/internal/settings` が Go で持つ（同一の OpenAPI スキーマと移植済みテストでドリフトを検出する）。

## 構成

- **永続化**: `server-go/internal/store`（SQLite `${DATA_DIR:-./data}/mirakc-ui.sqlite3`、generic `records` テーブル・collection `keyword-rules`）。1 接続を全ドメインで共有する。テストはテンポラリディレクトリの DB で実行する。
- **CRUD API**: `server-go/internal/httpapi` が `/api/keyword-rules` にマウント。GET（一覧）/ POST（追加）/ PUT `/{id}`（全項目上書き、有効・停止トグルもこれ）/ DELETE `/{id}`。

## client

- `/settings` は設定ポータル（`templates/Settings.tsx`）。番組表ツールバーと視聴ページの歯車（light/dark の左）から遷移し、各設定画面へのナビゲーションカードを並べる。今後 `/settings/notification` のカードを追加する。
- `/settings/keywords` ルート（`client/routes/settings/keywords.tsx`、レイアウト)。設定ポータルのカードから遷移。設定系ページは `/settings/` 配下に集約する。
- 登録/編集モーダルは URL と対応する子ルートが描画し、レイアウトの `<Outlet/>`（template の `children`）として一覧の上に重なる（番組表のモーダルと同じパターン）:
  - 新規: `/settings/keywords/new`（`client/routes/settings/keywords/new.tsx`）
  - 編集: `/settings/keywords/$ruleId`（`client/routes/settings/keywords/$ruleId.tsx`。読み込み後に対象が無ければ一覧へ戻す）
- ルールは素の fetch ラッパー（`client/lib/api/keyword-rules.ts`）+ TanStack Query。プレビュー用の番組・サービスは `$api` で取得（mirakc の OpenAPI に無い自前 API のため `$api` は使えない）。
- 「今後 7 日間」の対象番組の組み立ては `client/lib/keyword-preview.ts`（`buildUpcoming`）。
- コンポーネント:
  - `organisms/KeywordRules/RuleCard.tsx` — 有効/停止トグル・条件チップ（期間 / ChannelBadge / GenreTag）・一致件数ピル・編集・インライン削除確認
  - `organisms/KeywordRules/RuleFormModal.tsx` — 登録/編集モーダル。キーワード必須、期間（開始 > 終了はエラー）、チャンネル（放送波ごとにグループ表示）、ジャンル（ジャンル色チップ）、一致番組のライブプレビュー（先頭 5 件 + ほか N 件）。本文のみスクロールしフッターは固定（atoms/Modal は overflow を子に委ねる）
  - `templates/KeywordRules.tsx` — ツールバー（タイトル + 番組表へ + テーマ切替）、集計付きヘッダ（登録数・有効数・一致番組数）、カード一覧、空状態
