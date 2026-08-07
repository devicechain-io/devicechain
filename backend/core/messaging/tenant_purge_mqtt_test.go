// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// The MQTT gateway erasure, driven through a REAL MQTT GATEWAY.
//
// 🔴 A FAKE HERE WOULD MEASURE THE FAKE, AND THAT IS NOT A GENERAL WORRY, IT IS THIS FILE'S
// SPECIFIC ONE. Every address the erasure uses is a shape nats-server chose: the subject a
// session is filed under, the JSON field its client id sits in, the filter on the consumer
// a subscription creates. A fixture that writes those shapes is a fixture that agrees with
// whatever this package believes about them — including a belief that is wrong, which is
// exactly how the client-id pin came to ship with a false justification attached. So a real
// broker files the records, real clients cause them, and the assertions read them back
// through the LIBRARY's API rather than through the code under test.
//
// Every LAYOUT this package reads is therefore pinned against something the server produced:
// the prefixes against the stream configs it declared, the session digest against the
// consumer name it chose, the parked-packet subject against a packet it filed. Where a test
// still seeds records — the bulk-behaviour ones, which need several ids at once — it seeds
// at a layout one of those has already redeemed, so the seed is not this package's belief
// about the layout, only about how many of them there are.

const (
	mqttInstance  = "inst1"
	mqttVictim    = "victim-tenant"
	mqttBystander = "bystander-tenant"
)

// mqttRig starts a JetStream server with the MQTT gateway enabled and returns a NATS
// connection to it plus the MQTT listener address.
func mqttRig(t *testing.T) (*nats.Conn, nats.JetStreamContext, string) {
	return mqttRigInDomain(t, "")
}

// mqttRigInDomain is mqttRig with a JetStream domain, which is what puts an extra token
// into the subject a session is filed under.
//
// 🔴 IT EXISTS BECAUSE THE DOMAIN-LESS RIG CANNOT FAIL THE ASSERTION THAT NEEDS IT. With no
// domain, "$MQTT.sess.{digest}" makes "everything after the last dot" and "everything after
// the prefix" the same string, so a derivation that ignores the domain entirely passes —
// which was measured, not assumed: that mutation survived the test before this rig existed.
func mqttRigInDomain(t *testing.T, domain string) (*nats.Conn, nats.JetStreamContext, string) {
	t.Helper()
	mqttPort := dctest.FreeTCPPort(t)
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:            "127.0.0.1",
		Port:            -1,
		ServerName:      "mqtt-purge-test",
		JetStream:       true,
		JetStreamDomain: domain,
		StoreDir:        t.TempDir(),
		MQTT: natsserver.MQTTOpts{
			Host: "127.0.0.1",
			Port: mqttPort,
		},
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "the test broker never became ready")
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	return nc, js, fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort)
}

// connectDevice opens a PERSISTENT MQTT session for one device and subscribes it to its own
// command topic, which is what makes the gateway file a session record and create a durable
// consumer. It disconnects before returning: a persistent session outlives the connection,
// and erasing one that nothing holds open is the case the purge is actually for.
func connectDevice(t *testing.T, broker, tenant, device string) string {
	t.Helper()
	clientID, err := DeviceClientID(mqttInstance, tenant, device)
	require.NoError(t, err)

	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(clientID).
		SetCleanSession(false).
		SetConnectTimeout(10 * time.Second)
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	require.True(t, tok.WaitTimeout(10*time.Second), "connecting %q timed out", clientID)
	require.NoErrorf(t, tok.Error(), "connecting %q", clientID)

	topic := fmt.Sprintf("%s/%s/devices/%s/commands", mqttInstance, tenant, device)
	require.NoErrorf(t,
		SubscribeMqttConfirmed(c, topic, 1, func(mqtt.Client, mqtt.Message) {}, 10*time.Second),
		"subscribing %q to %q", clientID, topic)

	c.Disconnect(250)
	return clientID
}

// sessionRecords reads $MQTT_sess through the library, so the assertions do not go through
// the reader they are checking.
func sessionRecords(t *testing.T, js nats.JetStreamContext) map[string]mqttSessionRecord {
	t.Helper()
	info, err := js.StreamInfo(MqttSessionStore, &nats.StreamInfoRequest{
		SubjectsFilter: mqttSessSubjectPrefix + ">",
	})
	if err == nats.ErrStreamNotFound {
		return map[string]mqttSessionRecord{}
	}
	require.NoError(t, err)

	out := map[string]mqttSessionRecord{}
	for subject := range info.State.Subjects {
		msg, err := js.GetLastMsg(MqttSessionStore, subject)
		require.NoErrorf(t, err, "reading %s", subject)
		var rec mqttSessionRecord
		require.NoErrorf(t, json.Unmarshal(msg.Data, &rec), "decoding %s", subject)
		out[subject] = rec
	}
	return out
}

