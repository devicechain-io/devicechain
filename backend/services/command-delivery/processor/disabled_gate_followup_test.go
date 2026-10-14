// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeResumer struct {
	err   error
	calls atomic.Int32
}

func (f *fakeResumer) ResumeDevice(context.Context, string, string) error {
	f.calls.Add(1)
	return f.err
}

// A resume that fails leaves the enable fact UNACKED: by then the record already reads enabled, so
// the sync has no flip to act on and an acked fact would strand the held commands.
func TestAFailedResumeLeavesTheEnableFactForRedelivery(t *testing.T) {
	api := realApi(t)
	resumer := &fakeResumer{err: errors.New("database is down")}
	rc := newTestRosterConsumer(api, nil)
	rc.Resumer = resumer
	ack := &gateAck{}
	msg := rosterMsg(t, "dev-1", false, true)

	rc.handle(context.Background(), messaging.NewConsumedMessage(msg.Subject, msg.Value, 1, nil, ack))

	assert.Equal(t, int32(1), resumer.calls.Load())
	assert.Zero(t, ack.n, "the fact must be redelivered, not acked over a failed resume")

	resumer.err = nil
	rc.handle(context.Background(), messaging.NewConsumedMessage(msg.Subject, msg.Value, 2, nil, ack))
	assert.Equal(t, 1, ack.n, "once the resume succeeds the fact is acked")
}

// The toggle flag was lost (a producer that predates it), but the record knows the device was
// disabled and the fact says it no longer is: that is an enable, and it resumes.
func TestAnEnableIsRecognisedFromTheRecordWhenTheToggleFlagIsLost(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer}
	queueFor(t, api, "c-1", "dev-off")
	rc := newTestRosterConsumer(api, proc)
	rc.handle(context.Background(), rosterMsg(t, "dev-off", true, false))
	sweepOnce(t, proc)
	require.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-1"))

	rc.handle(context.Background(), rosterMsg(t, "dev-off", false, false)) // enabled flag absent

	assert.Equal(t, []string{"dev-off"}, writer.devices)
	assert.Equal(t, model.CommandSent.String(), gateStatusOf(t, api, "c-1"))
}

// A fact that is neither an enable nor a state change resumes nothing.
func TestAFactThatChangesNothingResumesNothing(t *testing.T) {
	api := realApi(t)
	resumer := &fakeResumer{}
	rc := newTestRosterConsumer(api, nil)
	rc.Resumer = resumer
	rc.handle(context.Background(), rosterMsg(t, "dev-1", false, false))
	assert.Zero(t, resumer.calls.Load())
}

// The device is absent when it is enabled. Its commands are released and then withheld again by
// the presence gate -- HELD, nothing published -- and are not sent into the dark.
func TestEnablingAnAbsentDeviceHoldsItsCommandsAgain(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer,
		Presence: &scriptedReader{states: map[string]presence.State{"dev-off": asserted(false, mqttSource)}}}
	queueFor(t, api, "c-1", "dev-off")
	rc := newTestRosterConsumer(api, proc)
	rc.handle(context.Background(), rosterMsg(t, "dev-off", true, false))
	sweepOnce(t, proc)

	rc.handle(context.Background(), rosterMsg(t, "dev-off", false, true))

	assert.Empty(t, writer.devices, "an absent device is not published to")
	assert.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-1"))
}

// gateFailingApi makes the disabled-device read fail.
type gateFailingApi struct{ *model.Api }

func (gateFailingApi) DisabledAmong(context.Context, []string) (map[string]bool, error) {
	return nil, errors.New("database is down")
}

// When the record cannot be read the batch is left exactly as it is: nothing is published,
// nothing is held or failed, and the failure is counted.
func TestAnUnreadableRecordLeavesTheBatchUntouched(t *testing.T) {
	real := realApi(t)
	writer := &recordingWriter{}
	proc := &CommandDeliveryProcessor{Api: gateFailingApi{real}, DeviceCommandsWriter: writer,
		DeliveryMetrics: DeliveryMetrics{DisabledGateErrors: prometheus.NewCounter(prometheus.CounterOpts{Name: "gate_errors_total"})}}
	queueFor(t, real, "c-1", "dev-1")

	sweepOnce(t, proc)

	assert.Empty(t, writer.devices)
	assert.Equal(t, model.CommandQueued.String(), gateStatusOf(t, real, "c-1"))
	assert.Equal(t, 1.0, testutil.ToFloat64(proc.DisabledGateErrors))
}

// With no presence gate the hold reconciler used to skip outright, so a lost resume left an
// enabled device's commands HELD until their TTL. It now releases the holds of devices the record
// says are enabled, and only those.
func TestWithoutPresenceTheReconcilerReleasesAnEnabledDevicesHolds(t *testing.T) {
	api := realApi(t)
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: &recordingWriter{}}
	for _, d := range []string{"dev-on", "dev-off", "dev-unknown"} {
		c := queueFor(t, api, "c-"+d, d)
		held, err := api.HoldCommand(gateTenantCtx(), c.ID)
		require.NoError(t, err)
		require.True(t, held)
	}
	_, err := api.SetDeviceDisabled(gateTenantCtx(), "dev-on", true)
	require.NoError(t, err)
	_, err = api.SetDeviceDisabled(gateTenantCtx(), "dev-on", false) // enabled; the resume was lost
	require.NoError(t, err)
	_, err = api.SetDeviceDisabled(gateTenantCtx(), "dev-off", true)
	require.NoError(t, err)

	require.NoError(t, proc.reconcileOnePage(context.Background()))

	assert.Equal(t, model.CommandQueued.String(), gateStatusOf(t, api, "c-dev-on"))
	assert.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-dev-off"), "still disabled")
	assert.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-dev-unknown"), "never recorded")
}

