// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// Loading an empty document populates the default MQTT + HTTP sources so the
// service ingests events out of the box (ADR-022 decision 1 defaulting via
// core.LoadConfiguration). An empty source list is load-bearing.
func TestLoadDefaultsEventSources(t *testing.T) {
	cfg := &EventSourcesConfiguration{}
	err := core.LoadConfiguration([]byte(``), cfg)

	assert.NoError(t, err)
	assert.Len(t, cfg.EventSources, 2)

	byId := map[string]EventSource{}
	for _, src := range cfg.EventSources {
		byId[src.Id] = src
	}

	mqttSrc := byId["mqtt1"]
	assert.Equal(t, "mqtt", mqttSrc.Type)
	assert.Equal(t, "json", mqttSrc.Decoder.Type)

	httpSrc := byId["http1"]
	assert.Equal(t, "http", httpSrc.Type)
	assert.Equal(t, "json", httpSrc.Decoder.Type)
	assert.Equal(t, "8081", httpSrc.Configuration["port"])

	assert.Equal(t, 100, cfg.InboundEventBatching.MaxBatchSize)
	assert.Equal(t, 100, cfg.InboundEventBatching.BatchTimeoutMs)
	assert.NoError(t, cfg.Validate())
}

// The constructor and the load path share one source of defaults.
func TestDefaultConfigurationValid(t *testing.T) {
	cfg := NewEventSourcesConfiguration()
	assert.Len(t, cfg.EventSources, 2)
	assert.NoError(t, cfg.Validate())
}
