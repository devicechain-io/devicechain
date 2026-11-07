// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-microservice/cmddownlink"
)

func lpwanFleet() (*fakeApi, *scriptedReader) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{
		queued(1, "c1"), queued(2, "c2"), queued(3, "c3"),
	}}
	reader := &scriptedReader{states: map[string]presence.State{
		"dev-c1": {Known: true, Source: integrationSource},
		"dev-c2": asserted(true, mqttSource),
		"dev-c3": asserted(true, sparkplugSource),
	}}
	return api, reader
}

// With the LPWAN downlink off, an integration device's command still fails on sight and nothing is
// stamped: the message of every other device is exactly what it was.
func TestLpwanOffLeavesIntegrationCommandsFailingAndNothingStamped(t *testing.T) {
	api, reader := lpwanFleet()
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = reader

	proc.sweepLocked(context.Background())

	assert.ElementsMatch(t, []uint{1, 3}, api.undeliverable, "integration and Sparkplug both fail on sight")
	require.Equal(t, 1, writer.count())
	for device, headers := range headersByDevice(writer) {
		assert.Nil(t, headers, "device %s: a header with nothing enabled", device)
	}
}

// NEGATIVE CONTROL for the guard above: with it on, the integration device's command is published
// with its routing headers, and the other transports are untouched.
func TestLpwanOnDispatchesIntegrationCommandsWithTheirSource(t *testing.T) {
	api, reader := lpwanFleet()
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = reader
	proc.LpwanDownlink = true

	proc.sweepLocked(context.Background())

	assert.Equal(t, []uint{3}, api.undeliverable, "only Sparkplug still fails on sight")
	require.Equal(t, 2, writer.count())
	headers := headersByDevice(writer)
	require.Equal(t, map[string]string{
		cmddownlink.HeaderTransport: "integration",
		cmddownlink.HeaderSource:    integrationSource,
	}, headers["dev-c1"])
	assert.Nil(t, headers["dev-c2"], "a plain MQTT device's message carries no header")
}

// The whole projected source rides only for an integration, and only when LPWAN is on.
func TestDispatchHeadersSourceIsStampedForIntegrationOnly(t *testing.T) {
	proc := procWith(&fakeApi{}, &recordingWriter{})
	cmd := queued(1, "c1")

	assert.Nil(t, proc.dispatchHeaders(cmd, routingOf(true, integrationSource)))

	proc.LpwanDownlink = true
	h := proc.dispatchHeaders(cmd, routingOf(true, integrationSource))
	assert.Equal(t, integrationSource, h[cmddownlink.HeaderSource])
	assert.Equal(t, "integration", h[cmddownlink.HeaderTransport])
	assert.Nil(t, proc.dispatchHeaders(cmd, routingOf(true, lwm2mSource)), "LPWAN alone stamps no other transport")
	assert.Nil(t, proc.dispatchHeaders(cmd, routingOf(true, sparkplugSource)))
	assert.Nil(t, proc.dispatchHeaders(cmd, routingOf(false, "")))

	// With Sparkplug commands also on, every minted transport is stamped as before, and an
	// integration additionally carries its source.
	proc.SparkplugCommands = true
	h = proc.dispatchHeaders(cmd, routingOf(true, integrationSource))
	assert.Equal(t, integrationSource, h[cmddownlink.HeaderSource])
	h = proc.dispatchHeaders(cmd, routingOf(true, lwm2mSource))
	assert.Equal(t, "lwm2m", h[cmddownlink.HeaderTransport])
	assert.NotContains(t, h, cmddownlink.HeaderSource)
}

// The expiry rides so the adapter can refuse a command the platform has given up on.
func TestLpwanHeadersCarryTheCommandExpiry(t *testing.T) {
	proc := procWith(&fakeApi{}, &recordingWriter{})
	proc.LpwanDownlink = true
	cmd := queued(1, "c1")
	require.NotContains(t, proc.dispatchHeaders(cmd, routingOf(true, integrationSource)), cmddownlink.HeaderCommandExpires)
	cmd.ExpiresAt.Valid = true
	cmd.ExpiresAt.Time = cmd.ExpiresAt.Time.Add(0)
	h := proc.dispatchHeaders(cmd, routingOf(true, integrationSource))
	require.Contains(t, h, cmddownlink.HeaderCommandExpires)
}
