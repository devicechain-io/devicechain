// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/governance"
)

const (
	// DefaultSweepIntervalSeconds is the cadence of the expiry + redelivery sweep that
	// times out stale commands and dispatches still-queued ones.
	//
	// 🔑 IT IS THE ONE INTERVAL HERE WITH NO ARGUMENT BEHIND ITS VALUE. The two below
	// each defend theirs at length; this one arrived as a bare constant and stayed. It
	// is now a default rather than a law, because it is also the interval that decides
	// user-visible latency: nothing dispatches on enqueue, so QUEUED -> SENT is uniform
	// on [0, interval] and an operator watching a command sit for half a minute is
	// watching this number.
	//
	// 🔴 LOWERING IT IS NOT FREE, AND THE HALF THAT COSTS IS NOT THE HALF YOU WOULD
	// EXPECT. The DELIVERY read is cheap and stays cheap: it selects QUEUED alone against
	// a partial index keyed (status, id), so it is a direct seek into the QUEUED partition
	// with no sort and no HELD rows touched, and an idle fleet of any size reads nothing.
	//
	// But a TICK is not only that read. sweepLocked runs ExpireStale FIRST, and that query
	// is `status NOT IN (terminal) AND <expiry predicate> AND id > cursor` -- and NO INDEX
	// IN THIS TABLE CONTAINS expires_at, so it walks the whole non-terminal set, HELD rows
	// included. That is precisely the set an offline fleet's backlog accumulates in. So the
	// per-tick cost scales with the BACKLOG, and halving the interval doubles that walk.
	//
	// What that means for an operator: on an instance with a small backlog, turning this
	// down is close to free and buys latency directly. On one with a large withheld
	// backlog it is not, and the honest fix for latency there is a dispatch on enqueue
	// rather than a faster poll.
	DefaultSweepIntervalSeconds = 30

	// MinSweepIntervalSeconds floors the sweep cadence.
	//
	// 🔑 IT IS A GUARD AGAINST A PATHOLOGICAL VALUE, NOT A RECOMMENDATION. Two costs
	// compound below it: every tick is a TrySweepLock round trip per replica, and every
	// tick is the unindexed expiry walk described above. Five seconds bounds that
	// amplification to six times the default's while still allowing most of the latency
	// win an operator would come here for.
	//
	// It is deliberately ABOVE 1. A floor of 1 would be unreachable code: ApplyDefaults
	// maps every non-positive value onto the default, so Validate's lower bound can only
	// ever fire for a value between 1 and the floor, and a floor of 1 leaves that range
	// empty -- a check that cannot fail, guarding nothing.
	MinSweepIntervalSeconds = 5

	// MaxSweepIntervalSeconds ceilings it. An over-long interval is refused rather than
	// accepted because this sweep is not only a delivery path: it is also what expires
	// stale commands, so stretching it delays every command's terminal state and not
	// just its dispatch.
	MaxSweepIntervalSeconds = 300

	// HoldReconcileInterval is the cadence (in seconds) of the pass that releases
	// WITHHELD commands whose devices have come back.
	//
	// 🔑 IT IS SLOW ON PURPOSE, AND IT IS NOT A LATENCY KNOB. The two passes ask
	// different questions: the sweep asks "is there new work?", where latency IS the
	// product, while this asks "has the world changed under a decision made earlier?",
	// where the answer is almost always no and asking cheaply beats asking quickly.
	// Turning this down to chase release latency would put the whole accumulated
	// withheld set — which is what the gate exists to let accumulate — back under a
	// near-continuous scan, which is the cost the gate was built to remove.
	//
	// The right fix for release latency is not a shorter interval but a wake: the
	// transports know the instant a device returns, and this is the net under it.
	HoldReconcileInterval = 120

	// StrandedReconcileInterval is the cadence (in seconds) of the pass that re-arms
	// commands abandoned in SENT.
	//
	// 🔑 SLOWER THAN THE HOLD RECONCILER, BECAUSE WHAT IT WAITS FOR IS SLOWER. A row is
	// not eligible until it has been in SENT for StrandedSentGrace (600s today). This is a
	// floor under a failure that is already minutes to hours from its visible consequence;
	// there is no latency to win by ticking faster.
	//
	// ⚠️ An earlier version of this note argued that ticking faster than the grace "would
	// mostly re-ask a question whose answer cannot have changed". That was never quite
	// right and is now plainly wrong: eligibility is per-row against each row's own
	// sent_time, so rows cross the horizon continuously and no pass re-asks the same
	// question. What this cadence actually bounds is how long a newly-eligible row waits
	// (at most one tick) and how fast the cursored 500-row walk drains a backlog. A
	// cadence shorter than the grace is therefore sensible, not redundant.
	//
	// ⚠️ It is deliberately NOT derived from StrandedSentGrace even though it is close to
	// it. They are independent: the grace period is a correctness bound (act no sooner
	// than this, or you race the messaging layer), while this is a polling cadence. Tying
	// them would mean a change to the broker's ack budget silently retuned a ticker.
	StrandedReconcileInterval = 300

	// DefaultCommandTTLSeconds is the fallback command time-to-live (168h = 7 days)
	// stamped onto a command whose creator supplies no explicit expiresAt. It is
	// deliberately aligned with the device-commands stream MaxAge (core/messaging
	// streamMaxAge = 7d): the stream already ages a command's *message* out at 7d, so
	// a command row that outlives that is undeliverable zombie state, never a promise
	// the platform can keep. Stamping a default closes the "stuck in SENT forever" gap
	// — a TTL-less command (every REACT send-command, ADR-051) previously never reached
	// a terminal state because ExpireStale only touches rows with a non-null expires_at.
	// It is also what makes the LwM2M queue-mode hold (ADR-075 L4b) bounded: a held
	// command a device never wakes to receive resolves to EXPIRED at this horizon —
	// EXPIRED rather than TIMEOUT because it was never dispatched, which is the
	// distinction HELD exists to preserve (see model.CommandStatus).
	DefaultCommandTTLSeconds = 168 * 60 * 60

	// MinCommandTTLSeconds floors the configured default so a fat-fingered tiny value
	// cannot expire commands out from under a device before it can answer.
	MinCommandTTLSeconds = 60

	// DefaultHeldCommandCeiling is the platform default bound on how many WITHHELD
	// (HELD) commands one tenant may accumulate. HELD is where an offline fleet's
	// backlog collects and it can sit for days, so it is the one lifecycle state that
	// grows without a natural brake: the delivery sweep reads every dispatchable row
	// into the pod on each tick, so an unbounded hold is both a per-tenant storage
	// problem and an instance-wide memory one.
	//
	// 🔴 A MISSING OR ZERO VALUE MEANS THIS DEFAULT, NEVER UNLIMITED — in the service
	// config (ApplyDefaults floors it), in a per-tenant override, and in a tier. A
	// governance bound whose absent value reads as "no bound" stops governing exactly
	// when the thing that carries it is unreachable.
	//
	// It is DERIVED from core's last-resort floor rather than restated, because the
	// two are the same number by intent: the resolver falls back to core's value when
	// this service supplies a non-positive default, so a lone edit here would leave a
	// tenant's ceiling depending on which of the two paths answered.
	DefaultHeldCommandCeiling = governance.DefaultHeldCommandCeiling

	// MinHeldCommandCeiling floors the configured ceiling. A handful of held commands
	// per tenant is not a bound, it is an outage for any fleet that sleeps: a device
	// waking to find its backlog was refused at enqueue never receives the commands
	// at all, and the refusal is invisible from the device's side.
	MinHeldCommandCeiling = 100

	// DefaultDeliveryMachineryReserve is the share of the ceiling kept for the platform's
	// own command delivery, so one fleet write cannot consume the whole ceiling and leave
	// every automated send-command for that tenant refused until the backlog drains.
	//
	// Derived from core's value for the same reason the ceiling above is: the two are one
	// number by intent, and a lone edit here would make a tenant's effective limit depend
	// on which path answered.
	DefaultDeliveryMachineryReserve = governance.DefaultDeliveryMachineryReserve

	// MaxDeliveryMachineryReserve caps the configured reserve. Past half the ceiling the
	// reserve stops being a reserve: it becomes the main allocation, and fleet writes —
	// the capability this whole subsystem exists to provide — are bounded by the leftovers
	// of a guard meant to protect delivery, not to prevent it.
	MaxDeliveryMachineryReserve = 0.5
)

