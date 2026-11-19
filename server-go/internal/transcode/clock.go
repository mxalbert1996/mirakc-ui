package transcode

import (
	"context"
	"time"
)

// clock is the time seam: the upstream retry window, its backoff
// cadence and the probe busy-retry window become deterministic under
// tests. Use realClock in production.
type clock struct {
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

func realClock() clock {
	return clock{
		Now: time.Now,
		Sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
}