// The record's "synced once" marker is shared, so a restart during an outage of the service the
// sync reads does not stop a tenant that earlier pods filled; a tenant never synced still waits.
func TestARestartDuringAnOutageDoesNotCloseTheGateOnASyncedTenant(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	src := &fakeSource{tenants: []string{"acme"}, disabled: map[string][]string{}}
	first := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer, DisabledSource: src}
	require.NoError(t, first.syncDisabledGate(context.Background()))

	down := &fakeSource{tenantsE: errors.New("user-management is down")}
	restarted := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer, DisabledSource: down,
		DeliveryMetrics: DeliveryMetrics{
			DisabledGateErrors:  prometheus.NewCounter(prometheus.CounterOpts{Name: "e_total"}),
			DisabledGateReady:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "ready"}),
			DisabledGatePending: prometheus.NewGauge(prometheus.GaugeOpts{Name: "pending"}),
		}}
	queueFor(t, api, "c-synced", "dev-1")
	_, err := api.CreateCommand(core.WithTenant(context.Background(), "fresh"),
		&model.CommandCreateRequest{Token: "c-fresh", DeviceToken: "dev-1", Name: "reboot"})
	require.NoError(t, err)

	require.Error(t, restarted.syncDisabledGate(context.Background()))
	sweepOnce(t, restarted)

	assert.Equal(t, model.CommandSent.String(), gateStatusOf(t, api, "c-synced"), "the tenant earlier pods synced still dispatches")
	found, err := api.CommandsByToken(core.WithTenant(context.Background(), "fresh"), []string{"c-fresh"})
	require.NoError(t, err)
	assert.Equal(t, model.CommandQueued.String(), found[0].Status, "a tenant never synced anywhere waits")
	assert.Equal(t, 1.0, testutil.ToFloat64(restarted.DisabledGateErrors), "the failed tenant list is counted")
	assert.Equal(t, 0.0, testutil.ToFloat64(restarted.DisabledGateReady), "and the gate reports it is not ready")
}

// A device that left the list because it was DELETED is not resumed: its held commands are
// cancelled. One that was enabled is resumed.
func TestTheSyncCancelsWhatADeletedDeviceWasHolding(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	src := &fakeSource{tenants: []string{"acme"}, disabled: map[string][]string{"acme": {"dev-gone", "dev-back"}}}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer, DisabledSource: src}
	queueFor(t, api, "c-gone", "dev-gone")
	queueFor(t, api, "c-back", "dev-back")
	require.NoError(t, proc.syncDisabledGate(context.Background()))
	sweepOnce(t, proc)
	require.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-gone"))
	time.Sleep(5 * time.Millisecond)

	src.mu.Lock()
	src.disabled["acme"] = nil
	src.deleted = map[string]bool{"dev-gone": true}
	src.mu.Unlock()
	require.NoError(t, proc.syncDisabledGate(context.Background()))

	assert.Equal(t, model.CommandCancelled.String(), gateStatusOf(t, api, "c-gone"))
	assert.Equal(t, model.CommandSent.String(), gateStatusOf(t, api, "c-back"))
	assert.Equal(t, []string{"dev-back"}, writer.devices, "nothing is published toward the deleted device")
}

// When the lookup that tells deleted from enabled fails, nothing is done for those devices.
func TestTheSyncDoesNothingForAFlippedDeviceItCannotClassify(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	src := &fakeSource{tenants: []string{"acme"}, disabled: map[string][]string{"acme": {"dev-1"}}}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer, DisabledSource: src}
	queueFor(t, api, "c-1", "dev-1")
	require.NoError(t, proc.syncDisabledGate(context.Background()))
	sweepOnce(t, proc)
	time.Sleep(5 * time.Millisecond)

	src.mu.Lock()
	src.disabled["acme"] = nil
	src.liveErr = errors.New("device-management is down")
	src.mu.Unlock()
	require.NoError(t, proc.syncDisabledGate(context.Background()))

	assert.Empty(t, writer.devices)
	assert.Equal(t, model.CommandHeld.String(), gateStatusOf(t, api, "c-1"))
}

// A tenant created after the periodic walk last ran gets its record filled, and its marker
// written, by its first roster fact instead of waiting for the walk's next turn.
func TestTheFirstFactOfAnUnsyncedTenantFillsItsRecord(t *testing.T) {
	api := realApi(t)
	src := &fakeSource{tenants: []string{"acme"}, disabled: map[string][]string{"acme": {"dev-old"}}}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: &recordingWriter{}, DisabledSource: src}
	rc := newTestRosterConsumer(api, proc)

	rc.handle(context.Background(), rosterMsg(t, "dev-new", true, false))

	synced, err := api.TenantGateSynced(gateTenantCtx())
	require.NoError(t, err)
	assert.True(t, synced, "the marker is written")
	got, err := api.DisabledAmong(gateTenantCtx(), []string{"dev-old", "dev-new"})
	require.NoError(t, err)
	assert.True(t, got["dev-old"], "the walk filled what the facts had missed")
	assert.True(t, got["dev-new"])
}