// CommandDeliveryConfiguration is the microservice configuration. Commands are
// persisted to the relational store (ADR-012 #4).
type CommandDeliveryConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration

	// DefaultCommandTTLSeconds is the TTL stamped on a command whose creator omits an
	// explicit expiresAt (a caller-supplied value always wins). Fail-safe: a zero or
	// negative value is replaced by the platform default in ApplyDefaults, so the field
	// can never disable expiry (which would resurrect the stuck-in-SENT-forever gap).
	DefaultCommandTTLSeconds int

	// HeldCommandCeiling is this instance's fallback bound on withheld (HELD) commands
	// per tenant, used when no per-tenant or tier ceiling has been resolved. Fail-safe
	// in the same shape as the TTL above: a zero or negative value is replaced by the
	// platform default in ApplyDefaults, so the field can never mean "unlimited".
	HeldCommandCeiling int

	// SweepIntervalSeconds is the cadence of the expiry + delivery sweep. Fail-safe in
	// the same direction as the fields below: absent, zero or negative lands on the
	// platform default in ApplyDefaults, never on "never sweep" -- a sweep that does not
	// run stops expiring commands as well as dispatching them.
	SweepIntervalSeconds int

	// DeliveryMachineryReserve is the fraction of the ceiling in force that only the
	// platform's own service-token callers may draw on. It is OPERATOR-SIDE ONLY — there
	// is deliberately no per-tenant override and no tier key, because a tenant able to
	// lower the reserve could defeat the protection that exists against its own fleet
	// writes.
	//
	// Fail-safe in the same direction as the two above: absent, zero, negative or NaN
	// means the platform reserve, never "no reserve".
	DeliveryMachineryReserve float64
}

