// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
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
	// victim selects a rule id belonging to this tenant on EITHER axis, and it is applied to
	// the engine and to the loop's buffers alike — see dropTenantBuffers for why the buffers
	// cannot use the id alone.
	//
	// 🔴 IT ASKS runtime.RuleTenant RATHER THAN SPELLING THE SEPARATOR HERE. The parse is
	// anchored by construction — the first segment of the id is the tenant, whole — which is
	// what rules out both ways a hand-written test goes wrong: a substring match takes
	// "other/acme@1/…" along with "acme/…", and a prefix with no separator takes "acme-2/…".
	// More to the point, "/" is that package's own unexported constant, and a copy of it here
	// would keep matching the old shape if it ever moved — an eviction that erases nothing
	// while reporting a number. The same question is already asked through RuleTenant twice
	// more in this changeset; this is the third caller, not a special case.
	victim := func(ruleID string) bool {
		if _, ok := byID[ruleID]; ok {
			return true
		}
		idTenant, ok := runtime.RuleTenant(ruleID)
		return ok && idTenant == tenant
	}

	// 🔴 ONLY THE ENGINE'S COUNT MAY DIRTY THE CHECKPOINT, and separating it from the total
	// is not bookkeeping fussiness. The engine is the ONLY thing swept here that is
	// serialized: the registry is rebuilt from the durable rule projection, the armer's views
	// and the attribute view from their projections, and the dead-letter ring and the two
	// buffers are not persisted at all. Folding them into the dirty test would force a full
	// snapshot rewrite for state a restart discards anyway — and, against a store that cannot
	// commit, would report "a restart would restore what was removed" about state a restart
	// provably clears.
	n := int64(len(removed))
	if evicted := rp.engine.RemoveMatching(victim); evicted > 0 {
		rp.dirty = true
		n += int64(evicted)
	}

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
	n += rp.dropTenantBuffers(victim, tenant)

	// 🔴 A CLEAN ANSWER REQUIRES A CLEAN CHECKPOINT, NOT A CLEAN ENGINE — including when this
	// pass evicted nothing at all.
	//
	// The tempting shortcut is "n == 0, so there was nothing to erase, so answer clean". It
	// is wrong, and the way it is wrong is this store's whole failure mode. An earlier pass
	// can have swept the tenant out of MEMORY and then failed to commit — that path exists a
	// few lines below and says so. The engine is then clean, `dirty` is still set, and the
	// committed blob still holds every one of the tenant's windows and buffered values. A
	// second pass moments later finds nothing, answers clean, and the coordinator starts its
	// settle window over data that is still on disk. The purge COMPLETES; a later restart
	// restores the tenant's state from the stale blob, and no pass remains that would ever
	// evict it again.
	//
	// So the condition is `dirty`, not `n`. While it is set, memory and disk may disagree,
	// and this process cannot say anything about what the snapshot holds until they agree.
	// When it is clear, memory IS the snapshot's content (a restore loads the whole payload),
	// and only then does an engine holding none of the tenant mean a snapshot holding none.
	if !rp.dirty {
		return tenantPurgeResult{evicted: n}
	}
	if !rp.checkpoint(rp.procCtx) {
		return tenantPurgeResult{evicted: n, err: errors.New("the DETECT engine could not " +
			"commit a checkpoint, so it cannot establish that its durable snapshot is free of " +
			"this tenant — anything evicted from memory would be restored by a restart")}
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
//
// 🔴 IT TAKES THE SAME PREDICATE THE ENGINE SWEEP USES, not a fresh prefix test, and the
// difference is a residue nothing can ever clear. A detection from a rule this tenant OWNS
// but whose id was minted under another's prefix passes a prefix test and stays buffered.
// Its rule is gone from the registry by then, so the next publish dead-letters it as an
// orphan — and an orphan dead letter records no tenant fields at all, so no later
// Publisher.RemoveTenant, for this tenant or any other, can select it. The tenant's device
// token then sits in an operator-readable diagnostic permanently, after the deletion record
// has said it is gone. The recheck sets are keyed on an explicit tenant field, so they stay
// an equality test.
func (rp *ResolvedEventsProcessor) dropTenantBuffers(victim func(ruleID string) bool,
	tenant string) int64 {
	n := int64(0)
	if len(rp.pendingDets) > 0 {
		kept := rp.pendingDets[:0]
		for _, d := range rp.pendingDets {
			if victim(d.RuleID) {
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
	// 🔴 HANDLED INLINE, NOT ON A GOROUTINE PER REQUEST, and the serial dispatch that costs
	// is the bound this needs rather than a limitation to work around.
	//
	// An earlier draft spawned one, copying the auth callout — where concurrency is the whole
	// point, because device connects arrive in storms and each is an independent credential
	// lookup. Nothing here is independent. Every request contends for the SAME single-writer
	// loop, and each one that reaches it runs a sweep over the whole instance's state plus a
	// synchronous snapshot and database commit, with detection stopped for every tenant
	// meanwhile. Spawning let anything that can reach this subject turn a burst of publishes
	// into that many live goroutines and that many serial checkpoints.
	//
	// One is all that is ever legitimately in flight: the coordinator holds an instance-wide
	// advisory lock for the whole of a purge pass. So nats.go's serial per-subscription
	// dispatch is exactly the right shape — a second request waits for the first, which is
	// what it would do anyway at the channel.
	sub, err := r.conn.Subscribe(subject, r.handle)
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
