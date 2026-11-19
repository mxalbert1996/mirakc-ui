package transcode

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Port of server/lib/encoder.test.ts: the exact arg arrays are
// backward-compat promises, order included.

func TestBuildVideoEncoderArgsLibx264(t *testing.T) {
	got := BuildVideoEncoderArgs(EncoderLibx264, Quality1080p, false)
	want := []string{"-c:v", "libx264", "-preset", "superfast", "-tune", "zerolatency", "-crf", "27", "-g", "15", "-sc_threshold", "0"}
	if !slices.Equal(got, want) {
		t.Fatalf("BuildVideoEncoderArgs(libx264, 1080p) = %q, want %q", got, want)
	}
	if slices.Contains(got, "-b:v") {
		t.Error("libx264 args must not contain -b:v")
	}
}

func TestBuildVideoEncoderArgsHWWithoutCQ(t *testing.T) {
	want := []string{"-c:v", "h264_v4l2m2m", "-b:v", "4500k", "-g", "15"}
	if got := BuildVideoEncoderArgs(EncoderH264V4l2m2m, Quality1080p, false); !slices.Equal(got, want) {
		t.Errorf("BuildVideoEncoderArgs(h264_v4l2m2m, 1080p) = %q, want %q", got, want)
	}
	want = []string{"-c:v", "hevc_v4l2m2m", "-b:v", "2000k", "-g", "15"}
	if got := BuildVideoEncoderArgs(EncoderHevcV4l2m2m, Quality720p, false); !slices.Equal(got, want) {
		t.Errorf("BuildVideoEncoderArgs(hevc_v4l2m2m, 720p) = %q, want %q", got, want)
	}
}

func TestBuildVideoEncoderArgsLibx264IgnoresCQ(t *testing.T) {
	if !slices.Equal(BuildVideoEncoderArgs(EncoderLibx264, Quality1080p, true),
		BuildVideoEncoderArgs(EncoderLibx264, Quality1080p, false)) {
		t.Fatal("libx264 must ignore the CQ flag")
	}
}

func TestBuildVideoEncoderArgsHWWithCQ(t *testing.T) {
	want := map[EncoderName]map[Quality]string{
		EncoderH264V4l2m2m: {Quality1080p: "28", Quality720p: "29", Quality480p: "29"},
		EncoderHevcV4l2m2m: {Quality1080p: "25", Quality720p: "26", Quality480p: "26"},
	}
	for _, encoder := range []EncoderName{EncoderH264V4l2m2m, EncoderHevcV4l2m2m} {
		for _, quality := range Qualities {
			args := BuildVideoEncoderArgs(encoder, quality, true)
			if args[indexOf(args, "-qp")+1] != want[encoder][quality] {
				t.Errorf("BuildVideoEncoderArgs(%s, %s, cq) -qp = %s", encoder, quality, args[indexOf(args, "-qp")+1])
			}
			// CQ disables rate control, so -b:v must be absent.
			if slices.Contains(args, "-b:v") {
				t.Errorf("BuildVideoEncoderArgs(%s, %s, cq) must not contain -b:v", encoder, quality)
			}
			if args[indexOf(args, "-g")+1] != "15" {
				t.Errorf("BuildVideoEncoderArgs(%s, %s, cq) -g = %s", encoder, quality, args[indexOf(args, "-g")+1])
			}
		}
	}
}

func TestBuildVideoEncoderArgsBframesHEVCOnly(t *testing.T) {
	args := BuildVideoEncoderArgs(EncoderHevcV4l2m2m, Quality1080p, true)
	if tail := args[len(args)-2:]; !slices.Equal(tail, []string{"-bf", "2"}) {
		t.Errorf("hevc CQ args tail = %q, want [-bf 2]", tail)
	}
	if slices.Contains(BuildVideoEncoderArgs(EncoderH264V4l2m2m, Quality1080p, true), "-bf") {
		t.Error("h264 CQ args must not contain -bf")
	}
	if slices.Contains(BuildVideoEncoderArgs(EncoderHevcV4l2m2m, Quality1080p, false), "-bf") {
		t.Error("hevc non-CQ args must not contain -bf")
	}
}

func TestBuildVideoEncoderArgsNoBitstreamFilter(t *testing.T) {
	// The SAR is written by the encoder-side patch, so no -bsf:v.
	for _, encoder := range []EncoderName{EncoderHevcV4l2m2m, EncoderH264V4l2m2m, EncoderLibx264} {
		if slices.Contains(BuildVideoEncoderArgs(encoder, Quality1080p, true), "-bsf:v") {
			t.Errorf("BuildVideoEncoderArgs(%s) must not contain -bsf:v", encoder)
		}
	}
}

