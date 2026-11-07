// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-edge-agent/config"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// RETIRE ON ACCEPTANCE.
//
// A spool entry is the only copy of an event the box holds, so what retires it decides what a
// failure can lose. A PUBACK says the broker took the message and nothing more (MQTT 3.1.1
// cannot refuse a publish): the platform can still shed the event at the tenant's ingest
// ceiling, or fail to store it. An agent that retires on PUBACK has deleted the event by then.
//
// With uplink.retireOn "accept" the entry stays in the spool, un-acked, after its PUBACK, and
// leaves it in exactly one of three ways:
//
//   - the platform sends an acceptance naming its idempotency key (handleAccept): durably
//     held on a platform stream, so the entry is acked;
//   - the platform sends a terminal refusal (handleNotice): the event is in the dead-letter
//     file, so the entry is acked;
//   - neither happens within the accept timeout: the spool's own redelivery offers it again,
//     with the same key, and the platform suppresses a duplicate.
//
// A shed at the ceiling is the fourth answer and is NOT terminal: the entry is kept and offered
// again after a delay (handleOverCeiling), because the same event is acceptable once the tenant
// is back under its ceiling.

// defaultOverCeilingRetry is how long a shed event waits before it is offered again.
const defaultOverCeilingRetry = 30 * time.Second

func (a *Agent) retireOnAccept() bool { return a.cfg.Uplink.RetireOn == config.RetireOnAccept }

// pendingAccepts holds the spool messages published and not yet accepted, by idempotency key.
type pendingAccepts struct {
	mu sync.Mutex
	m  map[string]pendingEntry
}

type pendingEntry struct {
	msg *nats.Msg
	at  time.Time
}

func newPendingAccepts() *pendingAccepts { return &pendingAccepts{m: map[string]pendingEntry{}} }

// put holds msg under altId. A redelivery of the same event replaces the earlier delivery.
func (p *pendingAccepts) put(altId string, msg *nats.Msg) {
	if altId == "" {
		return
	}
	p.mu.Lock()
	p.m[altId] = pendingEntry{msg: msg, at: time.Now()}
	p.mu.Unlock()
}

func (p *pendingAccepts) take(altId string) (*nats.Msg, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.m[altId]
	if ok {
		delete(p.m, altId)
	}
	return e.msg, ok
}

func (p *pendingAccepts) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.m)
}

// expire forgets entries held longer than olderThan and returns how many. Their messages are
// redelivered by the spool on its own (the ack wait is at least the accept timeout), and the
// redelivery holds them again; forgetting only bounds memory.
func (p *pendingAccepts) expire(olderThan time.Duration) int {
	cutoff := time.Now().Add(-olderThan)
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for k, e := range p.m {
		if e.at.Before(cutoff) {
			delete(p.m, k)
			n++
		}
	}
	return n
}

// handleAccept retires the spool entries the platform says it durably accepted.
func (a *Agent) handleAccept(altIds []string) {
	for _, id := range altIds {
		// The local copy kept for a late refusal is no longer needed: an accepted event is not
		// refused afterwards.
		a.sent.take(id)
		if msg, ok := a.pending.take(id); ok {
			a.ackAndCount(msg)
			a.accepted.Add(1)
		}
	}
}

// retirePending acks the spool entry for altId, if it is held in accept mode.
func (a *Agent) retirePending(altId string) {
	if !a.retireOnAccept() {
		return
	}
	if msg, ok := a.pending.take(altId); ok {
		a.ackAndCount(msg)
	}
}

func (a *Agent) overCeilingRetryDelay() time.Duration {
	if a.overCeilingRetry > 0 {
		return a.overCeilingRetry
	}
	return defaultOverCeilingRetry
}

// handleOverCeiling is the answer to an event the platform shed at the tenant's ingest ceiling.
// It is counted (a refusal is never silent) and the event stays in the spool, offered again
// after a delay: nothing is dead-lettered, because the event is not bad and the spool still
// holds it.
func (a *Agent) handleOverCeiling(n messaging.GatewayNotice) {
	a.overCeiling.Add(1)
	a.refused.Add(1)
	if a.metrics != nil {
		a.metrics.refused.WithLabelValues(messaging.GatewayNoticeReasonOverCeiling).Inc()
	}
	if a.refusalLog.allow("refused:"+messaging.GatewayNoticeReasonOverCeiling+":"+n.Device, refusalLogEvery) {
		a.log.Warn("the platform shed an event at the tenant's ingest ceiling: keeping it in the spool and offering it again",
			"device", n.Device, "retry_in", a.overCeilingRetryDelay())
	}
	if msg, ok := a.pending.take(n.AltId); ok {
		if err := msg.NakWithDelay(a.overCeilingRetryDelay()); err != nil {
			// The entry is still in the spool; the ack wait offers it again.
			a.log.Warn("could not schedule a shed event for retry; the spool will offer it again at its ack wait", "err", err)
		}
	}
}

// expirePending drops pending entries old enough that their redelivery has come and gone.
func (a *Agent) expirePending() {
	if !a.retireOnAccept() {
		return
	}
	if n := a.pending.expire(2 * a.ackWait()); n > 0 {
		a.unaccepted.Add(int64(n))
		a.log.Warn("events were not accepted by the platform within the accept timeout and are being offered again",
			"count", n, "accept_timeout_seconds", a.cfg.Uplink.AcceptTimeoutSeconds)
	}
}
