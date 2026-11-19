FROM --platform=$BUILDPLATFORM docker.io/denoland/deno:2.9.8 AS mirakc-ui-build

WORKDIR /app

COPY . .
RUN deno install
RUN deno task build

# Cross-compile the Go server the same way as the client builder above: run
# the compiler on the build platform and target $TARGETOS/$TARGETARCH, so
# multi-architecture builds never need an emulated toolchain. CGO_ENABLED=0
# makes the binary fully static (no libc dependency at runtime).
FROM --platform=$BUILDPLATFORM docker.io/golang:1.27 AS mirakc-ui-go-build

ARG TARGETOS TARGETARCH

WORKDIR /src
# Cache the module download separately from the sources so ordinary code
# edits do not re-fetch dependencies from the proxy.
COPY server-go/go.mod server-go/go.sum ./
RUN go mod download
COPY server-go ./server-go
# The client built above fills the go:embed all:static dir. The committed
# .gitkeep stays behind harmlessly: the web handler never serves it.
COPY --from=mirakc-ui-build /app/client/dist ./server-go/web/static
WORKDIR server-go
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags='-s -w' -o /out/mirakc-ui ./cmd/mirakc-ui

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
        build-essential pkg-config curl ca-certificates xz-utils bzip2 nasm patch
    rm -rf /var/lib/apt/lists/*
EOF

# ソースの取得 (ここから下の 2 つの RUN) はファイルへ落としてから展開する。
# `curl | tar` のパイプでは curl の失敗が解凍エラーとして現れて原因が分からなくなる
# (CI で "curl: (35) Recv failure" が "cd: can't cd to /tmp/ffmpeg-..." になっていた)。
# --retry-all-errors で一時的な切断を吸収し、set -eu で失敗した時点で止める。
RUN <<EOF
    set -eu
    curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors \
        -o /tmp/x264.tar.bz2 \
        "https://code.videolan.org/videolan/x264/-/archive/$X264_VERSION/x264-$X264_VERSION.tar.bz2"
    tar -xjf /tmp/x264.tar.bz2 -C /tmp
    cd "/tmp/x264-$X264_VERSION"
    ./configure --prefix=/usr/local --enable-static --disable-cli --enable-pic
    make -j"$(nproc)"
    make install
EOF

# server/routes/transcode.ts と server/lib/encoder.ts が使う機能を列挙する。
# - 入力: mpegts (pipe:0) / MPEG-2, H.264, AAC (AAC-LATM) のデコード
# - 映像: libx264 (ソフトウェア) / h264_v4l2m2m (HW H.264) / hevc_v4l2m2m (HW HEVC)
#   と yadif, scale, format。HW エンコーダは SPS の VUI を書かず SAR が消える
#   ため、docker/ffmpeg-patches のパッチで h264_metadata / hevc_metadata を
#   エンコーダ内部から掛け、入力フレームの SAR をそのまま書かせる。
#   同じパッチ群に AVE (Apple Video Encoder) の V4L2 m2m 拡張 (0001–0004) を
#   含み、SAR パッチ (0005) はその上に適用する
# - 音声: aac エンコーダ
# - 出力: mpegts (pipe:1)。`-c:d copy` のデータストリーム (ARIB 字幕) は
#   mpegts のデータストリームとしてそのまま通す
# - probe (リクエストごと): lavfi の testsrc 入力と -f null 出力
# wrapped_avframe / pcm_s16le は lavfi と -f null に必須 (decoder と encoder の両方)。
COPY docker/ffmpeg-patches /tmp/ffmpeg-patches
RUN <<EOF
    set -eu
    curl -fsSL --retry 5 --retry-delay 2 --retry-all-errors \
        -o /tmp/ffmpeg.tar.xz "https://ffmpeg.org/releases/ffmpeg-$FFMPEG_VERSION.tar.xz"
    tar -xJf /tmp/ffmpeg.tar.xz -C /tmp
    cd "/tmp/ffmpeg-$FFMPEG_VERSION"
    # 0001–0004 (AVE) をまとめて適用してから 0005 (SAR) を適用する
    for p in $(LC_ALL=C ls /tmp/ffmpeg-patches/*.patch); do
        echo "patches: $(basename "$p")"
        patch -p1 --fuzz=0 -i "$p"
    done
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
        --enable-encoder=libx264,aac,h264_v4l2m2m,hevc_v4l2m2m,wrapped_avframe,pcm_s16le \
        --enable-bsf=h264_metadata,hevc_metadata \
        --enable-parser=mpegvideo,h264,aac,aac_latm \
        --enable-demuxer=mpegts,data \
        --enable-muxer=mpegts,null \
        --enable-indev=lavfi \
        --enable-filter=yadif,scale,format,null,aresample,anull,color,testsrc,sine,anullsrc \
        --enable-protocol=pipe,file
    make -j"$(nproc)"
    install -m755 ffmpeg /usr/local/bin/ffmpeg
EOF

# The app is a single statically linked Go binary, so the runtime image is
# plain Debian plus tini and CA certificates — nothing else.
FROM docker.io/debian:trixie-slim

RUN <<EOF
    apt-get update
    apt-get install -y --no-install-recommends tini ca-certificates
    rm -rf /var/lib/apt/lists/*
EOF

COPY --from=tsreadex-build /tmp/tsreadex/build/tsreadex /usr/local/bin/tsreadex
COPY --from=ffmpeg-build /usr/local/bin/ffmpeg /usr/local/bin/ffmpeg
COPY --from=mirakc-ui-go-build /out/mirakc-ui /usr/local/bin/mirakc-ui
RUN chmod +x /usr/local/bin/tsreadex /usr/local/bin/mirakc-ui

# Run as a dedicated non-root user. Everything the server writes (SQLite
# db + WAL, export files) lives under DATA_DIR, which defaults to ./data
# relative to WORKDIR (/app/data) — the same volume convention as before
# (./mirakc-ui-data:/app/data). Nothing is written elsewhere, so a
# read-only root filesystem works.
RUN <<EOF
    useradd --system --create-home --uid 900 mirakc-ui
    mkdir -p /app/data
    chown mirakc-ui:mirakc-ui /app/data
EOF
USER mirakc-ui
WORKDIR /app

# Changes on every build; keep it last so it never invalidates cached layers.
ARG GIT_REVISION
ENV MIRAKC_UI_REVISION=${GIT_REVISION}

EXPOSE 8000

# Timezone is not baked into the image (neutral). Date formatting for
# recording notifications / file names follows the process TZ — pass it at
# runtime (compose environment: or docker run -e TZ=Asia/Tokyo); unset
# means UTC. No tzdata package is needed: the Go binary embeds time/tzdata.

# One binary serves the embedded client (SPA) and the /api routes. ENTRYPOINT
# is tini only, so `docker run <image> ffmpeg` / `sh` still works, and
# `mirakc-ui import` runs the explicit re-import subcommand.
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/mirakc-ui"]
