# FFmpeg パッチ

`Dockerfile` の `ffmpeg-build` ステージが、取得した FFmpeg 9.0.2 のソースへ
下記を名前順に適用する。`FFMPEG_VERSION` を上げたら適用できることを確認し、
必要ならパッチを作り直す (`--fuzz=0` で当てるため、文脈がずれるとビルドが止まる)。
0001–0004 は AVE (Apple Video Encoder) 向けの V4L2 m2m 拡張で、0002 の
dequeue の signature 変更は 0004 の呼び出し側と対で初めてコンパイルできるため、
まとめて適用してからビルドする。0005 はその上に載る SAR パッチ。

## 0001 — V4L2 のバッファ配置と色変換

V4L2 の OUTPUT バッファを bytesperline / plane ごとの stride に従って書き、
ドライバが 1080 を 1088 に丸めてもフレームの外を読まないようにする。
yuv420p / yuv420p10le を NV12 / NV21 / P010 へコピー中に変換し、P010 を
`AV_PIX_FMT_P010LE` に対応付ける。`v4l2_enc_utils` とその unit test
(`fate-v4l2-enc-utils`) を含む。

## 0002 — V4L2 の dequeue に timeout と入力 wake を渡す

`ff_v4l2_context_dequeue_packet()` に timeout と wake_for_input を追加する。
non-AVE の呼び出しは従来どおり `-1, 1` で、poll の意味は変えない。EOS
イベントは encoder かつ AVE のときだけ `eos_event` に倒し、それ以外は
従来どおり `ctx->done` を立てる。

## 0003 — V4L2 のデバイス選択と AVE の検出

`/dev/video*` を番号順に調べる `-device` オプションを追加する。
`VIDIOC_QUERYCAP` の `driver` が `apple-ave` で始まる場合に `is_apple_ave`
を立てる。検出はカード名ではなく driver 名を ABI とする (カード名は環境で
変わり得る)。AVE 以外は従来の受信ループを使う。

## 0004 — V4L2 エンコーダの AVE 対応

- OUTPUT をピクチャサイズに crop する (1080→1088 のパディングを SPS に残さない)
- `-qp` / `-q:v` の constant QP (rate control off)。どちらも AVE のみ。
- `-bf` と DTS の生成 (AVE のみ。non-AVE は従来どおり B フレーム非対応)
- AVE 用の queue-ahead 受信ループ。CAPTURE バッファを packet へコピーして
  即座に返却し、drain は bounded に待つ。non-AVE は upstream と同じ blocking ループ。
- global header (MP4/Matroska) 用の extradata 生成 (AVE のみ)
- `-qp` の QP は packet の quality stats として付ける (QP が実値のときだけ)

## 0005 — V4L2 の HW エンコーダにストリームの SAR を書かせる

`h264_v4l2m2m` などの V4L2 エンコーダは SPS の VUI を書かないため、画素が
4:3 の 1440x1080 (ISDB) が 4:3 で再生される。エンコーダに渡された
`avctx->sample_aspect_ratio` を `h264_metadata` / `hevc_metadata` で SPS に
書き込む (ドライバ依存なし、再エンコードなし)。SPS を含むアクセスユニット
だけをフィルタに通すので、それ以外のパケットはデバイスの出力のまま。
AVE の受信ループと non-AVE の legacy ループの両方から呼ぶ。

このパッチが入った ffmpeg を前提に、`server/lib/encoder.ts` は
`-bsf:v` を渡さず (渡すと入力の SAR を上書きしてしまう)、HW の拡縮も固定格子
ではなく `scale=-2:<height>` にしている。1080p では 1920x1080 のソースも
縮小されず、その 1:1 の SAR のまま出る。

## ライセンス

0001–0005 は FFmpeg の `libavcodec/v4l2_*` を変更するパッチで、変更部分は
FFmpeg 本体と同じ **LGPL-2.1-or-later** で提供する。パッチファイル自体に
別のライセンスはない。0001–0004 の原案は
[aquarat/apple-m-series-ave-driver](https://github.com/aquarat/apple-m-series-ave-driver)
の `ffmpeg/patches` (LGPL-2.1-or-later) に由来し、mirakc-ui 向けに FFmpeg
9.0.2 へ追従し、AVE 以外のドライバ向けのゲートと SAR の legacy 呼び出しを
追加したもの。0005 は mirakc-ui の SAR パッチ。
