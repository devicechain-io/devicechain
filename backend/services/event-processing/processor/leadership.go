// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// A DETECT replica may only fetch from its durables, ACK a fact, write the
// checkpoint or publish a derived event while it holds the partition lease. This
// file is what makes that true; ADR-070 is the design.
//
// WHAT WAS ACTUALLY BROKEN. The lease already existed and nothing consulted it.
// Its TTL is 30s and the consumers' AckWait is 60s, so a replica that lost the
// lease mid-batch still had messages in hand: the new leader restored from the
// snapshot, replayed to head and moved on, and 60s later the old replica's
// unacked batch redelivered — to an engine that had already advanced past it.
// applyResolved discards it as a duplicate and the next checkpoint ACKS it. The
// event is then gone from a stream nobody will re-read. The code that describes
// this failure has been sitting in checkpoint()'s neighbourhood unguarded.
//
// THE SHAPE OF A TERM:
//
//	acquire → watch → keepalive → WAIT out the old owner → build → run
//
// where the build binds the durables, restores the snapshot, catches the fact
// projections up, builds the three views and replays to head.
//
// The WAIT is the part that is easy to leave out and impossible to add later
// without an outage. A successful Acquire proves the SERVER expired the previous
// entry; it does not prove the previous OWNER has noticed. That replica may still
// be inside a fetch, a persist or a Save. So we wait out the worst case before
// restore reads the snapshot — see termSlack.

// termSlack is how long a fresh leader waits, after acquiring the lease and before
// reading any state, for the previous owner to stop writing.
//
// 🔴 EVERY INPUT HERE IS SECONDS, NOT MILLISECONDS, AND THAT IS THE WHOLE POINT.
// An earlier draft of this design budgeted "one round trip" and was wrong by three
// orders of magnitude. The bound is the sum of:
//
//   - the KeepAlive/Renew reply latency, ≤ 5s. This is the JetStream API timeout,
//     not a network RTT: the connection takes no request-wait option, so a reply
//     that lands at 4.9s stamps the old owner's validity window 4.9s after the
//     server had already refreshed its entry, and the window overshoots by that
//     much.
//   - a fetch long-poll, ~1s + RTT: a fetch the old owner issued just before its
//     window closed still lands.
//   - the old owner's window-end → term-cancel latency. A window that simply RUNS
//     OUT produces no watch event at all (a KV expiry is silent), so only the next
//     KeepAlive tick converts it into a cancel — and a Renew that is itself failing
//     can burn the 5s API timeout first. This is why renewInterval below is 1s
//     rather than the TTL/3 the other lease users take.
//   - the old owner's in-flight persist-and-ack, which is bounded only in practice.
//
// 20s is that sum with room, and it is spent ONCE per term on a path that already
// costs a snapshot restore plus a replay. It is deliberately not configurable:
// shortening it does not make handover faster, it makes handover wrong.
const termSlack = 20 * time.Second

// renewInterval is DETECT's own KeepAlive cadence, and it is a TENTH of what the
// other two lease users take (both pass DefaultLeaseTTL/3 = 10s).
//
// It is not a robustness dial — it is the third input to termSlack. With no
// successor, an expired window is invisible until KeepAlive's next tick, so the
// interval IS the latency between losing ownership and stopping. At the TTL/3 that
// DefaultLeaseTTL suggests that would be ~10s, larger than every other input
// combined. At 1s it collapses to the API timeout floor, and it costs one KV write
// per second on a bucket sized for exactly this.
const renewInterval = time.Second

// maxConsecutiveTermBuildFailures fuses a leader that holds the lease and cannot
// build a term — a Ready pod that detects nothing, holding the partition against
// every standby. Blowing the fuse ends the PROCESS (see failProcess), which is what
// lets the scheduler place a replacement; returning would leave the pod up.
//
// 🔴 IT COUNTS TERM BUILDS, AND ONLY TERM BUILDS. It must never be applied to
// Acquire: a failed Acquire is either the normal standby answer or a NATS outage,
// and fusing there turns a 25s broker blip into a pod exit — then into a
// CrashLoopBackOff, because the replacement meets the same outage. Acquire retries
// forever, deliberately.
const maxConsecutiveTermBuildFailures = 5

