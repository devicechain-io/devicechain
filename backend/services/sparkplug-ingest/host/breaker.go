// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"sync"
	"time"
)

// The per-source command circuit breaker.
//
// A customer broker that refuses a QoS 0 publish may answer by dropping the WHOLE session (some
// brokers do by default, others can be configured to). A dropped Host session flaps STATE and has
// the fleet rebirth, and it would repeat for every command queued behind the first. So a session
// that ends within breakerWatch of a command publish is read as "the broker refused it": the
// breaker OPENS, commands to the source fail fast and honestly for a backoff, and a single command
// is let through when the backoff ends (half-open) to find out whether the cause has been fixed.
//
// Ingest is unaffected. The session reconnects exactly as it always did; only the commands pause.
//
// A disconnect that merely coincides with a network blip trips it falsely. The price is a few
// minutes of fast refusals, and the trip counter lets an operator tell the two apart.

const (
	// breakerWatch is how soon after a command publish a lost session counts against the command.
	breakerWatch = 5 * time.Second
	// breakerMinBackoff is how long the breaker stays open the first time; each trip in a row
	// doubles it up to breakerMaxBackoff.
	breakerMinBackoff = 5 * time.Minute
	breakerMaxBackoff = time.Hour
	// breakerProbeGrace bounds how long a half-open probe slot may be held by a command that never
	// reached the wire, so a probe that failed for another reason cannot wedge the breaker.
	breakerProbeGrace = time.Minute
)

// BreakerState is the breaker's state, as exported on sparkplug_command_breaker_state.
type BreakerState int

const (
	BreakerClosed   BreakerState = 0
	BreakerOpen     BreakerState = 1
	BreakerHalfOpen BreakerState = 2
)

func (s BreakerState) String() string {
	switch s {
	case BreakerClosed:
		return "closed"
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half-open"
	}
	return "unknown"
}

// Breaker is one source's command circuit breaker. It is in memory only: a new leader starts
// closed and, at worst, pays one disconnect to learn what the last one knew.
type Breaker struct {
	mu  sync.Mutex
	now func() time.Time

	state     BreakerState
	openUntil time.Time
	// failures is the number of consecutive trips without a clean probe in between; it drives the
	// backoff.
	failures int
	// lastPublish is when a command was last put on the wire.
	lastPublish time.Time
	// probing is set from the moment the half-open breaker admits its one command until the
	// probe settles (closes, reopens, or is released). probeAdmitted is when it was admitted and
	// probePublished when that command reached the wire (zero until then).
	probing        bool
	probeAdmitted  time.Time
	probePublished time.Time
}

// NewBreaker builds a closed breaker reading time from now.
func NewBreaker(now func() time.Time) *Breaker {
	if now == nil {
		now = time.Now
	}
	return &Breaker{now: now}
}

// backoff is how long the breaker stays open after its n-th trip in a row.
func breakerBackoff(n int) time.Duration {
	d := breakerMinBackoff
	for i := 1; i < n; i++ {
		d *= 2
		if d >= breakerMaxBackoff {
			return breakerMaxBackoff
		}
	}
	return d
}

// settle applies the time-driven transitions: an open breaker whose backoff has ended becomes
// half-open, and a half-open probe that stayed on the wire for breakerWatch with no disconnect
// closes it. The caller holds mu.
func (b *Breaker) settle(now time.Time) {
	if b.state == BreakerOpen && !now.Before(b.openUntil) {
		b.state = BreakerHalfOpen
		b.probing = false
	}
	if b.state == BreakerHalfOpen && b.probing {
		switch {
		case !b.probePublished.IsZero() && now.Sub(b.probePublished) >= breakerWatch:
			b.state = BreakerClosed
			b.failures = 0
			b.probing = false
		case b.probePublished.IsZero() && now.Sub(b.probeAdmitted) >= breakerProbeGrace:
			b.probing = false // the admitted command never reached the wire; let another try
		}
	}
}

// State is the current state, without admitting anything.
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.settle(b.now())
	return b.state
}

// Admit asks to put one command on the wire now. A closed breaker admits every command, an open
// one none, and a half-open one exactly one until that probe settles. A command that was admitted
// and then did not reach the wire must call Release.
func (b *Breaker) Admit() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.settle(now)
	switch b.state {
	case BreakerClosed:
		return true
	case BreakerHalfOpen:
		if b.probing {
			return false
		}
		b.probing = true
		b.probeAdmitted = now
		b.probePublished = time.Time{}
		return true
	}
	return false
}

// Release gives back a half-open probe slot taken by Admit whose command did not reach the wire.
// It is a no-op in any other state.
func (b *Breaker) Release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == BreakerHalfOpen && b.probing && b.probePublished.IsZero() {
		b.probing = false
	}
}

// Published records that a command was put on the wire.
func (b *Breaker) Published() {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.lastPublish = now
	if b.state == BreakerHalfOpen && b.probing && b.probePublished.IsZero() {
		b.probePublished = now
	}
}

// Disconnected records that the Host session ended, and reports whether that tripped the breaker:
// true when a command was put on the wire within breakerWatch before it. An open breaker is not
// extended by a further disconnect (its commands were not being sent), and a half-open one that
// has not yet put its probe on the wire is not blamed for one.
func (b *Breaker) Disconnected() (tripped bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.settle(now)
	switch b.state {
	case BreakerOpen:
		return false
	case BreakerHalfOpen:
		if !b.probing || b.probePublished.IsZero() || now.Sub(b.probePublished) >= breakerWatch {
			return false
		}
	case BreakerClosed:
		if b.lastPublish.IsZero() || now.Sub(b.lastPublish) >= breakerWatch {
			return false
		}
	}
	b.failures++
	b.state = BreakerOpen
	b.openUntil = now.Add(breakerBackoff(b.failures))
	b.probing = false
	return true
}
