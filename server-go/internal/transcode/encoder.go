package transcode

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// EncoderName identifies a video encoder candidate. The two V4L2 m2m
// encoders take different quality controls than libx264, and AV1 HW
// encoders cannot mux into MPEG-TS, hence this candidate set.
type EncoderName string

const (
	EncoderHevcV4l2m2m EncoderName = "hevc_v4l2m2m"
	EncoderH264V4l2m2m EncoderName = "h264_v4l2m2m"
	EncoderLibx264     EncoderName = "libx264"
)

// x264CRF: libx264 needs a single resolution-independent CRF
// (docs/transcode-quality.md); ultrafast loses >1 dB at the same bitrate,
// hence the superfast preset in the args below.
const x264CRF = 27

// V4l2QP holds the constant-QP values of the two V4L2 m2m encoders per
// quality preset. -qp is only implemented by the AVE patch
// (docker/ffmpeg-patches 0004), so its use is opt-in via V4L2_CQ=1.
type V4l2QP struct {
	H264 int
	Hevc int
}

type QualitySetting struct {
	// Scale caps the output at the preset's lines (scale=-2:'min(ih,N)'):
	// presets only ever downscale, so a 480-line source stays 480 lines
	// even at quality=1080p — upscaling SD wasted encoder bitrate on
	// pixels the source never had.
	Scale   string
	Bitrate string
	CRF     int
	QP      V4l2QP
}

// QualitySettings holds the per-preset encoder settings. The numbers
// match the average bitrate of the old bitrate-only settings measured
// over five real streams (docs/transcode-quality.md).
var QualitySettings = map[Quality]QualitySetting{
	Quality480p:  {Scale: "-2:'min(ih,480)'", Bitrate: "1000k", CRF: x264CRF, QP: V4l2QP{H264: 29, Hevc: 26}},
	Quality720p:  {Scale: "-2:'min(ih,720)'", Bitrate: "2000k", CRF: x264CRF, QP: V4l2QP{H264: 29, Hevc: 26}},
	Quality1080p: {Scale: "-2:'min(ih,1080)'", Bitrate: "4500k", CRF: x264CRF, QP: V4l2QP{H264: 28, Hevc: 25}},
}

func (s QualitySetting) qpFor(encoder EncoderName) int {
	if encoder == EncoderHevcV4l2m2m {
		return s.QP.Hevc
	}
	return s.QP.H264
}

// V4L2ConstantQualityEnabled mirrors v4l2ConstantQualityEnabled
// (server/lib/encoder.ts). The caller resolves it ONCE at startup and
// passes it as Deps.V4L2CQ so probe and production ffmpeg args can
// never disagree mid-process.
func V4L2ConstantQualityEnabled() bool {
	return os.Getenv("V4L2_CQ") == "1"
}

// BuildVideoEncoderArgs builds the video encoder args. The GOP is fixed
// at 15 frames (0.5s at 30fps) because MSE (mpegts.js) can only start
// decoding at a keyframe. libx264 disables scene-change keyframes
// (-sc_threshold 0): the GOP is already 0.5s so they cost bitrate spikes
// (a Pi CPU-encode drop cause) with no quality gain; v4l2m2m rejects the
// option. With CQ, -b:v is dropped (rate control is off and ignores it).
// B-frames are added for HEVC only — H.264 accepts -bf but emits none.
func BuildVideoEncoderArgs(encoder EncoderName, quality Quality, v4l2Cq bool) []string {
	s := QualitySettings[quality]
	if encoder == EncoderLibx264 {
		return []string{
			"-c:v", "libx264",
			"-preset", "superfast",
			"-tune", "zerolatency",
			"-crf", strconv.Itoa(s.CRF),
			"-g", "15",
			"-sc_threshold", "0",
		}
	}
	if !v4l2Cq {
		return []string{"-c:v", string(encoder), "-b:v", s.Bitrate, "-g", "15"}
	}
	args := []string{"-c:v", string(encoder), "-qp", strconv.Itoa(s.qpFor(encoder)), "-g", "15"}
	if encoder == EncoderHevcV4l2m2m {
		args = append(args, "-bf", "2")
	}
	return args
}

// BuildVideoFilter builds the video filter chain. V4L2 mem2mem only
// accepts yuv420p, so HW encoders force the format; libx264 handles the
// input pix_fmt as-is. HW encoders skip the SPS VUI (SAR) but the
// docker/ffmpeg-patches patch copies the input SAR, so no SAR fixup.
func BuildVideoFilter(encoder EncoderName, quality Quality) string {
	const deinterlace = "yadif=mode=0:parity=-1:deint=1"
	scale := QualitySettings[quality].Scale
	if encoder == EncoderLibx264 {
		return deinterlace + ",scale=" + scale
	}
	return deinterlace + ",scale=" + scale + ",format=yuv420p"
}