func TestV4L2ConstantQualityEnabled(t *testing.T) {
	t.Setenv("V4L2_CQ", "")
	if V4L2ConstantQualityEnabled() {
		t.Error("V4L2_CQ unset must disable")
	}
	t.Setenv("V4L2_CQ", "0")
	if V4L2ConstantQualityEnabled() {
		t.Error("V4L2_CQ=0 must disable")
	}
	t.Setenv("V4L2_CQ", "1")
	if !V4L2ConstantQualityEnabled() {
		t.Error("V4L2_CQ=1 must enable")
	}
}

func TestBuildVideoFilter(t *testing.T) {
	cases := []struct {
		encoder EncoderName
		quality Quality
		want    string
	}{
		{EncoderH264V4l2m2m, Quality1080p, "yadif=mode=0:parity=-1:deint=1,scale=-2:1080,format=yuv420p"},
		{EncoderHevcV4l2m2m, Quality720p, "yadif=mode=0:parity=-1:deint=1,scale=-2:720,format=yuv420p"},
		{EncoderLibx264, Quality1080p, "yadif=mode=0:parity=-1:deint=1,scale=-2:1080"},
	}
	for _, c := range cases {
		if got := BuildVideoFilter(c.encoder, c.quality); got != c.want {
			t.Errorf("BuildVideoFilter(%s, %s) = %q, want %q", c.encoder, c.quality, got, c.want)
		}
	}
}

func TestEncoderChain(t *testing.T) {
	if got := EncoderChain(true); !slices.Equal(got, []EncoderName{EncoderHevcV4l2m2m, EncoderH264V4l2m2m, EncoderLibx264}) {
		t.Errorf("EncoderChain(true) = %v", got)
	}
	if got := EncoderChain(false); !slices.Equal(got, []EncoderName{EncoderH264V4l2m2m, EncoderLibx264}) {
		t.Errorf("EncoderChain(false) = %v", got)
	}
}

func TestIsV4l2Busy(t *testing.T) {
	busyStderr := strings.Join([]string{
		"[hevc_v4l2m2m @ 0x7f8] VIDIOC_STREAMON failed on capture context",
		"[hevc_v4l2m2m @ 0x7f8] Error encoding a frame: Device or resource busy",
		"Task finished with error code: -16 (Device or resource busy)",
		"Conversion failed!",
	}, "\n")
	if !IsV4l2Busy(busyStderr) {
		t.Error("real busy stderr must be detected")
	}
	if IsV4l2Busy("[hevc_v4l2m2m @ 0x7f8] Unknown option 'qp'") {
		t.Error("unrecoverable errors are not busy")
	}
	if IsV4l2Busy("[h264_v4l2m2m @ 0x7f8] Initialization failed: -38 (Function not implemented)") {
		t.Error("ENOSYS is not busy")
	}
	// VIDIOC_STREAMON failure alone is not proof of occupancy.
	if IsV4l2Busy("[h264_v4l2m2m @ 0x7f8] VIDIOC_STREAMON failed on capture context") {
		t.Error("STREAMON failure alone is not busy")
	}
	if IsV4l2Busy("") {
		t.Error("empty stderr is not busy")
	}
}

// strerror text follows LC_MESSAGES; rely on the untranslated errno.
func TestIsV4l2BusyLocalized(t *testing.T) {
	localized := "Task finished with error code: -16 (Gerät oder Ressource belegt)"
	if strings.Contains(localized, "Device or resource busy") {
		t.Fatal("fixture must be non-English")
	}
	if !IsV4l2Busy(localized) {
		t.Error("errno -16 must be detected regardless of locale")
	}
}

func TestQualitySettings(t *testing.T) {
	want := map[Quality]QualitySetting{
		Quality480p:  {Scale: "-2:480", Bitrate: "1000k", CRF: 27, QP: V4l2QP{H264: 29, Hevc: 26}},
		Quality720p:  {Scale: "-2:720", Bitrate: "2000k", CRF: 27, QP: V4l2QP{H264: 29, Hevc: 26}},
		Quality1080p: {Scale: "-2:1080", Bitrate: "4500k", CRF: 27, QP: V4l2QP{H264: 28, Hevc: 25}},
	}
	if !reflect.DeepEqual(QualitySettings, want) {
		t.Fatalf("QualitySettings = %+v, want %+v", QualitySettings, want)
	}
}

const busyStderr = "Task finished with error code: -16 (Device or resource busy)"

