// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-update-management/config"
	"github.com/stretchr/testify/require"
)

// updateManagementInfo.downloadsConfigured is read from the configuration, not assumed: a
// resolver built from a configuration with no download base must say downloads are not
// configured, and one with a base must say they are.
func TestSchemaResolverReportsWhetherADownloadBaseIsSet(t *testing.T) {
	unset := config.NewUpdateManagementConfiguration()
	require.False(t, newSchemaResolver(unset, "update-management", "filesystem").DownloadsConfigured)

	set := config.NewUpdateManagementConfiguration()
	set.Updates.DownloadBaseURL = "https://dc.example.com/api/update-management"
	require.True(t, newSchemaResolver(set, "update-management", "filesystem").DownloadsConfigured)
}
