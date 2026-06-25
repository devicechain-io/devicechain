// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// Loading an empty document populates the default MQTT source so the service
// ingests events out of the box (ADR-022 decision 1 defaulting via
// core.LoadConfiguration). An empty source list is load-bearing.
func TestLoadDefaultsMqttSource(t *testing.T) {
	cfg := &EventSourcesConfiguration{}
	err := core.LoadConfiguration([]byte(``), cfg)

	assert.NoError(t, err)
	assert.Len(t, cfg.EventSources, 1)
	assert.Equal(t, "mqtt1", cfg.EventSources[0].Id)
	assert.Equal(t, "mqtt", cfg.EventSources[0].Type)
	assert.Equal(t, "json", cfg.EventSources[0].Decoder.Type)
	assert.Equal(t, 100, cfg.InboundEventBatching.MaxBatchSize)
	assert.Equal(t, 100, cfg.InboundEventBatching.BatchTimeoutMs)
	assert.NoError(t, cfg.Validate())
}

// The constructor and the load path share one source of defaults.
func TestDefaultConfigurationValid(t *testing.T) {
	cfg := NewEventSourcesConfiguration()
	assert.Len(t, cfg.EventSources, 1)
	assert.NoError(t, cfg.Validate())
}
