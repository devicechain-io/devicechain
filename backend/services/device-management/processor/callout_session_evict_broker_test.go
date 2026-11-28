// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// The cap end to end against a real broker: real persistent sessions, the real bucket and the real
// evictor. Past the cap the least recently seen id's session record is gone from $MQTT_sess (with
// its consumer), the others survive, and the new id connects.
func TestSessionCapEvictsARealBrokerSession(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, ServerName: "session-cap-test", JetStream: true,
		StoreDir: dctest.JetStreamStoreDir(t),
		MQTT:     natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1},
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("the test broker never became ready")
	}
	t.Cleanup(srv.Shutdown)
	broker := fmt.Sprintf("tcp://127.0.0.1:%d", dctest.MQTTPort(t, srv))

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "session_cap_test"})
	if err != nil {
		t.Fatal(err)
	}
	evictor, err := messaging.NewMqttSessionEvictor(nc)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	b := &sessionBound{store: store, key: testIndexKey(t), evictor: evictor, max: 2}

	const required = "inst-1:acme-corp:sensor-001"
	now := time.Unix(1_780_000_000, 0)
	open := func(suffix string) {
		t.Helper()
		if why := b.admit(ctx, "acme-corp", "sensor-001", required, suffix, now); why != "" {
			t.Fatalf("%s was refused: %s", suffix, why)
		}
		id := required + suffix
		// An id whose session was evicted may be refused ONCE by the server that last served it (it
		// still holds the session in memory and its first save after the record is gone fails; it
		// then drops that copy), so one retry is allowed, and only one.
		err := tryConnect(broker, id)
		if err != nil {
			err = tryConnect(broker, id)
		}
		if err != nil {
			t.Fatalf("connecting %q: %v", id, err)
		}
		now = now.Add(time.Minute)
	}
	hasRecord := func(suffix string) bool {
		t.Helper()
		_, err := legacy.GetLastMsg(messaging.MqttSessionStore, "$MQTT.sess."+messaging.MqttSessionClientIDHash(required+suffix))
		if errors.Is(err, nats.ErrMsgNotFound) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		return true
	}
	consumers := func() int {
		n := 0
		for range legacy.ConsumerNames(messaging.MqttMessageStore) {
			n++
		}
		return n
	}

	open(":a")
	open(":b")
	if !hasRecord(":a") || !hasRecord(":b") || consumers() != 2 {
		t.Fatal("the rig did not leave two persistent sessions")
	}
	open(":c") // evicts :a, the least recently seen
	if hasRecord(":a") {
		t.Fatal("the evicted id's persistent session record is still in the broker's session store")
	}
	if !hasRecord(":b") || !hasRecord(":c") {
		t.Fatal("a session that was not evicted is gone")
	}
	if got := consumers(); got != 2 {
		t.Fatalf("the broker holds %d subscription consumers, want 2 (the evicted session's must go too)", got)
	}
	// And the evicted id can come back, evicting the next oldest.
	open(":a")
	if hasRecord(":b") || !hasRecord(":a") || !hasRecord(":c") {
		t.Fatal("re-admitting the evicted id did not evict the next least recently seen session")
	}
}

// tryConnect opens a persistent session under id, subscribes it, and disconnects.
func tryConnect(broker, id string) error {
	c := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(broker).SetClientID(id).
		SetCleanSession(false).SetConnectTimeout(10 * time.Second).SetAutoReconnect(false))
	tok := c.Connect()
	if !tok.WaitTimeout(10 * time.Second) {
		return errors.New("timed out")
	}
	if err := tok.Error(); err != nil {
		return err
	}
	defer c.Disconnect(250)
	return messaging.SubscribeMqttConfirmed(c, "inst-1/acme-corp/devices/sensor-001/commands", 1,
		func(mqtt.Client, mqtt.Message) {}, 10*time.Second)
}
