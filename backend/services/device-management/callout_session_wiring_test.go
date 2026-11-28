// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSessionStoreOpener struct{ err error }

func (f fakeSessionStoreOpener) MqttSessionDiscriminatorStore(context.Context) (jetstream.KeyValue, error) {
	return nil, f.err
}

func TestSessionBoundDefaultsAgree(t *testing.T) {
	assert.Equal(t, processor.DefaultMaxSessionsPerDevice, config.DefaultCalloutMaxSessionsPerDevice)
	assert.Equal(t, processor.DefaultConnectBurstPerDevice, config.DefaultCalloutConnectBurstPerDevice)
	assert.Equal(t, processor.DefaultConnectsPerMinutePerDevice, config.DefaultCalloutConnectsPerMinutePerDevice)
}

// The wired responder runs with the bounds the fixed configuration names, and a bucket that
// cannot be opened, or a root key the index cannot be keyed by, stops the start rather than
// leaving the callout unbounded.
func TestWireCalloutSessionBounds(t *testing.T) {
	cfg := &config.DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"callout":{"maxSessionsPerDevice":5,"connectBurstPerDevice":7,"connectsPerMinutePerDevice":9}}`), cfg))
	key, err := processor.NewSessionIndexKey(bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)

	r := &processor.CalloutResponder{}
	require.NoError(t, wireCalloutSessionBounds(r, fakeSessionStoreOpener{}, nil, key, cfg))
	m, b, p := r.SessionBounds()
	assert.Equal(t, []int{5, 7, 9}, []int{m, b, p})

	r2 := &processor.CalloutResponder{}
	err = wireCalloutSessionBounds(r2, fakeSessionStoreOpener{err: errors.New("bucket missing")}, nil, key, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session-discriminator bucket")
	m, b, p = r2.SessionBounds()
	assert.Equal(t, []int{0, 0, 0}, []int{m, b, p}, "a failed open must not leave half the bounds attached")

	r3 := &processor.CalloutResponder{}
	require.Error(t, wireCalloutSessionBounds(r3, fakeSessionStoreOpener{}, nil, nil, cfg))
}
