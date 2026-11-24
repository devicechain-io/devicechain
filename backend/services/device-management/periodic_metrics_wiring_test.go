// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
)

// Build the real tasks without starting them: every outcome must exist before any pass runs.
func TestPeriodicPassMetricsWiring(t *testing.T) {
	prevMS, prevReaper, prevClassifier, prevMarkerHeal := Microservice, AgentProposalReaper, AlarmPositionClassifier, CreateEnrollMarkerHeal
	prevAccounting, prevAccountant := DecodeAccounting, DecodeAccountant
	prevConfigurationMaintenance := ConfigurationReportMaintenance
	prevOrphanSweep := AttributeOrphanSweep
	prevFleetExpirySweep := FleetExpirySweep
	t.Cleanup(func() {
		Microservice, AgentProposalReaper, AlarmPositionClassifier, CreateEnrollMarkerHeal = prevMS, prevReaper, prevClassifier, prevMarkerHeal
		DecodeAccounting, DecodeAccountant = prevAccounting, prevAccountant
		ConfigurationReportMaintenance = prevConfigurationMaintenance
		AttributeOrphanSweep = prevOrphanSweep
		FleetExpirySweep = prevFleetExpirySweep
	})
	Microservice = &core.Microservice{FunctionalArea: "device-management"}
	reg := prometheus.NewRegistry()
	Microservice.UseMetricsRegistry(reg)
	require.NoError(t, initAgentProposalReaper())
	require.NoError(t, initAlarmPositionClassifier())
	require.NoError(t, initCreateEnrollMarkerHeal())
	require.NoError(t, initAttributeOrphanSweep())
	require.NoError(t, initFleetExpirySweep())
	require.NoError(t, initDecodeAccounting())
	require.NoError(t, initConfigurationReportMaintenance())
	tasks := []string{"agent_proposal_reaper", "alarm_position_classifier", "create_enroll_marker_heal", "device_attribute_orphan_sweep", "fleet_expiry_sweep", "decode_accounting", "configuration_report_maintenance"}
	subsystem := "devicemanagement"

	families, err := reg.Gather()
	require.NoError(t, err)
	for _, task := range tasks {
		name := "devicechain_" + subsystem + "_" + task + "_passes_total"
		found := false
		for _, family := range families {
			if family.GetName() != name {
				continue
			}
			found = true
			outcomes := map[string]float64{}
			for _, metric := range family.GetMetric() {
				require.Len(t, metric.GetLabel(), 1, name)
				label := metric.GetLabel()[0]
				require.Equal(t, "outcome", label.GetName(), name)
				outcomes[label.GetValue()] = metric.GetCounter().GetValue()
			}
			require.Equal(t, map[string]float64{
				"complete": 0, "partial": 0, "failed": 0, "skipped": 0, "cancelled": 0,
			}, outcomes, name)
		}
		require.True(t, found, "%s is not registered", name)
	}
}
