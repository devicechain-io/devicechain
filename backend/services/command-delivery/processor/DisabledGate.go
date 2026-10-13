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
	// LiveDevices answers which of the named devices still exist in the tenant. A device that
	// is not disabled any more AND does not exist was deleted, not enabled.
	LiveDevices(ctx context.Context, tenant string, deviceTokens []string) ([]string, error)
}

// disabledGateWriter is the correction the sync applies, held by *model.Api.
type disabledGateWriter interface {
	ReconcileDisabled(ctx context.Context, disabled []string, readStarted time.Time) ([]string, error)
	// DatabaseNow is the one clock the record's timestamps are compared on.
	DatabaseNow(ctx context.Context) (time.Time, error)
	// TenantGateSynced and MarkTenantGateSynced are the shared "this tenant's record has been
	// filled once" marker.
	TenantGateSynced(ctx context.Context) (bool, error)
	MarkTenantGateSynced(ctx context.Context) error
	// CancelDeviceCommands discards what is still held for a device that no longer exists.
	CancelDeviceCommands(ctx context.Context, deviceToken string) (int64, error)
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

// gateReady reports whether dispatch for a tenant may proceed. With a DisabledSource wired it
// is false until the tenant's record has been filled once: a table that is merely empty cannot be
// told from a table nobody has filled, and publishing is an actuation.
//
// "Filled once" is read from the database (DeviceGateSync), so a restart does not close the gate
// again when earlier pods already filled the table, even while device-management or
// user-management is down. A tenant with no such marker waits for this process's first read of
// it, except a tenant created after that first pass, which has no history to have missed. One
// tenant's failing read holds only that tenant, and a later failure for a tenant already read
// leaves its record in use. Without a source (an instance that cannot reach device-management)
// the gate runs on the roster facts alone, loudly, in main.
func (cproc *CommandDeliveryProcessor) gateReady(ctx context.Context, tenant string) bool {
	if cproc.DisabledSource == nil {
		return true
	}
	cproc.gateMu.Lock()
	synced := cproc.gateSynced[tenant]
	booted, pending := cproc.gateBooted, cproc.gatePending[tenant]
	cproc.gateMu.Unlock()
	if synced {
		return true
	}
	if marker, ok := cproc.Api.(disabledGateWriter); ok {
		if found, err := marker.TenantGateSynced(core.WithTenant(ctx, tenant)); err == nil && found {
			cproc.rememberSynced(tenant)
			return true
		}
	}
	return booted && !pending
}

func (cproc *CommandDeliveryProcessor) rememberSynced(tenant string) {
	cproc.gateMu.Lock()
	if cproc.gateSynced == nil {
		cproc.gateSynced = make(map[string]bool)
	}
	cproc.gateSynced[tenant] = true
	cproc.gateMu.Unlock()
}

// publishGateGauges reports how far the first sync has got: ready is 1 once this process has
// listed the tenants and read every one of them, and pending is how many it is still waiting on.
func (cproc *CommandDeliveryProcessor) publishGateGauges() {
	if cproc.DisabledSource == nil {
		return
	}
	cproc.gateMu.Lock()
	booted, pending := cproc.gateBooted, len(cproc.gatePending)
	cproc.gateMu.Unlock()
	if cproc.DisabledGatePending != nil {
		cproc.DisabledGatePending.Set(float64(pending))
	}
	if cproc.DisabledGateReady != nil {
		ready := 0.0
		if booted && pending == 0 {
			ready = 1
		}
		cproc.DisabledGateReady.Set(ready)
	}
}

// disabledAmong answers which of the tenant's devices are disabled, and false when it cannot
// say: the caller then does nothing with those devices, because the safe action for "perhaps
// shut out" is to leave the command where it is.
func (cproc *CommandDeliveryProcessor) disabledAmong(ctx context.Context, tenant string, devices []string) (map[string]bool, bool) {
	if !cproc.gateReady(ctx, tenant) {
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
func (cproc *CommandDeliveryProcessor) ResumeDevice(ctx context.Context, tenant, deviceToken string) error {
	reader := cproc.gateReader()
	if reader == nil {
		return nil
	}
	tenantCtx := core.WithTenant(ctx, tenant)
	released, err := reader.ReleaseHeldForDevice(tenantCtx, deviceToken)
	if err != nil {
		log.Error().Err(err).Str("tenant", tenant).Str("device", deviceToken).
			Msg("Could not release the commands held for an enabled device.")
		return err
	}
	incr(cproc.HoldsReleased, float64(released))
	queued, err := reader.QueuedCommandsForDevice(tenantCtx, deviceToken, resumeLimit)
	if err != nil {
		// Not returned: the commands are released, and the sweep dispatches them.
		log.Warn().Err(err).Str("tenant", tenant).Str("device", deviceToken).
			Msg("Could not read an enabled device's backlog; the delivery sweep will.")
		return nil
	}
	for _, batch := range groupByTenant(queued, cproc.tenantDeleted) {
		cproc.deliverTenantBatch(ctx, batch, pathNudge)
	}
	return nil
}

// syncDisabledGate corrects the disabled-device record to device-management's list, for every
// tenant, and resumes each device the correction found enabled. It is a no-op without a source.
//
// A tenant that cannot be read fails the pass (the next one retries) but does not stop the
// tenants after it: one tenant's trouble must not hold every other tenant's commands.
//
// 🔴 A DEVICE THAT LEFT THE LIST IS NOT NECESSARILY ENABLED. The list omits a device that was
// soft-deleted as readily as one that was enabled, so each device the correction flips is asked
// after (LiveDevices) before anything is done for it. An enabled one is resumed. A deleted one
// can never receive what it was holding, and leaving the commands to the hold reconciler would
// release them toward a device that is gone, so they are cancelled: the record then says they
// were called off rather than that they expired. If the question cannot be answered nothing is
// done for those devices; the hold reconciler releases the enabled ones' holds and the TTL ends
// the rest.
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
		incr(cproc.DisabledGateErrors, 1)
		cproc.publishGateGauges()
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
		if !cproc.syncTenant(ctx, writer, tenant) {
			failed++
		}
	}
	cproc.publishGateGauges()
	if failed > 0 {
		incr(cproc.DisabledGateErrors, 1)
		return fmt.Errorf("%d of %d tenants could not be synced", failed, len(tenants))
	}
	return nil
}

// syncTenant corrects one tenant's record and reports whether it succeeded.
func (cproc *CommandDeliveryProcessor) syncTenant(ctx context.Context, writer disabledGateWriter, tenant string) bool {
	tenantCtx := core.WithTenant(ctx, tenant)
	readStarted, err := writer.DatabaseNow(tenantCtx)
	if err != nil {
		log.Warn().Err(err).Str("tenant", tenant).Msg("Could not read the database clock for the disabled-device sync.")
		return false
	}
	disabled, err := cproc.DisabledSource.DisabledDevices(ctx, tenant)
	if err != nil {
		log.Warn().Err(err).Str("tenant", tenant).Msg("Could not read a tenant's disabled devices.")
		return false
	}
	flipped, err := writer.ReconcileDisabled(tenantCtx, disabled, readStarted)
	if err != nil {
		log.Warn().Err(err).Str("tenant", tenant).Msg("Could not correct a tenant's disabled-device record.")
		return false
	}
	cproc.gateMu.Lock()
	delete(cproc.gatePending, tenant)
	cproc.gateMu.Unlock()
	cproc.rememberSynced(tenant)
	if err := writer.MarkTenantGateSynced(tenantCtx); err != nil {
		// Costs only the restart shortcut: this process already knows the tenant is synced.
		log.Warn().Err(err).Str("tenant", tenant).Msg("Could not record that a tenant's disabled-device record was synced.")
	}
	if len(flipped) == 0 {
		return true
	}
	live, err := cproc.DisabledSource.LiveDevices(ctx, tenant, flipped)
	if err != nil {
		incr(cproc.DisabledGateErrors, 1)
		log.Warn().Err(err).Str("tenant", tenant).Int("devices", len(flipped)).
			Msg("Could not tell enabled devices from deleted ones; leaving their held commands to the hold reconciler.")
		return true
	}
	exists := make(map[string]bool, len(live))
	for _, device := range live {
		exists[device] = true
	}
	for _, device := range flipped {
		if exists[device] {
			if err := cproc.ResumeDevice(ctx, tenant, device); err != nil {
				log.Warn().Err(err).Str("device", device).Msg("Could not resume a device the sync found enabled.")
			}
			continue
		}
		if cancelled, err := writer.CancelDeviceCommands(tenantCtx, device); err != nil {
			log.Warn().Err(err).Str("device", device).Msg("Could not cancel the commands held for a deleted device.")
		} else if cancelled > 0 {
			log.Info().Str("tenant", tenant).Str("device", device).Int64("cancelled", cancelled).
				Msg("Cancelled the commands held for a device that no longer exists.")
		}
	}
	return true
}

// runGateSyncTicker syncs at start, retries until the first sync succeeds, then corrects on a
// slow cadence.
func (cproc *CommandDeliveryProcessor) runGateSyncTicker(ctx context.Context) {
	if cproc.DisabledSource == nil {
		return
	}
	cproc.publishGateGauges()
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
