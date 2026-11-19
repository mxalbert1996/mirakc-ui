package transcode

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"time"
)

// realProbe runs one probe attempt against the real ffmpeg (the Deno
// runProbe). Timeout and request abort SIGKILL immediately: a probe for
// a disconnected request must not hold the V4L2 device, or the next
// attempt would hit EBUSY.
func (h *handler) realProbe(ctx context.Context, encoder EncoderName, args []string) (string, error) {
	cmd, err := realSpawn("ffmpeg", args)
	if err != nil {
		if ctx.Err() == nil {
			// Keep the cause (e.g. ffmpeg missing); aborts are expected.
			h.log.Error(fmt.Sprintf("[encoder-probe %s] probe failed: %v", encoder, err))
		}
		return "", err
	}
	kill := time.AfterFunc(probeTimeout, func() { _ = cmd.Signal(syscall.SIGKILL) })
	defer kill.Stop()
	abortWatch := make(chan struct{})
	defer close(abortWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Signal(syscall.SIGKILL)
		case <-abortWatch:
		}
	}()

	var stderr strings.Builder
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		pipeStderr(cmd.Stderr(), fmt.Sprintf("[encoder-probe %s]", encoder), h.log, func(line string) {
			stderr.WriteString(line + "\n")
		})
		_ = cmd.Stderr().Close()
	}()
	_, waitErr := cmd.Wait()
	<-pumped
	// The probe uses neither stdin nor stdout; release their pipe ends.
	_ = cmd.Stdin().Close()
	_ = cmd.Stdout().Close()
	return stderr.String(), waitErr
}

// probeVideoEncoder verifies an encoder by actually encoding a few
// frames through the same filter and args as the live pipeline. Takes
// ~80ms HW / ~40ms x264 at 1080p on the reference hardware.
func (h *handler) probeVideoEncoder(ctx context.Context, encoder EncoderName, quality Quality) ProbeResult {
	args := buildProbeArgs(encoder, quality, h.v4l2Cq)
	return probeWithBusyRetry(ctx, func(context.Context) ProbeAttempt {
		stderr, err := h.probe(ctx, encoder, args)
		return ProbeAttempt{Success: err == nil, Stderr: stderr}
	}, h.clk)
}
