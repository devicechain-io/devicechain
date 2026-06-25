// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// Loading an empty document succeeds through the ADR-022 decision-1 defaulting
// and validation hooks (no constraints today).
func TestLoadEmptyConfiguration(t *testing.T) {
	cfg := &EventManagementConfiguration{}
	err := core.LoadConfiguration([]byte(``), cfg)

	assert.NoError(t, err)
}
