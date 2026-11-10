FROM --platform=$BUILDPLATFORM docker.io/denoland/deno:2.9.8 AS mirakc-ui-build

WORKDIR /app

COPY . .
RUN deno install
RUN deno task build

FROM docker.io/debian:bookworm-slim AS tsreadex-build

RUN <<EOF
    apt-get update
    apt-get install -y --no-install-recommends g++ cmake make curl ca-certificates
    rm -rf /var/lib/apt/lists/*
EOF

RUN <<EOF
    curl -fsSL https://github.com/xtne6f/tsreadex/archive/refs/tags/master-240517.tar.gz \
        | tar -xz -C /tmp
    mv /tmp/tsreadex-master-240517 /tmp/tsreadex
    cmake -B /tmp/tsreadex/build -S /tmp/tsreadex -DCMAKE_BUILD_TYPE=Release
    cmake --build /tmp/tsreadex/build
EOF

# 実行時イメージ用の Deno バイナリの取得元。ターゲットと同じプラットフォームの
# イメージから COPY するため、マルチアーキテクチャビルドでも arch が揃う。
FROM docker.io/denoland/deno:2.9.8 AS deno

# Debian 素のイメージに Deno バイナリだけを載せる (公式イメージは apt 済みの
# 追加パッケージを含み、実行時には不要な分だけイメージが大きくなる)。
FROM docker.io/debian:bookworm-slim

RUN <<EOF
    apt-get update
    apt-get install -y --no-install-recommends ffmpeg tini ca-certificates
    rm -rf /var/lib/apt/lists/*
EOF

COPY --from=deno /usr/bin/deno /usr/local/bin/deno
COPY --from=tsreadex-build /tmp/tsreadex/build/tsreadex /usr/local/bin/tsreadex
RUN chmod +x /usr/local/bin/deno /usr/local/bin/tsreadex

WORKDIR /app

# 実行時に必要な依存だけに絞った設定を作る。deno.json の import マップには
# client (React / Vite / Storybook / Vitest 等) の依存も含まれるため、そのまま
# `deno install` すると実行時に一切読まれない npm パッケージ (約 300 MB) が
# イメージに入る。server/ が import するパッケージだけを deno.json から引き継ぎ、
# バージョンの情報源は deno.json 1 つに保つ (追加時はここに名前を足す)。
# 不足があれば次の `deno cache server/main.ts` が解決に失敗してビルドが止まる。
COPY deno.json deno.lock ./
RUN <<EOF
    deno eval '
    const path = "deno.json";
    const config = JSON.parse(Deno.readTextFileSync(path));
    const runtimeDeps = ["hono", "@cfworker/json-schema", "json-schema-to-ts"];
    config.imports = Object.fromEntries(
      runtimeDeps.map((specifier) => [specifier, config.imports[specifier]]),
    );
    Deno.writeTextFileSync(path, JSON.stringify(config, null, 2) + "\n");
    '
EOF
RUN deno install
COPY --from=mirakc-ui-build /app/client/dist ./client/dist
COPY server ./server
RUN deno cache server/main.ts

# ビルドのたびに変わるため、キャッシュを無効化しないよう最後に置く。
ARG GIT_REVISION
ENV DENO_DEPLOYMENT_ID=${GIT_REVISION}

EXPOSE 8000

# タイムゾーンはイメージにハードコードしない (中立)。録画通知・録画ファイル名の
# 日時整形はプロセスの TZ 環境変数に依存するため、実行時に渡す
# (compose の environment: か docker run -e TZ=Asia/Tokyo)。未設定なら UTC。
# Deno (V8/ICU) はゾーン情報を内蔵するため tzdata の追加インストールは不要。

# Hono が client/dist を serveStatic しつつ /api を提供する。
# CMD は --env-file を使わず、コンテナの環境変数 (MIRAKC_URL / TZ 等) を直接読む。
# ENTRYPOINT は tini のみにして、`docker run <image> ffmpeg` / `sh` のような
# 任意コマンドの実行を可能にする (deno は CMD 側で指定する)。
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["deno", "serve", "-A", "--port", "8000", "server/main.ts"]
