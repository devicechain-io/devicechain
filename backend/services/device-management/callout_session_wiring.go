// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/processor"
	nats "github.com/nats-io/nats.go"
)

// sessionStoreOpener is the part of the NATS manager wireCalloutSessionBounds needs.
type sessionStoreOpener interface {
	MqttSessionDiscriminatorStore() (nats.KeyValue, error)
}

// wireCalloutSessionBounds attaches the per-device session bounds to the auth callout: the cap on
// distinct MQTT client ids per device, recorded in the instance's session-discriminator bucket,
// and the per-device connect rate. The bucket is created by the broker administrator before any
// pod of this release starts; one that cannot be opened stops the start, like every other bucket
// this area opens.
func wireCalloutSessionBounds(resp *processor.CalloutResponder, nmgr sessionStoreOpener, cfg *config.DeviceManagementConfiguration) error {
	store, err := nmgr.MqttSessionDiscriminatorStore()
	if err != nil {
		return fmt.Errorf("opening the MQTT session-discriminator bucket for the auth callout: %w", err)
	}
	c := cfg.Callout
	resp.WithSessionBounds(store, c.MaxSessionsPerDevice, c.ConnectBurstPerDevice, c.ConnectsPerMinutePerDevice)
	return nil
}
