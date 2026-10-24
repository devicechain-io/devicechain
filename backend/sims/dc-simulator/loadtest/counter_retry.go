// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// The count read-back is the one call a 60-minute run cannot afford to lose: it comes after
// the whole drive, over millions of rows, and a single timed-out COUNT used to end the run
// with no verdict (the G3 release-candidate run lost its MQTT verdict that way). A timed-out
// count says the database was busy, not that events are missing, so the read is retried with
// a bounded backoff before the run is given up as inconclusive.
const (
	// countAttempts is how many times one count read is tried before it is an error.
	countAttempts = 5
	// countBackoff is the wait before the second attempt; it doubles up to countBackoffMax.
	countBackoff    = 5 * time.Second
	countBackoffMax = 60 * time.Second
)

// retryingCounter retries a failed Count with a bounded, doubling backoff. It retries
// ONLY errors: a count that succeeds is returned as read, so the retry can never turn a short
// count into a long one. The last error is returned wrapped once the attempts are spent, and
// the caller still treats that as "no verdict".
type retryingCounter struct {
	inner      eventCounter
	attempts   int
	backoff    time.Duration
	maxBackoff time.Duration
	// sleep waits d or until ctx ends; tests replace it so no wall clock passes.
	sleep func(ctx context.Context, d time.Duration) error
}

// newRetryingCounter wraps c with the run-level retry policy.
func newRetryingCounter(c eventCounter) *retryingCounter {
	return &retryingCounter{inner: c, attempts: countAttempts, backoff: countBackoff, maxBackoff: countBackoffMax}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func (r *retryingCounter) Count(ctx context.Context, w Window) (int64, error) {
	sleep := r.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	wait := r.backoff
	var err error
	for attempt := 1; attempt <= r.attempts; attempt++ {
		var n int64
		n, err = r.inner.Count(ctx, w)
		if err == nil {
			return n, nil
		}
		// A cancelled run is not a busy database: stop at once, with the real cause.
		if ctx.Err() != nil {
			return 0, err
		}
		if attempt == r.attempts {
			break
		}
		log.Warn().Err(err).Int("attempt", attempt).Int("of", r.attempts).Dur("retryIn", wait).
			Msg("count read-back failed; retrying")
		if serr := sleep(ctx, wait); serr != nil {
			return 0, err
		}
		wait = min(wait*2, r.maxBackoff)
	}
	return 0, fmt.Errorf("count read-back failed %d times, the last error: %w", r.attempts, err)
}
