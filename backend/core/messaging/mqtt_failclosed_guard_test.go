// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// GUARD: the nats-server behaviours a fail-closed MQTT acknowledgement is built on.
//
// # What is being pinned, and why a test and not a comment
//
// The idea: the MQTT gateway of an embedded nats-server already answers a QoS 1 PUBLISH only
// after the message is stored in its own message stream, $MQTT_msgs. If that store REFUSES the
// message the gateway sends no PUBACK and closes the connection, and a device that kept its
// unacknowledged packet re-sends it on reconnect. Give that stream a bound, DiscardNew, and a
// platform-owned durable consumer, and "PUBACKed" can mean "the platform holds it".
//
// Every step of that is nats-server behaviour that is not part of any documented contract:
//
//   - the gateway looks the stream up before it creates it and never reconciles a stream that
//     is already there, so a stream the platform created first keeps its Discard, its bound
//     and its replicas (mqtt.go, mqttCreateAccountSessionManager);
//   - a QoS 1 publish is acknowledged through a pipeline that closes the connection, with no
//     PUBACK, when the store answers with an error or does not answer in five seconds
//     (mqttAckLoop);
//   - the stream has INTEREST retention, so a message nobody has a consumer for is SKIPPED and
//     still acknowledged (stream.go, processJetStreamMsg): the hazard that makes the durable
//     load-bearing, pinned below so that nobody forgets it;
//   - the stream is per ACCOUNT, so two platform instances in one account share one bound.
//
// A nats-server bump that changes any of these would turn a fail-closed design into a silent
// loss with a normal PUBACK, and nothing else in the tree would notice. So each is held to a
// real broker, over real MQTT, and the numbers are the ones a device would observe: which
// publishes were PUBACKed, and which of those the platform's own consumer later read back.
//
// # The negative control
//
// TestADiscardOldMessageStoreLosesPubackedMessagesAndTheGuardSeesIt runs the same scenario
// as the first test against the stream the gateway creates by default (DiscardOld) and
// ASSERTS THAT PUBACKed MESSAGES ARE LOST. It is a control in both directions: the loss
// measurement (pubackedLost) is shown to be able to report loss, and a broker change that
// stopped evicting would fail this test too, which is exactly the kind of change the other
// tests' premises would need to be re-read for.
//
// The cluster halves of the guard are in mqtt_failclosed_guard_cluster_test.go.

const (
	guardInstance = "mqfc"
	guardTenant   = "t1"
	guardDurable  = "mqfc-platform"
	// guardCap is the message ceiling the single-server tests put on $MQTT_msgs. A count
	// bound rather than a byte bound so the point at which the store fills does not depend on
	// the size of the broker's header or of a payload.
	guardCap = 20
)

// guardFilter is the platform durable's filter subject for an instance: every device event of
// every tenant. It is the shape the design gives the platform's own consumer.
func guardFilter(instance string) string {
	return mqttMsgsSubjectPrefix + instance + ".*.devices.*.events"
}

// guardTopic is the MQTT topic whose gateway subject guardFilter matches.
func guardTopic(instance, tenant, device string) string {
	return fmt.Sprintf("%s/%s/devices/%s/events", instance, tenant, device)
}

// guardStoreConfig is $MQTT_msgs as the platform would declare it before any MQTT client
// connects: bounded, interest retention (the only retention the gateway's stream may have,
// since every QoS 1 SUBSCRIBE adds a session consumer), and the given discard policy.
func guardStoreConfig(discard nats.DiscardPolicy, maxMsgs int64, replicas int) *nats.StreamConfig {
	return &nats.StreamConfig{
		Name:      MqttMessageStore,
		Subjects:  []string{mqttMsgsSubjectPrefix + ">"},
		Storage:   nats.FileStorage,
		Retention: nats.InterestPolicy,
		Discard:   discard,
		MaxMsgs:   maxMsgs,
		MaxBytes:  4 << 20,
		Replicas:  replicas,
	}
}

// guardAddDurable creates the platform durable on $MQTT_msgs.
func guardAddDurable(js nats.JetStreamContext, name, filter string, replicas int) error {
	_, err := js.AddConsumer(MqttMessageStore, &nats.ConsumerConfig{
		Durable:       name,
		FilterSubject: filter,
		AckPolicy:     nats.AckExplicitPolicy,
		DeliverPolicy: nats.DeliverNewPolicy,
		Replicas:      replicas,
	})
	return err
}