// NewCommandDeliveryConfiguration creates the default command delivery configuration.
func NewCommandDeliveryConfiguration() *CommandDeliveryConfiguration {
	cfg := &CommandDeliveryConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the ADR-022 decision-1 defaulting hook for this service. It
// floors the command TTL to the platform default when unset or non-positive — the
// fail-safe that keeps a missing/zero value from disabling expiry entirely.
func (c *CommandDeliveryConfiguration) ApplyDefaults() {
	if c.DefaultCommandTTLSeconds <= 0 {
		c.DefaultCommandTTLSeconds = DefaultCommandTTLSeconds
	}
	// A missing or zero ceiling is the platform default, NEVER unlimited — the same
	// fail-safe direction as the TTL: the harmless-looking value (absent, zero) must
	// land on the bound, not remove it.
	if c.HeldCommandCeiling <= 0 {
		c.HeldCommandCeiling = DefaultHeldCommandCeiling
	}
	// Same direction again for the reserve. Written as a negated positive test rather than
	// `<= 0` on purpose: NaN compares false to everything, so the obvious spelling would
	// let a NaN through here AND through the upper-bound check in Validate. It would not
	// actually remove the reserve — governance.RestrictedCommandLimit makes the same test
	// and lands on the platform reserve — but a config value that survives validation while
	// meaning nothing is worth refusing where it enters rather than where it is used.
	if !(c.DeliveryMachineryReserve > 0) {
		c.DeliveryMachineryReserve = DefaultDeliveryMachineryReserve
	}
	// Same direction again for the sweep cadence: the harmless-looking value lands on the
	// default, never on the pathological reading. Zero seconds is not "as fast as possible".
	if c.SweepIntervalSeconds <= 0 {
		c.SweepIntervalSeconds = DefaultSweepIntervalSeconds
	}
}

// Validate is the ADR-022 decision-1 validation hook for this service. It rejects a
// command TTL below the floor: a sub-minute default would expire commands before a
// device on a marginal radio could ever answer — a check that cannot fail silently
// (the whole point of the default is that every command reaches a terminal state).
func (c *CommandDeliveryConfiguration) Validate() error {
	if c.DefaultCommandTTLSeconds < MinCommandTTLSeconds {
		return fmt.Errorf("defaultCommandTtlSeconds must be at least %d (got %d)",
			MinCommandTTLSeconds, c.DefaultCommandTTLSeconds)
	}
	// Same shape for the held-command ceiling: a value below the floor is refused
	// rather than quietly accepted, because a tiny ceiling is not a conservative
	// setting — it refuses a sleeping fleet's backlog at enqueue, and the device
	// never learns the commands existed.
	if c.HeldCommandCeiling < MinHeldCommandCeiling {
		return fmt.Errorf("heldCommandCeiling must be at least %d (got %d)",
			MinHeldCommandCeiling, c.HeldCommandCeiling)
	}
	// The sweep cadence is bounded at BOTH ends, and the two ends refuse different
	// mistakes. Below the floor the pass stops being a sweep and becomes a spin against
	// the advisory lock; above the ceiling it delays expiry, not just dispatch, so a
	// command's terminal state drifts with it.
	//
	// 🔴 THE CEILING IS ALSO A CORRECTNESS BOUND, NOT ONLY AN ERGONOMIC ONE, THOUGH NOT
	// IN THE DIRECTION IT FIRST APPEARS. processor.StrandedSentGrace is a const EXPRESSION
	// over MaxSweepIntervalSeconds, so raising this ceiling raises that horizon WITH it,
	// automatically -- which is the whole point of deriving rather than writing a number.
	// Raising it therefore cannot make the stranded pass park commands too early; what it
	// does is lengthen stranded DETECTION, since a row waits the longer horizon before it
	// is eligible at all.
	//
	// The thing that would leave the horizon short is REPLACING that term -- deriving the
	// grace from the default, say -- and TestStrandedGraceCoversTheSlowestPermittedSweep
	// is what catches that.
	if c.SweepIntervalSeconds < MinSweepIntervalSeconds || c.SweepIntervalSeconds > MaxSweepIntervalSeconds {
		return fmt.Errorf("sweepIntervalSeconds must be between %d and %d (got %d)",
			MinSweepIntervalSeconds, MaxSweepIntervalSeconds, c.SweepIntervalSeconds)
	}
	// An over-large reserve is refused rather than clamped. Unlike an absent value —
	// which has an obviously right answer — a reserve of 0.8 or 3 is an operator saying
	// something they did not mean, and silently substituting 0.2 would leave them
	// believing a split that is not in force.
	if c.DeliveryMachineryReserve > MaxDeliveryMachineryReserve {
		return fmt.Errorf("deliveryMachineryReserve must be at most %v (got %v)",
			MaxDeliveryMachineryReserve, c.DeliveryMachineryReserve)
	}
	return nil
}
