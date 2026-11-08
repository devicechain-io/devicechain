// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// PromMetrics counts downlinks by result. The label is a fixed enum (Results): never a tenant,
// device or connector.
type PromMetrics struct{ downlinks *prometheus.CounterVec }

// NewPromMetrics registers the counter and creates every result's child at 0, so a rate over the
// series has a baseline. Call it once, from the initialize phase.
func NewPromMetrics(ms *core.Microservice) *PromMetrics {
	if ms == nil {
		return nil
	}
	v := ms.NewCounterVec("lpwan_downlinks_total",
		"LoRaWAN command downlinks by result: queued (the network server accepted it), the refused_* results (answered not delivered, before any call), unsent (the network server or the egress policy refused it), unanswered (the request may have been queued), not_served (another transport's command), and the faults claim_failed, lookup_failed and response_failed.",
		[]string{"result"})
	for _, r := range Results {
		v.WithLabelValues(r)
	}
	return &PromMetrics{downlinks: v}
}

// Count implements Metrics.
func (m *PromMetrics) Count(result string) {
	if m == nil {
		return
	}
	m.downlinks.WithLabelValues(result).Inc()
}