// guardBroker is a single embedded server with the MQTT gateway on, restartable on the same
// ports and the same store directory.
type guardBroker struct {
	t        *testing.T
	storeDir string
	natsPort int
	mqttPort int
	srv      *natsserver.Server
	nc       *nats.Conn
	js       nats.JetStreamContext
}

func startGuardBroker(t *testing.T) *guardBroker {
	t.Helper()
	b := &guardBroker{
		t:        t,
		storeDir: dctest.JetStreamStoreDir(t),
		natsPort: -1,
		mqttPort: -1,
	}
	b.start()
	t.Cleanup(b.stop)
	return b
}

func (b *guardBroker) start() {
	b.t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:       "127.0.0.1",
		Port:       b.natsPort,
		ServerName: "mqfc-single",
		JetStream:  true,
		StoreDir:   b.storeDir,
		MQTT:       natsserver.MQTTOpts{Host: "127.0.0.1", Port: b.mqttPort},
	})
	require.NoError(b.t, err)
	go srv.Start()
	require.True(b.t, srv.ReadyForConnections(15*time.Second), "the test broker never became ready")
	b.srv = srv
	// The first start binds both ports itself (-1); read them back so a restart can come
	// back on the SAME ports, which is the point of restart(). That rebind is the one place
	// a released port is reused, and it has to be: the clients under test reconnect to it.
	b.natsPort = srv.Addr().(*net.TCPAddr).Port
	b.mqttPort = dctest.MQTTPort(b.t, srv)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(b.t, err)
	js, err := nc.JetStream()
	require.NoError(b.t, err)
	b.nc, b.js = nc, js
}

func (b *guardBroker) stop() {
	if b.nc != nil {
		b.nc.Close()
		b.nc = nil
	}
	if b.srv != nil {
		b.srv.Shutdown()
		b.srv.WaitForShutdown()
		b.srv = nil
	}
}

// restart stops the server and starts another on the same ports over the same store.
func (b *guardBroker) restart() {
	b.t.Helper()
	b.stop()
	b.start()
}

func (b *guardBroker) mqttURL() string { return fmt.Sprintf("tcp://127.0.0.1:%d", b.mqttPort) }

// declareStore creates $MQTT_msgs (and the platform durable, when filter is not empty) the way
// the platform would before the gateway has an account to set up.
func (b *guardBroker) declareStore(discard nats.DiscardPolicy, filter string) {
	b.t.Helper()
	_, err := b.js.AddStream(guardStoreConfig(discard, guardCap, 1))
	require.NoError(b.t, err)
	if filter != "" {
		require.NoError(b.t, guardAddDurable(b.js, guardDurable, filter, 0))
	}
}

func (b *guardBroker) storeInfo() *nats.StreamInfo {
	b.t.Helper()
	si, err := b.js.StreamInfo(MqttMessageStore)
	require.NoError(b.t, err)
	return si
}

// guardClient is a paho client that remembers whether, and when, its connection was lost.
type guardClient struct {
	t    *testing.T
	c    mqtt.Client
	lost chan struct{}
}

// connectGuardClient connects a persistent (CleanSession=false) session. The store is shared
// between the clients of one device so that a packet the first connection left unacknowledged
// is the one the second re-sends, which is what a real device's persistent session does.
func connectGuardClient(t *testing.T, broker, clientID string, store mqtt.Store, reconnect bool) *guardClient {
	t.Helper()
	g, err := tryConnectGuardClient(t, broker, clientID, store, reconnect)
	require.NoErrorf(t, err, "connecting %q", clientID)
	return g
}

// tryConnectGuardClient is connectGuardClient that returns the failure, for a test that
// connects while the broker is being moved under it and is entitled to try again.
func tryConnectGuardClient(t *testing.T, broker, clientID string, store mqtt.Store, reconnect bool) (*guardClient, error) {
	t.Helper()
	g := &guardClient{t: t, lost: make(chan struct{})}
	var once sync.Once
	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(clientID).
		SetCleanSession(false).
		SetStore(store).
		SetAutoReconnect(reconnect).
		SetMaxReconnectInterval(time.Second).
		SetConnectTimeout(10 * time.Second).
		SetConnectionLostHandler(func(mqtt.Client, error) { once.Do(func() { close(g.lost) }) })
	g.c = mqtt.NewClient(opts)
	tok := g.c.Connect()
	if !tok.WaitTimeout(15 * time.Second) {
		return nil, fmt.Errorf("connect timed out")
	}
	if err := tok.Error(); err != nil {
		return nil, err
	}
	t.Cleanup(func() { g.c.Disconnect(100) })
	return g, nil
}

