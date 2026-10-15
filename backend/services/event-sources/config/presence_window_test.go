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
func TestBrokerPresencePublishWindowDefault(t *testing.T) {
	assert.Equal(t, 128, DefaultPresencePublishWindow)

	for name, doc := range map[string]string{"absent": `{}`, "null": `{"brokerPresence":{"publishWindow":null}}`} {
		cfg := &EventSourcesConfiguration{}
		require.NoError(t, core.LoadConfiguration([]byte(doc), cfg), name)
		require.NotNil(t, cfg.BrokerPresence.PublishWindow, "%s: ApplyDefaults must fill an unset window", name)
		assert.Equal(t, 128, *cfg.BrokerPresence.PublishWindow, name)
	}
	assert.Equal(t, 128, BrokerPresence{}.PublishWindowSize(), "an unread default is still the default")
}

// An explicit value is honoured; an explicit 0 and anything outside 1..MaxOrderedWindow stops
// the load, naming the setting. A window silently clamped is a throughput limit nobody chose.
func TestBrokerPresencePublishWindowIsFailClosed(t *testing.T) {
	load := func(v int) (*EventSourcesConfiguration, error) {
		cfg := &EventSourcesConfiguration{}
		err := core.LoadConfiguration([]byte(fmt.Sprintf(`{"brokerPresence":{"publishWindow":%d}}`, v)), cfg)
		return cfg, err
	}
	for _, ok := range []int{1, 32, 128, messaging.MaxOrderedWindow} {
		cfg, err := load(ok)
		require.NoError(t, err, "window %d", ok)
		assert.Equal(t, ok, cfg.BrokerPresence.PublishWindowSize())
	}
	for _, bad := range []int{0, -1, messaging.MaxOrderedWindow + 1} {
		_, err := load(bad)
		require.Error(t, err, "window %d must be refused", bad)
		assert.Contains(t, err.Error(), "brokerPresence.publishWindow", "window %d", bad)
	}
}

// The setting sits beside the others in a typed section: a misspelling beside it is refused.
func TestBrokerPresenceRefusesAnUnknownKey(t *testing.T) {
	cfg := &EventSourcesConfiguration{}
	assert.Error(t, core.LoadConfiguration([]byte(`{"brokerPresence":{"publishWindw":8}}`), cfg))
}
