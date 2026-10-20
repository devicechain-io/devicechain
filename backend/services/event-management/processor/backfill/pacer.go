// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"context"
	"time"
)

// pacer spends a budget of rows per second, so a backfill reading and writing history cannot
// starve the live write path that shares the event store. It is a token bucket with a burst of
// one second's budget: a worker that has been idle may do a second's work at once, and then
// runs at the rate.
//
// It is used by ONE goroutine (the worker holds an instance-wide lock), so it takes no lock.
type pacer struct {
	rate   float64 // rows per second
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) error
}

func newPacer(rowsPerSecond int) *pacer {
	p := &pacer{rate: float64(rowsPerSecond), burst: float64(rowsPerSecond), now: time.Now, sleep: sleepCtx}
	p.tokens = p.burst
	p.last = p.now()
	return p
}

// wait spends n rows of budget, blocking until the bucket can pay. It returns the context's
// error if that ends first. A spend larger than the burst waits for the deficit rather than
// being refused: the rate holds on average.
func (p *pacer) wait(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	now := p.now()
	p.tokens = min(p.burst, p.tokens+now.Sub(p.last).Seconds()*p.rate)
	p.last = now
	p.tokens -= float64(n)
	if p.tokens >= 0 {
		return ctx.Err()
	}
	d := time.Duration(-p.tokens / p.rate * float64(time.Second))
	return p.sleep(ctx, d)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
