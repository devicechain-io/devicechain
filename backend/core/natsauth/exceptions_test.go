// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
)

// 🔑 THE TENANT PURGE REACHES THE BROKER THROUGH A RAW CONNECTION, which messaging's catalog
// refusal cannot see and the services' boot tests do not run (it needs a deleted tenant and an
// MQTT gateway holding sessions). It is one of the two named exceptions to "an area touches what
// the catalog declares for it", so it is exercised here, against the broker the grant is
// generated for, with data in every place it erases from:
//
//   - persistent MQTT sessions with a subscription each (the session records, the subscription
//     consumers and the ordered read over the session store);
//   - the cache buckets device-management keeps, with a key of the tenant's and one of another's
//     (the key listing and the subject purge, in place of a KV read or write);
//   - the DETECT eviction request and the cache-eviction broadcast.
//
// The same run with one subject removed is the negative control: it must be refused, or the
// zero-violation result above it proves nothing.

const purgeInstance = "pinst"

func seedPurgeData(t *testing.T, b *permtest.Broker) {
	t.Helper()
	// Cache buckets, created and filled as the area that owns them.
	nc := b.Connect(t, "device-management")
	js, err := nc.JetStream()
	require.NoError(t, err)
	bucket := messaging.CacheBucketName(purgeInstance, "device-management", kv.BucketDeviceByToken)
	store, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: bucket, TTL: time.Hour})
	require.NoError(t, err)
	for _, key := range []string{"victim|dev1", "bystander|dev1"} {
		_, err := store.Put(base64.RawURLEncoding.EncodeToString([]byte(key)), []byte("x"))
		require.NoError(t, err)
	}

	// Persistent MQTT sessions with a subscription, as the unrestricted shared login: the point
	// is to give the purge session records and consumers to find, not to test what a device may do.
	for _, tenant := range []string{"victim", "bystander"} {
		clientID, err := messaging.DeviceClientID(purgeInstance, tenant, "dev1")
		require.NoError(t, err)
		c := mqtt.NewClient(mqtt.NewClientOptions().
			AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", b.MQTTPort(t))).
			SetClientID(clientID).
			SetUsername(natsauth.ServiceUser).SetPassword(permtest.Password(natsauth.ServiceUser)).
			SetCleanSession(false).SetConnectTimeout(10 * time.Second))
		tok := c.Connect()
		require.True(t, tok.WaitTimeout(10*time.Second))
		require.NoError(t, tok.Error())
		require.NoError(t, messaging.SubscribeMqttConfirmed(c,
			fmt.Sprintf("%s/%s/devices/dev1/commands", purgeInstance, tenant), 1,
			func(mqtt.Client, mqtt.Message) {}, 10*time.Second))
		c.Disconnect(250)
	}
}

// purgeAsUserManagement runs every broker call user-management's tenant purge makes.
func purgeAsUserManagement(t *testing.T, b *permtest.Broker) (rows int64, err error) {
	t.Helper()
	nc := b.Connect(t, "user-management")
	js, jerr := nc.JetStream()
	require.NoError(t, jerr)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := messaging.PurgeTenant(ctx, nc, purgeInstance, "victim")
	if err != nil {
		return 0, err
	}
	keys, err := messaging.PurgeTenantKv(ctx, js, purgeInstance, "victim")
	if err != nil {
		return 0, err
	}
	if _, err := messaging.PurgeTenantDetect(ctx, nc, purgeInstance, "victim", nil); err != nil {
		return 0, err
	}
	if err := messaging.BroadcastTenantCacheEviction(nc, purgeInstance, "victim"); err != nil {
		return 0, err
	}
	require.NoError(t, nc.Flush())
	return res.Rows() + keys.Keys, nil
}

func TestTenantPurgeRunsUnderTheGeneratedGrant(t *testing.T) {
	b := permtest.Start(t, purgeInstance, permtest.WithMQTT())
	seedPurgeData(t, b)

	rows, err := purgeAsUserManagement(t, b)
	require.NoError(t, err)
	// One session record, one subscription consumer, one cache key: the paths ran, so a silent
	// "found nothing" cannot be what the empty violation list below is made of.
	require.GreaterOrEqual(t, rows, int64(3), "the purge erased nothing: the grant was not exercised")
	b.RequireNoViolations(t)
}

// The negative control. Withdrawing the purge of one stream must be refused by the broker: if it
// is not, the broker is not enforcing what the grant says and the test above proves nothing.
func TestTenantPurgeIsRefusedWhenOneDeclaredSubjectIsRemoved(t *testing.T) {
	b := permtest.Start(t, purgeInstance, permtest.WithMQTT(), permtest.Without("$JS.API.STREAM.PURGE."+messaging.MqttMessageStore))
	seedPurgeData(t, b)

	_, err := purgeAsUserManagement(t, b)
	require.Error(t, err, "the purge succeeded with a subject it needs withdrawn")
	require.NotEmpty(t, b.Violations(), "the broker did not log a permission violation for the withdrawn subject")
}
