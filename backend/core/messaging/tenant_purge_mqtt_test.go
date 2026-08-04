// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"fmt"
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
// broker files the records, a real paho client causes them, and the assertions read them
// back through the LIBRARY's API rather than through the code under test.
//
// The two places a record cannot be caused deterministically (a parked QoS 2 packet needs a
// client that abandons the handshake mid-flight) are seeded — but seeded at a subject shape
// this file first pins against the server's OWN stream configuration, so the shape is still
// not this package's assumption.

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
	sub := c.Subscribe(topic, 1, func(mqtt.Client, mqtt.Message) {})
	require.True(t, sub.WaitTimeout(10*time.Second), "subscribing %q timed out", clientID)
	require.NoErrorf(t, sub.Error(), "subscribing %q to %q", clientID, topic)

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
		require.NotEmpty(t, rec.Cons, "a subscribed session must carry its consumer")
		for _, c := range rec.Cons {
			assert.NotEmpty(t, c.Durable, "a consumer with no durable name cannot be deleted")
			assert.Containsf(t, c.FilterSubject, mqttVictim,
				"a subscription consumer's filter is what the tenant sweep matches on; %q does not "+
					"name the tenant", c.FilterSubject)
		}
	}
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

	survivors := map[string]bool{bystanderID: true, adjacentTenantID: true}
	for i, id := range []string{victimID, suffixedID, siblingID, adjacentTenantID, bystanderID} {
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