// publish sends one QoS 1 message and reports whether the broker PUBACKed it. A connection
// the broker closed instead is "no", and so is nothing at all within wait: the test fails on
// that, because the broker is meant to answer one way or the other well inside the wait.
func (g *guardClient) publish(topic, payload string, wait time.Duration) bool {
	g.t.Helper()
	tok := g.c.Publish(topic, 1, false, payload)
	select {
	case <-tok.Done():
		return tok.Error() == nil
	case <-g.lost:
		// The ack may have been written just before the close.
		select {
		case <-tok.Done():
			return tok.Error() == nil
		default:
			return false
		}
	case <-time.After(wait):
		g.t.Fatalf("publish of %q was neither acknowledged nor refused within %v", payload, wait)
		return false
	}
}

// publishUntilRefused publishes payloads "<prefix>-000", "<prefix>-001", ... one at a time
// until one is not PUBACKed or n have been, and returns the ones that were.
func (g *guardClient) publishUntilRefused(topic, prefix string, n int) (acked []string, refused string) {
	g.t.Helper()
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("%s-%03d", prefix, i)
		if !g.publish(topic, p, 20*time.Second) {
			return acked, p
		}
		acked = append(acked, p)
	}
	return acked, ""
}

// guardDrain reads a durable until `done` says it has seen enough or the deadline passes,
// acknowledging every message it reads. It returns what it read, in order, duplicates kept.
func guardDrain(t *testing.T, js nats.JetStreamContext, durable string, within time.Duration, done func(got []string) bool) []string {
	t.Helper()
	ci, err := js.ConsumerInfo(MqttMessageStore, durable)
	require.NoError(t, err)
	sub, err := js.PullSubscribe(ci.Config.FilterSubject, durable, nats.Bind(MqttMessageStore, durable))
	require.NoError(t, err)
	defer sub.Unsubscribe()
	var got []string
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		msgs, err := sub.Fetch(64, nats.MaxWait(400*time.Millisecond))
		for _, m := range msgs {
			got = append(got, string(m.Data))
			_ = m.AckSync()
		}
		if err != nil && !errors.Is(err, nats.ErrTimeout) {
			t.Fatalf("reading durable %q: %v", durable, err)
		}
		if done != nil && done(got) {
			return got
		}
		if done == nil && len(msgs) == 0 {
			return got
		}
	}
	return got
}

// pubackedLost is the measurement the whole guard rests on: the messages a device was told
// were stored that the platform's own consumer never read.
func pubackedLost(acked, stored []string) []string {
	have := make(map[string]bool, len(stored))
	for _, s := range stored {
		have[s] = true
	}
	var lost []string
	for _, a := range acked {
		if !have[a] {
			lost = append(lost, a)
		}
	}
	return lost
}

func containsString(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}

