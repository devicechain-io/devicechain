// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"reflect"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestBothGatewaySourcesWirePlacementHolds(t *testing.T) {
	saved, savedFallback := PlacementHeldGauge, PlacementHoldFallbackCounter
	t.Cleanup(func() { PlacementHeldGauge, PlacementHoldFallbackCounter = saved, savedFallback })
	PlacementHoldFallbackCounter = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "total_placement_hold_fallback"}, []string{"source", "reason"})
	PlacementHeldGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "placement_held_messages"}, []string{"source"})
	buildInMeteringMode(t, IngestMeteringLegacy)
	for name, source := range map[string]interface{}{"capture": GatewaySource, "message-store": StoreSource} {
		fields := reflect.ValueOf(source).Elem()
		check := fields.FieldByName("placementCheck")
		require.False(t, check.IsNil(), "%s source needs placement recovery wired", name)
		require.Equal(t, reflect.ValueOf(inboundPlacementCheck).Pointer(), check.Pointer(), "%s must use the tenant placement check", name)
		require.False(t, fields.FieldByName("placementHeld").IsNil(), "%s needs the held-message gauge", name)
	}
}
