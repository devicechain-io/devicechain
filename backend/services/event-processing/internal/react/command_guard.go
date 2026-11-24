// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"sync"
	"time"
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
// its snapshots. Replay never enters it; preview has no live sinks and evaluation
// uses recording sinks with WithCommandSimulation. It uses the server clock,
// never device event time (a device must not advance its own allowance).
//
// HA: state is PER REPLICA, not shared or checkpointed. REACT's DETECT term gate
// normally leaves only the lease holder dispatching. Restart/failover starts a
// fresh allowance; the documented ~5s lease-handover overlap can admit both
// replicas' allowances. This is containment within a term, not a cluster-wide
// exactly-once or durable loop guarantee. The outbound governance gate is shared
// with connector actions in the same process and has the same HA limitation.
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

// WithCommandSimulation is used only by the isolated rule evaluator, whose sinks
// record intents. It neither spends live governance tokens nor holds loop state.
func WithCommandSimulation() Option {
	return func(d *Dispatcher) { d.commandSimulation = true }
}

func (d *Dispatcher) commandRefusal(evTenant, rule, device string, at, sentAt time.Time) string {
	if d.commandSimulation {
		return ""
	}
	if d.connectorRate != nil && !d.connectorRate.AllowAt(evTenant, at) {
		return CommandRateLimited
	}
	return d.commandLoop.admit(evTenant, rule, device, sentAt)
}