// consumerNames lists a stream's consumers through the library's own iterator.
func consumerNames(t *testing.T, js nats.JetStreamContext, stream string) []string {
	t.Helper()
	var names []string
	for n := range js.ConsumerNames(stream) {
		names = append(names, n)
	}
	return names
}

// TestMqttGatewaySubjectPrefixesAreTheServersOwn pins this package's four gateway prefixes
// against the subjects nats-server declared on the streams it created.
//
// 🔑 IT IS THE REASON THE REST OF THE FILE MAY SEED RECORDS. A wrong prefix constant is the
// one defect that makes every purge silently match nothing while every behavioural test
// that seeds at the same wrong prefix passes — the constant would be checked against itself.
// Reading the shapes off the server's stream configuration breaks that circle without
// needing to provoke a record of each kind.
func TestMqttGatewaySubjectPrefixesAreTheServersOwn(t *testing.T) {
	_, js, broker := mqttRig(t)
	connectDevice(t, broker, mqttVictim, "sensor-001")

	for _, tc := range []struct{ stream, prefix string }{
		{MqttSessionStore, mqttSessSubjectPrefix},
		{MqttMessageStore, mqttMsgsSubjectPrefix},
		{MqttQoS2InStore, mqttQoS2InSubjectPrefix},
		{MqttPubRelStore, "$MQTT.out."},
	} {
		t.Run(tc.stream, func(t *testing.T) {
			info, err := js.StreamInfo(tc.stream)
			require.NoErrorf(t, err, "the gateway did not create %s", tc.stream)
			require.NotEmpty(t, info.Config.Subjects)

			var matched bool
			for _, s := range info.Config.Subjects {
				if strings.HasPrefix(s, tc.prefix) {
					matched = true
				}
			}
			assert.Truef(t, matched, "this package builds addresses under %q, but the server "+
				"declared %s over %v — every filter under that prefix would match nothing and the "+
				"purge would report a clean broker", tc.prefix, tc.stream, info.Config.Subjects)
		})
	}

	// The pubrel prefix is a level deeper than the stream's declared subject, so it gets its
	// own check: it must sit UNDER the stream, or a purge of it addresses a stream that
	// would reject the subject.
	assert.True(t, strings.HasPrefix(mqttPubRelSubjectPrefix, "$MQTT.out."),
		"the PUBREL subject prefix must lie inside %s's subject space", MqttPubRelStore)
}

// parkQoS2Packet leaves ONE inbound QoS 2 message parked in $MQTT_qos2in, by speaking MQTT
// 3.1.1 directly and simply never sending the PUBREL that would release it.
//
// 🔴 PAHO CANNOT DO THIS, AND THAT IS WHY THERE IS A HAND-ROLLED CLIENT HERE. A client
// library completes the QoS 2 handshake for you: it answers PUBREC with PUBREL, which is
// precisely the step that deletes the record under test. Abandoning the handshake needs a
// client that stops mid-exchange, and the alternative — seeding a message at the subject
// this package BELIEVES the server uses — is the circle this whole file exists to avoid.
// Only the server can be trusted to say where it files a parked packet.
//
// The frames are the two smallest in the protocol. CONNECT: fixed header, protocol name
// "MQTT" at level 4, flags (0 = persistent session, so the record outlives the connection),
// keepalive, then the client id. PUBLISH at QoS 2: fixed header with the QoS bits set,
// topic, packet id, payload. Then we stop.
func parkQoS2Packet(t *testing.T, broker, clientID, topic string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(broker, "tcp://"), 10*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))

	str := func(s string) []byte {
		return append([]byte{byte(len(s) >> 8), byte(len(s))}, s...)
	}
	frame := func(header byte, body []byte) []byte {
		// Remaining Length is a varint; every frame here is far below 128 bytes, and the
		// assertion says so rather than the encoder pretending to handle more.
		require.Less(t, len(body), 128, "this hand-rolled encoder only writes single-byte lengths")
		return append([]byte{header, byte(len(body))}, body...)
	}

	var connect []byte
	connect = append(connect, str("MQTT")...)
	connect = append(connect, 4 /* protocol level 3.1.1 */, 0 /* flags: persistent */, 0, 60 /* keepalive */)
	connect = append(connect, str(clientID)...)
	_, err = conn.Write(frame(0x10, connect))
	require.NoError(t, err)

	// CONNACK: 4 bytes, and the last one is the return code. Reading it is what makes a
	// refused connection fail here rather than silently produce no parked packet later.
	ack := make([]byte, 4)
	_, err = io.ReadFull(conn, ack)
	require.NoError(t, err, "reading CONNACK for %q", clientID)
	require.EqualValuesf(t, 0, ack[3], "the broker refused CONNECT for %q with code %d", clientID, ack[3])

	var publish []byte
	publish = append(publish, str(topic)...)
	publish = append(publish, 0, 7 /* packet id */)
	publish = append(publish, []byte("parked")...)
	// 0x34 = PUBLISH with the QoS bits set to 2. The server files the message the moment it
	// processes this; it is the PUBREL we deliberately never send that would remove it.
	_, err = conn.Write(frame(0x34, publish))
	require.NoError(t, err)

	// Wait for PUBREC, which is the server telling us it has stored the packet. Without
	// this the test races the server and the "parked" record may not exist yet.
	rec := make([]byte, 4)
	_, err = io.ReadFull(conn, rec)
	require.NoError(t, err, "reading PUBREC for %q", clientID)
	require.EqualValuesf(t, 0x50, rec[0], "expected PUBREC, got frame type %#x", rec[0])
}

