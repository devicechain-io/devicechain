// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run the REAL command API over a real (sqlite) database: what is being measured is
// a command's state across a disable, an enable and a sweep, and a fake that answered each write
// from a field would agree with any predicate at all.

func gateTenantCtx() context.Context { return core.WithTenant(context.Background(), "acme") }

// queueFor enqueues one command for a device and returns it.
func queueFor(t *testing.T, api *model.Api, token, device string) *model.Command {
	t.Helper()
	created, err := api.CreateCommand(gateTenantCtx(), &model.CommandCreateRequest{Token: token, DeviceToken: device, Name: "reboot"})
	require.NoError(t, err)
	return created
}

func gateStatusOf(t *testing.T, api *model.Api, token string) string {
	t.Helper()
	found, err := api.CommandsByToken(gateTenantCtx(), []string{token})
	require.NoError(t, err)
	require.Len(t, found, 1)
	return found[0].Status
}

func sweepOnce(t *testing.T, proc *CommandDeliveryProcessor) {
	t.Helper()
	require.NoError(t, proc.deliverPendingCommands(context.Background()))
}

// A command queued for a device that is then disabled is held by the sweep: not sent, not
// cancelled. Another device's command in the same batch still goes out.
func TestTheSweepHoldsADisabledDevicesCommands(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer}
	queueFor(t, api, "c-off-1", "dev-off")
	queueFor(t, api, "c-off-2", "dev-off")
	queueFor(t, api, "c-on", "dev-on")
	_, err := api.SetDeviceDisabled(gateTenantCtx(), "dev-off", true)
	require.NoError(t, err)

	sweepOnce(t, proc)

	assert.Equal(t, []string{"dev-on"}, writer.devices, "only the enabled device's command is published")
	assert.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-off-1"))
	assert.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-off-2"))
	assert.Equal(t, model.CommandSent.String(), gateStatusOf(t, api, "c-on"))

	// A second sweep neither dispatches nor disturbs what is held.
	sweepOnce(t, proc)
	assert.Equal(t, []string{"dev-on"}, writer.devices)
	assert.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-off-1"))
}

// The dispatch nudge is the other path to a publish, and it applies the same gate.
func TestTheNudgeDoesNotDispatchToADisabledDevice(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer}
	queueFor(t, api, "c-1", "dev-off")
	_, err := api.SetDeviceDisabled(gateTenantCtx(), "dev-off", true)
	require.NoError(t, err)

	proc.DrainDevice(context.Background(), "acme", "dev-off")

	assert.Empty(t, writer.devices)
	assert.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-1"))
}

// A dispatch that read the batch before the device was disabled still loses at the claim: the
// record is consulted by the UPDATE itself, so no path that reaches deliverCommand can send.
func TestAStaleBatchCannotDispatchToADeviceDisabledSinceItWasRead(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer}
	created := queueFor(t, api, "c-1", "dev-off")
	_, err := api.SetDeviceDisabled(gateTenantCtx(), "dev-off", true)
	require.NoError(t, err)

	require.NoError(t, proc.deliverCommand(context.Background(), created, pathSweep, routing{}))

	assert.Empty(t, writer.devices, "the claim was refused, so nothing may be published")
	assert.Equal(t, model.CommandQueued.String(), gateStatusOf(t, api, "c-1"))
}

// Enabling the device (the roster fact's enabled flag) releases what is held and dispatches it
// at once, oldest first, without waiting for the sweep.
func TestEnablingADeviceDispatchesItsHeldCommandsInOrder(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer}
	queueFor(t, api, "c-1", "dev-off")
	queueFor(t, api, "c-2", "dev-off")
	rc := newTestRosterConsumer(api, proc)

	rc.handle(context.Background(), rosterMsg(t, "dev-off", true, false)) // disable
	sweepOnce(t, proc)
	require.Empty(t, writer.devices)
	require.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-1"))

	rc.handle(context.Background(), rosterMsg(t, "dev-off", false, true)) // enable

	assert.Equal(t, []string{"dev-off", "dev-off"}, writer.devices)
	assert.Equal(t, model.CommandSent.String(), gateStatusOf(t, api, "c-1"))
	assert.Equal(t, model.CommandSent.String(), gateStatusOf(t, api, "c-2"))
	var order []string
	for _, m := range writer.messages {
		order = append(order, string(m.Key))
	}
	assert.Equal(t, []string{"c-1", "c-2"}, order, "the backlog goes out oldest first")
}

