// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// GatewayFeedback is what the capture reader tells a gateway about each event it took from
// the gateway's namespace: ACCEPTED (durably stored on a platform stream, so the gateway may
// retire it) or REFUSED (a notice, with a reason). Acceptances are batched per gateway, so a
// gateway publishing thousands of events a second costs the broker tens of small messages a
// second; a refusal is sent at once.
//
// Everything here is best effort by design (see GatewayAccept): a failure to send is counted
// and logged, never allowed to hold up or fail the ingest path it is called from, and the cost
// of a lost message is a resend the idempotency key suppresses.
type GatewayFeedback struct {
	pub *GatewayNoticePublisher

	mu      sync.Mutex
	pending map[gatewayKey][]string
	total   int

	flushEvery time.Duration
	batchMax   int
	pendingMax int

	sent    atomic.Int64 // acceptances handed to the broker
	dropped atomic.Int64 // ids not sent: over pendingMax, or a failed publish
}

type gatewayKey struct{ tenant, gateway string }

// Default batching bounds: a batch is sent when it reaches gatewayAcceptBatchMax ids or every
// gatewayAcceptFlushEvery, whichever is first; gatewayAcceptPendingMax bounds what is held
// between flushes across every gateway, so a stalled broker cannot grow memory.
const (
	gatewayAcceptBatchMax   = 256
	gatewayAcceptFlushEvery = 50 * time.Millisecond
	gatewayAcceptPendingMax = 65536
)

// NewGatewayFeedback builds the feedback over a notice publisher. Run must be started for
// acceptances to flush on the clock.
func NewGatewayFeedback(pub *GatewayNoticePublisher) *GatewayFeedback {
	return &GatewayFeedback{
		pub:        pub,
		pending:    map[gatewayKey][]string{},
		flushEvery: gatewayAcceptFlushEvery,
		batchMax:   gatewayAcceptBatchMax,
		pendingMax: gatewayAcceptPendingMax,
	}
}

// Accepted records that the event keyed altId, from gateway, is durably accepted.
func (f *GatewayFeedback) Accepted(tenant, gateway, altId string) {
	if f == nil || altId == "" {
		return
	}
	k := gatewayKey{tenant, gateway}
	f.mu.Lock()
	if f.total >= f.pendingMax {
		f.mu.Unlock()
		f.dropped.Add(1)
		return
	}
	ids := append(f.pending[k], altId)
	f.total++
	var full []string
	if len(ids) >= f.batchMax {
		full, ids = ids, nil
		f.total -= len(full)
	}
	if ids == nil {
		delete(f.pending, k)
	} else {
		f.pending[k] = ids
	}
	f.mu.Unlock()
	if full != nil {
		f.send(k, full)
	}
}

// Refused sends a refusal notice at once.
func (f *GatewayFeedback) Refused(tenant, gateway string, n GatewayNotice) {
	if f == nil {
		return
	}
	if err := f.pub.Notice(tenant, gateway, n); err != nil {
		f.dropped.Add(1)
		log.Debug().Err(err).Str("tenant", tenant).Str("gateway", gateway).Msg("Could not send a gateway notice.")
	}
}

// Flush sends everything held.
func (f *GatewayFeedback) Flush() {
	if f == nil {
		return
	}
	f.mu.Lock()
	held := f.pending
	f.pending = map[gatewayKey][]string{}
	f.total = 0
	f.mu.Unlock()
	for k, ids := range held {
		f.send(k, ids)
	}
}

func (f *GatewayFeedback) send(k gatewayKey, ids []string) {
	if err := f.pub.Accept(k.tenant, k.gateway, ids); err != nil {
		f.dropped.Add(int64(len(ids)))
		log.Debug().Err(err).Str("tenant", k.tenant).Str("gateway", k.gateway).Msg("Could not send a gateway acceptance.")
		return
	}
	f.sent.Add(int64(len(ids)))
}

// Run flushes on the clock until ctx ends, then once more.
func (f *GatewayFeedback) Run(ctx context.Context) {
	t := time.NewTicker(f.flushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			f.Flush()
			return
		case <-t.C:
			f.Flush()
		}
	}
}

// Sent is the number of acceptances handed to the broker; Dropped the number not sent.
func (f *GatewayFeedback) Sent() int64    { return f.sent.Load() }
func (f *GatewayFeedback) Dropped() int64 { return f.dropped.Load() }