// TestParkedQoS2SubjectsCarryTheClientIdVerbatim closes the last place this file could have
// been agreeing with itself.
//
// mqttQoS2InClientID reads a LAYOUT — "$MQTT.qos2.in.{clientID}.{packetID}" — and every
// other test that touches parked packets seeds them at that same layout, so all of them
// would stay green if the belief were wrong while production purges silently skipped every
// parked packet. TestMqttGatewaySubjectPrefixesAreTheServersOwn does not redeem it either:
// it pins the PREFIX, not what follows.
//
// So this one makes a real broker file a real parked packet and asserts the client id comes
// back out of the subject the server chose.
func TestParkedQoS2SubjectsCarryTheClientIdVerbatim(t *testing.T) {
	_, js, broker := mqttRig(t)
	clientID, err := DeviceClientID(mqttInstance, mqttVictim, "sensor-001")
	require.NoError(t, err)
	parkQoS2Packet(t, broker, clientID, fmt.Sprintf("%s/%s/devices/sensor-001/events", mqttInstance, mqttVictim))

	info, err := js.StreamInfo(MqttQoS2InStore, &nats.StreamInfoRequest{
		SubjectsFilter: mqttQoS2InSubjectPrefix + ">",
	})
	require.NoError(t, err, "the gateway did not create %s", MqttQoS2InStore)
	require.Len(t, info.State.Subjects, 1,
		"the abandoned QoS 2 handshake should have left exactly one parked packet; got %v",
		info.State.Subjects)

	for subject := range info.State.Subjects {
		assert.Equalf(t, clientID, mqttQoS2InClientID(subject),
			"the server filed a parked packet at %q, and this package reads the client id out of "+
				"it as %q — so a tenant purge would not recognise its own devices' parked packets",
			subject, mqttQoS2InClientID(subject))
	}
}

// TestMqttSessionRecordMatchesWhatTheServerWrites reads a record a real gateway wrote and
// asserts the fields the erasure navigates by are the fields that are actually there.
//
// 🔴 THIS IS THE SNAPSHOT STRUCT'S ONLY GUARD. mqttSessionRecord is a local declaration of
// another project's wire shape, and encoding/json ignores what it does not recognise — so a
// server that renamed "id" would leave ID empty, every session would look unattributable,
// and the purge would erase nothing while reporting success on a clean broker. Nothing else
// in the suite would notice.
func TestMqttSessionRecordMatchesWhatTheServerWrites(t *testing.T) {
	_, js, broker := mqttRig(t)
	clientID := connectDevice(t, broker, mqttVictim, "sensor-001")

	records := sessionRecords(t, js)
	require.Len(t, records, 1, "one persistent session was opened, so the gateway should hold one record")

	for subject, rec := range records {
		assert.Equalf(t, clientID, rec.ID,
			"the session record at %s does not carry the client id the device connected with, so "+
				"nothing in $MQTT_sess can be attributed to a tenant", subject)
	}

	// And the premise the consumer sweep rests on, read off the CONSUMER rather than off a
	// copy of it in the record: a subscription's filter subject names the tenant. If it did
	// not, the sweep in step 1 would match nothing and report a clean broker.
	names := consumerNames(t, js, MqttMessageStore)
	require.Len(t, names, 1)
	info, err := js.ConsumerInfo(MqttMessageStore, names[0])
	require.NoError(t, err)
	assert.Containsf(t, info.Config.FilterSubject, mqttVictim,
		"a subscription consumer's filter is what the tenant sweep matches on; %q does not name "+
			"the tenant", info.Config.FilterSubject)
}