// A full broker store gives the device NO PUBACK and closes its connection; the platform's
// durable then drains; and the packet the device kept is stored when it re-sends.
//
// Every message the device was told was stored is read back from the durable (nothing
// PUBACKed is lost), the refused one is not in the store when it is refused, and it is after
// the resend.
//
// GUARD: kills a broker whose full store PUBACKs anyway (acked > the bound, or no close), one
// that stores the refused packet anyway (the store is over the bound), and one that loses or
// drops a PUBACKed message on the way to the consumer.
func TestAFullBrokerStoreWithholdsThePubackAndClosesTheConnection(t *testing.T) {
	b := startGuardBroker(t)
	b.declareStore(nats.DiscardNew, guardFilter(guardInstance))

	clientID, err := DeviceClientID(guardInstance, guardTenant, "d1")
	require.NoError(t, err)
	topic := guardTopic(guardInstance, guardTenant, "d1")
	store := mqtt.NewMemoryStore()

	first := connectGuardClient(t, b.mqttURL(), clientID, store, false)
	acked, refused := first.publishUntilRefused(topic, "m", 3*guardCap)
	require.Len(t, acked, guardCap, "the store holds %d messages; that many PUBACKs, then none", guardCap)
	require.Equal(t, fmt.Sprintf("m-%03d", guardCap), refused, "the first publish over the bound is the one refused")
	select {
	case <-first.lost:
	case <-time.After(10 * time.Second):
		t.Fatal("the broker withheld the PUBACK but left the connection open")
	}
	require.EqualValues(t, guardCap, b.storeInfo().State.Msgs, "a refused message is not stored")

	// The platform drains its durable; interest retention then frees the store.
	stored := guardDrain(t, b.js, guardDurable, 10*time.Second, nil)
	require.Equal(t, acked, stored, "the durable reads back exactly the messages that were PUBACKed, in order")
	require.Eventually(t, func() bool { return b.storeInfo().State.Msgs == 0 }, 5*time.Second, 50*time.Millisecond,
		"an acknowledged message leaves an interest-retention store")

	// The device reconnects with the same persistent session and the packet it never had a
	// PUBACK for goes out again.
	connectGuardClient(t, b.mqttURL(), clientID, store, false)
	resent := guardDrain(t, b.js, guardDurable, 15*time.Second, func(got []string) bool { return len(got) >= 1 })
	require.Equal(t, []string{refused}, resent, "the resend of the refused packet is stored once the store has room")

	all := append(append([]string{}, stored...), resent...)
	require.Empty(t, pubackedLost(acked, all), "a PUBACKed message was lost")
}

// The negative control for the test above: the stream the gateway creates when nobody
// declared one is DiscardOld, and there a full store EVICTS the oldest message to make room
// and PUBACKs the new one. The device is told every message was stored; the consumer reads
// back fewer. THIS TEST ASSERTS THE LOSS.
//
// GUARD: it is the measurement's own control (pubackedLost must be able to say "lost"), and a
// nats-server that stopped evicting under DiscardOld, or started refusing, fails it.
func TestADiscardOldMessageStoreLosesPubackedMessagesAndTheGuardSeesIt(t *testing.T) {
	b := startGuardBroker(t)
	b.declareStore(nats.DiscardOld, guardFilter(guardInstance))

	clientID, err := DeviceClientID(guardInstance, guardTenant, "d1")
	require.NoError(t, err)
	topic := guardTopic(guardInstance, guardTenant, "d1")

	c := connectGuardClient(t, b.mqttURL(), clientID, mqtt.NewMemoryStore(), false)
	total := guardCap + 10
	acked, refused := c.publishUntilRefused(topic, "m", total)
	require.Empty(t, refused, "a DiscardOld store never refuses")
	require.Len(t, acked, total, "every publish was PUBACKed")
	select {
	case <-c.lost:
		t.Fatal("a DiscardOld store closed the connection")
	default:
	}

	stored := guardDrain(t, b.js, guardDurable, 10*time.Second, nil)
	lost := pubackedLost(acked, stored)
	require.Len(t, lost, total-guardCap, "the store keeps %d of %d PUBACKed messages", guardCap, total)
	require.Equal(t, "m-000", lost[0], "it is the oldest that are evicted")
}

