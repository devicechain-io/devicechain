// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// PromMetrics counts downlinks by result. The label is a fixed enum (Results): never a tenant,
// device or connector.
type PromMetrics struct{ downlinks, acks *prometheus.CounterVec }

// NewPromMetrics registers the counter and creates every result's child at 0, so a rate over the
// series has a baseline. Call it once, from the initialize phase.
func NewPromMetrics(ms *core.Microservice) *PromMetrics {
	if ms == nil {
		return nil
	}
	v := ms.NewCounterVec("lpwan_downlinks_total",
		"LoRaWAN command downlinks by result: queued (the network server accepted it), queued_confirmed (accepted, the command waits for the acknowledgement), pending_failed (accepted but not trackable), the refused_* results (answered not delivered, before any call), unsent (the network server or the egress policy refused it), unanswered (the request may have been queued), not_served (another transport's command), and the faults claim_failed, lookup_failed and response_failed.",
		[]string{"result"})
	for _, r := range Results {
		v.WithLabelValues(r)
	}
	a := ms.NewCounterVec("lpwan_ack_reports_total",
		"Network-server reports about confirmed LoRaWAN downlinks by result: acknowledged (the command succeeded), timed_out (the network server gave up waiting), transmitted (a gateway took the frame), deadline (no report arrived and the wait ran out), and the dropped or faulted results unmatched, mismatch, poison, store_failed and response_failed. unmatched and mismatch are anomalies worth alerting on.",
		[]string{"result"})
	for _, r := range AckResults {
		a.WithLabelValues(r)
	}
	return &PromMetrics{downlinks: v, acks: a}
}

// CountAck implements AckMetrics.
func (m *PromMetrics) CountAck(result string) {
	if m == nil {
		return
	}
	m.acks.WithLabelValues(result).Inc()
}

// Count implements Metrics.
func (m *PromMetrics) Count(result string) {
	if m == nil {
		return
	}
	m.downlinks.WithLabelValues(result).Inc()
}
