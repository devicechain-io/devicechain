// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/nats-io/nats.go/jetstream"
)

// sessionStoreOpener is the part of the NATS manager wireCalloutSessionBounds needs.
type sessionStoreOpener interface {
	MqttSessionDiscriminatorStore(ctx context.Context) (jetstream.KeyValue, error)
}

// sessionStoreOpenTimeout bounds opening the bucket at start.
const sessionStoreOpenTimeout = 10 * time.Second

// wireCalloutSessionBounds attaches the per-device session bounds to the auth callout: the cap on
// suffixed MQTT client ids per device (recorded in the instance's session-discriminator bucket under
// a key derived from the instance root key, evicting the least recently used session past it), and
// the per-device connect rate. The bucket is created by the broker administrator before any pod of
// this release starts; one that cannot be opened stops the start, like every other bucket this area
// opens.
func wireCalloutSessionBounds(resp *processor.CalloutResponder, nmgr sessionStoreOpener, evictor *messaging.MqttSessionEvictor,
	key *processor.SessionIndexKey, cfg *config.DeviceManagementConfiguration) error {
	if key == nil {
		return errors.New("the auth callout's MQTT session index has no key: it is derived from the instance root key at start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionStoreOpenTimeout)
	defer cancel()
	store, err := nmgr.MqttSessionDiscriminatorStore(ctx)
	if err != nil {
		return fmt.Errorf("opening the MQTT session-discriminator bucket for the auth callout: %w", err)
	}
	c := cfg.Callout
	resp.WithSessionBounds(store, key, evictor, c.MaxSessionsPerDevice, c.ConnectBurstPerDevice, c.ConnectsPerMinutePerDevice)
	return nil
}