// TestMqttSessionIDHashAgreesWithTheServer checks the file's ONE derived value against the
// server's own.
//
// mqttSessionIDHash reads a digest off a session's subject; nats-server names a
// subscription's durable consumer "{idHash}_{nuid}" from the same digest. Neither knows
// about the other, so their agreement is evidence rather than a tautology — and this is the
// only assertion in the suite that would fail if the subject layout gained a token or the
// digest gained a "." in its alphabet.
// It runs in BOTH domain modes on purpose. Without a domain the subject is
// "$MQTT.sess.{digest}", where reading past the prefix and reading past the last dot give
// the same answer — so the domain-less case alone cannot distinguish a derivation that
// handles the domain from one that ignores it. The "hub" case is the one that can.
func TestMqttSessionIDHashAgreesWithTheServer(t *testing.T) {
	for _, domain := range []string{"", "hub"} {
		t.Run("domain="+domain, func(t *testing.T) {
			_, js, broker := mqttRigInDomain(t, domain)
			connectDevice(t, broker, mqttVictim, "sensor-001")

			records := sessionRecords(t, js)
			require.Len(t, records, 1)
			var subject string
			for s := range records {
				subject = s
			}
			if domain != "" {
				require.Containsf(t, subject, "."+domain+".",
					"the rig was asked for domain %q but the server filed the session at %q, so this "+
						"case is not testing what it claims to", domain, subject)
			}

			names := consumerNames(t, js, MqttMessageStore)
			require.Len(t, names, 1, "one subscription was made, so the gateway should hold one consumer")
			serverHash, _, ok := strings.Cut(names[0], "_")
			require.Truef(t, ok, "the gateway names a subscription consumer {idHash}_{nuid}; %q has no "+
				"separator, so this test can no longer recover the server's own digest", names[0])

			assert.Equalf(t, serverHash, mqttSessionIDHash(subject),
				"the digest read off the session subject %q disagrees with the one the server put in "+
					"consumer %q — so a PUBREL purge for a session with no pubrel block would address "+
					"the wrong subject and silently purge nothing", subject, names[0])
		})
	}
}

// TestPurgeTenantMqttGatewayErasesASessionAndItsConsumer is what the slice exists for.
//
// The bystander is the control, and it is not decoration: an erasure that deleted the whole
// of $MQTT_sess and every consumer on $MQTT_msgs would satisfy "the victim is gone"
// perfectly. Half of every assertion here is that the other tenant survived it.
func TestPurgeTenantMqttGatewayErasesASessionAndItsConsumer(t *testing.T) {
	nc, js, broker := mqttRig(t)
	victimID := connectDevice(t, broker, mqttVictim, "sensor-001")
	bystanderID := connectDevice(t, broker, mqttBystander, "sensor-001")

	before := sessionRecords(t, js)
	require.Len(t, before, 2, "both devices opened a persistent session")
	require.Len(t, consumerNames(t, js, MqttMessageStore), 2, "both devices subscribed")

	res, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, mqttVictim)
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Sessions, "the victim's session record should have been deleted")
	assert.EqualValues(t, 1, res.Consumers, "the victim's subscription consumer should have been deleted")
	assert.Zero(t, res.UnownedSessions, "both client ids have the pinned shape")

	after := sessionRecords(t, js)
	assert.Len(t, after, 1, "exactly one session record should survive")
	var survivor string
	for _, rec := range after {
		survivor = rec.ID
	}
	assert.Equal(t, bystanderID, survivor,
		"the surviving session belongs to the wrong tenant: %q was purged for %q", survivor, mqttVictim)
	assert.NotEqual(t, victimID, survivor)

	names := consumerNames(t, js, MqttMessageStore)
	require.Len(t, names, 1, "exactly one subscription consumer should survive")
	info, err := js.ConsumerInfo(MqttMessageStore, names[0])
	require.NoError(t, err)
	assert.Containsf(t, info.Config.FilterSubject, mqttBystander,
		"the surviving consumer filters %q, which is not the bystander's — the sweep deleted the "+
			"wrong one", info.Config.FilterSubject)
}

