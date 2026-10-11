// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-update-management/config"
	"github.com/stretchr/testify/require"
)

func TestNewUpdateManagementConfiguration(t *testing.T) {
	cfg := config.NewUpdateManagementConfiguration()
	require.NotNil(t, cfg)
	require.NoError(t, cfg.Validate())
}

// The chart renders "{}" for an area with no config override, which is what every
// install of this area gets today. It must load.
func TestEmptyConfigurationLoads(t *testing.T) {
	cfg := &config.UpdateManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte("{}"), cfg))
}

// Typed config fails closed: a key this service does not define is refused at startup
// rather than silently ignored.
func TestUnknownConfigurationKeyIsRefused(t *testing.T) {
	cfg := &config.UpdateManagementConfiguration{}
	require.Error(t, core.LoadConfiguration([]byte(`{"artifactStore": {}}`), cfg))
}