// acquireBackoff paces retries when the partition is held or the broker is down.
const (
	acquireBackoffMin = 500 * time.Millisecond
	acquireBackoffMax = 5 * time.Second
)

// newTermContext mints the context this term's loops and consumers select on, and
// publishes it for the tenant-purge responder to read.
//
// 🔴 A FRESH ONE PER TERM IS REQUIRED, NOT TIDY. catchUpFactProjections and every
// store call return immediately on a cancelled context, so a second term reusing
// the cancelled first one would race through its build doing nothing and then run a
// loop that reads nothing — reporting itself leader the whole way.
func (rp *ResolvedEventsProcessor) newTermContext() {
	rp.procMu.Lock()
	defer rp.procMu.Unlock()
	rp.procCtx, rp.procCancel = context.WithCancel(rp.supCtx)
}

// pctx returns the current term's context.
func (rp *ResolvedEventsProcessor) pctx() context.Context {
	rp.procMu.RLock()
	defer rp.procMu.RUnlock()
	return rp.procCtx
}

// pcancel ends the current term. It is nil-safe: the tenant-purge responder and the
// stale-checkpoint halt can both reach it before a term exists.
func (rp *ResolvedEventsProcessor) pcancel() {
	rp.procMu.RLock()
	cancel := rp.procCancel
	rp.procMu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

// TermGate carries the current leadership term's ownership signal from the
// processor to the readers it gates.
//
// 🔴 IT IS A SEPARATE OBJECT BECAUSE OF CONSTRUCTION ORDER, NOT INDIRECTION FOR ITS
// OWN SAKE. A reader's gate predicate is fixed when the reader is created, and the
// readers are created before the processor that will lead with them — so the
// predicate has to be late-bound to something that exists first. Sharing this one
// object is what lets a reader built at process start gate correctly on a term
// acquired minutes later, and on every term after that.
type TermGate struct {
	// held is the current term's ownership predicate, or nil between terms. It is a
	// predicate rather than the lease's own signal object so that the gate knows
	// nothing about the broker: what a reader needs to answer is "am I inside a term",
	// and the term decides what that means.
	held atomic.Pointer[func() bool]
}

// NewTermGate returns a gate that is CLOSED: no term has been entered, so a reader
// holding it consumes nothing. That is the safe initial state — the alternative
// would let a reader drain messages in the window between process start and the
// first acquisition.
func NewTermGate() *TermGate { return &TermGate{} }

// Held reports whether a leadership term is currently held. No term means false.
func (g *TermGate) Held() bool {
	f := g.held.Load()
	return f != nil && (*f)()
}

// Enter opens the gate on a term whose ownership the given predicate reports.
func (g *TermGate) Enter(held func() bool) { g.held.Store(&held) }

// Exit closes the gate. Every reader holding it parks at its next message.
func (g *TermGate) Exit() { g.held.Store(nil) }

// heldNow is the checkpoint path's view of the same question.
func (rp *ResolvedEventsProcessor) heldNow() bool {
	return rp.Gate != nil && rp.Gate.Held()
}

// leadershipEnabled reports whether this processor takes a lease. It is false on
// the scaffold and unit-test paths, which run the engine directly with no broker
// coordination; those behave exactly as they did before ADR-070.
func (rp *ResolvedEventsProcessor) leadershipEnabled() bool { return rp.Lease != nil && rp.Gate != nil }

// termHandle is one live leadership term: the lease, its ownership signal, the
// context every loop in the term selects on, and the renewer to join at teardown.
type termHandle struct {
	lease         *messaging.Lease
	holder        *messaging.Holder
	ctx           context.Context
	keepAliveDone <-chan struct{}
}

// resetForTerm clears everything a previous term may have left behind. This is the
// re-entry contract, and it exists because every field it touches was written for a
// component that started exactly once.
//
// 🔴 THE BUFFERS ARE THE DANGEROUS HALF. A term that ends while holding buffered
// acks and undelivered detections runs finalCheckpoint, and that checkpoint can be
// REFUSED — by a lost holder, by a broker that is down, by the very outage that
// ended the term. What is left behind is then a set of messages this replica no
// longer owns. Carrying them into the next term would ack, under a new ownership
// epoch, messages fetched under an old one — and would publish detections derived
// from a window the new term is about to replay for itself. They are dropped here,
// not flushed: the messages were never acked, so the successor (or this replica's
// own next term) redelivers and re-derives them.
//
// The channel drains are the same argument for the fact consumers: residue queued
// by the previous term's consumers describes a membership, rule set or fence set
// that the new term's catch-up and reconciles are about to read from the durable
// projections anyway, in their proper order.
func (rp *ResolvedEventsProcessor) resetForTerm() {
	rp.pendingAcks = rp.pendingAcks[:0]
	rp.pendingDets = rp.pendingDets[:0]
	rp.dirty = false
	rp.idleUncommitted = false
	drain(rp.ruleUpdates)
	drain(rp.armUpdates)
	drain(rp.attrUpdates)
	drain(rp.fenceUpdates)
	// These two are maps of rechecks the previous term queued but never completed.
	// They are cleared rather than drained, for the same reason: the new term's
	// reconciles read the same durable projections from scratch.
	clear(rp.armRetries)
	clear(rp.attrRetries)
}

// drain empties a buffered channel without blocking. The loop that would have
// consumed these has already exited (readerWG is joined before a term ends), so
// nothing is racing this.
func drain[T any](ch chan T) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// runTerms is the supervisor: it owns the lease for the process lifetime and drives
// one term after another. ExecuteStart starts it only after the FIRST term is live.
//
// 🔴 WHY THE FIRST TERM IS AWAITED RATHER THAN BACKGROUNDED. A pod that reported
// Ready while standing by would be a warm standby, and DETECT is not ready for one
// yet: the tenant-purge responder answers on the per-term context, so a standby
// holding a LIVE context would tell the purge coordinator "no work here" while the
// real leader still holds that tenant's keyed state — a clean answer that is a lie.
// Awaiting the first term means the context between terms is always CANCELLED,
// which makes that responder ERROR instead, and an error fails a purge safely. The
// Ready-standby posture lands with the term-scoped purge responder, not before it.
//
// It costs nothing on the deploy shape event-processing actually uses: replicas:1
// with strategy Recreate, so the departing pod RELEASED the partition and this one
// both acquires immediately and skips termSlack. The wait materialises on an
// eviction or node-drain overlap, where it is bounded by the lease TTL plus
// termSlack, ~50s.
//
// 🔴 THAT IS NOT MEASURED AGAINST THE STARTUP PROBE, WHICH NEVER SEES IT. The probe
// polls /readyz, which reports the auth gate and is independent of this processor —
// so a slow term build cannot fail the probe, and no probe budget bounds it.
//
// What it DOES delay is everything main() starts after the processor: the tenant-purge
// responder and the REACT dispatcher, both of which are started after
// ResolvedEventsProcessor.Start returns. Their work is durable and nothing is lost,
// but REACT dispatch is deferred by the handover wait plus the build.
func (rp *ResolvedEventsProcessor) runTerms() {
	for {
		if rp.supCtx.Err() != nil {
			return
		}
		handle, err := rp.beginTermWithRetry()
		if err != nil {
			if rp.supCtx.Err() == nil {
				// 🔴 END THE PROCESS, DO NOT MERELY RETURN. Nothing else here can notice
				// that leadership has stopped: /healthz is unconditional, /readyz reports
				// the auth gate, and neither reads this supervisor — so a bare return
				// leaves a pod that is Ready, live-healthy and detecting nothing, for
				// every tenant, until a human looks at a gauge. That is precisely the
				// silently-dead singleton that buildTerm refuses to START as, and it must
				// not be reachable a minute later just because startup went well.
				// DetectHasNoLeader then covers the window while the pod is being
				// replaced; DetectLeaderIsNotConsuming covers the state this avoids.
				rp.supCancel()
				rp.failProcess(err)
			}
			return
		}
		rp.awaitTermEnd(handle)
	}
}

// beginTermWithRetry acquires the partition and builds a term on it, retrying a
// failed BUILD until the fuse blows.
//
// The two retry policies here are different on purpose and the difference is the
// point of the whole loss table: acquisition retries forever (inside
// acquireWithBackoff), because a failure there is either a healthy cluster with a
// leader or a broker outage, and neither is this pod's fault. A term BUILD that
// keeps failing is this pod's fault — it holds the partition and detects nothing,
// which is worse than not holding it — so that one is fused.
func (rp *ResolvedEventsProcessor) beginTermWithRetry() (*termHandle, error) {
	for {
		handle, err := rp.beginTerm()
		if err == nil {
			return handle, nil
		}
		if rp.supCtx.Err() != nil {
			return nil, rp.supCtx.Err()
		}
		if errors.Is(err, errTermBuildFailed) {
			rp.termBuildFailures++
			log.Error().Err(err).Int("consecutive", rp.termBuildFailures).Str("partition", rp.cfg.PartitionId).
				Msg("DETECT term build failed; the partition was released and will be re-acquired")
			if rp.termBuildFailures >= maxConsecutiveTermBuildFailures {
				return nil, fmt.Errorf("event-processing: %d consecutive DETECT term builds failed for partition %q; "+
					"this pod holds the lease and detects nothing, so it gives up and lets a replacement take the "+
					"partition: %w", rp.termBuildFailures, rp.cfg.PartitionId, err)
			}
			continue
		}
		// Not a build failure and not shutdown: the term could not be SET UP (no
		// holder signal). The partition is already released; go round again.
		continue
	}
}

// errTermBuildFailed marks the one failure the fuse counts.
var errTermBuildFailed = errors.New("event-processing: DETECT term build failed")

// acquireWithBackoff blocks until this replica owns the partition.
//
// It retries FOREVER on both failure modes, and the two are logged differently
// because they mean opposite things to whoever is reading: ErrLeaseHeld is the
// normal answer on a healthy cluster that already has a leader, while anything else
// is the broker. Neither is fused — see maxConsecutiveTermBuildFailures.
func (rp *ResolvedEventsProcessor) acquireWithBackoff() (*messaging.Lease, bool, error) {
	backoff := acquireBackoffMin
	logged := false
	for {
		if err := rp.supCtx.Err(); err != nil {
			return nil, false, err
		}
		// Asked BEFORE the Create, because the Create is what destroys the answer: the
		// lease bucket keeps one revision per key, so our own entry replaces the delete
		// marker that distinguishes a clean release from an expiry.
		clean := rp.Lease.PriorOwnerReleasedCleanly(rp.cfg.PartitionId)
		lease, err := rp.Lease.Acquire(rp.cfg.PartitionId)
		if err == nil {
			return lease, clean, nil
		}
		if errors.Is(err, messaging.ErrLeaseHeld) {
			if !logged {
				log.Info().Str("partition", rp.cfg.PartitionId).
					Msg("DETECT partition is held by another replica; standing by to take it over")
				logged = true
			}
		} else {
			log.Warn().Err(err).Str("partition", rp.cfg.PartitionId).
				Msg("DETECT lease acquisition failed; retrying, because a broker outage must not evict a pod")
		}
		select {
		case <-rp.supCtx.Done():
			return nil, false, rp.supCtx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > acquireBackoffMax {
			backoff = acquireBackoffMax
		}
	}
}

// beginTerm acquires the partition, waits out the previous owner, and builds a term.
func (rp *ResolvedEventsProcessor) beginTerm() (*termHandle, error) {
	lease, priorReleasedCleanly, err := rp.acquireWithBackoff()
	if err != nil {
		return nil, err
	}
	log.Info().Str("partition", rp.cfg.PartitionId).Uint64("epoch", lease.Epoch()).
		Msg("DETECT acquired the partition lease")

	// A fresh context for this term's loops, consumers and store calls.
	rp.newTermContext()
	handle := &termHandle{lease: lease, ctx: rp.pctx()}

	// is_leader goes up HERE, at acquire, not after the build. See setLeader.
	rp.metrics.setLeader(true)
	rp.metrics.setDetectLive(false)

	holder, err := lease.WatchHolder(handle.ctx)
	if err != nil {
		// No holder signal means no gate, and no gate means the readers would consume
		// ungoverned. Give the partition back rather than run unguarded.
		log.Error().Err(err).Str("partition", rp.cfg.PartitionId).
			Msg("DETECT could not watch its lease key; releasing the partition and standing by")
		rp.endTerm(handle)
		return nil, err
	}
	handle.holder = holder
	rp.Gate.Enter(holder.Held)

	keepAliveDone := make(chan struct{})
	go func() {
		defer close(keepAliveDone)
		if kerr := lease.KeepAlive(handle.ctx, renewInterval); kerr != nil {
			log.Warn().Err(kerr).Str("partition", rp.cfg.PartitionId).
				Msg("DETECT lease renewal has definitively failed; ending the term")
			rp.pcancel()
		}
	}()
	handle.keepAliveDone = keepAliveDone

	// 🔴 WAIT OUT THE PREVIOUS OWNER BEFORE READING ANY STATE — unless it is PROVEN
	// there is nothing to wait for. Acquire proves the SERVER forgot the old entry,
	// not that the old OWNER noticed; see termSlack for what the wait is made of.
	//
	// 🔴 "MY ACQUIRE SUCCEEDED FIRST TRY" IS NOT THAT PROOF, and an earlier version of
	// this used it. An absent key has three causes and that test only rules out one:
	// the previous entry may have EXPIRED under an owner that is still running, which
	// is exactly the case a partitioned-but-alive replica produces and exactly the one
	// this wait exists for. Only an explicit release proves the predecessor stopped —
	// endTerm releases last, after joining every loop and flushing — and that is what
	// PriorOwnerReleasedCleanly reads.
	//
	// So an ordinary rollout, where the departing pod released, skips the wait; a fresh
	// install pays it once for want of evidence; and every ambiguous case pays it.
	if !priorReleasedCleanly {
		log.Info().Str("partition", rp.cfg.PartitionId).Dur("slack", termSlack).
			Msg("DETECT cannot prove the previous owner released this partition; waiting it out before reading any state")
		select {
		case <-handle.ctx.Done():
			rp.endTerm(handle)
			return nil, handle.ctx.Err()
		case <-time.After(termSlack):
		}
	}

	if err := rp.buildTerm(handle.ctx); err != nil {
		rp.endTerm(handle)
		return nil, fmt.Errorf("%w: %w", errTermBuildFailed, err)
	}
	rp.termBuildFailures = 0
	rp.metrics.setDetectLive(true)
	log.Info().Str("partition", rp.cfg.PartitionId).Msg("DETECT term is live")
	return handle, nil
}

// awaitTermEnd blocks until this term ends, then tears it down.
//
// Two independent signals, because neither can see the other's case: the watch sees
// a takeover or a delete and nothing else, and the term context sees an EXPIRED
// window (converted into a cancel by KeepAlive, since an expiry emits no watch
// event at all), a stale checkpoint, or shutdown.
func (rp *ResolvedEventsProcessor) awaitTermEnd(handle *termHandle) {
	select {
	case <-handle.holder.Lost():
		log.Warn().Str("partition", rp.cfg.PartitionId).
			Msg("DETECT lost the partition to another owner; ending the term")
	case <-handle.ctx.Done():
	}
	rp.endTerm(handle)
}

// endTerm tears one term down, and the ORDER of these steps is the whole of it.
func (rp *ResolvedEventsProcessor) endTerm(handle *termHandle) {
	// 1. Stop everything that reads or writes. Cancelling the term context unwinds
	//    the six loop goroutines and the pump; readerWG joins them, so past this
	//    point nothing is mid-persist.
	rp.pcancel()
	rp.readerWG.Wait()
	if handle.keepAliveDone != nil {
		// 2. Join the renewer BEFORE releasing. Renew runs its KV Update outside the
		//    lease mutex, so a Release racing one deletes with the pre-renew revision,
		//    fails the CAS, and leaves a freshly renewed entry to age out on its own —
		//    the next pod then waits a full TTL for a lease nobody holds. At a 1s renew
		//    interval this race is not theoretical.
		<-handle.keepAliveDone
	}
	// 3. Withdraw the readers' reply-inbox interest. This is what stops a pull
	//    request buffered across a disconnect from being served after the successor
	//    takes over. It is local and works while disconnected, unlike the bind.
	rp.unbindTermReaders()
	// 4. Flush LAST, and before the release. finalCheckpoint runs on a fresh
	//    background context so the term cancel above does not stop it, and it still
	//    holds the lease here, so its holder check passes.
	//
	//    🔴 NEVER RELEASE FIRST. A released lease reads as not-held, so every clean
	//    rollout would refuse its own final checkpoint; worse, a deleted key is
	//    acquirable immediately, so the successor could Load the snapshot before this
	//    Save landed — manufacturing the stale-checkpoint exit this design says
	//    cannot happen.
	rp.finalCheckpoint()
	rp.Gate.Exit()
	if err := handle.lease.Release(); err != nil {
		// Informational: our own hold is relinquished regardless, and a failed delete
		// just leaves the entry to age out — the crash-path handover, no corruption.
		log.Warn().Err(err).Str("partition", rp.cfg.PartitionId).Msg("DETECT lease release did not land; the entry will expire")
	}
	rp.metrics.setDetectLive(false)
	rp.metrics.setLeader(false)
}

// bindTermReaders attaches every durable this term consumes.
//
// 🔴 THE BIND IS AT TERM START AND THE UNBIND IS AT TERM END, AND THE ASYMMETRY IS
// THE POINT. Binding makes three JetStream API calls, so doing it at term END means
// doing it during exactly the outage that ended the term: each call times out, the
// bind fails, and the reader is left holding a CLOSED subscription. The next term's
// first fetch then reports ErrSubscriptionClosed, which the reader maps to io.EOF,
// which every one of the six loops treats as shutdown — a term that builds cleanly,
// reports is_leader=1 and detect_live=1, and reads nothing. Here, that same failure
// is a term-build failure that the fuse can see.
func (rp *ResolvedEventsProcessor) bindTermReaders() error {
	for _, r := range rp.termReaders() {
		if err := r.BindTerm(); err != nil {
			return err
		}
	}
	return nil
}

func (rp *ResolvedEventsProcessor) unbindTermReaders() {
	for _, r := range rp.termReaders() {
		if err := r.UnbindTerm(); err != nil {
			log.Warn().Err(err).Msg("DETECT could not unbind a durable at term end")
		}
	}
}

// termReaders is every reader whose consumption belongs to a term. A reader that
// does not implement TermBoundReader (the unit-test fakes) is skipped, which is what
// keeps the unleased path identical to what it was.
func (rp *ResolvedEventsProcessor) termReaders() []messaging.TermBoundReader {
	candidates := []messaging.MessageReader{
		rp.ResolvedEventsReader, rp.RuleUpdatesReader, rp.RosterReader,
		rp.EntityDeletedReader, rp.AttributeReader, rp.FenceSetReader,
	}
	bound := make([]messaging.TermBoundReader, 0, len(candidates))
	for _, c := range candidates {
		if c == nil {
			continue
		}
		if tb, ok := c.(messaging.TermBoundReader); ok {
			bound = append(bound, tb)
		}
	}
	return bound
}

// failProcess ends the process with a non-zero status, off this goroutine.
//
// Off this goroutine because FailNow tears the microservice down, which calls
// ExecuteStop, which waits on supWG — the very group this goroutine belongs to.
// Calling it inline would deadlock the shutdown it is asking for.
func (rp *ResolvedEventsProcessor) failProcess(err error) {
	wrapped := fmt.Errorf("event-processing: DETECT leadership has stopped for partition %q and cannot resume; "+
		"this pod holds no partition and detects nothing, so it exits to be replaced: %w", rp.cfg.PartitionId, err)
	if rp.Microservice == nil {
		log.Error().Err(wrapped).Msg("DETECT cannot end the process; it has no microservice handle")
		return
	}
	go rp.Microservice.FailNow(wrapped)
}