// TestPurgeTenantMqttGatewayErasesAConsumerNoSessionRecordNames is the case the consumer
// listing exists FOR, and the only one that can fail if it is broken.
//
// nats-server creates a subscription's durable consumer and then updates the session
// record; a crash between the two leaves a consumer nothing points at. On $MQTT_msgs —
// interest retention — that orphan does not merely linger, it keeps NEW messages on the
// deleted tenant's subjects alive. It is reachable only because a consumer's filter subject
// names the tenant.
//
// 🔴 IT ALSO EXISTS BECAUSE THE OTHER TESTS CANNOT SEE THIS PATH FAIL, WHICH WAS MEASURED
// RATHER THAN REASONED. Breaking the consumer listing's wire envelope left every other test
// green: the session record's own list of consumers is a second route to the same deletion,
// so the erasure test passed over a listing that returned nothing at all. Deleting the
// record first is what removes the healthy neighbour and leaves the listing alone on trial.
func TestPurgeTenantMqttGatewayErasesAConsumerNoSessionRecordNames(t *testing.T) {
	nc, js, broker := mqttRig(t)
	connectDevice(t, broker, mqttVictim, "sensor-001")
	connectDevice(t, broker, mqttBystander, "sensor-001")
	require.Len(t, consumerNames(t, js, MqttMessageStore), 2)

	// Orphan the victim's consumer by deleting the record that names it, which is the state
	// the crash window leaves behind.
	for subject, rec := range sessionRecords(t, js) {
		if rec.ID != mqttInstance+":"+mqttVictim+":sensor-001" {
			continue
		}
		msg, err := js.GetLastMsg(MqttSessionStore, subject)
		require.NoError(t, err)
		require.NoError(t, js.DeleteMsg(MqttSessionStore, msg.Sequence))
	}
	require.Len(t, sessionRecords(t, js), 1, "only the bystander's record should remain")

	res, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, mqttVictim)
	require.NoError(t, err)
	assert.Zero(t, res.Sessions, "the victim's record was already gone")
	assert.EqualValues(t, 1, res.Consumers,
		"the orphaned consumer must be found through the consumer listing, since nothing else "+
			"names it any more")

	names := consumerNames(t, js, MqttMessageStore)
	require.Len(t, names, 1, "the orphan should be gone and the bystander's should not")
	info, err := js.ConsumerInfo(MqttMessageStore, names[0])
	require.NoError(t, err)
	assert.Containsf(t, info.Config.FilterSubject, mqttBystander,
		"the surviving consumer filters %q, so the sweep deleted the wrong one", info.Config.FilterSubject)
}

// deliverQoS2 makes the gateway create a session's PUBREL consumer on $MQTT_out, by having
// one device subscribe to a topic at QoS 2 and publish to it. The server creating that
// consumer is a side effect of DELIVERING a QoS 2 message, so it cannot be provoked without
// a real round trip.
//
// It returns the connected client so the caller can close it after inspecting the broker.
func deliverQoS2(t *testing.T, broker, tenant, device string) (mqtt.Client, string) {
	t.Helper()
	clientID, err := DeviceClientID(mqttInstance, tenant, device)
	require.NoError(t, err)

	delivered := make(chan struct{}, 1)
	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(clientID).
		SetCleanSession(false).
		SetConnectTimeout(10 * time.Second)
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	require.True(t, tok.WaitTimeout(10*time.Second), "connecting %q timed out", clientID)
	require.NoErrorf(t, tok.Error(), "connecting %q", clientID)
	t.Cleanup(func() { c.Disconnect(250) })

	topic := fmt.Sprintf("%s/%s/devices/%s/commands", mqttInstance, tenant, device)
	require.NoError(t, SubscribeMqttConfirmed(c, topic, 2, func(mqtt.Client, mqtt.Message) {
		select {
		case delivered <- struct{}{}:
		default:
		}
	}, 10*time.Second))

	pub := c.Publish(topic, 2, false, []byte("qos2"))
	require.True(t, pub.WaitTimeout(10*time.Second), "publishing QoS 2 to %q timed out", topic)
	require.NoError(t, pub.Error())

	select {
	case <-delivered:
	case <-time.After(10 * time.Second):
		t.Fatalf("the QoS 2 message was never delivered to %q, so the gateway never created its "+
			"PUBREL consumer and this test would prove nothing", clientID)
	}
	return c, clientID
}

