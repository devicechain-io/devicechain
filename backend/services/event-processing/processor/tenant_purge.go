// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// Evicting one tenant from the running DETECT engine (ADR-077 slice 2c).
//
// The engine is the only storage system in the platform whose contents no query can
// reach. Its windows, timers, accumulators and dead-man armings live in this process's
// memory, keyed by rule ids that carry the tenant as a prefix, and are checkpointed as
// one opaque blob per partition holding every tenant at once. So the purge coordinator
// cannot erase this the way it erases a table or a stream; it has to ask, and this file
// is the ear.
//
// Two properties make the answer worth trusting, and both are the reason this is not
// simply a subscription that calls into the engine:
//
//   - The eviction runs ON THE SINGLE-WRITER LOOP, marshalled through a channel exactly
//     like the three fact-consumer paths. The engine has no locks by design; touching it
//     from a NATS callback goroutine would be a data race that testing would find only
//     under load.
//   - The reply is sent only after the checkpoint COMMITS. An eviction that lives in
//     memory is undone by the next restart, which replays the tenant's still-pending
//     events into a rule set rebuilt from a projection the purge had not yet swept. So a
//     reply that said "evicted" off an in-memory change would be a claim the very next
//     crash falsifies — and the coordinator would already have written it into the
//     deletion record.
//
// What this deliberately does NOT do is fence future events for the tenant. The engine
// keeps no purge state, and it does not need to: the coordinator purges the tenant's
// subjects from the broker in the same pass, so the events that could rebuild the state
// are deleted rather than filtered, and the device-plane gate has already stopped new
// ones being admitted. If any do arrive late, they rebuild state that the NEXT pass
// evicts — and because a pass that evicts anything restarts the coordinator's settle
// window, completion cannot happen until a full window has passed with nothing found.
// That is the same convergence argument every other store rests on, and it is why the
// engine needs no notion of a purge that is "in progress".

// tenantPurgeRequest is one eviction marshalled onto the single-writer loop.
type tenantPurgeRequest struct {
	tenant string
	// reply MUST be buffered with room for one value. The loop sends the result without
	// selecting on anything, so an unbuffered channel whose reader has already timed out
	// would wedge the entire DETECT loop — every tenant's detection stopped by one purge
	// request that gave up.
	reply chan tenantPurgeResult
}

// tenantPurgeResult is what the loop did.
type tenantPurgeResult struct {
	// evicted counts the state entries removed across the engine, the rule registry, the
	// dead-man armer and the attribute view.
	evicted int64
	// err is non-nil when the eviction could not be made durable. The entries are still
	// gone from memory, but that is not what was asked: the caller is entitled to treat
	// anything short of a committed checkpoint as data still held.
	err error
}

// EvictTenant removes every trace of one tenant from this partition's DETECT engine and
// commits the result, returning how many state entries went.
//
// It is safe to call from any goroutine — the work is marshalled onto the single-writer
// loop — and it is idempotent: a second call finds nothing and returns zero, which is
// what the purge coordinator's settle window is waiting to observe.
func (rp *ResolvedEventsProcessor) EvictTenant(ctx context.Context, tenant string) (int64, error) {
	// 🔴 VALIDATED AGAIN HERE, having already been validated by the caller that built the
	// request. This is the side that holds the data, and the check is what stands between a
	// malformed token and a prefix match that reaches other tenants' state: eviction matches
	// rule ids under "{tenant}/", so an empty token or one carrying "/" selects far more
	// than it names. A responder that trusts the sender is trusting anything that can reach
	// the subject.
	if err := core.ValidateToken(tenant); err != nil {
		return 0, fmt.Errorf("refusing to evict %q from the DETECT engine: %w", tenant, err)
	}
	req := tenantPurgeRequest{tenant: tenant, reply: make(chan tenantPurgeResult, 1)}
	select {
	case rp.tenantPurges <- req:
	case <-ctx.Done():
		return 0, fmt.Errorf("the DETECT loop did not accept the eviction of %q: %w", tenant, ctx.Err())
	case <-rp.procCtx.Done():
		return 0, fmt.Errorf("the DETECT loop is shutting down, so %q was not evicted", tenant)
	}
	select {
	case res := <-req.reply:
		return res.evicted, res.err
	case <-ctx.Done():
		return 0, fmt.Errorf("the DETECT loop did not answer the eviction of %q: %w", tenant, ctx.Err())
	case <-rp.procCtx.Done():
		return 0, fmt.Errorf("the DETECT loop shut down mid-eviction of %q", tenant)
	}
}

