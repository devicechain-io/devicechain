// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-lwm2m-ingest/config"
	"github.com/devicechain-io/dc-microservice/core"
)

// TestLoadConfigurationParsesRenderedChartDocument pins the chart↔service seam. The
// Helm chart renders this exact JSON into the mounted config document; the service
// parses it with DisallowUnknownFields, so a single key the struct does not name (a
// chart/struct drift) is a startup crash-loop, invisible to a test that hand-builds the
// struct. This feeds the literal rendered bytes through the real loader.
func TestLoadConfigurationParsesRenderedChartDocument(t *testing.T) {
	rendered := `{"listen":{"host":"0.0.0.0","port":5684},"security":{"connectionIdLength":8,"handshakeTimeoutSeconds":10,"idleTimeoutSeconds":0,"maxSessions":50000,"identities":[{"identity":"dev-1","pskEnv":"DC_LWM2M_PSK_DEV1"}]}}`

	var cfg config.Lwm2mConfiguration
	require.NoError(t, core.LoadConfiguration([]byte(rendered), &cfg))
	assert.Equal(t, "0.0.0.0", cfg.Listen.Host)
	assert.Equal(t, 5684, cfg.Listen.Port)
	require.NotNil(t, cfg.Security.ConnectionIdLength)
	assert.Equal(t, 8, *cfg.Security.ConnectionIdLength)
	assert.Equal(t, 50000, cfg.Security.MaxSessions)
	require.Len(t, cfg.Security.Identities, 1)
	assert.Equal(t, "dev-1", cfg.Security.Identities[0].Identity)
	assert.Equal(t, "DC_LWM2M_PSK_DEV1", cfg.Security.Identities[0].PskEnv)
}

// The default (empty) configuration must load and validate — a bare render with no
// identities is a valid inert deployment.
func TestEmptyConfigurationIsValid(t *testing.T) {
	var cfg config.Lwm2mConfiguration
	require.NoError(t, core.LoadConfiguration([]byte(`{}`), &cfg))
	assert.Equal(t, config.DefaultListenPort, cfg.Listen.Port)
}
