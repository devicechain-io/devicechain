// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"sync"
	"time"
)

// bucket is a token bucket: rate tokens a second, at most burst held. reserve always takes a token
// and says how long the caller must wait before using it, so a caller that waits is queued behind
// the ones before it instead of racing them.
type bucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newBucket(rate float64, burst int, now time.Time) *bucket {
	return &bucket{rate: rate, burst: float64(burst), tokens: float64(burst), last: now}
}

// reserve takes one token at now and returns how long to wait before the command may go.
func (b *bucket) reserve(now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += elapsed.Seconds() * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.last = now
	}
	b.tokens--
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(-b.tokens / b.rate * float64(time.Second))
}
