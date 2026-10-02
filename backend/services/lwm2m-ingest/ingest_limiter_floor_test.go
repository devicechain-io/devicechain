// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-lwm2m-ingest/config"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/require"
)

// The ONE construction site of this service's limiter must floor the sample burst at the
// per-event limit, the size of the pieces AdmitSamples charges. At the smallest ceiling a
// tenant can have (burst 1), a 300-sample Notify then admits one full event; a floor below
// 256 would admit none of it, on every Notify.
func TestBuildIngestLimiterFloorsTheSampleBurstAtOneEvent(t *testing.T) {
	limiter := buildIngestLimiter(nil, mscfg.InfrastructureConfiguration{},
		config.IngestRateLimit{MessagesPerSecond: 1e-9, Burst: 1}, nil)
	require.Equal(t, 256, limiter.AdmitSamples("acme", 300))
}

// The sample budget IS the tenant's ingest ceiling, counted in readings — not 25 times it.
// At 10/s with a burst of 20, one full event fits the floored burst and the next 200 samples
// at once do not; and 200 ms later about 2 more readings have accrued, not 50. Scaled 25
// times (rate 250, burst 500) the second call admitted 200 and the third 50.
func TestLwM2MSampleBudgetIsTheTenantCeilingInReadings(t *testing.T) {
	limiter := buildIngestLimiter(nil, mscfg.InfrastructureConfiguration{},
		config.IngestRateLimit{MessagesPerSecond: 10, Burst: 20}, nil)
	require.Equal(t, 256, limiter.AdmitSamples("t", 256), "one full event fits the floored burst")
	require.Equal(t, 0, limiter.AdmitSamples("t", 200), "the burst is one event, not 25 times the tier's")
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, 0, limiter.AdmitSamples("t", 50), "200 ms at 10 readings/s accrues about 2, not 50")
}
