// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

// reconcilePageSize bounds one reconcile pass's read of the withheld set.
//
// The walk resumes from a cursor, so this bounds the work per pass rather than the
// set that is ever examined: a backlog larger than a page is covered across
// consecutive passes instead of being ignored.
const reconcilePageSize = 500

// reconcileHolds releases withheld commands whose devices have come back.
//
// 🔴 THE GATE CREATES A STATE, AND THIS IS CURRENTLY THE ONLY DOOR OUT OF IT. Without a
// release path, a command withheld from a device that returns thirty seconds later would
// sit in HELD until its TTL dragged it to EXPIRED — days, by default. That is strictly
// worse than the silent loss the gate replaces, because the loss at least happened
// promptly. This is why the reconciler ships WITH the gate rather than after it.
//
// It is a NET, not the mechanism. The intended release is a wake on the device's return —
// the transports know the moment it happens, and asking the database on a timer is a poor
// substitute for being told. Until that exists this is the whole answer; after it exists
// this stays, because a wake is a message and messages are lost.
//
// Its cadence is minutes rather than the delivery sweep's thirty seconds, which is the
// difference between the two questions. The sweep asks "is there new work?", where
// latency is the product. This asks "has the world changed underneath a decision I made
// earlier?", where the answer is usually no and asking cheaply matters more than asking
// quickly.
func (cproc *CommandDeliveryProcessor) reconcileHolds(ctx context.Context) error {
	ran, err := cproc.Api.TryReconcileLock(ctx, func() error {
		return cproc.reconcileOnePage(ctx)
	})
	// `ran` before `err`: TryAdvisoryLock returns `true, fn()`, so a non-nil error is the
	// lock's only when the body never ran. See sweepLocked.
	if !ran {
		if err != nil {
			return fmt.Errorf("acquiring the hold-reconcile lock: %w", err)
		}
		return core.ErrPassSkipped
	}
	return err
}

// reconcileOnePage walks one bounded page of held commands and releases what it can.
func (cproc *CommandDeliveryProcessor) reconcileOnePage(ctx context.Context) error {
	// 🔴 NOTHING TO RECONCILE AGAINST MEANS RECONCILE NOTHING. With no presence reader
	// there is no basis on which to undo a hold, and releasing on that basis would throw
	// an absent fleet's whole accumulated backlog back into the dispatch path, where it
	// would be published into the void — undoing the gate's protection wholesale at
	// exactly the moment the gate cannot function.
	//
	// 🔑 THIS IS THE OPPOSITE DIRECTION FROM THE SWEEP'S FAIL-OPEN, AND IT IS THE SAME
	// RULE. Neither path may act on information it does not have. The gate's new
	// behaviour on the sweep is WITHHOLDING, so an unavailable projection means don't
	// withhold; the new behaviour here is RELEASING, so it means don't release. Both fall
	// back to not performing the new behaviour — which happens to point in opposite
	// directions because the two paths act in opposite directions.
	//
	// The consequence of a gate that is turned OFF while holds exist is worth stating,
	// because it is the one case where "keep holding" is permanent rather than transient:
	// those commands are never released and lapse to EXPIRED at their TTL. That is the
	// right outcome even so. EXPIRED says the platform never sent it, which is exactly
	// true; releasing them would dispatch the lot into a broker with no presence check in
	// front of it, and every one would land in SENT and then TIMEOUT — the record that
	// blames the device, which is the thing this whole path exists to stop writing.
	if cproc.Presence == nil {
		// Skipped, not complete: there is nothing to reconcile AGAINST, so this pass did
		// not do its work — it correctly declined to. Reported as success it would keep
		// the last-success gauge fresh on an instance where holds are lapsing to EXPIRED.
		return cproc.releaseEnabledHolds(ctx)
	}
	held, err := cproc.readHeldPage(ctx)
	if err != nil {
		return err
	}
	if len(held) == 0 {
		return nil
	}
	for _, batch := range groupByTenant(held, cproc.tenantDeleted) {
		cproc.reconcileTenantBatch(ctx, batch)
	}
	return nil
}

// reconcileTenantBatch releases one tenant's holds whose devices are no longer absent.
func (cproc *CommandDeliveryProcessor) reconcileTenantBatch(ctx context.Context, batch tenantBatch) {
	tenantCtx := core.WithTenant(ctx, batch.tenant)
	states, err := cproc.Presence.StatesFor(tenantCtx, distinctDevices(batch.commands))
	if err != nil {
		// Fail closed, per reconcileOnePage: keep holding the devices this read could
		// not cover. Counted on the same meter as the sweep's read failures, because it
		// is the same outage and an operator looking at it wants one number for "the
		// gate cannot see presence".
		incr(cproc.PresenceReadErrors, 1)
		log.Warn().Err(err).Str("tenant", batch.tenant).
			Int("asked", len(distinctDevices(batch.commands))).Int("answered", len(states)).
			Msg("Could not read presence for some withheld commands' devices; those stay withheld.")
	}
	// 🔴 A HOLD PLACED BECAUSE THE DEVICE IS DISABLED IS NOT THIS RECONCILER'S TO RELEASE. Its
	// question is "is the device still absent?", and a disabled device being present says
	// nothing about it: only an enable releases those (ResumeDevice). A tenant whose record
	// cannot be read is skipped whole, since releasing is the direction that dispatches.
	disabled, known := cproc.disabledAmong(tenantCtx, batch.tenant, distinctDevices(batch.commands))
	if !known {
		return
	}
	for _, cmd := range batch.commands {
		if disabled[cmd.DeviceToken] {
			continue
		}
		// 🔴🔴 THE UNANSWERED QUESTION IS SKIPPED BEFORE ANY VERDICT IS ASKED FOR, AND
		// THIS LINE IS LOAD-BEARING IN A WAY THE NEXT ONE HIDES. A device the read could
		// not cover is absent from States, which Decide answers Dispatch — i.e. NOT Hold
		// — so without this check the loop below would RELEASE it. One device-state blip
		// would return a whole page of withheld commands to QUEUED, the sweep would
		// publish them at brokers where those devices are absent, and every one would
		// land in SENT and then TIMEOUT: the record that blames the device for the
		// platform's outage, which is the outcome this reconciler exists to avoid
		// writing.
		//
		// It is NOT the same check as "the projection has no row for this device". That
		// case genuinely releases, deliberately — see below. Resolved is what tells the
		// two apart, and the read's error is the only thing that can.
		if !presence.Resolved(states, cmd.DeviceToken, err) {
			continue
		}
		// 🔑 THE ONLY QUESTION ASKED HERE IS "IS THIS DEVICE STILL ABSENT?". Anything
		// that is not a Hold releases — including Undeliverable, which is not a mistake:
		// a row held before its device's transport was known to carry no commands gets
		// returned to QUEUED, and the sweep then fails it properly on the next tick with
		// a reason recorded. One place decides what happens to a dispatchable command.
		if presence.Decide(states[cmd.DeviceToken], cproc.SparkplugCommands) == presence.Hold {
			continue
		}
		cproc.releaseHold(tenantCtx, cmd)
	}
}