// TestPurgeTenantMqttGatewayErasesThePubRelConsumer covers the $MQTT_out sweep, which is the
// one addressed by a DIGEST rather than by a subject naming the tenant.
//
// 🔴 IT EXISTS BECAUSE A MUTATION PROVED THE PATH WAS UNTESTED. Breaking
// pubRelSubjectIDHash so it returned the whole subject instead of the digest left the entire
// suite green: nothing anywhere created a PUBREL consumer, so nothing could notice that the
// sweep matching them had stopped matching. A QoS 2 DELIVERY is what makes the gateway
// create one, which is why this test does a real round trip rather than seeding.
//
// The bystander is the control, and here it is doing real work: the digest is opaque, so a
// sweep that matched on the wrong part of the subject would plausibly match everything.
func TestPurgeTenantMqttGatewayErasesThePubRelConsumer(t *testing.T) {
	nc, js, broker := mqttRig(t)
	_, victimID := deliverQoS2(t, broker, mqttVictim, "sensor-001")
	deliverQoS2(t, broker, mqttBystander, "sensor-001")

	before := consumerNames(t, js, MqttPubRelStore)
	require.Len(t, before, 2,
		"both devices took a QoS 2 delivery, so the gateway should hold a PUBREL consumer for each")

	// The digest the victim's consumer is filed under, taken from the SESSION record's
	// subject — which is the path the purge uses, cross-checked here against the consumer the
	// server actually created.
	var victimDigest string
	for subject, rec := range sessionRecords(t, js) {
		if rec.ID == victimID {
			victimDigest = mqttSessionIDHash(subject)
		}
	}
	require.NotEmpty(t, victimDigest)

	res, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, mqttVictim)
	require.NoError(t, err)
	assert.GreaterOrEqualf(t, res.Consumers, int64(2),
		"the victim's subscription consumer AND its PUBREL consumer should both have gone; got %d",
		res.Consumers)

	after := consumerNames(t, js, MqttPubRelStore)
	require.Len(t, after, 1, "exactly one PUBREL consumer should survive")
	info, err := js.ConsumerInfo(MqttPubRelStore, after[0])
	require.NoError(t, err)
	assert.NotEqualf(t, victimDigest, pubRelSubjectIDHash(info.Config.FilterSubject),
		"the surviving PUBREL consumer filters %q, which is the victim's own digest — the sweep "+
			"deleted the bystander's", info.Config.FilterSubject)
}

// TestSessionAttributionIsAnchoredAtTheStartOfTheClientId covers the membership test that
// decides which session records get deleted.
//
// 🔴 THE TWO PREFIX TESTS ARE SEPARATE CODE PATHS AND EACH NEEDS ITS OWN CONTROL. Parked
// QoS 2 packets are attributed from a subject; session records are attributed from a
// payload. A mutation swapping HasPrefix for Contains in the SESSION path survived the whole
// suite even after the packet path gained an anchoring control — the two look alike and are
// covered by nothing in common.
//
// The seeded ids are the three ways attribution can go wrong at a boundary: an id that
// merely contains the prefix, one whose tenant is a string extension of it, and one for a
// different instance entirely.
func TestSessionAttributionIsAnchoredAtTheStartOfTheClientId(t *testing.T) {
	nc, js, broker := mqttRig(t)
	// A real connection first, so the gateway creates $MQTT_sess before anything is seeded.
	victimID := connectDevice(t, broker, mqttVictim, "sensor-001")

	adjacentTenantID, err := DeviceClientID(mqttInstance, mqttVictim+"-2", "sensor-001")
	require.NoError(t, err)
	otherInstanceID, err := DeviceClientID("inst2", mqttVictim, "sensor-001")
	require.NoError(t, err)
	// Written out because DeviceClientID would refuse to mint it: this shape can only arrive
	// on the wire. It names tenant "inst1" in instance "other", so it CONTAINS the victim's
	// prefix without starting with it.
	unanchoredID := "other:" + mqttInstance + ":" + mqttVictim + ":sensor-001"

	survivors := map[string]bool{adjacentTenantID: true, otherInstanceID: true, unanchoredID: true}
	for i, id := range []string{adjacentTenantID, otherInstanceID, unanchoredID} {
		rec, err := json.Marshal(mqttSessionRecord{ID: id})
		require.NoError(t, err)
		_, err = js.Publish(fmt.Sprintf("%sseed%04d", mqttSessSubjectPrefix, i), rec)
		require.NoErrorf(t, err, "seeding a session record for %q", id)
	}

	res, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, mqttVictim)
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Sessions,
		"only the victim's own session should have been deleted")
	assert.Zero(t, res.UnownedSessions,
		"all three survivors are device-shaped, just not this tenant's")

	after := sessionRecords(t, js)
	require.Len(t, after, len(survivors))
	for subject, rec := range after {
		assert.NotEqualf(t, victimID, rec.ID, "the victim's own record survived at %s", subject)
		assert.Truef(t, survivors[rec.ID],
			"%q survived the purge of %q but is not one of the three boundary cases seeded",
			rec.ID, mqttVictim)
	}
}

