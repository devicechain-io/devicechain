// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// paho installs no write deadline at zero, so a write to a dead peer blocks for good and the
// ping timeout is never checked. The built options must carry a bound.
func TestMqttEventSourceOptionsBoundWritesAndStateKeepAlive(t *testing.T) {
	es, err := NewMqttEventSource("ext", "cid-wt", map[string]string{"host": "h", "port": "1883", "topic": "t"},
		nil, "", "", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {},
		InlinePublisher(func(string, string, *model.UnresolvedEvent, interface{}, string) error { return nil }),
		func(string, string, []byte, error) error { return nil },
		nil, admitAllReadings, admitAll, alwaysOwns, func(error) {})
	require.NoError(t, err)
	assert.Equal(t, mqttWriteTimeout, es.opts.WriteTimeout)
	assert.Greater(t, int64(es.opts.WriteTimeout), int64(0))
	assert.Equal(t, mqttKeepAlive, time.Duration(es.opts.KeepAlive)*time.Second)
}