func TestProbeWithBusyRetryOKAfterBusy(t *testing.T) {
	clk := newFakeClock()
	results := []ProbeAttempt{
		{Success: false, Stderr: busyStderr},
		{Success: false, Stderr: busyStderr},
		{Success: true},
	}
	calls := 0
	got := probeWithBusyRetry(context.Background(), func(context.Context) ProbeAttempt {
		defer func() { calls++ }()
		return results[calls]
	}, clk.clock())
	if got != ProbeOK {
		t.Fatalf("result = %q, want ok", got)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestProbeWithBusyRetryExhaustsWindow(t *testing.T) {
	clk := newFakeClock()
	calls := 0
	got := probeWithBusyRetry(context.Background(), func(context.Context) ProbeAttempt {
		calls++
		return ProbeAttempt{Success: false, Stderr: busyStderr}
	}, clk.clock())
	if got != ProbeBusy {
		t.Fatalf("result = %q, want busy", got)
	}
	if calls < 2 {
		t.Fatalf("calls = %d, want retries within the window", calls)
	}
	if sleeps := clk.recorded(); len(sleeps) == 0 || sleeps[0] != 100*time.Millisecond {
		t.Fatalf("sleeps = %v, want 100ms cadence", sleeps)
	}
}

func TestProbeWithBusyRetryNonBusyNoRetry(t *testing.T) {
	calls := 0
	got := probeWithBusyRetry(context.Background(), func(context.Context) ProbeAttempt {
		calls++
		return ProbeAttempt{Success: false, Stderr: "[hevc_v4l2m2m @ 0x7f8] Unknown option 'qp'"}
	}, newFakeClock().clock())
	if got != ProbeUnusable {
		t.Fatalf("result = %q, want unusable", got)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestProbeWithBusyRetryAbortedBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	got := probeWithBusyRetry(ctx, func(context.Context) ProbeAttempt {
		calls++
		return ProbeAttempt{Success: false, Stderr: busyStderr}
	}, newFakeClock().clock())
	if got != ProbeUnusable {
		t.Fatalf("result = %q, want unusable", got)
	}
	if calls != 0 {
		t.Fatalf("calls = %d, want 0", calls)
	}
}

func TestProbeWithBusyRetryAbortedDuringWait(t *testing.T) {
	clk := newFakeClock()
	ctx, cancel := context.WithCancel(context.Background())
	clk.onSleep = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}
	start := time.Now()
	calls := 0
	got := probeWithBusyRetry(ctx, func(context.Context) ProbeAttempt {
		calls++
		return ProbeAttempt{Success: false, Stderr: busyStderr}
	}, clk.clock())
	if got != ProbeUnusable {
		t.Fatalf("result = %q, want unusable", got)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("abort took %v; the 500ms window must be cut short", elapsed)
	}
}

// The window is measured from the FIRST busy result: a slow first
// attempt must not eat it.
func TestProbeWithBusyRetryWindowFromFirstBusy(t *testing.T) {
	clk := newFakeClock()
	calls := 0
	got := probeWithBusyRetry(context.Background(), func(context.Context) ProbeAttempt {
		calls++
		if calls == 1 {
			// A slow probe (device handshake etc.) that ends busy.
			clk.Advance(400 * time.Millisecond)
		}
		return ProbeAttempt{Success: false, Stderr: busyStderr}
	}, clk.clock())
	if got != ProbeBusy {
		t.Fatalf("result = %q, want busy", got)
	}
	// With the deadline at firstBusy+500ms there is room for several
	// retries; measuring from t0 would exhaust after ~1-2 calls.
	if calls < 4 {
		t.Fatalf("calls = %d, want >= 4", calls)
	}
}

func TestPipeStderr(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"split lines across reads", "a\nbc\n", []string{"a", "bc"}},
		{"final partial line", "done", []string{"done"}},
		{"empty lines skipped", "x\n\ny\n", []string{"x", "y"}},
		{"empty input", "", nil},
	}
	for _, c := range cases {
		var lines []string
		pipeStderr(strings.NewReader(c.input), "[test]",
			slog.New(slog.NewTextHandler(io.Discard, nil)), func(line string) {
				lines = append(lines, line)
			})
		if !slices.Equal(lines, c.want) {
			t.Errorf("%s: lines = %q, want %q", c.name, lines, c.want)
		}
	}
}

func TestBuildProbeArgs(t *testing.T) {
	want := []string{
		"-hide_banner", "-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=1440x1080:rate=30:duration=0.34",
		"-vf", "yadif=mode=0:parity=-1:deint=1,scale=-2:720",
		"-c:v", "libx264", "-preset", "superfast", "-tune", "zerolatency", "-crf", "27", "-g", "15", "-sc_threshold", "0",
		"-frames:v", "10", "-f", "null", "-",
	}
	if got := buildProbeArgs(EncoderLibx264, Quality720p, false); !slices.Equal(got, want) {
		t.Fatalf("buildProbeArgs = %q, want %q", got, want)
	}
}

func indexOf(args []string, key string) int {
	for i, a := range args {
		if a == key {
			return i
		}
	}
	panic("indexOf: " + key + " not in " + strings.Join(args, " "))
}