// applyTenantPurge evicts a tenant on the single-writer loop and commits the result.
//
// The ORDER below is not arbitrary. The registry is swept first so it can report which
// rule ids it dropped, and the engine sweep is then given a predicate that covers both
// those ids AND anything else carrying the tenant's prefix. Sweeping the engine by prefix
// alone would leave state belonging to a rule the registry filed under this tenant with a
// mis-minted id; sweeping only the registry's ids would leave state that outlived its own
// rule, which the engine holds by design (a restored dead-man arming for a rule the
// rebuilt rule set no longer contains).
func (rp *ResolvedEventsProcessor) applyTenantPurge(tenant string) tenantPurgeResult {
	if rp.stale {
		// A halted split-brain writer. Its engine is no longer the durable truth for this
		// partition and its checkpoint would be refused, so it must not answer for one.
		return tenantPurgeResult{err: errors.New("this DETECT writer is halted as a stale " +
			"split-brain loser; it cannot commit an eviction for this partition")}
	}

	removed := rp.registry.RemoveTenant(tenant)
	byID := make(map[string]struct{}, len(removed))
	for _, id := range removed {
		byID[id] = struct{}{}
	}
	prefix := tenant + "/"
	n := int64(rp.engine.RemoveMatching(func(ruleID string) bool {
		if _, ok := byID[ruleID]; ok {
			return true
		}
		// 🔴 HasPrefix on "{tenant}/", never Contains and never a bare token compare. A
		// substring test would evict "other-tenant/acme/…" along with "acme/…"; a prefix with
		// no separator would evict "acme-2/…" along with "acme/…". Both leave one tenant's
		// detection silently dead because another was deleted.
		return strings.HasPrefix(ruleID, prefix)
	}))
	n += int64(len(removed))

	if rp.armer != nil {
		n += int64(rp.armer.RemoveTenant(tenant))
	}
	if rp.attrView != nil {
		n += int64(rp.attrView.RemoveTenant(tenant))
	}
	if rp.publisher != nil {
		// The dead-letter ring is the one thing swept here that the loop does not own — it is
		// mutex-guarded and shared with the publish path — and the one whose contents are pure
		// diagnostics. It is still the tenant's rule ids and device tokens, readable by an
		// operator after the deletion record says they are gone.
		n += int64(rp.publisher.RemoveTenant(tenant))
	}
	n += rp.dropTenantBuffers(tenant)

	if n == 0 {
		// Nothing was held, so nothing needs committing: the engine's state IS the durable
		// snapshot's content (a restore loads the whole payload), so an engine holding none
		// of the tenant is a snapshot holding none of it either.
		return tenantPurgeResult{}
	}
	// The eviction changed serializable state, so it MUST be checkpointed — the same reason
	// applyRuleUpdate marks a rule GC dirty, and here with a caller waiting on the answer.
	rp.dirty = true
	if !rp.checkpoint(rp.procCtx) {
		return tenantPurgeResult{evicted: n, err: errors.New("the DETECT engine evicted the " +
			"tenant in memory but could not commit the checkpoint, so the eviction is not yet " +
			"durable and a restart would restore what was removed")}
	}
	log.Info().Str("tenant", tenant).Str("partition", rp.cfg.PartitionId).Int64("entries", n).
		Msg("Evicted a purged tenant from the DETECT engine and committed the checkpoint.")
	return tenantPurgeResult{evicted: n}
}

// dropTenantBuffers clears the loop-owned buffers that are keyed on a tenant but are not
// engine state: undelivered detections and the two pending-recheck sets.
//
// The detections matter most and are not merely hygiene. A buffered detection is
// published to its tenant's own subject on the next checkpoint, so leaving one would emit
// an alarm for a tenant whose erasure has just been reported — into a subject tree the
// broker purge has already swept. The recheck sets are smaller but are still the tenant's
// device tokens sitting in memory, and a retained entry would re-read a projection whose
// rows are being deleted underneath it.
func (rp *ResolvedEventsProcessor) dropTenantBuffers(tenant string) int64 {
	n := int64(0)
	prefix := tenant + "/"
	if len(rp.pendingDets) > 0 {
		kept := rp.pendingDets[:0]
		for _, d := range rp.pendingDets {
			if strings.HasPrefix(d.RuleID, prefix) {
				n++
				continue
			}
			kept = append(kept, d)
		}
		rp.pendingDets = kept
	}
	for k := range rp.armRetries {
		if k.tenant == tenant {
			delete(rp.armRetries, k)
			n++
		}
	}
	for k := range rp.attrRetries {
		if k.tenant == tenant {
			delete(rp.attrRetries, k)
			n++
		}
	}
	return n
}