// EncoderChain returns the try order reflecting the client's wish: HEVC
// first only when the client declared support (codec=hevc), falling
// back to H.264 HW then software.
func EncoderChain(preferHevc bool) []EncoderName {
	if preferHevc {
		return []EncoderName{EncoderHevcV4l2m2m, EncoderH264V4l2m2m, EncoderLibx264}
	}
	return []EncoderName{EncoderH264V4l2m2m, EncoderLibx264}
}

// pipeStderr pumps a child's stderr to the log line by line with a
// prefix; onLine (if non-nil) receives each non-empty line without the
// newline. Streams broken by process exit are swallowed, like the
// catch-all in the Deno source.
func pipeStderr(src io.Reader, prefix string, log *slog.Logger, onLine func(string)) {
	br := bufio.NewReader(src)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			trimmed := strings.TrimSuffix(line, "\n")
			if trimmed != "" {
				log.Error(prefix + " " + trimmed)
				if onLine != nil {
					onLine(trimmed)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// pumpStderr pipes a child's stderr to the log in the background and
// closes the reader at stream end, bounding the fd lifecycle.
func pumpStderr(c Cmd, prefix string, log *slog.Logger) {
	go func() {
		pipeStderr(c.Stderr(), prefix, log, nil)
		_ = c.Stderr().Close()
	}()
}

type ProbeResult string

const (
	ProbeOK       ProbeResult = "ok"
	ProbeUnusable ProbeResult = "unusable"
	// ProbeBusy: the encoder works but the device is held by another
	// process (the previous session's ffmpeg still exiting) — retry a
	// little instead of dropping the candidate.
	ProbeBusy ProbeResult = "busy"
)

// ProbeAttempt is one probe run; stderr feeds busy detection only.
type ProbeAttempt struct {
	Success bool
	Stderr  string
}

// ProbeRunner runs one probe attempt. err nil means exit 0 (usable).
type ProbeRunner func(ctx context.Context, encoder EncoderName, args []string) (stderr string, err error)

const (
	// ISDB is mostly 1440x1080; a SAR 1:1 test pattern cannot detect the
	// SAR-writing ffmpeg patch, which only the real stream needs.
	probeInput   = "testsrc=size=1440x1080:rate=30:duration=0.34"
	probeTimeout = 5 * time.Second

	// The same client's switch releases the device in ~120ms, so 500ms
	// from the FIRST busy is enough; the window must stay short so a
	// genuinely occupied device doesn't stall the request.
	busyRetry     = 100 * time.Millisecond
	busyRetryWind = 500 * time.Millisecond
)

// buildProbeArgs probes through the same filter and encoder args as the
// live pipeline, so resolution limits and EBUSY surface here too.
func buildProbeArgs(encoder EncoderName, quality Quality, v4l2Cq bool) []string {
	args := []string{
		"-hide_banner", "-v", "error",
		"-f", "lavfi", "-i", probeInput,
		"-vf", BuildVideoFilter(encoder, quality),
	}
	args = append(args, BuildVideoEncoderArgs(encoder, quality, v4l2Cq)...)
	return append(args, "-frames:v", "10", "-f", "null", "-")
}

// IsV4l2Busy detects EBUSY (device held). strerror text follows
// LC_MESSAGES so non-English deployments need the errno: ffmpeg's
// "error code: -16" is untranslated, and the probe's -f null cannot
// produce -16 for any other reason.
func IsV4l2Busy(stderr string) bool {
	return strings.Contains(stderr, "Device or resource busy") ||
		strings.Contains(stderr, "error code: -16")
}

// probeWithBusyRetry retries EBUSY probes every busyRetry for up to
// busyRetryWind measured from the FIRST busy result (a slow first
// attempt must not eat the window). On abort it stops retrying and
// reports unusable — the caller's loop exits on abort anyway.
func probeWithBusyRetry(ctx context.Context, run func(context.Context) ProbeAttempt, clk clock) ProbeResult {
	var retryDeadline time.Time
	for {
		if ctx.Err() != nil {
			return ProbeUnusable
		}
		attempt := run(ctx)
		if attempt.Success {
			return ProbeOK
		}
		if !IsV4l2Busy(attempt.Stderr) {
			return ProbeUnusable
		}
		if retryDeadline.IsZero() {
			retryDeadline = clk.Now().Add(busyRetryWind)
		}
		if !clk.Now().Add(busyRetry).Before(retryDeadline) {
			return ProbeBusy
		}
		_ = clk.Sleep(ctx, busyRetry)
	}
}