// releaseHold returns one command to QUEUED.
func (cproc *CommandDeliveryProcessor) releaseHold(ctx context.Context, cmd *model.Command) {
	released, err := cproc.Api.ReleaseHold(ctx, cmd.ID)
	if err != nil {
		log.Error().Err(err).Uint("command", cmd.ID).Str("device", cmd.DeviceToken).
			Msg("unable to release a withheld command whose device has returned")
		return
	}
	if !released {
		// The row left HELD between the page read and this write — answered, cancelled,
		// expired, or claimed by another release path. The conditional update doing its
		// job, not a failure.
		return
	}
	incr(cproc.HoldsReleased, 1)
	log.Debug().Str("command", cmd.Token).Str("device", cmd.DeviceToken).
		Msg("Released a withheld command; its device is reachable again.")
}

// releaseEnabledHolds is the hold reconciler's pass when there is no presence gate to ask: it
// releases the holds of devices the disabled-device record says are ENABLED.
//
// 🔴 IT IS THE BACKSTOP FOR A LOST RESUME, AND ONLY FOR THAT. Enabling a device releases its
// held commands (ResumeDevice), but the roster fact that triggers it is acked, redelivered or
// lost like any other message, and the record already reads "enabled" by then, so the sync has
// no flip left to act on. With a presence gate the pass above releases such holds (the device is
// present, so nothing withholds them). Without one it returned at the top and the commands sat in
// HELD until their TTL.
//
// What it releases is narrow on purpose: a hold whose device has a row in the record saying NOT
// disabled, which only a device that was disabled at some point can have. A device the record
// never heard of is left alone, and so is a disabled one. The cost is that a presence hold
// placed before the gate was turned off, on a device that was also once disabled, is released
// with the rest; that is the dispatch the gate being off permits anyway.
//
// With no disabled-device record to read it has nothing to reconcile against and skips, as the
// pass always did.
func (cproc *CommandDeliveryProcessor) releaseEnabledHolds(ctx context.Context) error {
	reader := cproc.gateReader()
	if reader == nil {
		return core.ErrPassSkipped
	}
	held, err := cproc.readHeldPage(ctx)
	if err != nil {
		return err
	}
	for _, batch := range groupByTenant(held, cproc.tenantDeleted) {
		tenantCtx := core.WithTenant(ctx, batch.tenant)
		if !cproc.gateReady(tenantCtx, batch.tenant) {
			continue
		}
		enabled, err := reader.EnabledAmong(tenantCtx, distinctDevices(batch.commands))
		if err != nil {
			incr(cproc.DisabledGateErrors, 1)
			log.Warn().Err(err).Str("tenant", batch.tenant).
				Msg("Could not read the disabled-device record; the holds of enabled devices stay held this pass.")
			continue
		}
		for _, cmd := range batch.commands {
			if enabled[cmd.DeviceToken] {
				cproc.releaseHold(tenantCtx, cmd)
			}
		}
	}
	return nil
}

// readHeldPage reads the next bounded page of the withheld set, across tenants, and advances the
// walk's cursor.
//
// 🔴 THE ONE CROSS-TENANT READ OF THE HOLD RECONCILER, AND THE ONLY ONE IT NEEDS. The withheld set
// spans every tenant and the walk pages it by row id, so a per-tenant statement would not be the
// same walk: a tenant with a large stuck backlog would again fill every page. The statement
// carries no tenant predicate; everything the passes do with its rows is scoped, because they
// group by the row's own tenant and write under core.WithTenant. Both passes (the presence
// release and the enabled-device backstop) read through here so there is a single sanctioned
// bypass, not one per pass.
func (cproc *CommandDeliveryProcessor) readHeldPage(ctx context.Context) ([]*model.Command, error) {
	held, next, err := cproc.Api.HeldCommands(core.WithSystemContext(ctx), cproc.reconcileCursor, reconcilePageSize)
	if err != nil {
		log.Error().Err(err).Msg("unable to read withheld commands for reconciliation")
		return nil, fmt.Errorf("reading withheld commands: %w", err)
	}
	cproc.reconcileCursor = next
	return held, nil
}
