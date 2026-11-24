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
)

type commandLoopKey struct {
	tenant string
	rule   string
	device string
}

type commandLoopEntry struct {
	until time.Time
	sends int
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
// or the outbound gate; preview has no live sinks and evaluation uses recording
// sinks with WithCommandSimulation. It uses the server clock, never device event
// time (a device must not advance its own allowance).
//
// HA: state is PER REPLICA, not shared or checkpointed. REACT's DETECT term gate
// normally leaves only the lease holder dispatching. Restart/failover starts a
// fresh allowance; the documented ~5s lease-handover overlap can admit both
// replicas' allowances. This is containment within a term, not a cluster-wide
// exactly-once or durable loop guarantee. The command rate gate has the same HA
// limitation, with a separate budget of the same size as connector actions.
type commandLoopGuard struct {
	mu        sync.Mutex
	keys      map[commandLoopKey]commandLoopEntry
	mark      time.Time
	nextSweep time.Time
}

func newCommandLoopGuard() *commandLoopGuard {
	return &commandLoopGuard{keys: make(map[commandLoopKey]commandLoopEntry)}
}

func (g *commandLoopGuard) admit(tenant, rule, device string, now time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Rewinds cannot mint another window, even across devices on different lanes.
	if now.Before(g.mark) {
		now = g.mark
	}
	g.mark = now
	if !now.Before(g.nextSweep) {
		for key, entry := range g.keys {
			if !now.Before(entry.until) {
				delete(g.keys, key)
			}
		}
		g.nextSweep = now.Add(commandLoopWindow)
	}
	key := commandLoopKey{tenant: tenant, rule: stableContributorID(rule), device: device}
	entry, found := g.keys[key]
	if !found || !now.Before(entry.until) {
		if !found && len(g.keys) >= commandLoopKeys {
			// Never evict a live key to admit a new one: churn would reset loops.
			return CommandGuardFull
		}
		entry = commandLoopEntry{until: now.Add(commandLoopWindow)}
	}
	if entry.sends >= commandLoopSends {
		return CommandLoop
	}
	entry.sends++
	g.keys[key] = entry
	return ""
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
		delete(g.keys, key)
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
// stops it from actuating twice.
func (d *Dispatcher) commandRefusal(ev runtime.DerivedEvent, sentAt time.Time) string {
	if d.commandSimulation || ev.Replayed {
		return ""
	}
	if code := d.commandLoop.admit(ev.Tenant, ev.RuleID, ev.Series, sentAt); code != "" {
		return code
	}
	if d.commandRate != nil && !d.commandRate.AllowAt(ev.Tenant, ev.TriggeredAt) {
		d.commandLoop.refund(ev.Tenant, ev.RuleID, ev.Series)
		return CommandRateLimited
	}
	return ""
}