// The platform's declared $MQTT_msgs, and the durable on it, are what the gateway uses: the
// gateway looks the stream up and leaves a stream it finds alone, it does not apply its own
// config over it, and none of that changes across a server restart.
//
// GUARD: kills an nats-server that rewrites an existing stream at account setup (Discard,
// bound, replicas, retention or subjects changing), that loses the durable at restart, or
// whose restart brings the stream back unbounded; and, last, shows the restarted store still
// refuses rather than evicts.
func TestThePrecreatedMessageStoreConfigSurvivesARestartAndTheGatewaysAccountSetup(t *testing.T) {
	b := startGuardBroker(t)
	b.declareStore(nats.DiscardNew, guardFilter(guardInstance))
	want := b.storeInfo().Config

	assertUnchanged := func(when string) {
		t.Helper()
		si := b.storeInfo()
		require.Equal(t, nats.DiscardNew, si.Config.Discard, "%s: discard policy", when)
		require.Equal(t, nats.InterestPolicy, si.Config.Retention, "%s: retention", when)
		require.EqualValues(t, want.MaxMsgs, si.Config.MaxMsgs, "%s: message bound", when)
		require.EqualValues(t, want.MaxBytes, si.Config.MaxBytes, "%s: byte bound", when)
		require.Equal(t, want.Replicas, si.Config.Replicas, "%s: replicas", when)
		require.Equal(t, want.Subjects, si.Config.Subjects, "%s: subjects", when)
		ci, err := b.js.ConsumerInfo(MqttMessageStore, guardDurable)
		require.NoErrorf(t, err, "%s: the platform durable", when)
		require.Equal(t, guardFilter(guardInstance), ci.Config.FilterSubject, "%s: durable filter", when)
	}
	assertUnchanged("before any MQTT client")

	// A first MQTT client makes the gateway set the account up; it creates its other streams
	// then, which is how this test knows the setup ran.
	clientID, err := DeviceClientID(guardInstance, guardTenant, "d1")
	require.NoError(t, err)
	connectGuardClient(t, b.mqttURL(), clientID, mqtt.NewMemoryStore(), false)
	_, err = b.js.StreamInfo("$MQTT_sess")
	require.NoError(t, err, "the gateway's account setup did not run")
	assertUnchanged("after the gateway's account setup")

	b.restart()
	assertUnchanged("after a server restart")

	connectGuardClient(t, b.mqttURL(), clientID, mqtt.NewMemoryStore(), false)
	assertUnchanged("after a restart and a reconnect")

	c := connectGuardClient(t, b.mqttURL(), "restarted-"+clientID, mqtt.NewMemoryStore(), false)
	acked, refused := c.publishUntilRefused(guardTopic(guardInstance, guardTenant, "d1"), "r", 3*guardCap)
	require.Len(t, acked, guardCap, "the restarted store holds %d messages", guardCap)
	require.NotEmpty(t, refused, "the restarted store refuses what it cannot hold")
}

// With no consumer on $MQTT_msgs a QoS 1 message is SKIPPED (interest retention has nobody
// to keep it for) and the device is still PUBACKed. This is the hazard the fail-closed
// design has to close with a durable that is always there, and the test pins it so the design
// cannot forget it: the PUBACK alone says nothing about whether anything holds the message.
//
// GUARD: the positive control is the same publishes with the durable present, which are
// stored; a broker that started storing without interest (or refusing it) fails the first
// half, and one that stopped storing with interest fails the second.
func TestWithNoConsumerOnTheMessageStoreAQoS1MessageIsSkippedYetAcknowledged(t *testing.T) {
	b := startGuardBroker(t)
	b.declareStore(nats.DiscardNew, "")

	clientID, err := DeviceClientID(guardInstance, guardTenant, "d1")
	require.NoError(t, err)
	topic := guardTopic(guardInstance, guardTenant, "d1")
	c := connectGuardClient(t, b.mqttURL(), clientID, mqtt.NewMemoryStore(), false)

	acked, refused := c.publishUntilRefused(topic, "n", 5)
	require.Empty(t, refused)
	require.Len(t, acked, 5, "every publish was PUBACKed")
	require.EqualValues(t, 0, b.storeInfo().State.Msgs, "and not one of them is in the store")

	require.NoError(t, guardAddDurable(b.js, guardDurable, guardFilter(guardInstance), 0))
	more, refused := c.publishUntilRefused(topic, "y", 3)
	require.Empty(t, refused)
	require.Len(t, more, 3)
	require.EqualValues(t, 3, b.storeInfo().State.Msgs, "with a consumer in place the same publishes are stored")
	stored := guardDrain(t, b.js, guardDurable, 10*time.Second, nil)
	require.Equal(t, more, stored, "and only those; the skipped ones are not recoverable by creating a consumer later")
}

