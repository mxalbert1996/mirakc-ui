FROM --platform=$BUILDPLATFORM docker.io/denoland/deno:2.9.8 AS mirakc-ui-build

WORKDIR /app

COPY . .
RUN deno install
RUN deno task build

FROM docker.io/debian:trixie-slim AS tsreadex-build

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

# ffmpeg は Debian のパッケージを使わず、必要な機能だけを有効にしてビルドする。
# ffmpeg パッケージは libavdevice 経由で mesa/LLVM 等を引き連れ約 410 MB になるが、
# 実行時に使うのは下記のコーデック・フィルタ・コンテナだけのため、最小構成で十分。
# 生成物は glibc 以外に依存しないので、実行時ステージに追加パッケージは要らない。
FROM docker.io/debian:trixie-slim AS ffmpeg-build

# 再現性のためバージョンは固定する。更新はここを書き換えてリビルドし、後述の
# 実機ストリームでの確認をやり直す (ffmpeg は公式リリースを使う。x264 はリリースが
# 無いため stable ブランチの先頭 SHA を固定する)。
ARG FFMPEG_VERSION=9.0.2
ARG X264_VERSION=b35605ace3ddf7c1a5d67a2eb553f034aef41d55

# nasm は x86_64 のアセンブリ実装 (x264 / ffmpeg の x86asm) に必須。aarch64 は gas を
# 使うため不要だが、マルチアーキテクチャビルドでは linux/amd64 側の configure が
# "Found no assembler" で失敗するため常に入れる。
RUN <<EOF
    apt-get update
    apt-get install -y --no-install-recommends \
        build-essential pkg-config curl ca-certificates xz-utils bzip2 nasm
    rm -rf /var/lib/apt/lists/*
EOF

RUN <<EOF
    curl -fsSL "https://code.videolan.org/videolan/x264/-/archive/$X264_VERSION/x264-$X264_VERSION.tar.bz2" \
        | tar -xj -C /tmp
    cd "/tmp/x264-$X264_VERSION"
    ./configure --prefix=/usr/local --enable-static --disable-cli --enable-pic
    make -j"$(nproc)"
    make install
EOF

# server/routes/transcode.ts と server/lib/encoder.ts が使う機能を列挙する。
# - 入力: mpegts (pipe:0) / MPEG-2, H.264, AAC (AAC-LATM) のデコード
# - 映像: libx264 (ソフトウェア) / h264_v4l2m2m (ハードウェア) と yadif, scale, format
# - 音声: aac エンコーダ
# - 出力: mpegts (pipe:1)。`-c:d copy` のデータストリーム (ARIB 字幕) は
#   mpegts のデータストリームとしてそのまま通す
# - 起動時 probe: lavfi の color 入力と -f null 出力
# wrapped_avframe / pcm_s16le は lavfi と -f null に必須 (decoder と encoder の両方)。
RUN <<EOF
    curl -fsSL "https://ffmpeg.org/releases/ffmpeg-$FFMPEG_VERSION.tar.xz" \
        | tar -xJ -C /tmp
    cd "/tmp/ffmpeg-$FFMPEG_VERSION"
    ./configure \
        --disable-everything \
        --disable-doc \
        --disable-ffplay \
        --disable-ffprobe \
        --disable-network \
        --disable-autodetect \
        --enable-static \
        --disable-shared \
        --enable-gpl \
        --enable-libx264 \
        --enable-v4l2-m2m \
        --enable-swscale \
        --enable-swresample \
        --enable-decoder=mpeg2video,h264,aac,aac_latm,rawvideo,wrapped_avframe,pcm_s16le \
        --enable-encoder=libx264,aac,h264_v4l2m2m,wrapped_avframe,pcm_s16le \
        --enable-parser=mpegvideo,h264,aac,aac_latm \
        --enable-demuxer=mpegts,data \
        --enable-muxer=mpegts,null \
        --enable-indev=lavfi \
        --enable-filter=yadif,scale,format,null,aresample,anull,color,testsrc,sine,anullsrc \
        --enable-protocol=pipe,file
    make -j"$(nproc)"
    install -m755 ffmpeg /usr/local/bin/ffmpeg
EOF

# Debian 素のイメージに Deno バイナリだけを載せる (公式イメージは apt 済みの
# 追加パッケージを含み、実行時には不要な分だけイメージが大きくなる)。
FROM docker.io/debian:trixie-slim

RUN <<EOF
    apt-get update
    apt-get install -y --no-install-recommends tini ca-certificates
    rm -rf /var/lib/apt/lists/*
EOF

COPY --from=deno /usr/bin/deno /usr/local/bin/deno
COPY --from=tsreadex-build /tmp/tsreadex/build/tsreadex /usr/local/bin/tsreadex
COPY --from=ffmpeg-build /usr/local/bin/ffmpeg /usr/local/bin/ffmpeg
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
