// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/adapter"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

// The service's own builder meters at the configured ceiling, in readings, floored at one
// full event: at 10/s with a burst of 20, a 300-reading message admits one event and the
// next admits nothing.
func TestBuildSampleLimiterMetersAtTheConfiguredCeiling(t *testing.T) {
	l := buildSampleLimiter(mscfg.InfrastructureConfiguration{}, config.IngestRateLimit{ReadingsPerSecond: 10, Burst: 20},
		adapter.IngestLimiterMetrics{}, nil)
	require.Equal(t, 256, l.AdmitSamples("acme", 300))
	require.Equal(t, 0, l.AdmitSamples("acme", 300))
}

// Every client the service builds to ingest is handed the service's limiter, and a source
// list given an ingest path with no limiter is refused rather than built unmetered.
func TestResolveSourcesGivesEachIngestingClientTheLimiter(t *testing.T) {
	cfg := &config.SparkplugConfiguration{Sources: []config.SparkplugSource{src("tcp://a:1883"), src("ssl://b:8883")}}
	ingester := host.NewIngester(nil, nil, host.IngestMetrics{})
	limiter := adapter.NewSampleLimiter(core.StaticCeiling(10, 20), adapter.IngestLimiterMetrics{}, nil)

	clients, err := resolveSources(cfg, "inst", ingester, limiter, nil, host.Metrics{})
	require.NoError(t, err)
	require.Len(t, clients, 2)
	for i, c := range clients {
		assert.Same(t, limiter, c.SampleLimiter(), "source %d's client was not given the service's limiter", i)
	}

	_, err = resolveSources(cfg, "inst", ingester, nil, nil, host.Metrics{})
	require.Error(t, err, "an ingest path with no ceiling must be refused, not built unmetered")
}

// The configuration defaults to the platform ceiling, floors a non-positive or NaN value to
// it per field, keeps a positive one, and is read from the key the chart renders.
func TestSparkplugIngestRateLimitDefaults(t *testing.T) {
	c := config.NewSparkplugConfiguration()
	assert.Equal(t, config.IngestRateLimit{ReadingsPerSecond: 1000, Burst: 2000}, c.IngestRateLimit)

	for _, bad := range []config.IngestRateLimit{{ReadingsPerSecond: -1, Burst: -1}, {ReadingsPerSecond: math.NaN()}} {
		c := &config.SparkplugConfiguration{IngestRateLimit: bad}
		c.ApplyDefaults()
		assert.Equal(t, config.IngestRateLimit{ReadingsPerSecond: 1000, Burst: 2000}, c.IngestRateLimit, "%+v", bad)
	}

	var parsed config.SparkplugConfiguration
	require.NoError(t, core.LoadConfiguration([]byte(`{"sources":[],"ingestRateLimit":{"readingsPerSecond":50,"burst":300}}`), &parsed))
	parsed.ApplyDefaults()
	assert.Equal(t, config.IngestRateLimit{ReadingsPerSecond: 50, Burst: 300}, parsed.IngestRateLimit)

	require.Error(t, core.LoadConfiguration([]byte(`{"ingestRateLimit":{"messagesPerSecond":50}}`), &config.SparkplugConfiguration{}),
		"an unknown key is refused, not ignored")
}
