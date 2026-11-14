# Transcode quality settings

How the encoder arguments in `server/lib/encoder.ts` were chosen, and what they
cost. Measured 2026-11 on this machine (Apple AVE, FFmpeg 9.0.2 with the patches
in `docker/ffmpeg-patches`).

## Decision

- **libx264: always CRF**, `-preset superfast -tune zerolatency -crf 27`. CRF is
  resolution independent, so one value covers all presets. `superfast` is the
  knee: at the same bitrate it is ~1.1 dB better than `ultrafast` for ~35% more
  CPU, while `veryfast` adds only ~0.1 dB for another ~20%.
- **v4l2m2m: constant QP behind `V4L2_CQ=1`**, otherwise the previous target
  bitrate. `-qp` returns `ENOSYS` during init on non-AVE drivers (patch 0004),
  so a mis-set flag fails the probe and the chain falls back to x264 instead of
  streaming without rate control. The encoder names (`h264_v4l2m2m`,
  `hevc_v4l2m2m`) are generic wrappers and do not reveal the driver, which is
  why enabling it is explicit rather than automatic.
- **QP values** land within ~10% of the old per-preset bitrate: 1080p `28`/`25`,
  720p `29`/`26`, 480p `29`/`26` (H.264/HEVC).
- **HEVC adds `-bf 2` in CQ mode only.** B frames save ~17% at equal QP and pay
  for roughly one QP step. `h264_v4l2m2m` ignores `-bf` (no B frames are ever
  emitted), so the flag would be dead weight there.

## Measurements

10 s samples from five live services (sports, movie, news ticker, anime, golf),
encoded through the production filter chain. Quality is Y-PSNR / SSIM against
that same filtered source, averaged over the five clips.

| preset | encoder  | args                      | kbps | Y-PSNR | SSIM   |
| ------ | -------- | ------------------------- | ---- | ------ | ------ |
| 1080p  | old      | `h264_v4l2m2m -b:v 4500k` | 3688 | 36.64  | 0.9684 |
| 1080p  | H.264 HW | `-qp 28`                  | 4856 | 37.39  | 0.9735 |
| 1080p  | HEVC HW  | `-qp 25 -bf 2`            | 4778 | 37.86  | 0.9775 |
| 1080p  | x264     | `superfast -crf 27`       | 4212 | 39.01  | 0.9734 |
| 720p   | old      | `h264_v4l2m2m -b:v 2000k` | 1608 | 34.82  | 0.9630 |
| 720p   | H.264 HW | `-qp 29`                  | 1990 | 35.42  | 0.9678 |
| 720p   | HEVC HW  | `-qp 26 -bf 2`            | 1949 | 35.98  | 0.9740 |
| 720p   | x264     | `superfast -crf 27`       | 1976 | 38.36  | 0.9712 |
| 480p   | old      | `h264_v4l2m2m -b:v 1000k` | 794  | 33.89  | 0.9545 |
| 480p   | H.264 HW | `-qp 29`                  | 1093 | 35.01  | 0.9640 |
| 480p   | HEVC HW  | `-qp 26 -bf 2`            | 1098 | 35.68  | 0.9718 |
| 480p   | x264     | `superfast -crf 27`       | 1082 | 37.45  | 0.9684 |

The old path undershoots its configured bitrate by ~20% (AVE rate control), which
is why the CQ rows sit slightly higher: they are matched to the configured
budget, not to what the old path actually delivered.

## Caveats

- CQ has no bitrate ceiling. 2 s peaks reach ~11 Mbps at 1080p on the AVE
  encoders (x264 CRF stays at 4-7 Mbps). Fine on a LAN, but there is no bitrate
  mode to fall back to: libx264 is CRF only, and the v4l2m2m bitrate path is
  either off (`V4L2_CQ` unset) or replaced by CQ. Add a VBV ceiling
  (`-maxrate`/`-bufsize`) if constrained links become a requirement.
- PSNR favors x264 while SSIM is roughly even: x264 keeps more texture with
  mild blocking, AVE is softer and cleaner. The ranking is a matter of taste.
- `-bf 2` adds one frame of reorder latency (~33 ms), negligible next to the
  0.5 s GOP.
- These numbers are content specific; re-measure if the source mix changes.
