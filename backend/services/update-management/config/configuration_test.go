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

func TestUpdatesCeilingsDefaultAndValidate(t *testing.T) {
	cfg := &config.UpdateManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte("{}"), cfg))
	require.Equal(t, config.DefaultMaxArtifactBytes, cfg.Updates.MaxArtifactBytes)
	require.Equal(t, config.DefaultMaxTenantArtifactBytes, cfg.Updates.MaxTenantArtifactBytes)

	neg := &config.UpdateManagementConfiguration{}
	require.Error(t, core.LoadConfiguration([]byte(`{"updates":{"maxArtifactBytes":-1}}`), neg))

	// A per-artifact ceiling above the tenant total could never be used in full.
	inverted := &config.UpdateManagementConfiguration{}
	require.Error(t, core.LoadConfiguration([]byte(`{"updates":{"maxArtifactBytes":2048,"maxTenantArtifactBytes":1024}}`), inverted))

	// The unknown-key refusal reaches the nested block too.
	typo := &config.UpdateManagementConfiguration{}
	require.Error(t, core.LoadConfiguration([]byte(`{"updates":{"maxArtifactByte":1}}`), typo))
}

// On S3 the Put buffers, so the effective ceiling is clamped; the filesystem backend streams.
func TestEffectiveCeilingIsClampedOnS3Only(t *testing.T) {
	cfg := config.NewUpdateManagementConfiguration()
	require.Equal(t, config.DefaultMaxArtifactBytes, cfg.EffectiveMaxArtifactBytes("filesystem"))
	require.Equal(t, config.S3BufferedPutCap, cfg.EffectiveMaxArtifactBytes("s3"))
	cfg.Updates.MaxArtifactBytes = 1 << 20
	require.Equal(t, int64(1<<20), cfg.EffectiveMaxArtifactBytes("s3"))
}
