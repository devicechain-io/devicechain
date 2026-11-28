// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// connectPersistent opens a persistent session under clientID, subscribes it (so the gateway
// creates a durable consumer for it) and disconnects, leaving the session stored.
func connectPersistent(t *testing.T, broker, clientID, tenant, device string) {
	t.Helper()
	require.NoErrorf(t, tryPersistent(broker, clientID, tenant, device), "connecting %q", clientID)
}

// reconnectEvicted is connectPersistent for an id whose session was evicted. The server that last
// served it still holds the session in memory, expecting the record it wrote; its first save after
// the record is gone fails, the server drops its copy and closes that ONE connect, and the device's
// retry starts a fresh session. This pins that it is exactly one, never a lock-out.
func reconnectEvicted(t *testing.T, broker, clientID, tenant, device string) {
	t.Helper()
	if err := tryPersistent(broker, clientID, tenant, device); err != nil {
		require.NoErrorf(t, tryPersistent(broker, clientID, tenant, device),
			"the evicted id %q was refused twice: eviction must cost it at most one reconnect", clientID)
	}
}

func tryPersistent(broker, clientID, tenant, device string) error {
	c := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(broker).SetClientID(clientID).
		SetCleanSession(false).SetConnectTimeout(10 * time.Second).SetAutoReconnect(false))
	tok := c.Connect()
	if !tok.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("timed out")
	}
	if err := tok.Error(); err != nil {
		return err
	}
	defer c.Disconnect(250)
	topic := fmt.Sprintf("%s/%s/devices/%s/commands", mqttInstance, tenant, device)
	return SubscribeMqttConfirmed(c, topic, 1, func(mqtt.Client, mqtt.Message) {}, 10*time.Second)
}

// The digest this package computes is the one the server files a session under, in both domain
// modes (the domain adds a token before the digest, which a wrong prefix would not survive).
func TestMqttSessionClientIDHashAgreesWithTheServer(t *testing.T) {
	for _, domain := range []string{"", "hub"} {
		t.Run("domain="+domain, func(t *testing.T) {
			_, js, broker := mqttRigInDomain(t, domain)
			ids := []string{}
			for _, suffix := range []string{"", ":pub", ":sub", ":a:b", ":" + strings.Repeat("x", 300)} {
				id := mqttInstance + ":" + mqttVictim + ":sensor-001" + suffix
				connectPersistent(t, broker, id, mqttVictim, "sensor-001")
				ids = append(ids, id)
			}
			records := sessionRecords(t, js)
			require.Len(t, records, len(ids))
			for subject, rec := range records {
				assert.Equalf(t, mqttSessionIDHash(subject), MqttSessionClientIDHash(rec.ID),
					"the server filed %q under %q", rec.ID, subject)
			}
		})
	}
}

// An eviction removes exactly the evicted id's session record and its subscription consumer, a
// sibling session of the same device survives, and the evicted id can connect again afterwards.
func TestMqttSessionEvictorRemovesOneSession(t *testing.T) {
	for _, domain := range []string{"", "hub"} {
		t.Run("domain="+domain, func(t *testing.T) {
			nc, js, broker := mqttRigInDomain(t, domain)
			plain := mqttInstance + ":" + mqttVictim + ":sensor-001"
			victim, keep := plain+":old", plain+":keep"
			connectPersistent(t, broker, victim, mqttVictim, "sensor-001")
			connectPersistent(t, broker, keep, mqttVictim, "sensor-001")
			require.Len(t, sessionRecords(t, js), 2)
			require.Len(t, consumerNames(t, js, MqttMessageStore), 2)

			ev, err := NewMqttSessionEvictor(nc)
			require.NoError(t, err)
			owned := func(id string) bool { return DeviceClientIDMatches(id, plain) }
			removed, err := ev.Evict(context.Background(), MqttSessionClientIDHash(victim), owned)
			require.NoError(t, err)
			require.True(t, removed, "the evicted id's session was not found")

			after := sessionRecords(t, js)
			require.Len(t, after, 1, "exactly the evicted session should be gone")
			for _, rec := range after {
				assert.Equal(t, keep, rec.ID, "the wrong session was evicted")
			}
			names := consumerNames(t, js, MqttMessageStore)
			require.Len(t, names, 1, "the evicted session's consumer outlived it")
			assert.True(t, strings.HasPrefix(names[0], MqttSessionClientIDHash(keep)+"_"),
				"the surviving consumer %q is not the kept session's", names[0])

			// Evicting again finds nothing and is not an error.
			removed, err = ev.Evict(context.Background(), MqttSessionClientIDHash(victim), owned)
			require.NoError(t, err)
			assert.False(t, removed)

			// The evicted id connects again, as a new session.
			reconnectEvicted(t, broker, victim, mqttVictim, "sensor-001")
			assert.Len(t, sessionRecords(t, js), 2)
		})
	}
}

// A record whose client id the caller does not own (a digest collision, or another device) is
// never touched.
func TestMqttSessionEvictorLeavesARecordItDoesNotOwn(t *testing.T) {
	nc, js, broker := mqttRig(t)
	other := mqttInstance + ":" + mqttBystander + ":sensor-001:x"
	connectPersistent(t, broker, other, mqttBystander, "sensor-001")
	ev, err := NewMqttSessionEvictor(nc)
	require.NoError(t, err)
	removed, err := ev.Evict(context.Background(), MqttSessionClientIDHash(other), func(string) bool { return false })
	require.NoError(t, err)
	assert.False(t, removed)
	assert.Len(t, sessionRecords(t, js), 1)
	assert.Len(t, consumerNames(t, js, MqttMessageStore), 1)
}

// Before any MQTT client has connected there is no session store, and nothing to evict.
func TestMqttSessionEvictorWithNoSessionStore(t *testing.T) {
	nc, _, _ := mqttRig(t)
	ev, err := NewMqttSessionEvictor(nc)
	require.NoError(t, err)
	removed, err := ev.Evict(context.Background(), "ABCDEFGH", func(string) bool { return true })
	require.NoError(t, err)
	assert.False(t, removed)
	_, err = ev.Evict(context.Background(), "a.b", func(string) bool { return true })
	assert.Error(t, err, "a malformed digest must be refused, not composed into a subject")
}

// An eviction carries its caller's deadline.
func TestMqttSessionEvictorHonoursTheDeadline(t *testing.T) {
	nc, _, broker := mqttRig(t)
	connectPersistent(t, broker, mqttInstance+":"+mqttVictim+":sensor-001:x", mqttVictim, "sensor-001")
	ev, err := NewMqttSessionEvictor(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ev.Evict(ctx, MqttSessionClientIDHash(mqttInstance+":"+mqttVictim+":sensor-001:x"), func(string) bool { return true })
	assert.Error(t, err, "a cancelled context must stop the eviction")
}
