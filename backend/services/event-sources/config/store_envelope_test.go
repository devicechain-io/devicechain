// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

func TestMqttStoreEnvelopesIsExplicitOptIn(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		want bool
	}{
		{`{}`, false},
		{`{"capture":{"mqttStoreEnvelopes":false}}`, false},
		{`{"capture":{"mqttStoreEnvelopes":true}}`, true},
	} {
		cfg := &EventSourcesConfiguration{}
		require.NoError(t, core.LoadConfiguration([]byte(tc.doc), cfg))
		require.Equal(t, tc.want, cfg.Capture.MqttStoreEnvelopes)
	}
	require.False(t, NewEventSourcesConfiguration().Capture.MqttStoreEnvelopes)
}
