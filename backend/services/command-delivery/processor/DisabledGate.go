// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

// A command queued for a device that is then DISABLED is neither dispatched nor cancelled: it is
// held until the device is enabled again (or its TTL lapses it meanwhile). This file is that
// behaviour's dispatch side.
//
// WHERE THE FACT COMES FROM. command-delivery never asks device-management per dispatch. It
// keeps its own record of disabled devices (model.DeviceGate), written by the device-roster
// fact (RosterConsumer) and corrected against device-management's own list at startup and every
// few minutes (syncDisabledGate). A device disabled in the last moments before a dispatch can
// therefore still be dispatched to: the race window is the fact's delivery, and the next sweep
// closes it. The claim (Api.MarkSent) also carries the record, so a fact that lands between the
// batch's read and its claim still wins.
//
// WHY A SYNC AND NOT JUST THE FACT. The roster durable starts at new facts (replaying the
// stream's retention window would re-offer old state), so a device disabled before this service
// first ran, or a fact that never reached the stream, would otherwise be invisible until the
// next change to that device. event-processing solves the same problem with a durable
// projection and a reconcile; this is the same shape, over a table small enough (disabled
// devices only) to need no replay.

// DisabledSource is what the sync reads: the tenants of the instance, and the devices
// device-management has disabled for one of them.
type DisabledSource interface {
	Tenants(ctx context.Context) ([]string, error)
	DisabledDevices(ctx context.Context, tenant string) ([]string, error)
}

// disabledGateWriter is the correction the sync applies, held by *model.Api.
type disabledGateWriter interface {
	ReconcileDisabled(ctx context.Context, disabled []string, readStarted time.Time) ([]string, error)
}

const (
	// gateSyncInterval is the cadence of the corrective walk once one has succeeded.
	gateSyncInterval = 5 * time.Minute
	// gateRetryInterval is how soon a walk that has never succeeded is tried again. Until one
	// succeeds nothing is dispatched, so this is also how long a device-management outage at
	// startup delays delivery beyond the outage itself.
	gateRetryInterval = 10 * time.Second
	// resumeLimit bounds how much of an enabled device's backlog is dispatched at the enable
	// itself; the sweep takes the remainder within a tick.
	resumeLimit = 100
)

// gateReader returns the disabled-device read, or nil when the Api has none.
func (cproc *CommandDeliveryProcessor) gateReader() model.DeviceGateReader {
	reader, _ := cproc.Api.(model.DeviceGateReader)
	return reader
}

// gateReady reports whether dispatch for a tenant may proceed. With a DisabledSource wired it is
// false until this process has listed the tenants, and for a tenant whose first read of
// device-management has not yet succeeded: a table that is merely empty cannot be told from a
// table nobody has filled, and publishing is an actuation. One tenant's failing read holds only
// that tenant. Once a tenant has been read, a later failure leaves its record in use rather than
// stopping it. Without a source (an instance that cannot reach device-management) the gate runs
// on the roster facts alone, loudly, in main.
func (cproc *CommandDeliveryProcessor) gateReady(tenant string) bool {
	if cproc.DisabledSource == nil {
		return true
	}
	cproc.gateMu.Lock()
	defer cproc.gateMu.Unlock()
	return cproc.gateBooted && !cproc.gatePending[tenant]
}

// disabledAmong answers which of the tenant's devices are disabled, and false when it cannot
// say: the caller then does nothing with those devices, because the safe action for "perhaps
// shut out" is to leave the command where it is.
func (cproc *CommandDeliveryProcessor) disabledAmong(ctx context.Context, tenant string, devices []string) (map[string]bool, bool) {
	if !cproc.gateReady(tenant) {
		return nil, false
	}
	reader := cproc.gateReader()
	if reader == nil {
		return nil, true
	}
	disabled, err := reader.DisabledAmong(ctx, devices)
	if err != nil {
		incr(cproc.DisabledGateErrors, 1)
		log.Warn().Err(err).Int("devices", len(devices)).
			Msg("Could not read the disabled-device record; the affected commands are left where they are.")
		return nil, false
	}
	return disabled, true
}

// withholdDisabled holds the commands of disabled devices and returns the rest. ok is false when
// the batch must not be touched at all.
//
// The commands it holds go QUEUED to HELD, not back to the sweep's set on every tick: a disabled
// device's backlog can be as large as its tenant's ceiling and sit there until the device is
// enabled, and QUEUED is the set the sweep reads whole every tick. HELD is the state that
// already means "the platform is deliberately withholding this".
func (cproc *CommandDeliveryProcessor) withholdDisabled(ctx context.Context, tenant string, commands []*model.Command) ([]*model.Command, bool) {
	disabled, ok := cproc.disabledAmong(ctx, tenant, distinctDevices(commands))
	if !ok {
		return nil, false
	}
	if len(disabled) == 0 {
		return commands, true
	}
	rest := make([]*model.Command, 0, len(commands))
	for _, cmd := range commands {
		if disabled[cmd.DeviceToken] {
			cproc.holdCommand(ctx, cmd)
			continue
		}
		rest = append(rest, cmd)
	}
	return rest, true
}