// The hold reconciler's question is "is the device still absent?", and a disabled device that
// is present answers no -- but it must NOT be released. Only an enable releases it.
func TestTheHoldReconcilerDoesNotReleaseADisabledDevicesHold(t *testing.T) {
	api := realApi(t)
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: &recordingWriter{},
		Presence: &scriptedReader{states: map[string]presence.State{
			"dev-off": asserted(true, mqttSource), "dev-back": asserted(true, mqttSource)}}}
	off := queueFor(t, api, "c-off", "dev-off")
	back := queueFor(t, api, "c-back", "dev-back")
	for _, c := range []*model.Command{off, back} {
		held, err := api.HoldCommand(gateTenantCtx(), c.ID)
		require.NoError(t, err)
		require.True(t, held)
	}
	_, err := api.SetDeviceDisabled(gateTenantCtx(), "dev-off", true)
	require.NoError(t, err)

	require.NoError(t, proc.reconcileOnePage(context.Background()))

	assert.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-off"), "a disabled device's hold is not the presence reconciler's to release")
	assert.Equal(t, model.CommandQueued.String(), gateStatusOf(t, api, "c-back"), "a returned device's hold still is")
}

// A command held for a disabled device still lapses by its TTL while held.
func TestAHeldCommandStillExpiresByItsTTL(t *testing.T) {
	api := realApi(t)
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: &recordingWriter{}}
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	_, err := api.CreateCommand(gateTenantCtx(), &model.CommandCreateRequest{Token: "lapsing", DeviceToken: "dev-off", Name: "x", ExpiresAt: &past})
	require.NoError(t, err)
	_, err = api.SetDeviceDisabled(gateTenantCtx(), "dev-off", true)
	require.NoError(t, err)
	sweepOnce(t, proc)
	require.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "lapsing"))

	_, _, err = api.ExpireStale(core.WithSystemContext(gateTenantCtx()), time.Now())
	require.NoError(t, err)

	assert.Equal(t, model.CommandExpired.String(), gateStatusOf(t, api, "lapsing"))
}

// fakeSource is the correction source: a fixed list of tenants and disabled devices.
type fakeSource struct {
	mu       sync.Mutex
	tenants  []string
	disabled map[string][]string
	fail     map[string]error
	tenantsE error
	// deleted names devices LiveDevices reports as gone; liveErr makes the lookup fail.
	deleted map[string]bool
	liveErr error
}

func (f *fakeSource) LiveDevices(_ context.Context, _ string, tokens []string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.liveErr != nil {
		return nil, f.liveErr
	}
	var live []string
	for _, t := range tokens {
		if !f.deleted[t] {
			live = append(live, t)
		}
	}
	return live, nil
}

func (f *fakeSource) Tenants(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tenants, f.tenantsE
}

func (f *fakeSource) DisabledDevices(_ context.Context, tenant string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[tenant]; err != nil {
		return nil, err
	}
	return f.disabled[tenant], nil
}

// A device disabled BEFORE this service ever heard a roster fact (the durable starts at the
// stream tail) must still be held: nothing is dispatched until the first sync has read
// device-management, and the sync then holds the pre-disabled device and releases the rest.
func TestStartupHoldsADeviceDisabledBeforeTheServiceStarted(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	src := &fakeSource{tenants: []string{"acme"}, disabled: map[string][]string{"acme": {"dev-off"}}}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer, DisabledSource: src}
	queueFor(t, api, "c-off", "dev-off")
	queueFor(t, api, "c-on", "dev-on")

	sweepOnce(t, proc)
	assert.Empty(t, writer.devices, "nothing is published before the first sync has read the disabled list")
	assert.Equal(t, model.CommandQueued.String(), gateStatusOf(t, api, "c-on"))

	require.NoError(t, proc.syncDisabledGate(context.Background()))
	sweepOnce(t, proc)

	assert.Equal(t, []string{"dev-on"}, writer.devices)
	assert.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-off"), "the pre-disabled device's command is held")
}

