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
func TestIngestPublishWindowDefault(t *testing.T) {
	assert.Equal(t, 32, DefaultIngestPublishWindow)

	cfg := &SparkplugConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{}`), cfg))
	require.NotNil(t, cfg.Ingest.PublishWindow, "ApplyDefaults must fill an unset window")
	assert.Equal(t, 32, *cfg.Ingest.PublishWindow)
	assert.Equal(t, 32, cfg.Ingest.PublishWindowSize())
	assert.Equal(t, 32, Ingest{}.PublishWindowSize(), "an unread default is still the default")
}

// An explicit value is honoured; an explicit 0 and anything outside 1..MaxOrderedWindow
// stops the load. A window silently clamped is a throughput limit nobody chose.
func TestIngestPublishWindowIsFailClosed(t *testing.T) {
	load := func(v int) (*SparkplugConfiguration, error) {
		cfg := &SparkplugConfiguration{}
		err := core.LoadConfiguration([]byte(fmt.Sprintf(`{"ingest":{"publishWindow":%d}}`, v)), cfg)
		return cfg, err
	}
	for _, ok := range []int{1, 8, 32, messaging.MaxOrderedWindow} {
		cfg, err := load(ok)
		require.NoError(t, err, "window %d", ok)
		assert.Equal(t, ok, cfg.Ingest.PublishWindowSize())
	}
	for _, bad := range []int{0, -1, messaging.MaxOrderedWindow + 1} {
		_, err := load(bad)
		require.Error(t, err, "window %d must be refused", bad)
		assert.Contains(t, err.Error(), "ingest.publishWindow", "window %d", bad)
	}
}

// An unknown key beside it is still refused: the section is typed.
func TestIngestRefusesAnUnknownKey(t *testing.T) {
	cfg := &SparkplugConfiguration{}
	assert.Error(t, core.LoadConfiguration([]byte(`{"ingest":{"publishWindw":8}}`), cfg))
}
