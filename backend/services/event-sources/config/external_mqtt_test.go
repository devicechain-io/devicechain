// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shipped default is written out here, so changing it is a change made in two places,
// with a measurement behind it.
func TestExternalMqttPublishWindowDefault(t *testing.T) {
	assert.Equal(t, 128, DefaultExternalMqttPublishWindow)

	cfg := &EventSourcesConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{}`), cfg))
	require.NotNil(t, cfg.ExternalMqtt.PublishWindow, "ApplyDefaults must fill an unset window")
	assert.Equal(t, 128, *cfg.ExternalMqtt.PublishWindow)
	assert.Equal(t, 128, cfg.ExternalMqtt.PublishWindowSize())
	assert.Equal(t, 128, ExternalMqtt{}.PublishWindowSize(), "an unread default is still the default")
}

// An explicit value is honoured; an explicit 0 and anything outside 1..MaxOrderedWindow
// stops the load. A window silently clamped is a throughput limit nobody chose.
func TestExternalMqttPublishWindowIsFailClosed(t *testing.T) {
	load := func(v int) (*EventSourcesConfiguration, error) {
		cfg := &EventSourcesConfiguration{}
		err := core.LoadConfiguration([]byte(fmt.Sprintf(`{"externalMqtt":{"publishWindow":%d}}`, v)), cfg)
		return cfg, err
	}
	for _, ok := range []int{1, 32, 128, messaging.MaxOrderedWindow} {
		cfg, err := load(ok)
		require.NoError(t, err, "window %d", ok)
		assert.Equal(t, ok, cfg.ExternalMqtt.PublishWindowSize())
	}
	for _, bad := range []int{0, -1, messaging.MaxOrderedWindow + 1} {
		_, err := load(bad)
		require.Error(t, err, "window %d must be refused", bad)
		assert.Contains(t, err.Error(), "externalMqtt.publishWindow", "window %d", bad)
	}
}

// An unknown key beside it is still refused: the section is typed.
func TestExternalMqttRefusesAnUnknownKey(t *testing.T) {
	cfg := &EventSourcesConfiguration{}
	assert.Error(t, core.LoadConfiguration([]byte(`{"externalMqtt":{"publishWindw":8}}`), cfg))
}
