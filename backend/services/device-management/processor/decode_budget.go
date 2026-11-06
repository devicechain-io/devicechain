// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"sync"
	"time"
)

// THE BUDGETS OF A FAILING DECODER.
//
// A decoder that fails fails on every uplink it sees. At 20k uplinks a second one broken decoder of
// a large tenant would write 20k failed-events records a second to a platform-wide stream, and the
// failed-events index's own flood gate only acts after that stream write. These two budgets sit in
// front of it, per replica:
//
//   - the RECORD budget, per (tenant, device type): a token bucket of 20, refilled 10 a minute. A
//     failure inside it is a failed-events record; a failure past it is counted as suppressed and
//     its source is acked without a record;
//   - the SAMPLE budget, per (tenant, profile): one stored failing uplink every 10 seconds.
//
// Neither is ever a silent drop. The first 20 failures of a window are always recorded, so a broken
// decoder is visible before anything is suppressed, and every suppressed uplink is in the health
// tally.
const (
	// DecodeRecordBurst is the record bucket's capacity.
	DecodeRecordBurst = 20
	// DecodeRecordRefillPerMinute is how many records a bucket earns back each minute.
	DecodeRecordRefillPerMinute = 10
	// DecodeSampleInterval is the minimum time between two stored samples of one profile.
	DecodeSampleInterval = 10 * time.Second

	// maxBudgetKeys bounds the number of distinct keys either budget holds. Keys come from tenants
	// and device types, which a caller does not choose, but the bound is what keeps the map small if
	// that ever stops being true. Past it, a key with no bucket gets none: it is DENIED, which
	// suppresses (counted) rather than records, so exhausting the map can only quiet the stream.
	maxBudgetKeys = 50_000
)

type recordKey struct {
	tenant     string
	deviceType uint
}

type bucket struct {
	tokens float64
	at     time.Time
}

// decodeBudget is the pair of budgets. The zero value is not usable; use newDecodeBudget.
type decodeBudget struct {
	now func() time.Time

	mu      sync.Mutex
	records map[recordKey]*bucket
	samples map[string]time.Time // "tenant\x00profile" -> last stored
	swept   time.Time
}

func newDecodeBudget(now func() time.Time) *decodeBudget {
	if now == nil {
		now = time.Now
	}
	return &decodeBudget{now: now, records: map[recordKey]*bucket{}, samples: map[string]time.Time{}}
}

// AllowRecord takes one token from the (tenant, device type) bucket and reports whether there was
// one. A bucket starts full.
func (b *decodeBudget) AllowRecord(tenant string, deviceType uint) bool {
	now := b.now()
	k := recordKey{tenant, deviceType}
	b.mu.Lock()
	defer b.mu.Unlock()
	bk, ok := b.records[k]
	if !ok {
		b.sweep(now)
		if len(b.records) >= maxBudgetKeys {
			return false
		}
		bk = &bucket{tokens: DecodeRecordBurst, at: now}
		b.records[k] = bk
	} else if el := now.Sub(bk.at); el > 0 {
		bk.tokens = min(DecodeRecordBurst, bk.tokens+el.Minutes()*DecodeRecordRefillPerMinute)
		bk.at = now
	}
	if bk.tokens < 1 {
		return false
	}
	bk.tokens--
	return true
}

// AllowSample reports whether the profile may store a sample now, and if so starts its interval.
func (b *decodeBudget) AllowSample(tenant, profile string) bool {
	now := b.now()
	k := tenant + "\x00" + profile
	b.mu.Lock()
	defer b.mu.Unlock()
	last, ok := b.samples[k]
	if ok && now.Sub(last) < DecodeSampleInterval {
		return false
	}
	if !ok {
		b.sweep(now)
		if len(b.samples) >= maxBudgetKeys {
			return false
		}
	}
	b.samples[k] = now
	return true
}

// sweep forgets what has recovered: a bucket that would be full again and a sample interval that
// has passed hold nothing a fresh key would not. At most once a minute, and only when the maps are
// big enough to matter. Called with mu held.
func (b *decodeBudget) sweep(now time.Time) {
	if len(b.records)+len(b.samples) < 1024 || now.Sub(b.swept) < time.Minute {
		return
	}
	b.swept = now
	full := time.Duration(float64(time.Minute) * DecodeRecordBurst / DecodeRecordRefillPerMinute)
	for k, bk := range b.records {
		if now.Sub(bk.at) >= full {
			delete(b.records, k)
		}
	}
	for k, t := range b.samples {
		if now.Sub(t) >= DecodeSampleInterval {
			delete(b.samples, k)
		}
	}
}
