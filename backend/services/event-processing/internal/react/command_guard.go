// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
)

const (
	CommandRateLimited = "command_rate_limited"
	CommandLoop        = "command_loop"
	CommandGuardFull   = "command_guard_full"

	// Fixed platform safety defaults: no new instance-document keys are needed, so
	// previous-release pods still accept the configuration during a rolling upgrade.
	// Two commands/second from one logical rule to one device is deliberately generous.
	commandLoopSends  = 120
	commandLoopWindow = time.Minute
	commandLoopKeys   = 65536
	// commandLoopTenantKeys is one tenant's share of commandLoopKeys. The table is
	// per process and serves every tenant, so without a share one tenant with a large
	// fleet or a raised ceiling filled it and refused every other tenant's commands
	// with command_guard_full. A quarter still covers far more distinct rule/device
	// pairs a minute than the default command ceiling (100/s, burst 200) can admit;
	// commandLoopKeys stays the bound on the whole table.
	commandLoopTenantKeys = commandLoopKeys / 4
)

type commandLoopKey struct {
	tenant string
	rule   string
	device string
}

type commandLoopEntry struct {
	until time.Time
	sends int
	// gen identifies this window of the key, so an expiry record left by an earlier
	// window (a refunded or expired key that was admitted again) cannot end this one.
	gen uint64
}

// commandLoopExpiry records when one window of a key ends. Windows are opened at a
// clock that never moves backwards (mark), all for the same length, so records are
// appended in expiry order and the oldest is always at the head.
type commandLoopExpiry struct {
	key   commandLoopKey
	until time.Time
	gen   uint64
}

// commandLoopGuard bounds send attempts in an anchored window per tenant/logical
// rule/device. Commands already carry CommandCause.RuleID and detection identity;
// resolved device telemetry does not carry a trustworthy command-causation chain.
// Consequently this conservatively bounds ALL commands for the key, including
// retries, rather than trusting a device to echo a correlation or hop counter.
//
// This is side-effect safety state, outside the replay-correct DETECT engine and
// its snapshots. A detection the engine re-derives on startup replay is still SENT
// (deliver-before-checkpoint: it may never have left before the crash, and the
// idempotency token collapses it downstream if it did) but never enters this state
// or the outbound gate. A backlog that startup replay applies for the first time is
// not a re-derivation and is governed like live input. Preview has no live sinks and
// evaluation uses recording sinks with WithCommandSimulation. It meters on the
// detection's platform trigger time capped at the server clock (loopMeteringTime),
// never device event time (a device must not advance its own allowance).
//
// HA: state is PER REPLICA, not shared or checkpointed. REACT's DETECT term gate
// normally leaves only the lease holder dispatching. Restart/failover starts a
// fresh allowance; the documented ~5s lease-handover overlap can admit both
// replicas' allowances. This is containment within a term, not a cluster-wide
// exactly-once or durable loop guarantee. The command rate gate has the same HA
// limitation, with a separate budget of the same size as connector actions.
//
// CAPACITY: a key holds a slot for its window. A tenant holds at most
// commandLoopTenantKeys slots and the table at most commandLoopKeys; a new key past
// either is refused with command_guard_full rather than evicting a live key (churn
// would reset a loop's allowance). An expired key is released before anything is
// refused, so a full table only ever holds live windows, and a tenant that exhausts
// its share refuses only its own new keys.
type commandLoopGuard struct {
	mu   sync.Mutex
	keys map[commandLoopKey]commandLoopEntry
	// tenants counts each tenant's keys, for its share of the table.
	tenants map[string]int
	// expiry is every open window in expiry order, from head on. Records whose key was
	// since refunded are skipped when reached and dropped when the queue is compacted.
	expiry []commandLoopExpiry
	head   int
	gen    uint64
	mark   time.Time
}

func newCommandLoopGuard() *commandLoopGuard {
	return &commandLoopGuard{keys: make(map[commandLoopKey]commandLoopEntry), tenants: make(map[string]int)}
}

func (g *commandLoopGuard) admit(tenant, rule, device string, now time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Rewinds cannot mint another window, even across devices on different lanes.
	if now.Before(g.mark) {
		now = g.mark
	}
	g.mark = now
	g.expire(now)
	key := commandLoopKey{tenant: tenant, rule: stableContributorID(rule), device: device}
	entry, found := g.keys[key]
	if !found {
		// Never evict a live key to admit a new one: churn would reset loops.
		if g.tenants[tenant] >= commandLoopTenantKeys || len(g.keys) >= commandLoopKeys {
			return CommandGuardFull
		}
		g.gen++
		entry = commandLoopEntry{until: now.Add(commandLoopWindow), gen: g.gen}
		g.tenants[tenant]++
		g.expiry = append(g.expiry, commandLoopExpiry{key: key, until: entry.until, gen: entry.gen})
	}
	if entry.sends >= commandLoopSends {
		return CommandLoop
	}
	entry.sends++
	g.keys[key] = entry
	return ""
}

