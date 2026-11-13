# FFmpeg パッチ

`Dockerfile` の `ffmpeg-build` ステージが、取得した FFmpeg のソースへ下記を
名前順に適用する。`FFMPEG_VERSION` を上げたら適用できることを確認し、必要なら
パッチを作り直す (`--fuzz=0` で当てるため、文脈がずれるとビルドが止まる)。

## 0001 — V4L2 の HW エンコーダにストリームの SAR を書かせる

`h264_v4l2m2m` などの V4L2 エンコーダは SPS の VUI を書かないため、画素が
4:3 の 1440x1080 (ISDB) が 4:3 で再生される。エンコーダに渡された
`avctx->sample_aspect_ratio` を `h264_metadata` / `hevc_metadata` で SPS に
書き込む (ドライバ依存なし、再エンコードなし)。SPS を含むアクセスユニット
だけをフィルタに通すので、それ以外のパケットはデバイスの出力のまま。

このパッチが入った ffmpeg を前提に、`server/lib/encoder.ts` は
`-bsf:v` を渡さず (渡すと入力の SAR を上書きしてしまう)、HW の拡縮も固定格子
ではなく `scale=-2:<height>` にしている。1080p では 1920x1080 のソースも
縮小されず、その 1:1 の SAR のまま出る。