// TenantPurgeResponder answers the purge coordinator's eviction requests over NATS.
//
// It is a plain core-NATS responder rather than a stream consumer, and that is the right
// shape for what it is: the caller needs an ANSWER, and a durable fact would be worse than
// useless here — the coordinator purges the tenant's own subjects in the same pass, so a
// per-tenant fact carrying the eviction request would be deleted by the very purge it was
// driving.
type TenantPurgeResponder struct {
	conn       *nats.Conn
	instanceId string
	partition  string
	evict      func(ctx context.Context, tenant string) (int64, error)
	sub        *nats.Subscription
}

// NewTenantPurgeResponder wires a responder to a running processor.
func NewTenantPurgeResponder(conn *nats.Conn, instanceId string,
	rp *ResolvedEventsProcessor) *TenantPurgeResponder {
	return &TenantPurgeResponder{
		conn:       conn,
		instanceId: instanceId,
		partition:  rp.cfg.PartitionId,
		evict:      rp.EvictTenant,
	}
}

// Start subscribes to this instance's eviction subject.
//
// A plain Subscribe, NOT a queue group: every partition must answer for itself. A queue
// group would deliver each request to exactly one subscriber, which on a sharded fleet
// would evict one partition and let the caller believe the whole engine was clean — the
// failure being invisible precisely because the reply it did get looked complete.
func (r *TenantPurgeResponder) Start() error {
	subject := messaging.DetectPurgeSubject(r.instanceId)
	sub, err := r.conn.Subscribe(subject, func(msg *nats.Msg) {
		// Handled off the callback goroutine: an eviction takes a database write, and
		// nats.go dispatches a subscription's callbacks serially, so doing it inline would
		// block the connection's delivery for this subject.
		go r.handle(msg)
	})
	if err != nil {
		return fmt.Errorf("subscribing to the DETECT tenant-eviction subject %q: %w", subject, err)
	}
	r.sub = sub
	log.Info().Str("subject", subject).Str("partition", r.partition).
		Msg("DETECT tenant-eviction responder subscribed.")
	return nil
}

// Stop tears down the subscription.
func (r *TenantPurgeResponder) Stop() error {
	if r.sub == nil {
		return nil
	}
	return r.sub.Unsubscribe()
}

// handle evicts one tenant and replies with what happened.
//
// It ALWAYS replies when it can, including on failure. Silence and "I hold nothing" are
// the same observation to a caller that only sees a timeout, and they are opposite facts:
// the whole reason the reply carries an error field is so a partition that could not
// commit is recorded as still holding the tenant instead of being counted as one that
// answered clean.
func (r *TenantPurgeResponder) handle(msg *nats.Msg) {
	if msg.Reply == "" {
		log.Warn().Msg("Ignoring a DETECT tenant-eviction request with no reply subject.")
		return
	}
	reply := messaging.DetectPurgeReply{PartitionId: r.partition}

	var req messaging.DetectPurgeRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		reply.Error = fmt.Sprintf("could not decode the eviction request: %v", err)
	} else {
		// Bounded by the caller's own gather window: past it the answer is unwanted, and an
		// unbounded wait would leak one goroutine per pass against a wedged loop.
		ctx, cancel := context.WithTimeout(context.Background(), messaging.DetectPurgeWindow)
		evicted, err := r.evict(ctx, req.Tenant)
		cancel()
		reply.Evicted = evicted
		if err != nil {
			reply.Error = err.Error()
			log.Warn().Err(err).Str("tenant", req.Tenant).Str("partition", r.partition).
				Msg("DETECT tenant eviction did not complete; reporting the tenant as still held.")
		}
	}

	body, err := json.Marshal(reply)
	if err != nil {
		log.Error().Err(err).Msg("Could not encode a DETECT tenant-eviction reply.")
		return
	}
	if err := r.conn.Publish(msg.Reply, body); err != nil {
		log.Warn().Err(err).Msg("Could not send a DETECT tenant-eviction reply; the caller will " +
			"record this partition as not having answered.")
	}
}
