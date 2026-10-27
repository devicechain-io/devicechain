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

	require.Equal(t, config.DefaultMaxConcurrentUploads, cfg.Updates.MaxConcurrentUploads)
	require.Less(t, config.DefaultMaxTenantArtifactBytes, int64(5<<30),
		"the per-tenant ceiling stays below the chart's default 5Gi volume, which every tenant shares")
	negUploads := &config.UpdateManagementConfiguration{}
	require.Error(t, core.LoadConfiguration([]byte(`{"updates":{"maxConcurrentUploads":-1}}`), negUploads))

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

func TestDownloadBaseIsInsecureOnlyForPlainHttpOffLoopback(t *testing.T) {
	for base, want := range map[string]bool{
		"": false,
		"https://dc.example.com/api/update-management": false,
		"http://dc.example.com/api/update-management":  true,
		"http://localhost/api/update-management":       false,
		"http://127.0.0.1:8080/x":                      false,
		"http://[::1]:8080/x":                          false,
		"http://LOCALHOST/x":                           false,
		"http://127.0.0.2/x":                           false,
		"http://dc.localhost/x":                        false,
		"http://notlocalhost/x":                        true,
		"http://10.0.0.5/x":                            true,
	} {
		u := config.UpdatesConfiguration{DownloadBaseURL: base}
		require.Equal(t, want, u.DownloadBaseIsInsecure(), base)
	}
}

func TestUpdatePlaneDefaultsAndBounds(t *testing.T) {
	cfg := &config.UpdateManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte("{}"), cfg))
	require.False(t, cfg.Updates.AllowUnverifiedArtifacts, "nothing may be assigned until the instance opts in")
	require.Empty(t, cfg.Updates.DownloadBaseURL)
	require.Equal(t, config.DefaultMaxConcurrentDownloads, cfg.Updates.MaxConcurrentDownloads)
	require.Equal(t, 15*60, cfg.Updates.CapabilityTTLSeconds)
	require.NoError(t, cfg.Policy().Validate(), "every default deadline is positive")

	ok := &config.UpdateManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"updates":{"allowUnverifiedArtifacts":true,
		"downloadBaseUrl":"https://dc.example.com/api/update-management","capabilityTtlSeconds":60,
		"deadlines":{"downloadSeconds":120}}}`), ok))
	require.True(t, ok.Updates.AllowUnverifiedArtifacts)
	require.EqualValues(t, 120, ok.Policy().Download.Seconds())

	for name, doc := range map[string]string{
		"a capability that outlives the ceiling": `{"updates":{"capabilityTtlSeconds":901}}`,
		"a negative capability lifetime":         `{"updates":{"capabilityTtlSeconds":-1}}`,
		"a negative deadline":                    `{"updates":{"deadlines":{"installSeconds":-5}}}`,
		"a negative download cap":                `{"updates":{"maxConcurrentDownloads":-1}}`,
		"a relative base URL":                    `{"updates":{"downloadBaseUrl":"/api/update-management"}}`,
		"a base URL with a query":                `{"updates":{"downloadBaseUrl":"https://x.example.com/?a=1"}}`,
		"a base URL with credentials":            `{"updates":{"downloadBaseUrl":"https://u:p@x.example.com/"}}`,
		"a base URL of the wrong scheme":         `{"updates":{"downloadBaseUrl":"ftp://x.example.com/"}}`,
		"an unknown deadline":                    `{"updates":{"deadlines":{"rebootSeconds":5}}}`,
	} {
		require.Error(t, core.LoadConfiguration([]byte(doc), &config.UpdateManagementConfiguration{}), name)
	}
}

// updates.objectStore is a closed vocabulary: the default is the instance store, "none" is the
// explicit no-store mode, and anything else is refused at startup rather than read as the default.
func TestObjectStoreModeDefaultsAndFailsClosed(t *testing.T) {
	cfg := &config.UpdateManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte("{}"), cfg))
	require.Equal(t, config.ObjectStoreInstance, cfg.Updates.ObjectStore)

	none := &config.UpdateManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"updates":{"objectStore":"none"}}`), none))
	require.Equal(t, config.ObjectStoreNone, none.Updates.ObjectStore)

	for _, bad := range []string{"None", "off", "s3", "filesystem"} {
		c := &config.UpdateManagementConfiguration{}
		require.Error(t, core.LoadConfiguration([]byte(`{"updates":{"objectStore":"`+bad+`"}}`), c), bad)
	}
}
