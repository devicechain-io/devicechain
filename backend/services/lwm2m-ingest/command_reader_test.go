// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// The downlink command reader's durable must be created at the stream tail. Created with
// the default policy it would replay the retained device-commands backlog (up to the
// stream's retention) and dispatch long-expired commands to devices as live actuations.
// The policy is read off the broker's consumer, so dropping the option from
// newCommandReader fails here rather than at a customer's device.
func TestCommandReaderStartsAtTheStreamTail(t *testing.T) {
	startRunningManager(t)

	reader, err := newCommandReader(NatsManager)
	require.NoError(t, err)
	require.NotNil(t, reader)

	js, err := NatsManager.Conn().JetStream()
	require.NoError(t, err)

	var policies []nats.DeliverPolicy
	for name := range js.StreamNames() {
		for info := range js.ConsumersInfo(name) {
			policies = append(policies, info.Config.DeliverPolicy)
		}
	}
	require.Len(t, policies, 1, "the reader must create exactly one durable consumer")
	require.Equal(t, nats.DeliverNewPolicy, policies[0],
		"the command reader's durable must start at the stream tail, not replay the retained backlog")
}