// ResumeDevice is what an enable does: it returns the device's withheld commands to the queue
// and dispatches the front of the backlog now rather than at the sweep's next tick.
//
// It goes through deliverTenantBatch, so every gate the sweep applies (tenant lifecycle,
// presence, and the disabled record itself, in case the device was disabled again) applies here,
// and through the claim, so a sweep reaching the same rows loses cleanly. Oldest first, and
// bounded: the rest is the sweep's.
func (cproc *CommandDeliveryProcessor) ResumeDevice(ctx context.Context, tenant, deviceToken string) {
	reader := cproc.gateReader()
	if reader == nil {
		return
	}
	tenantCtx := core.WithTenant(ctx, tenant)
	released, err := reader.ReleaseHeldForDevice(tenantCtx, deviceToken)
	if err != nil {
		log.Error().Err(err).Str("tenant", tenant).Str("device", deviceToken).
			Msg("Could not release the commands held for an enabled device; the hold reconciler will.")
		return
	}
	incr(cproc.HoldsReleased, float64(released))
	queued, err := reader.QueuedCommandsForDevice(tenantCtx, deviceToken, resumeLimit)
	if err != nil {
		log.Warn().Err(err).Str("tenant", tenant).Str("device", deviceToken).
			Msg("Could not read an enabled device's backlog; the delivery sweep will.")
		return
	}
	for _, batch := range groupByTenant(queued, cproc.tenantDeleted) {
		cproc.deliverTenantBatch(ctx, batch, pathNudge)
	}
}

// syncDisabledGate corrects the disabled-device record to device-management's list, for every
// tenant, and resumes each device the correction found enabled. It is a no-op without a source.
//
// A tenant that cannot be read fails the pass (the next one retries) but does not stop the
// tenants after it: one tenant's trouble must not hold every other tenant's commands. The
// process counts as synced only when EVERY tenant was read, since dispatch for an unread tenant
// would run against an unfilled record.
func (cproc *CommandDeliveryProcessor) syncDisabledGate(ctx context.Context) error {
	if cproc.DisabledSource == nil {
		return nil
	}
	writer, ok := cproc.Api.(disabledGateWriter)
	if !ok {
		return fmt.Errorf("the Api cannot record disabled devices")
	}
	tenants, err := cproc.DisabledSource.Tenants(ctx)
	if err != nil {
		return err
	}
	// The first pass learns which tenants exist and marks every one as not yet read; each is
	// cleared as its read succeeds. A tenant created later is not in the set, so it dispatches
	// at once (it has no history to have missed, and its facts keep the record current).
	cproc.gateMu.Lock()
	if !cproc.gateBooted {
		cproc.gatePending = make(map[string]bool, len(tenants))
		for _, tenant := range tenants {
			cproc.gatePending[tenant] = true
		}
		cproc.gateBooted = true
	}
	cproc.gateMu.Unlock()

	var failed int
	for _, tenant := range tenants {
		if cproc.tenantDeleted(tenant) {
			continue
		}
		readStarted := time.Now()
		disabled, err := cproc.DisabledSource.DisabledDevices(ctx, tenant)
		if err != nil {
			failed++
			log.Warn().Err(err).Str("tenant", tenant).Msg("Could not read a tenant's disabled devices.")
			continue
		}
		tenantCtx := core.WithTenant(ctx, tenant)
		enabled, err := writer.ReconcileDisabled(tenantCtx, disabled, readStarted)
		if err != nil {
			failed++
			log.Warn().Err(err).Str("tenant", tenant).Msg("Could not correct a tenant's disabled-device record.")
			continue
		}
		cproc.gateMu.Lock()
		delete(cproc.gatePending, tenant)
		cproc.gateMu.Unlock()
		for _, device := range enabled {
			cproc.ResumeDevice(ctx, tenant, device)
		}
	}
	if failed > 0 {
		incr(cproc.DisabledGateErrors, 1)
		return fmt.Errorf("%d of %d tenants could not be synced", failed, len(tenants))
	}
	return nil
}

// runGateSyncTicker syncs at start, retries until the first sync succeeds, then corrects on a
// slow cadence.
func (cproc *CommandDeliveryProcessor) runGateSyncTicker(ctx context.Context) {
	if cproc.DisabledSource == nil {
		return
	}
	for {
		wait := cproc.gateSyncWait()
		if err := cproc.syncDisabledGate(ctx); err != nil {
			log.Warn().Err(err).Msg("Disabled-device sync failed; commands are held back until one succeeds.")
			wait = cproc.gateRetryWait()
		}
		select {
		case <-ctx.Done():
			return
		case <-cproc.quit:
			return
		case <-time.After(wait):
		}
	}
}

func (cproc *CommandDeliveryProcessor) gateSyncWait() time.Duration {
	if cproc.gateSyncEvery > 0 {
		return cproc.gateSyncEvery
	}
	return gateSyncInterval
}

func (cproc *CommandDeliveryProcessor) gateRetryWait() time.Duration {
	if cproc.gateRetryEvery > 0 {
		return cproc.gateRetryEvery
	}
	return gateRetryInterval
}
