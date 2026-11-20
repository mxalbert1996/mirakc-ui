# 実装・運用の知見

## React / TanStack の hook 規約

- **render body 内で `setState` を呼ばない** — React のアンチパターン。`useEffect` に寄せる。
- **リスト要素には `key` prop を必ず指定する** — `Array.map()` で `<li>` 等を返すとき差分計算が狂う。`key={stableId}` を付ける。
- サーバー状態は `$api.useQuery` / `$api.useMutation`（TanStack Query, `client/lib/api/client.ts`）で扱い、`{ data, isPending, isError, error }` で分岐する。自前の loading / initialized フラグは持たない（旧 `useGet` の `initialized` 永久 false で Loading から抜けられない罠はこれで消えた）。mutation 後の再取得は `queryClient.invalidateQueries({ queryKey: ["get", "/x"] })`。
- URL 状態は TanStack Router の `validateSearch` + `useNavigate` に寄せる。`history.pushState` の手書きはしない。
- 非同期初期化の stale closure 対策には `useRef` を使う。`Player.tsx` の mpegts/aribb24 初期化では `destroyed` フラグと ref で cleanup タイミングを管理する。React StrictMode の double-effect（effect が 2 回走る）に注意し、cleanup を冪等にする。

## Deno のサブプロセス管理 (stream を Response に流す場合)

`Deno.Command(...).spawn()` で子プロセスを spawn し、`child.stdout` を `new Response(...)` の body に渡す構成では、**client が接続を切っても子プロセスが残る** 問題に注意。ffmpeg 等はバッファが埋まって stdout に書けなくなると backpressure でブロックし、プロセスがゾンビ化する。

### 対処パターン

以下 5 経路から冪等な `cleanup(reason)` を発火させる。

1. `ctx.req.signal` の `abort` イベント (client 切断)
2. `childA.status` resolve (子プロセス終了)
3. `childB.status` resolve (もう一方の子プロセス終了)
4. 上流パイプの `.pipeTo(...).catch(...)` 経路
5. Response body を自前の `ReadableStream` でラップし `UnderlyingSource.cancel` を拾う

`cleanup` は SIGTERM を送り、3 秒後に生存していたら SIGKILL。

```ts
const responseBody = new ReadableStream<Uint8Array>({
  async start(controller) {
    const reader = childA.stdout.getReader();
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) {
          controller.close();
          return;
        }
        controller.enqueue(value);
      }
    } catch (e) {
      controller.error(e);
    }
  },
  cancel() {
    cleanup("response body cancel");
  },
});
```

また `stderr` は `piped` にして必ずログに吐くこと。`"null"` だと障害解析ができない。改行単位で `console.error` に流すヘルパを用意するのが定石。

## dev サーバ・Go サーバの残骸がポートを握る

`pnpm dev` は concurrently で Go サーバ(:8000) と Vite(:5173) を並行起動する（`-k` で伝播するため孤児は残らない）。それでも 8000 番ポートが占有される場合は `go run` の残骸が考えられる。

`pgrep -af mirakc-ui` で確認し、`pkill -f mirakc-ui` で始末する。

## Deno で外部エンコーダを実 probe 検出する

`ffmpeg -encoders` の文字列一致は**デバイス有無を判定できない**ので誤検出する (Debian の ffmpeg pkg は `h264_v4l2m2m` をビルド済み。ただし `/dev/video*` が無ければ使えない)。

対処: `ffmpeg -f lavfi -i color=... -c:v <encoder> -frames:v 10 -f null -` のような**実エンコードテスト**を 5 秒程度の timeout (`AbortController`) 付きで走らせ、exit code で判定する。

並行リクエストのレースは `let detectingPromise: Promise<...> | null` で直列化。両方失敗時は `null` を返してハンドラ側で 503 応答。`null` はキャッシュしない (プロセス内で ffmpeg 再インストール等の復旧余地を残す)。

## mirakc まわりの運用知見

- `/api/services/:id/stream?decode=1` は **MPEG-2 Video のまま**。ブラウザ (MSE / WebCodecs) では再生できないので、H.264 などへのトランスコード層を挟む必要がある。
- トランスコード層は mirakc-ui 内で実装する (A 方式) か、mirakc の `post-filters` に寄せる (B' 方式) かで設計が分かれる。`post-filters` はクエリ per-request 指定なので**他クライアント (EPGStation 等) に影響しない**。詳細は [#11](https://github.com/ansanloms/mirakc-ui/issues/11) / [#16](https://github.com/ansanloms/mirakc-ui/issues/16)。
- tsreadex は ARIB 字幕 PES を ID3 timed-metadata に変換するツール。`aribb24.js` は mpegts.js の `TIMED_ID3_METADATA_ARRIVED` だけでなく `PES_PRIVATE_DATA_ARRIVED` 経由でも字幕を受けられるため、tsreadex 無しでも描画できる可能性がある (要実機検証)。

## Raspberry Pi 5 特有

- **H.264 HW encoder 非搭載** (BCM2712 から削除)。Pi 4 までは `h264_v4l2m2m` が使えたが、Pi 5 では encode/decode ともに CPU 処理になる。
- libx264 `-preset ultrafast -tune zerolatency` で 720p リアルタイム 1 ストリームは実用、1080p は 1〜2 ストリームが限度。
- `/dev/video*` が存在しても encoder として使えない点に注意 (Pi 4 前提の検出ロジックを使い回すと誤判定する)。

## Git 運用の小ネタ

- ローカルの追跡切れブランチは `git branch -vv` で `[origin/xxx: gone]` を確認してから `git branch -D` で削除。
- 機能領域を分割して PR を起こしたい時は、`git checkout <source-branch> -- <path>` でファイル単位の取り込みが使える。cherry-pick より差分が明確。
- PR ブランチを main と同期するときは rebase ではなく merge commit で十分 (CI の synchronize が自動再走するため)。
- destructive な操作 (`git reset --hard`, `git push --force`) は明示指示が無ければ避ける。

## 動作確認のアプローチ

自動テストは client (`pnpm test` — vitest) と server (`cd server-go && go test -race ./...`) で維持されている。型は `pnpm check`、lint/fmt は `pnpm lint`。テストが効かない UI / ストリーム挙動は以下で確認する。

1. **dev サーバ起動**: `pnpm dev` が Go API サーバ(:8000) と Vite(:5173) を並行起動する。`/api` は Vite proxy 経由で Go サーバに届く。
2. **ホスト名経由のアクセス**: Raspberry Pi 等のリモート環境で開発する場合は `VITE_ALLOWED_HOSTS` env を設定した上で `http://<host>:5173/` にブラウザでアクセス。
3. **ブラウザ DevTools で検証**: Chrome の DevTools (`mcp__chrome-devtools__*` MCP が使える環境なら Claude Code からも操作可) で以下を観察する。
   - Console タブ: エラー / 警告の発生状況
   - Network タブ: API リクエストのステータス、ストリームの `Content-Type`、レスポンスサイズ
   - Elements タブ: `<video>` / `<canvas>` / state を反映する DOM 属性
4. **サーバログの確認**: `docker logs <コンテナ>` で slog の JSON ログ、tsreadex / ffmpeg の stderr を追う。
5. **サービスプロセスの確認**: `pgrep -af 'ffmpeg|tsreadex'` でゾンビ子プロセスの有無を確認する。client 切断後も残っている場合はクリーンアップが効いていない。
