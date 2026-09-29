// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-lwm2m-ingest/config"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/require"
)

// The ONE construction site of this service's limiter must floor the sample burst at the
// per-event limit, the size of the pieces AdmitSamples charges. At the smallest ceiling a
// tenant can have (burst 1, so 25 sample tokens before the floor), a 300-sample Notify then
// admits one full event; a floor below 256 would admit none of it, on every Notify.
func TestBuildIngestLimiterFloorsTheSampleBurstAtOneEvent(t *testing.T) {
	limiter := buildIngestLimiter(nil, mscfg.InfrastructureConfiguration{},
		config.IngestRateLimit{MessagesPerSecond: 1e-9, Burst: 1}, nil)
	require.Equal(t, 256, limiter.AdmitSamples("acme", 300))
}