// TestPurgeTenantMqttGatewayIsIdempotent covers the Store contract's actual requirement: it
// is called on every pass until it reports clean, so the pass AFTER the work must succeed
// and report zero rather than error on records that are already gone.
func TestPurgeTenantMqttGatewayIsIdempotent(t *testing.T) {
	nc, js, broker := mqttRig(t)
	connectDevice(t, broker, mqttVictim, "sensor-001")

	first, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, mqttVictim)
	require.NoError(t, err)
	require.NotZero(t, first.Total(), "the first pass must actually erase something, or the "+
		"second proves nothing")

	second, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, mqttVictim)
	require.NoError(t, err)
	assert.Zero(t, second.Total(), "a second pass found something to erase, so the first did not finish")
	assert.Empty(t, sessionRecords(t, js))
}

// TestPurgeTenantMqttGatewayPurgesParkedQoS2Packets covers the stream whose subjects carry
// the client id verbatim.
//
// The packets are seeded, because leaving one parked needs a client that abandons the QoS 2
// handshake after PUBREC and paho completes it — but they are seeded at
// mqttQoS2InSubjectPrefix, which TestMqttGatewaySubjectPrefixesAreTheServersOwn has already
// checked against the server's own declaration of that stream. What is being tested here is
// the attribution: that the client-id token inside the subject decides which packets go.
func TestPurgeTenantMqttGatewayPurgesParkedQoS2Packets(t *testing.T) {
	nc, js, broker := mqttRig(t)
	// One real connection, so the gateway creates its streams before anything is seeded.
	connectDevice(t, broker, mqttVictim, "sensor-001")

	victimID, err := DeviceClientID(mqttInstance, mqttVictim, "sensor-001")
	require.NoError(t, err)
	bystanderID, err := DeviceClientID(mqttInstance, mqttBystander, "sensor-001")
	require.NoError(t, err)
	// A device of the victim's tenant using its own discriminator — the suffixed form the
	// callout admits. It must be purged too, or firmware that splits publish and subscribe
	// across two connections leaves half its state behind.
	suffixedID := victimID + ":pub"
	// A second device of the victim's tenant, whose token merely starts with the first's.
	// It IS the victim's and must go — the device field is not what bounds a tenant purge.
	siblingID, err := DeviceClientID(mqttInstance, mqttVictim, "sensor-0011")
	require.NoError(t, err)
	// 🔑 THE ADJACENCY CONTROL, AND IT IS THE ONLY THING THAT CATCHES A TENANT PREFIX BUILT
	// WITHOUT ITS TRAILING SEPARATOR. "victim-tenant" is a string prefix of
	// "victim-tenant-2", so a purge of the first would take the second's packets with it —
	// cross-tenant data loss, silently, and every other control here would stay green
	// because no other tenant in this file shares a prefix with the victim.
	adjacentTenantID, err := DeviceClientID(mqttInstance, mqttVictim+"-2", "sensor-001")
	require.NoError(t, err)
	// 🔑 THE ANCHORING CONTROL. This id CONTAINS the victim's prefix but does not start with
	// it — it names tenant "inst1" in instance "other", a different tenant that happens to be
	// named after ours. A membership test written with Contains rather than HasPrefix admits
	// it and deletes another tenant's packets; nothing else in this file has that shape, and a
	// mutation swapping the two survived the whole suite before this case existed.
	// Written out rather than composed: DeviceClientID would refuse to build it, which is the
	// point — this is a shape that can only arrive on the wire, never from our own minting.
	unanchoredID := "other:" + mqttInstance + ":" + mqttVictim + ":sensor-001"
	require.Containsf(t, unanchoredID, mqttInstance+":"+mqttVictim+":",
		"this control only controls anything if %q really does contain the victim's prefix", unanchoredID)

	survivors := map[string]bool{bystanderID: true, adjacentTenantID: true, unanchoredID: true}
	for i, id := range []string{victimID, suffixedID, siblingID, adjacentTenantID, bystanderID, unanchoredID} {
		_, err := js.Publish(fmt.Sprintf("%s%s.%d", mqttQoS2InSubjectPrefix, id, i+1), []byte("parked"))
		require.NoErrorf(t, err, "seeding a parked packet for %q", id)
	}

	res, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, mqttVictim)
	require.NoError(t, err)
	assert.EqualValues(t, 3, res.Messages,
		"the victim tenant's three client ids — plain, suffixed and a sibling device — should "+
			"all have been purged, and nothing else")

	info, err := js.StreamInfo(MqttQoS2InStore, &nats.StreamInfoRequest{
		SubjectsFilter: mqttQoS2InSubjectPrefix + ">",
	})
	require.NoError(t, err)
	require.Len(t, info.State.Subjects, len(survivors),
		"only the other tenants' packets should survive")
	for subject := range info.State.Subjects {
		id := mqttQoS2InClientID(subject)
		assert.Truef(t, survivors[id],
			"%q survived the purge of %q but belongs to neither surviving tenant", id, mqttVictim)
	}
}

