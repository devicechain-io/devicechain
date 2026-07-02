// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"testing"

	"github.com/devicechain-io/dc-dashboard-management/config"
	"github.com/stretchr/testify/assert"
)

func TestNewDashboardManagementConfiguration(t *testing.T) {
	cfg := config.NewDashboardManagementConfiguration()
	assert.NotNil(t, cfg)
	assert.NoError(t, cfg.Validate())
}
