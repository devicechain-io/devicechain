// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"sync"
	"time"
)

// EpochSource mints the strictly-monotone session epoch (the ADR-067 presence
// SessionId) that orders every CONNECTED/DISCONNECTED a source emits. A presence
// projection accepts a transition only when its SessionId supersedes the stored one
// (a higher epoch always wins; the same epoch resolves by presence time), so the
// epoch is the single thing that keeps a fresh session from being rejected as stale
// and a stale re-derivation from tearing down a live one.
//
// It is shared across the protocols that produce presence (Sparkplug's session
// machine, LwM2M's registration registry): each holds one and mints through it, so
// the floor + monotonicity live in exactly one place rather than being reimplemented
// (and subtly diverging) per protocol. Its mutex is a LEAF — a caller already holding
// a coarser lock (e.g. the Sparkplug tracker's tr.mu, taken on the connect hot path)
// may call Next under it, so EpochSource must never call back out under e.mu.
type EpochSource struct {
	mu   sync.Mutex
	now  func() time.Time
	last uint64
}

// NewEpochSource builds an epoch source over a clock (nil ⇒ time.Now). The clock
// supplies the wall-clock nanoseconds each epoch is derived from; injecting it lets a
// test drive monotonicity across a clock step-back deterministically.
func NewEpochSource(now func() time.Time) *EpochSource {
	if now == nil {
		now = time.Now
	}
	return &EpochSource{now: now}
}

// Next mints a strictly-monotone epoch: the wall-clock UnixNano, floored to last+1 so
// an in-process clock step-back can never invert ordering against an already-issued
// epoch. Every mint (a birth, a registration, a reconcile timeout) goes through here,
// so the process never issues the same or a lower epoch twice.
func (e *EpochSource) Next() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	v := uint64(e.now().UnixNano())
	if v <= e.last {
		v = e.last + 1
	}
	e.last = v
	return v
}

// Mint is Next under a name that reads correctly at the failover call site (ADR-067
// SP4b), where a fresh epoch above the current floor is minted to stamp the
// timeout-DISCONNECTED transitions: called AFTER SetFloor(max+1), it exceeds every
// stored session so the reconcile-DISCONNECT supersedes them, while a genuine re-birth
// during the probe window mints a LATER (higher) epoch via Next and thus supersedes the
// reconcile-DISCONNECT — so a slow-but-alive node self-heals regardless of the race.
func (e *EpochSource) Mint() uint64 {
	return e.Next()
}

// SetFloor raises the floor so every subsequently-minted epoch exceeds any already
// persisted for this source's devices (ADR-067). A fresh leader (or a restart across a
// wall-clock step-back) calls it with max(session_id)+1 read from the device-state
// projection, so a handover can never mint an epoch the projection rejects as stale.
// Lowering is a no-op.
func (e *EpochSource) SetFloor(floor uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if floor > e.last {
		e.last = floor
	}
}