// TestPurgeTenantMqttGatewayReportsASessionItCannotAttribute pins the one thing that is
// still deferred, and pins that it is CONDITIONAL.
//
// A record whose client id is not device-shaped belongs to no tenant — the edge agent's
// uplink is the standing example — so it must be counted and left alone rather than erased.
// Counting it is what makes a pre-pin device session, which would look identical and WOULD
// be tenant data, visible instead of silent.
func TestPurgeTenantMqttGatewayReportsASessionItCannotAttribute(t *testing.T) {
	nc, js, broker := mqttRig(t)
	connectDevice(t, broker, mqttVictim, "sensor-001")

	// Seeded rather than connected: the callout is what refuses this shape in a real
	// deployment, and there is no callout here — which is the point. This is a record that
	// exists despite the pin, either because it predates it or because it belongs to a
	// client the pin does not govern.
	rec, err := json.Marshal(mqttSessionRecord{ID: "dc-edge-agent-inst1-site-42"})
	require.NoError(t, err)
	_, err = js.Publish(mqttSessSubjectPrefix+"abcd1234", rec)
	require.NoError(t, err)

	res, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, mqttVictim)
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Sessions, "the victim's own session must still be erased")
	assert.EqualValues(t, 1, res.UnownedSessions,
		"a record with a non-device client id must be counted, not silently skipped")

	// And it survives: erasing what cannot be attributed would take the edge agent's session
	// with it on every unrelated tenant's purge.
	after := sessionRecords(t, js)
	require.Len(t, after, 1)
	for _, r := range after {
		assert.Equal(t, "dc-edge-agent-inst1-site-42", r.ID)
	}
}

// TestPurgeTenantMqttGatewayRefusesAWideningTenant covers the guard, and its control.
//
// The prefix built from an empty tenant is "inst1::", which matches no legal client id — so
// the damage would be nil here and the guard looks like ceremony. It is not: the same
// validation is what stops a token carrying ":" from composing a prefix that spans two
// fields and selects another tenant's sessions.
func TestPurgeTenantMqttGatewayRefusesAWideningTenant(t *testing.T) {
	nc, _, broker := mqttRig(t)
	connectDevice(t, broker, mqttVictim, "sensor-001")

	for _, bad := range []string{"", "  ", "victim-tenant:", "*", "victim.tenant"} {
		t.Run(fmt.Sprintf("%q", bad), func(t *testing.T) {
			_, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, bad)
			assert.Error(t, err, "a tenant token outside the grammar must be refused, not composed")
		})
	}

	// The control: a well-formed token is not caught by the same guard, or this test would
	// pass over an erasure that never runs at all.
	res, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, mqttVictim)
	require.NoError(t, err)
	assert.NotZero(t, res.Total(), "a legitimate tenant must still be erased")
}

// TestPurgeTenantMqttGatewayToleratesAnInstanceThatHasNeverUsedMqtt covers the ordinary
// case that would otherwise fail every purge on every instance with no MQTT devices: the
// gateway's streams are created lazily, so none of them exists.
func TestPurgeTenantMqttGatewayToleratesAnInstanceThatHasNeverUsedMqtt(t *testing.T) {
	nc, _ := purgeRig(t)

	res, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, mqttVictim)
	require.NoError(t, err, "an instance with no MQTT gateway holds nothing; that is not a failure")
	assert.Zero(t, res.Total())
	assert.Zero(t, res.UnownedSessions)
}
