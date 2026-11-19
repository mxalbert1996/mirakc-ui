package transcode

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"testing"
	"time"
)

// Real-ffmpeg probe tests, guarded by an exec.LookPath check so
// environments without ffmpeg (CI) skip.

func realProbeHandler() *handler {
	h := newHandler(Deps{}, realClock())
	h.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return h
}

func TestRealProbeLibx264OK(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	h := realProbeHandler()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if res := h.probeVideoEncoder(ctx, EncoderLibx264, Quality1080p); res != ProbeOK {
		t.Fatalf("libx264 probe = %q, want ok", res)
	}
}

func TestRealProbeUnknownEncoderUnusable(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	h := realProbeHandler()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if res := h.probeVideoEncoder(ctx, EncoderName("bogus_encoder_zzz"), Quality1080p); res != ProbeUnusable {
		t.Fatalf("bogus encoder probe = %q, want unusable", res)
	}
}
