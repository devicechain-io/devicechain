// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// 🔴 THE PIN IS ON UNLESS THE OPERATOR SAYS OTHERWISE, and a callout that cannot learn the broker's
// server ids does not start: a certificate callout that quietly ran unpinned is the failure this
// closes.
func TestCalloutIssuerPinWiring(t *testing.T) {
	ms := &core.Microservice{InstanceId: "pinwiring", FunctionalArea: "device-management"}
	nc := mscfg.NatsConfiguration{Hostname: "127.0.0.1", Port: 1}

	t.Run("no system login: the service refuses to start", func(t *testing.T) {
		rt, err := wireCalloutIssuerPin(context.Background(), nil, config.NewDeviceManagementConfiguration(), nc, ms)
		require.ErrorIs(t, err, errNoSystemLoginForIssuerPin)
		require.Nil(t, rt)
	})
	t.Run("a user without a password is no login", func(t *testing.T) {
		withUser := nc
		withUser.Auth.SysUser = "dc_sys"
		_, err := wireCalloutIssuerPin(context.Background(), nil, config.NewDeviceManagementConfiguration(), withUser, ms)
		require.ErrorIs(t, err, errNoSystemLoginForIssuerPin)
	})
	t.Run("switched off explicitly: no pin and no error", func(t *testing.T) {
		cfg := config.NewDeviceManagementConfiguration()
		cfg.Callout.IssuerPinDisabled = true
		rt, err := wireCalloutIssuerPin(context.Background(), nil, cfg, nc, ms)
		require.NoError(t, err)
		require.Nil(t, rt)
	})
	t.Run("the configuration's defaults are the pin's", func(t *testing.T) {
		require.Equal(t, processor.DefaultIssuerPinRefreshInterval, config.DefaultCalloutIssuerPinRefresh)
		require.Equal(t, processor.DefaultIssuerPinMinRefreshInterval, config.DefaultCalloutIssuerPinMinRefresh)
		require.Equal(t, processor.DefaultIssuerPinGatherWindow, config.DefaultCalloutIssuerPinGatherWindow)
	})
}
