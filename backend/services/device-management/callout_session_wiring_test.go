// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-microservice/core"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSessionStoreOpener struct{ err error }

func (f fakeSessionStoreOpener) MqttSessionDiscriminatorStore() (nats.KeyValue, error) {
	return nil, f.err
}

func TestSessionBoundDefaultsAgree(t *testing.T) {
	assert.Equal(t, processor.DefaultMaxSessionsPerDevice, config.DefaultCalloutMaxSessionsPerDevice)
	assert.Equal(t, processor.DefaultConnectBurstPerDevice, config.DefaultCalloutConnectBurstPerDevice)
	assert.Equal(t, processor.DefaultConnectsPerMinutePerDevice, config.DefaultCalloutConnectsPerMinutePerDevice)
}

// The wired responder runs with the bounds the fixed configuration names, and a bucket that
// cannot be opened stops the start rather than leaving the callout unbounded.
func TestWireCalloutSessionBounds(t *testing.T) {
	cfg := &config.DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"callout":{"maxSessionsPerDevice":5,"connectBurstPerDevice":7,"connectsPerMinutePerDevice":9}}`), cfg))

	r := &processor.CalloutResponder{}
	require.NoError(t, wireCalloutSessionBounds(r, fakeSessionStoreOpener{}, cfg))
	m, b, p := r.SessionBounds()
	assert.Equal(t, []int{5, 7, 9}, []int{m, b, p})

	r2 := &processor.CalloutResponder{}
	err := wireCalloutSessionBounds(r2, fakeSessionStoreOpener{err: errors.New("bucket missing")}, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session-discriminator bucket")
	m, b, p = r2.SessionBounds()
	assert.Equal(t, []int{0, 0, 0}, []int{m, b, p}, "a failed open must not leave half the bounds attached")
}