// One tenant whose first read fails holds only that tenant; others dispatch.
func TestAFailingTenantReadHoldsOnlyThatTenant(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	src := &fakeSource{tenants: []string{"acme", "other"}, disabled: map[string][]string{},
		fail: map[string]error{"other": errors.New("device-management is down")}}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer, DisabledSource: src}
	queueFor(t, api, "c-acme", "dev-1")
	_, err := api.CreateCommand(core.WithTenant(context.Background(), "other"),
		&model.CommandCreateRequest{Token: "c-other", DeviceToken: "dev-1", Name: "reboot"})
	require.NoError(t, err)

	require.Error(t, proc.syncDisabledGate(context.Background()), "the pass reports the tenant it could not read")
	sweepOnce(t, proc)

	assert.Equal(t, []string{"dev-1"}, writer.devices, "only acme's command is published")
	assert.Equal(t, model.CommandSent.String(), gateStatusOf(t, api, "c-acme"))
	found, err := api.CommandsByToken(core.WithTenant(context.Background(), "other"), []string{"c-other"})
	require.NoError(t, err)
	assert.Equal(t, model.CommandQueued.String(), found[0].Status, "the tenant that could not be read waits")
}

// The sync that finds a device enabled (a lost enable fact) resumes it.
func TestTheSyncResumesADeviceItFindsEnabled(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	src := &fakeSource{tenants: []string{"acme"}, disabled: map[string][]string{"acme": {"dev-off"}}}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer, DisabledSource: src}
	queueFor(t, api, "c-1", "dev-off")
	require.NoError(t, proc.syncDisabledGate(context.Background()))
	sweepOnce(t, proc)
	require.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-1"))
	time.Sleep(5 * time.Millisecond)

	src.mu.Lock()
	src.disabled["acme"] = nil
	src.mu.Unlock()
	require.NoError(t, proc.syncDisabledGate(context.Background()))

	assert.Equal(t, []string{"dev-off"}, writer.devices)
	assert.Equal(t, model.CommandSent.String(), gateStatusOf(t, api, "c-1"))
}

// Without a source (the degraded mode main logs) the record rests on facts alone and dispatch is
// not held up waiting for a sync that cannot happen.
func TestWithoutASourceDispatchDoesNotWaitForASync(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer}
	queueFor(t, api, "c-1", "dev-1")

	sweepOnce(t, proc)

	assert.Equal(t, []string{"dev-1"}, writer.devices)
}

// ---- roster consumer ----

type gateAck struct{ n int }

func (a *gateAck) Ack() error { a.n++; return nil }

func newTestRosterConsumer(api *model.Api, proc *CommandDeliveryProcessor) *RosterConsumer {
	ms := &core.Microservice{InstanceId: "inst", FunctionalArea: "command-delivery"}
	return NewRosterConsumer(ms, nil, core.NewNoOpLifecycleCallbacks(), api, proc, nil)
}

func rosterMsg(t *testing.T, device string, disabled, enabled bool) messaging.Message {
	t.Helper()
	b, err := dmproto.MarshalDeviceRosterEvent(&dmmodel.DeviceRosterEvent{DeviceToken: device, Disabled: disabled, Enabled: enabled})
	require.NoError(t, err)
	return messaging.NewConsumedMessage("inst.acme.device-roster", b, 1, nil, &gateAck{})
}

// A fact that cannot be used is dropped and acked; a failed write is left for redelivery.
func TestTheRosterConsumerDropsWhatItCannotUseAndRetriesWhatFailed(t *testing.T) {
	api := realApi(t)
	rc := newTestRosterConsumer(api, nil)

	ack := &gateAck{}
	rc.handle(context.Background(), messaging.NewConsumedMessage("inst.acme.device-roster", []byte("not-proto\xff"), 1, nil, ack))
	assert.Equal(t, 1, ack.n)
	ack = &gateAck{}
	rc.handle(context.Background(), messaging.NewConsumedMessage("no-tenant", nil, 1, nil, ack))
	assert.Equal(t, 1, ack.n)

	failing := newTestRosterConsumer(nil, nil)
	failing.Api = failingRosterApi{}
	ack = &gateAck{}
	msg := rosterMsg(t, "dev-1", true, false)
	failing.handle(context.Background(), messaging.NewConsumedMessage(msg.Subject, msg.Value, 1, nil, ack))
	assert.Zero(t, ack.n, "a transient failure must be left for redelivery")
}

type failingRosterApi struct{}

func (failingRosterApi) SetDeviceDisabled(context.Context, string, bool) (bool, error) {
	return false, errors.New("database is down")
}
