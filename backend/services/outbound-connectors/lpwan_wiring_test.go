// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-outbound-connectors/config"
)

// TestTheLpwanAdapterFailsClosedWithoutWhatItNeeds: an adapter that read commands it could not claim
// would send a device a downlink twice, and one that could not answer would leave every command to
// time out, so a missing coordinate is a startup error and never a mode. Each coordinate is removed
// in turn from an otherwise complete set, so a check that tests only the first one fails here.
func TestTheLpwanAdapterFailsClosedWithoutWhatItNeeds(t *testing.T) {
	complete := func() mscfg.InfrastructureConfiguration {
		var i mscfg.InfrastructureConfiguration
		i.ServiceAuth.Secret = "s"
		i.UserManagement = mscfg.UserManagementConfiguration{Hostname: "um", Port: 8080}
		i.CommandDelivery = mscfg.CommandDeliveryConfiguration{Hostname: "cd", Port: 8080}
		i.DeviceManagement = mscfg.DeviceManagementConfiguration{Hostname: "dm", Port: 8080}
		return i
	}
	for name, mut := range map[string]func(*mscfg.InfrastructureConfiguration){
		"no service secret":     func(i *mscfg.InfrastructureConfiguration) { i.ServiceAuth.Secret = "" },
		"no user-management":    func(i *mscfg.InfrastructureConfiguration) { i.UserManagement = mscfg.UserManagementConfiguration{} },
		"no command-delivery":   func(i *mscfg.InfrastructureConfiguration) { i.CommandDelivery = mscfg.CommandDeliveryConfiguration{} },
		"no device-management":  func(i *mscfg.InfrastructureConfiguration) { i.DeviceManagement = mscfg.DeviceManagementConfiguration{} },
		"command-delivery port": func(i *mscfg.InfrastructureConfiguration) { i.CommandDelivery.Port = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			i := complete()
			mut(&i)
			a, f, err := buildLpwan(nil, i, nil)
			require.Error(t, err)
			require.Nil(t, a)
			require.Nil(t, f)
		})
	}
}

// The switch defaults to off, so a service whose configuration predates the adapter starts exactly
// as it did.
func TestTheLpwanAdapterDefaultsOff(t *testing.T) {
	require.False(t, config.NewOutboundConnectorsConfiguration().LpwanDownlink.Enabled)
	require.Nil(t, Lpwan, "nothing builds the adapter unless it is enabled")
	require.Nil(t, LpwanAcks, "nor its acknowledgement feed")
}