// expire releases every key whose window has ended by now. Each record is visited
// once, so the cost is amortized over the admissions that appended them.
func (g *commandLoopGuard) expire(now time.Time) {
	for g.head < len(g.expiry) {
		rec := g.expiry[g.head]
		if now.Before(rec.until) {
			break
		}
		if entry, ok := g.keys[rec.key]; ok && entry.gen == rec.gen {
			g.release(rec.key)
		}
		g.expiry[g.head] = commandLoopExpiry{}
		g.head++
	}
	// Compact once the spent prefix outgrows what remains, or refunds have left more
	// stale records than live ones: either way the copy is paid for by what it drops.
	if pending := len(g.expiry) - g.head; g.head > pending || pending > 2*len(g.keys)+1024 {
		kept := make([]commandLoopExpiry, 0, len(g.keys))
		for _, rec := range g.expiry[g.head:] {
			if entry, ok := g.keys[rec.key]; ok && entry.gen == rec.gen {
				kept = append(kept, rec)
			}
		}
		g.expiry, g.head = kept, 0
	}
}

// release deletes a key and returns its tenant's slot.
func (g *commandLoopGuard) release(key commandLoopKey) {
	delete(g.keys, key)
	if n := g.tenants[key.tenant]; n > 1 {
		g.tenants[key.tenant] = n - 1
	} else {
		delete(g.tenants, key.tenant)
	}
}

// refund returns the allowance admit just took for an attempt another policy then
// refused, so a send that never happened neither shortens the key's allowance nor,
// for a key admit created, holds one of the bounded slots. Events of one device are
// dispatched one after another, so nothing else moves the key in between.
func (g *commandLoopGuard) refund(tenant, rule, device string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := commandLoopKey{tenant: tenant, rule: stableContributorID(rule), device: device}
	entry, found := g.keys[key]
	if !found {
		return
	}
	if entry.sends <= 1 {
		g.release(key)
		return
	}
	entry.sends--
	g.keys[key] = entry
}

// WithCommandSimulation is used only by the isolated rule evaluator, whose sinks
// record intents. It neither spends live governance tokens nor holds loop state.
func WithCommandSimulation() Option {
	return func(d *Dispatcher) { d.commandSimulation = true }
}

// commandRefusal decides whether source policy refuses one command, and charges
// what it admits. The loop guard is asked FIRST: a looping rule's refused attempts
// must not keep drawing on the tenant's command budget, or the guard would contain
// the loop for its device while the loop starved every other command of the tenant.
// An attempt the guard admits and the rate gate then refuses
// is refunded to the guard, so a tenant over its rate creates no loop state either.
//
// A replayed detection is exempt from both: it is a re-derivation of output that may
// never have been delivered, not a fresh trigger, and the idempotency token is what
// stops it from actuating twice. DETECT marks only detections of events an earlier
// term was handed; a backlog no term ever saw arrives unmarked and is charged here.
// loopMeteringTime is the time the loop guard charges an attempt at: the detection's
// platform trigger time, the same clock the rate gate meters on, never later than
// the dispatch's own clock. Live traffic is triggered moments before it is
// dispatched, so it is metered at the dispatch time to within the pipeline's latency.
// A backlog drained after an outage is metered on the timeline it was produced on:
// charged at drain time, minutes of compliant traffic would land in one window, so
// every distinct rule/device pair of those minutes would hold a slot at once
// (command_guard_full, and across tenants a full table) and a device commanded
// every few seconds would read as a loop (command_loop). A loop inside the backlog
// still sends faster than the allowance on its own timeline, and is still cut.
//
// The guard clamps every time to its mark, so its windows open in order. While a
// backlog and live traffic drain together, the live traffic pushes the mark to now
// and the backlog is metered there, as it was before.
func loopMeteringTime(ev runtime.DerivedEvent, sentAt time.Time) time.Time {
	if ev.TriggeredAt.IsZero() || ev.TriggeredAt.After(sentAt) {
		return sentAt
	}
	return ev.TriggeredAt
}

func (d *Dispatcher) commandRefusal(ev runtime.DerivedEvent, sentAt time.Time) string {
	if d.commandSimulation || ev.Replayed {
		return ""
	}
	if code := d.commandLoop.admit(ev.Tenant, ev.RuleID, ev.Series, loopMeteringTime(ev, sentAt)); code != "" {
		return code
	}
	if d.commandRate != nil && !d.commandRate.AllowAt(ev.Tenant, ev.TriggeredAt) {
		d.commandLoop.refund(ev.Tenant, ev.RuleID, ev.Series)
		return CommandRateLimited
	}
	return ""
}
