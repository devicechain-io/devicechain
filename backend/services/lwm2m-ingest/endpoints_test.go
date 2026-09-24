// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mscfg "github.com/devicechain-io/dc-microservice/config"
)

// fullInfra is an infrastructure block naming every coordinate a credentialed adapter needs.
func fullInfra() mscfg.InfrastructureConfiguration {
	var infra mscfg.InfrastructureConfiguration
	infra.ServiceAuth.Secret = "s"
	infra.DeviceManagement.Hostname, infra.DeviceManagement.Port = "dm", 8080
	infra.DeviceState.Hostname, infra.DeviceState.Port = "ds", 8080
	infra.CommandDelivery.Hostname, infra.CommandDelivery.Port = "cd", 8080
	return infra
}

// TestStartupRefusesAMissingCommandDeliveryEndpoint: every live command is confirmed with
// command-delivery before it actuates, so an adapter without the coordinate could actuate
// nothing. That used to start with a warning about the wake drain; it must now refuse, through
// the same call startup makes.
func TestStartupRefusesAMissingCommandDeliveryEndpoint(t *testing.T) {
	infra := fullInfra()
	infra.CommandDelivery.Hostname = ""

	_, _, _, err := serviceEndpoints(infra)
	require.Error(t, err, "a credentialed adapter with no command-delivery coordinate must not start")
	assert.True(t, strings.Contains(err.Error(), "infrastructure.commandDelivery"),
		"the refusal must name the missing setting: %v", err)

	infra = fullInfra()
	infra.CommandDelivery.Port = 0
	_, _, _, err = serviceEndpoints(infra)
	require.Error(t, err, "a coordinate with no port is as missing as one with no host")
}

// TestStartupResolvesEveryEndpoint is the counterweight: a complete block still starts, and
// each URL is the one its coordinate names.
func TestStartupResolvesEveryEndpoint(t *testing.T) {
	ingest, state, cd, err := serviceEndpoints(fullInfra())
	require.NoError(t, err)
	assert.Equal(t, "http://dm:8080/graphql", ingest)
	assert.Equal(t, "http://ds:8080/graphql", state)
	assert.Equal(t, "http://cd:8080/graphql", cd)
}