// Two instances that share one broker (one account) share ONE $MQTT_msgs, and so one bound:
// one instance's backlog refuses the other's devices. This is why a bounded, fail-closed
// store is a single-instance arrangement, and why on a broker serving several instances the
// design stays on the per-instance capture streams.
//
// GUARD: kills a nats-server that scopes the message store by anything finer than the account
// (a second store would appear, or the other instance's publish would be acknowledged).
func TestTwoInstancesOnOneBrokerShareOneMessageStoreSoOnesBacklogRefusesTheOthers(t *testing.T) {
	b := startGuardBroker(t)
	b.declareStore(nats.DiscardNew, "")
	require.NoError(t, guardAddDurable(b.js, guardDurable+"-a", guardFilter("insta"), 0))
	require.NoError(t, guardAddDurable(b.js, guardDurable+"-b", guardFilter("instb"), 0))

	idA, err := DeviceClientID("insta", guardTenant, "d1")
	require.NoError(t, err)
	idB, err := DeviceClientID("instb", guardTenant, "d1")
	require.NoError(t, err)
	a := connectGuardClient(t, b.mqttURL(), idA, mqtt.NewMemoryStore(), false)
	bb := connectGuardClient(t, b.mqttURL(), idB, mqtt.NewMemoryStore(), false)

	// Both instances' messages land in the one stream.
	ackedB, refused := bb.publishUntilRefused(guardTopic("instb", guardTenant, "d1"), "b", 3)
	require.Empty(t, refused)
	require.Len(t, ackedB, 3)
	ackedA, refused := a.publishUntilRefused(guardTopic("insta", guardTenant, "d1"), "a", 2)
	require.Empty(t, refused)
	require.Len(t, ackedA, 2)
	require.EqualValues(t, 5, b.storeInfo().State.Msgs, "one store holds both instances' messages")
	var stores []string
	for name := range b.js.StreamNames() {
		if strings.HasPrefix(name, MqttMessageStore) {
			stores = append(stores, name)
		}
	}
	require.Equal(t, []string{MqttMessageStore}, stores, "there is one message store per account, not per instance")

	// Instance A's reader stalls (nobody drains durable a) and A fills the shared bound.
	more, refused := a.publishUntilRefused(guardTopic("insta", guardTenant, "d1"), "a2", 3*guardCap)
	require.NotEmpty(t, refused, "the shared store fills")
	require.Len(t, more, guardCap-5, "A fills what the three of B's messages left")

	// Instance B's device is refused although B's own durable holds only its three.
	require.False(t, bb.publish(guardTopic("instb", guardTenant, "d1"), "b-late", 20*time.Second),
		"B's publish was PUBACKed into a store A filled")
	select {
	case <-bb.lost:
	case <-time.After(10 * time.Second):
		t.Fatal("the broker withheld B's PUBACK but left its connection open")
	}
}

// The tenant purge sweeps $MQTT_msgs consumers by filter subject. The platform durable's
// filter has a wildcard where the tenant would be, so it can never begin with a tenant's
// prefix, and the purge leaves it alone: erasing a tenant must not delete the consumer whose
// absence would turn the fail-closed store into silent loss.
//
// The control is in the same run: the purge DOES delete the victim's own session consumer on
// that stream, so a purge that matched nothing could not pass for one that spared the durable.
//
// GUARD: kills a purge filter that matches by anything looser than the tenant's literal
// prefix (a wildcard-blind or Contains-style match), and a tenant token that could be a
// wildcard.
func TestTheTenantPurgeNeverDeletesThePlatformDurable(t *testing.T) {
	nc, js, broker := mqttRig(t)
	connectDevice(t, broker, mqttVictim, "dev1")
	connectDevice(t, broker, mqttBystander, "dev1")
	require.NoError(t, guardAddDurable(js, guardDurable, guardFilter(mqttInstance), 0))
	before := consumerNames(t, js, MqttMessageStore)
	require.Contains(t, before, guardDurable)

	res, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, mqttVictim)
	require.NoError(t, err)
	require.Greater(t, res.Consumers, int64(0), "the control: the purge deleted the victim's own consumer")
	for _, tenant := range []string{mqttBystander, "devices", "events"} {
		_, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, tenant)
		require.NoError(t, err)
	}
	// A wildcard is not a tenant: the purge refuses to compose a prefix from it.
	_, err = PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, "*")
	require.Error(t, err)
	_, err = PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, ">")
	require.Error(t, err)

	after := consumerNames(t, js, MqttMessageStore)
	require.Equal(t, []string{guardDurable}, after, "only the platform durable remains")
	ci, err := js.ConsumerInfo(MqttMessageStore, guardDurable)
	require.NoError(t, err)
	require.Equal(t, guardFilter(mqttInstance), ci.Config.FilterSubject)
}
