// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

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
func (cproc *CommandDeliveryProcessor) reconcileHolds(ctx context.Context) {
	ran, err := cproc.Api.TryReconcileLock(ctx, func() error {
		cproc.reconcileOnePage(ctx)
		return nil
	})
	if err != nil {
		log.Error().Err(err).Msg("could not acquire the hold-reconcile lock")
		return
	}
	if !ran {
		log.Debug().Msg("Another replica holds the hold-reconcile lock; skipping this pass.")
	}
}

// reconcileOnePage walks one bounded page of held commands and releases what it can.
func (cproc *CommandDeliveryProcessor) reconcileOnePage(ctx context.Context) {
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
		return
	}
	held, next, err := cproc.Api.HeldCommands(core.WithSystemContext(ctx), cproc.reconcileCursor, reconcilePageSize)
	if err != nil {
		log.Error().Err(err).Msg("unable to read withheld commands for reconciliation")
		return
	}
	cproc.reconcileCursor = next
	if len(held) == 0 {
		return
	}
	for _, batch := range groupByTenant(held, cproc.tenantDeleted) {
		cproc.reconcileTenantBatch(ctx, batch)
	}
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
	for _, cmd := range batch.commands {
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
		if presence.Decide(states[cmd.DeviceToken]) == presence.Hold {
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
