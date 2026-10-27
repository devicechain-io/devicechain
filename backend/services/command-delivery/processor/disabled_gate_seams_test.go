// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gateGauges() DeliveryMetrics {
	return DeliveryMetrics{
		DisabledGateErrors:  prometheus.NewCounter(prometheus.CounterOpts{Name: "e_total"}),
		DisabledGateReady:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "ready"}),
		DisabledGatePending: prometheus.NewGauge(prometheus.GaugeOpts{Name: "pending"}),
	}
}

// A tenant that is deleted is skipped by the sync, and before this it also stayed in the pending
// set for the life of the process: pending stuck at 1 and ready at 0, an outage that was not
// happening. It must leave the set.
func TestADeletedTenantDoesNotHoldTheGateGaugesNotReady(t *testing.T) {
	api := realApi(t)
	src := &fakeSource{tenants: []string{"acme", "gone"}, disabled: map[string][]string{}}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: &recordingWriter{}, DisabledSource: src,
		DeliveryMetrics: gateGauges(), TenantDeleted: func(tenant string) bool { return tenant == "gone" }}

	require.NoError(t, proc.syncDisabledGate(context.Background()))

	assert.Equal(t, 0.0, testutil.ToFloat64(proc.DisabledGatePending), "a deleted tenant is still counted as pending")
	assert.Equal(t, 1.0, testutil.ToFloat64(proc.DisabledGateReady), "a deleted tenant holds the gate not-ready")
}

// A tenant that disappears from a later listing leaves the set too; a tenant still listed and
// still failing stays — so the prune is not simply "clear the set".
func TestATenantThatVanishesFromTheListingLeavesThePendingSet(t *testing.T) {
	api := realApi(t)
	src := &fakeSource{tenants: []string{"acme", "flaky", "doomed"}, disabled: map[string][]string{},
		fail: map[string]error{"flaky": errors.New("down"), "doomed": errors.New("down")}}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: &recordingWriter{}, DisabledSource: src,
		DeliveryMetrics: gateGauges()}

	require.Error(t, proc.syncDisabledGate(context.Background()))
	require.Equal(t, 2.0, testutil.ToFloat64(proc.DisabledGatePending), "control: both failing tenants wait")

	src.mu.Lock()
	src.tenants = []string{"acme", "flaky"} // "doomed" was purged
	src.mu.Unlock()
	require.Error(t, proc.syncDisabledGate(context.Background()))

	assert.Equal(t, 1.0, testutil.ToFloat64(proc.DisabledGatePending), "only the listed, still-failing tenant waits")
	assert.Equal(t, 0.0, testutil.ToFloat64(proc.DisabledGateReady))
}

// A claim the disabled record refused is not a lost claim: it is counted and logged as what it
// is, and a genuine lost claim still lands on ClaimsLost.
func TestAClaimRefusedForADisabledDeviceIsNotCountedAsLost(t *testing.T) {
	api := realApi(t)
	lost := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "lost_total"}, []string{"path"})
	refused := prometheus.NewCounter(prometheus.CounterOpts{Name: "refused_total"})
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: &recordingWriter{},
		DeliveryMetrics: DeliveryMetrics{ClaimsLost: lost, ClaimsRefusedDisabled: refused}}

	off := queueFor(t, api, "c-off", "dev-off")
	_, err := api.SetDeviceDisabled(gateTenantCtx(), "dev-off", true)
	require.NoError(t, err)
	require.NoError(t, proc.deliverCommand(context.Background(), off, pathSweep, routing{}))

	assert.Equal(t, 1.0, testutil.ToFloat64(refused), "the disabled refusal is not counted as one")
	assert.Equal(t, 0.0, testutil.ToFloat64(lost.WithLabelValues(string(pathSweep))),
		"a disabled refusal was counted as another dispatcher winning the claim")

	// A genuine lost claim: another dispatcher already took the row.
	on := queueFor(t, api, "c-on", "dev-on")
	_, claimed, err := api.MarkSent(gateTenantCtx(), on.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, proc.deliverCommand(context.Background(), on, pathSweep, routing{}))

	assert.Equal(t, 1.0, testutil.ToFloat64(lost.WithLabelValues(string(pathSweep))), "a real lost claim is still counted")
	assert.Equal(t, 1.0, testutil.ToFloat64(refused), "a real lost claim was counted as a disabled refusal")
	assert.Equal(t, model.CommandQueued.String(), gateStatusOf(t, api, "c-off"))
}
