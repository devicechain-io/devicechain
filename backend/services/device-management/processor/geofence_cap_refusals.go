// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"github.com/prometheus/client_golang/prometheus"
)

// GeoFenceCapRefusalCounter adapts a labelled Prometheus counter to the model's narrow
// refusal-counting seam, so the model package states the QUESTION (which cap refused) and
// nothing about how it is reported.
//
// 🔴 THE ONLY LABEL IS THE CAP, AND ADDING A TENANT LABEL HERE WOULD BE A DENIAL OF SERVICE
// ON THE MONITORING STACK. This is a multi-tenant instance; a per-tenant label is unbounded
// cardinality, one time series per tenant per cap, forever. Which of the three caps refused
// is a three-valued label and is the question that decides an operator's next action —
// whether to raise a tier's position ceiling, its fence count, or its budget. Which TENANT
// hit it is answered by the API error, which names the tenant's own number and the cost it
// bounds, at the point of refusal.
type GeoFenceCapRefusalCounter struct {
	refusals *prometheus.CounterVec
}

// NewGeoFenceCapRefusalCounter wraps the counter vector. A nil vector makes every count a
// no-op, so a test or a partly-wired service never has to guard the call.
func NewGeoFenceCapRefusalCounter(refusals *prometheus.CounterVec) *GeoFenceCapRefusalCounter {
	return &GeoFenceCapRefusalCounter{refusals: refusals}
}

// CountGeoFenceCapRefusal records one authoring call refused by the named cap.
func (c *GeoFenceCapRefusalCounter) CountGeoFenceCapRefusal(cap string) {
	if c == nil || c.refusals == nil {
		return
	}
	c.refusals.WithLabelValues(cap).Inc()
}
