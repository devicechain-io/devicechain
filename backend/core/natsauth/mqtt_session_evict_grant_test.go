// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

// device-management evicts one session at a time (messaging.MqttSessionEvictor): it may read and
// delete a session record and delete gateway consumers, but never event-sources' fail-closed
// durables, and no other area gains the verbs.
func TestOnlyDeviceManagementEvictsMqttSessions(t *testing.T) {
	dm, err := AreaPermissions(permInstance, "device-management")
	require.NoError(t, err)
	for _, s := range []string{
		"$JS.API.STREAM.INFO." + messaging.MqttSessionStore,
		"$JS.API.STREAM.MSG.GET." + messaging.MqttSessionStore,
		"$JS.API.STREAM.MSG.DELETE." + messaging.MqttSessionStore,
		"$JS.API.CONSUMER.DELETE." + messaging.MqttMessageStore + ".ABCDEFGH_nuid",
		"$JS.API.CONSUMER.DELETE." + messaging.MqttPubRelStore + ".ABCDEFGH_nuid",
	} {
		require.True(t, dm.publishes(s), "device-management cannot %s", s)
	}
	for _, d := range messaging.MqttFailClosedConsumerNames(permInstance) {
		require.False(t, dm.publishes("$JS.API.CONSUMER.DELETE."+messaging.MqttMessageStore+"."+d),
			"device-management could delete the fail-closed durable %s", d)
	}
	require.False(t, dm.publishes("$JS.API.STREAM.PURGE."+messaging.MqttSessionStore))
	require.False(t, dm.publishes("$JS.API.STREAM.DELETE."+messaging.MqttSessionStore))

	for _, area := range []string{"event-processing", "device-state", "command-delivery"} {
		p, err := AreaPermissions(permInstance, area)
		require.NoError(t, err)
		require.False(t, p.publishes("$JS.API.STREAM.MSG.DELETE."+messaging.MqttSessionStore), "%s can delete session records", area)
	}
}
