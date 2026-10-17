// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// The fail-closed reconcile against a real broker with its MQTT gateway on. The guard tests
// beside this file pin the broker behaviours on a store they declare by hand; these hold the
// RECONCILER to producing that store, and to the rest of what it owes: refusing a shared
// broker, undoing itself when the setting goes off, leaving a store it never touched exactly
// as the gateway made it, and the watch noticing what changes underneath it.

// fcHotBytes is the hot ceiling these tests run at, so a store fills in a few hundred
// publishes. The fail-closed store takes the capture stream's ceiling, which is the hot one.
const fcHotBytes = 16 << 10

// failClosedManager is a manager for instance over an existing connection, with the
// fail-closed setting as given.
func failClosedManager(t *testing.T, nc *nats.Conn, js nats.JetStreamContext, instance string, on bool) *NatsManager {
	t.Helper()
	ic := config.NewDefaultInstanceConfiguration()
	ic.Infrastructure.Nats.StreamMaxBytes = fcHotBytes
	ic.Infrastructure.Nats.StreamMaxBytesCold = fcHotBytes
	ic.Infrastructure.Nats.MqttFailClosed = on
	ic.ApplyDefaults()
	if on {
		require.EqualValues(t, fcHotBytes, ic.Infrastructure.Nats.MqttMessageStoreMaxBytes(),
			"premise: the fail-closed store takes the hot ceiling")
	}
	ms := &core.Microservice{InstanceId: instance, FunctionalArea: "event-sources", InstanceConfiguration: *ic}
	return &NatsManager{Microservice: ms, nc: nc, js: js}
}

// fastMqttStoreLookup drops the wait for a gateway stream that does not exist yet: these
// tests never set up the QoS 2 stores, and waiting the real schedule for them is only slow.
func fastMqttStoreLookup(t *testing.T) {
	restore := mqttStoreLookupBackoff
	mqttStoreLookupBackoff = []time.Duration{0}
	t.Cleanup(func() { mqttStoreLookupBackoff = restore })
}

// With the setting on, the disk budget reserves exactly what the fail-closed reconcile bounds
// the message store at, plus the two QoS 2 stores ReconcileMqttQoS2Stores bounds.
//
// GUARD: kills a reservation that still counts the safety cap for a store bounded at the hot
// ceiling, which would understate the disk floor by the difference.
func TestWithTheSettingOnTheReservationIsWhatTheReconcileApplies(t *testing.T) {
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	state, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	n := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	var qos2 int64
	for _, bound := range MqttStoreBounds(state.MaxBytes, n.MqttQoS2StoreMaxBytes) {
		if bound.Name != MqttMessageStore {
			qos2 += bound.MaxBytes
		}
	}
	require.Equal(t, n.MqttStoreReservation(), state.MaxBytes+qos2)
}

// fcMetrics is the watch's series, unregistered, for a test to read.
func fcMetrics(instance string) *MqttFailClosedMetrics {
	m := &MqttFailClosedMetrics{
		durablePresent:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "p"}, []string{"durable"}),
		durableMissing:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "m"}, []string{"durable"}),
		storeRepaired:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "r"}, []string{"setting"}),
		foreignInstances: prometheus.NewGauge(prometheus.GaugeOpts{Name: "f"}),
	}
	m.init(instance)
	return m
}

func fcDurable(t *testing.T, instance string, kind MqttFailClosedKind) MqttFailClosedDurable {
	t.Helper()
	for _, d := range MqttFailClosedDurables(instance) {
		if d.Kind == kind {
			return d
		}
	}
	t.Fatalf("no platform durable of kind %q", kind)
	return MqttFailClosedDurable{}
}

// The reconcile declares the store BEFORE any MQTT client has connected, the gateway keeps
// it when its account is set up, and the result is the fail-closed behaviour end to end: a
// full store gives the device no PUBACK and closes the connection, and everything that WAS
// PUBACKed is read back from the platform's events durable.
//
// GUARD: kills a reconcile that leaves the store discard-old or unbounded (acked > what is
// read back, or no refusal), one that creates no durable or one with the wrong filter (the
// publishes are skipped: nothing read back), and a config the gateway would overwrite.
func TestTheFailClosedReconcileDeclaresAStoreThatRefusesWhatItCannotKeep(t *testing.T) {
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)

	state, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	require.False(t, state.Epoch.IsZero(), "the store's epoch is its creation time")
	require.EqualValues(t, fcHotBytes, state.MaxBytes)
	require.Equal(t, 1, state.Replicas)

	si := b.storeInfo()
	require.Equal(t, nats.DiscardNew, si.Config.Discard)
	require.Equal(t, nats.InterestPolicy, si.Config.Retention)
	require.EqualValues(t, fcHotBytes, si.Config.MaxBytes)
	require.Equal(t, 1, si.Config.Replicas, "replicas are set explicitly")
	require.Equal(t, []string{mqttMsgsSubjectPrefix + ">"}, si.Config.Subjects)
	require.Equal(t, guardInstance, si.Config.Metadata[mqttFailClosedOwnerKey])
	require.Equal(t, state.Epoch, si.Created)
	for _, d := range MqttFailClosedDurables(guardInstance) {
		ci, err := b.js.ConsumerInfo(MqttMessageStore, d.Name)
		require.NoErrorf(t, err, "the %s durable", d.Kind)
		require.Equal(t, d.Filter, ci.Config.FilterSubject)
		require.Equal(t, nats.AckExplicitPolicy, ci.Config.AckPolicy)
		require.Equal(t, nats.DeliverNewPolicy, ci.Config.DeliverPolicy)
		require.Equal(t, MaxDeliver, ci.Config.MaxDeliver)
		require.Equal(t, AckWait, ci.Config.AckWait)
	}

	clientID, err := DeviceClientID(guardInstance, guardTenant, "d1")
	require.NoError(t, err)
	c := connectGuardClient(t, b.mqttURL(), clientID, mqtt.NewMemoryStore(), false)
	_, err = b.js.StreamInfo(MqttSessionStore)
	require.NoError(t, err, "premise: the gateway's account setup ran")
	require.Equal(t, nats.DiscardNew, b.storeInfo().Config.Discard, "the gateway kept the declared store")
	require.EqualValues(t, fcHotBytes, b.storeInfo().Config.MaxBytes)

	acked, refused := c.publishUntilRefused(guardTopic(guardInstance, guardTenant, "d1"), "m", 5000)
	require.NotEmpty(t, acked)
	require.NotEmpty(t, refused, "a full fail-closed store refuses")
	select {
	case <-c.lost:
	case <-time.After(10 * time.Second):
		t.Fatal("the broker withheld the PUBACK but left the connection open")
	}
	stored := guardDrain(t, b.js, fcDurable(t, guardInstance, MqttFailClosedEvents).Name, 20*time.Second, nil)
	require.Equal(t, acked, stored, "the events durable reads back exactly what was PUBACKed")
}

// Command responses and device reports are device QoS 1 publishes too, and each lands on its
// own durable and on no other.
//
// GUARD: kills a durable set that covers events only, and filters that overlap (a message
// counted on two durables would be forwarded twice).
func TestEachKindOfDevicePublishIsHeldByItsOwnDurable(t *testing.T) {
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	_, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)

	clientID, err := DeviceClientID(guardInstance, guardTenant, "d1")
	require.NoError(t, err)
	c := connectGuardClient(t, b.mqttURL(), clientID, mqtt.NewMemoryStore(), false)
	topics := map[MqttFailClosedKind]string{
		MqttFailClosedEvents:    guardTopic(guardInstance, guardTenant, "d1"),
		MqttFailClosedResponses: fmt.Sprintf("%s/%s/%s/d1", guardInstance, guardTenant, SubjectCommandResponses),
		MqttFailClosedReports:   fmt.Sprintf("%s/%s/%s/d1", guardInstance, guardTenant, SubjectDeviceReports),
	}
	for kind, topic := range topics {
		require.Truef(t, c.publish(topic, string(kind), 10*time.Second), "publishing a %s", kind)
	}
	// Another instance's subjects are not this instance's to hold.
	require.True(t, c.publish(guardTopic("otherinst", guardTenant, "d1"), "foreign", 10*time.Second))

	for kind := range topics {
		got := guardDrain(t, b.js, fcDurable(t, guardInstance, kind).Name, 10*time.Second, nil)
		require.Equalf(t, []string{string(kind)}, got, "the %s durable holds its own kind and only that", kind)
	}
}

// A store the gateway created first (discard old, unbounded: a device connected before
// event-sources ever started) is updated in place into the fail-closed config, keeping its
// creation time, and a second pass changes nothing.
//
// GUARD: kills a reconcile that only creates (an existing store would stay discard-old), one
// that recreates (the epoch would move and the store's messages would go), and one that
// writes on every start.
func TestTheFailClosedReconcileUpdatesAGatewayCreatedStoreInPlaceAndOnlyOnce(t *testing.T) {
	b := startGuardBroker(t)
	clientID, err := DeviceClientID(guardInstance, guardTenant, "d1")
	require.NoError(t, err)
	connectGuardClient(t, b.mqttURL(), clientID, mqtt.NewMemoryStore(), false)
	before := b.storeInfo()
	require.Equal(t, nats.DiscardOld, before.Config.Discard, "premise: the gateway's own store")
	require.LessOrEqual(t, before.Config.MaxBytes, int64(0), "premise: the gateway's own store is unbounded")

	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	state, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	after := b.storeInfo()
	require.Equal(t, nats.DiscardNew, after.Config.Discard)
	require.EqualValues(t, fcHotBytes, after.Config.MaxBytes)
	require.Equal(t, before.Created, after.Created, "updated in place, not recreated")
	require.Equal(t, before.Created, state.Epoch)

	cfg := after.Config
	cfg.Description = "idempotency-marker"
	_, err = b.js.UpdateStream(&cfg)
	require.NoError(t, err)
	rep, err := nmgr.reconcileMqttFailClosed(context.Background(), true)
	require.NoError(t, err)
	require.Empty(t, rep.repaired)
	require.Empty(t, rep.createdDurables)
	require.Equal(t, "idempotency-marker", b.storeInfo().Config.Description, "a conforming store is not rewritten")
}

// On a broker that serves another instance the reconcile refuses, names the reason, and
// writes nothing. Two kinds of evidence, each alone: another instance's platform stream, and
// a store already owned by another instance. The control is this instance's own platform
// streams, including one whose suffix ends in another suffix, which must not read as a
// foreign instance.
//
// GUARD: kills a detection that is missing either kind of evidence, one that reads our own
// streams as another instance's, and a reconcile that changes the store before it decides.
func TestTheFailClosedReconcileRefusesABrokerThatServesAnotherInstance(t *testing.T) {
	t.Run("our own streams are not another instance", func(t *testing.T) {
		b := startGuardBroker(t)
		for _, s := range streams.All {
			_, err := b.js.AddStream(&nats.StreamConfig{Name: StreamName(guardInstance, s.Suffix),
				Subjects: []string{"own." + sanitizeName(s.Suffix)}, Storage: nats.MemoryStorage})
			require.NoError(t, err)
		}
		_, err := failClosedManager(t, b.nc, b.js, guardInstance, true).ReconcileMqttFailClosed(context.Background())
		require.NoError(t, err)
	})
	t.Run("another instance's platform stream", func(t *testing.T) {
		b := startGuardBroker(t)
		_, err := b.js.AddStream(&nats.StreamConfig{Name: StreamName("otherinst", streams.InboundEvents),
			Subjects: []string{"otherinst.*." + streams.InboundEvents}, Storage: nats.MemoryStorage})
		require.NoError(t, err)
		_, err = failClosedManager(t, b.nc, b.js, guardInstance, true).ReconcileMqttFailClosed(context.Background())
		require.ErrorIs(t, err, ErrMqttFailClosedSharedBroker)
		require.Contains(t, err.Error(), "otherinst")
		require.Contains(t, err.Error(), "mqttFailClosed")
		_, err = b.js.StreamInfo(MqttMessageStore)
		require.ErrorIs(t, err, nats.ErrStreamNotFound, "a refused reconcile creates nothing")
	})
	t.Run("a store another instance owns", func(t *testing.T) {
		b := startGuardBroker(t)
		cfg := guardStoreConfig(nats.DiscardNew, 0, 1)
		cfg.MaxMsgs = 0
		cfg.Metadata = map[string]string{mqttFailClosedOwnerKey: "otherinst"}
		_, err := b.js.AddStream(cfg)
		require.NoError(t, err)
		_, err = failClosedManager(t, b.nc, b.js, guardInstance, true).ReconcileMqttFailClosed(context.Background())
		require.ErrorIs(t, err, ErrMqttFailClosedSharedBroker)
		require.Equal(t, "otherinst", b.storeInfo().Config.Metadata[mqttFailClosedOwnerKey], "nothing was rewritten")
		names := consumerNames(t, b.js, MqttMessageStore)
		require.Empty(t, names, "no durable was created")
	})
}

// otherInstanceStream reads a stream name as another instance's only when no declared suffix
// reads it as ours.
func TestOtherInstanceStreamReadsOnlyAnotherInstancesPlatformStreams(t *testing.T) {
	tails := make([]string, 0, len(streams.All))
	for _, s := range streams.All {
		tails = append(tails, "_"+sanitizeName(s.Suffix))
	}
	for _, s := range streams.All {
		if other, ok := otherInstanceStream(StreamName("inst1", s.Suffix), "inst1", tails); ok {
			t.Errorf("our own %s stream read as instance %q", s.Suffix, other)
		}
		other, ok := otherInstanceStream(StreamName("inst2", s.Suffix), "inst1", tails)
		if !ok || other != "inst2" {
			t.Errorf("instance inst2's %s stream read as (%q, %v)", s.Suffix, other, ok)
		}
	}
	for _, name := range []string{MqttMessageStore, MqttSessionStore, "KV_inst2_cache", "unrelated", "_" + streams.InboundEvents} {
		if other, ok := otherInstanceStream(name, "inst1", tails); ok {
			t.Errorf("%q read as instance %q", name, other)
		}
	}
}

// 🔴 FLAG OFF IS TODAY. With the setting off, a store the gateway created and this instance
// never made fail-closed comes out of ReconcileMqttStores exactly as it did before the
// setting existed: the bound applied, and every other field of its config, its consumers and
// its metadata untouched.
//
// GUARD: kills any flag-off path that writes the owner mark, changes the discard policy,
// creates or deletes a consumer, or touches anything but MaxBytes.
func TestWithTheSettingOffANeverEnabledStoreIsReconciledExactlyAsBefore(t *testing.T) {
	fastMqttStoreLookup(t)
	b := startGuardBroker(t)
	connectDevice(t, b.mqttURL(), guardTenant, "d1") // a session consumer on the store
	before := b.storeInfo()
	consumersBefore := consumerNames(t, b.js, MqttMessageStore)
	require.NotEmpty(t, consumersBefore, "premise: the gateway's session consumer")

	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, false)
	n := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	require.NoError(t, nmgr.ReconcileMqttStores(context.Background(), n.MqttMessageStoreMaxBytes(), n.MqttQoS2StoreMaxBytes))

	after := b.storeInfo()
	want := before.Config
	want.MaxBytes = n.MqttStoreMaxBytes
	require.Equal(t, want, after.Config, "only the bound moved")
	require.Equal(t, before.Created, after.Created)
	require.ElementsMatch(t, consumersBefore, consumerNames(t, b.js, MqttMessageStore))
}

// Turning the setting off after it was on removes the platform durables, puts the gateway's
// default discard policy back and drops the owner mark, so the store is the safety-capped
// store it was before: left alone, durables nobody reads would fill a discard-new store and
// the gateway would then refuse every QoS 1 publish.
//
// GUARD: kills a flag-off start that leaves the durables, the discard policy or the mark.
func TestTurningTheSettingOffReleasesAStoreThisInstanceMadeFailClosed(t *testing.T) {
	fastMqttStoreLookup(t)
	b := startGuardBroker(t)
	_, err := failClosedManager(t, b.nc, b.js, guardInstance, true).ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)

	off := failClosedManager(t, b.nc, b.js, guardInstance, false)
	n := off.Microservice.InstanceConfiguration.Infrastructure.Nats
	require.NoError(t, off.ReconcileMqttStores(context.Background(), n.MqttMessageStoreMaxBytes(), n.MqttQoS2StoreMaxBytes))

	si := b.storeInfo()
	require.Equal(t, nats.DiscardOld, si.Config.Discard)
	require.EqualValues(t, n.MqttStoreMaxBytes, si.Config.MaxBytes)
	_, marked := si.Config.Metadata[mqttFailClosedOwnerKey]
	require.False(t, marked, "the owner mark is dropped")
	require.Empty(t, consumerNames(t, b.js, MqttMessageStore), "the platform durables are gone")

	// A pod still running with the setting on (a rolling update turning it off) must not
	// create the durables again behind the pod that removed them: its watch stands down and
	// reports them missing, since in its view the guarantee is not in force.
	on := failClosedManager(t, b.nc, b.js, guardInstance, true)
	m := fcMetrics(guardInstance)
	on.checkMqttFailClosed(context.Background(), m)
	require.Empty(t, consumerNames(t, b.js, MqttMessageStore), "the watch recreated durables on a released store")
	require.Equal(t, nats.DiscardOld, b.storeInfo().Config.Discard, "the watch undid the release")
	for _, d := range MqttFailClosedDurables(guardInstance) {
		require.EqualValues(t, 0, testutil.ToFloat64(m.durablePresent.WithLabelValues(d.Name)))
	}
	// A START with the setting on declares the store again, released or not.
	_, err = on.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	require.Equal(t, nats.DiscardNew, b.storeInfo().Config.Discard)
	_, releasedMark := b.storeInfo().Config.Metadata[mqttFailClosedReleasedKey]
	require.False(t, releasedMark, "enabling again clears the released mark")
	require.Len(t, consumerNames(t, b.js, MqttMessageStore), len(MqttFailClosedDurables(guardInstance)))
	require.NoError(t, off.ReconcileMqttStores(context.Background(), n.MqttMessageStoreMaxBytes(), n.MqttQoS2StoreMaxBytes))
	require.Empty(t, consumerNames(t, b.js, MqttMessageStore), "and turning it off again releases it again")

	// Another instance's mark is not this instance's to release: the bound is applied as it
	// always was, and nothing else is touched.
	_, err = failClosedManager(t, b.nc, b.js, "otherinst", true).ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	require.NoError(t, off.ReconcileMqttStores(context.Background(), n.MqttMessageStoreMaxBytes(), n.MqttQoS2StoreMaxBytes))
	require.Equal(t, "otherinst", b.storeInfo().Config.Metadata[mqttFailClosedOwnerKey])
	require.Len(t, consumerNames(t, b.js, MqttMessageStore), len(MqttFailClosedDurables("otherinst")))
}

// The watch is what turns a durable deleted at runtime from silent loss into an alert: it
// counts the durable missing, creates it again, and reports it present; it puts back a store
// setting changed out of band and counts it; and it reports another instance appearing on the
// broker without abandoning its own durables.
//
// GUARD: kills a watch that does not recreate, does not count, or reads "could not ask" as
// "missing"; and a watch that removes its durables when it sees another instance.
func TestTheWatchRecreatesAMissingDurableAndCountsWhatItRepaired(t *testing.T) {
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	_, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	m := fcMetrics(guardInstance)
	events := fcDurable(t, guardInstance, MqttFailClosedEvents)

	nmgr.checkMqttFailClosed(context.Background(), m)
	for _, d := range MqttFailClosedDurables(guardInstance) {
		require.EqualValues(t, 1, testutil.ToFloat64(m.durablePresent.WithLabelValues(d.Name)))
		require.EqualValues(t, 0, testutil.ToFloat64(m.durableMissing.WithLabelValues(d.Name)), "a quiet pass counts nothing")
	}

	require.NoError(t, b.js.DeleteConsumer(MqttMessageStore, events.Name))
	nmgr.checkMqttFailClosed(context.Background(), m)
	require.EqualValues(t, 1, testutil.ToFloat64(m.durableMissing.WithLabelValues(events.Name)))
	require.EqualValues(t, 1, testutil.ToFloat64(m.durablePresent.WithLabelValues(events.Name)))
	_, err = b.js.ConsumerInfo(MqttMessageStore, events.Name)
	require.NoError(t, err, "the watch created the durable again")

	cfg := b.storeInfo().Config
	cfg.Discard = nats.DiscardOld
	_, err = b.js.UpdateStream(&cfg)
	require.NoError(t, err)
	nmgr.checkMqttFailClosed(context.Background(), m)
	require.EqualValues(t, 1, testutil.ToFloat64(m.storeRepaired.WithLabelValues("discard")))
	require.Equal(t, nats.DiscardNew, b.storeInfo().Config.Discard)

	_, err = b.js.AddStream(&nats.StreamConfig{Name: StreamName("otherinst", streams.InboundEvents),
		Subjects: []string{"otherinst.*." + streams.InboundEvents}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	nmgr.checkMqttFailClosed(context.Background(), m)
	require.EqualValues(t, 1, testutil.ToFloat64(m.foreignInstances))
	require.Len(t, consumerNames(t, b.js, MqttMessageStore), len(MqttFailClosedDurables(guardInstance)),
		"the durables stay: removing them would turn refusals into silent loss")

	// A pass that cannot reach the broker leaves both gauges as they were. The context is
	// live: a cancelled one returns before the pass reads anything, and would hold this to
	// nothing.
	b.stop()
	nmgr.checkMqttFailClosed(context.Background(), m)
	require.EqualValues(t, 1, testutil.ToFloat64(m.durablePresent.WithLabelValues(events.Name)),
		"could not ask read as missing")
	require.EqualValues(t, 1, testutil.ToFloat64(m.foreignInstances),
		"could not ask read as no other instance")
}

// The store deleted at runtime is created again by the watch, with a new epoch, and counted.
func TestTheWatchRecreatesADeletedStoreWithANewEpoch(t *testing.T) {
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	first, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	require.NoError(t, b.js.DeleteStream(MqttMessageStore))
	time.Sleep(5 * time.Millisecond)

	m := fcMetrics(guardInstance)
	nmgr.checkMqttFailClosed(context.Background(), m)
	require.EqualValues(t, 1, testutil.ToFloat64(m.storeRepaired.WithLabelValues("store")))
	for _, d := range MqttFailClosedDurables(guardInstance) {
		require.EqualValues(t, 1, testutil.ToFloat64(m.durableMissing.WithLabelValues(d.Name)))
	}
	require.True(t, b.storeInfo().Created.After(first.Epoch), "a recreated store has a new epoch")
}

// The tenant purge never deletes any of the platform durables the reconcile creates (slice
// 1's guard pins it for a hand-made events durable; this holds it for the real set).
//
// GUARD: kills a durable whose filter could begin with a tenant's literal prefix.
func TestTheTenantPurgeNeverDeletesAFailClosedDurable(t *testing.T) {
	nc, js, broker := mqttRig(t)
	connectDevice(t, broker, mqttVictim, "dev1")
	nmgr := failClosedManager(t, nc, js, mqttInstance, true)
	_, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)

	res, err := PurgeTenantMqttGateway(context.Background(), nc, mqttInstance, mqttVictim)
	require.NoError(t, err)
	require.Greater(t, res.Consumers, int64(0), "the control: the purge deleted the victim's own consumer")
	for _, d := range MqttFailClosedDurables(mqttInstance) {
		_, err := js.ConsumerInfo(MqttMessageStore, d.Name)
		require.NoErrorf(t, err, "the purge deleted the %s durable", d.Kind)
	}
}

// The dedup id: empty when a part is missing, distinct across epochs for one sequence, and
// shaped so no capture id can equal it.
func TestTheMessageStoreDedupIDCarriesItsSourceAndEpoch(t *testing.T) {
	e1 := time.Unix(1_800_000_000, 1)
	e2 := e1.Add(time.Nanosecond)
	for _, c := range []struct {
		tenant string
		epoch  time.Time
		seq    uint64
	}{{"", e1, 1}, {"acme", time.Time{}, 1}, {"acme", e1, 0}} {
		require.Emptyf(t, MqttMessageStoreDedupID(c.tenant, c.epoch, c.seq), "%+v must publish no id", c)
	}
	a := MqttMessageStoreDedupID("acme", e1, 7)
	require.NotEqual(t, a, MqttMessageStoreDedupID("acme", e2, 7), "a recreated store's sequence 7 is a different id")
	require.NotEqual(t, a, MqttMessageStoreDedupID("other", e1, 7))
	require.Equal(t, a, MqttMessageStoreDedupID("acme", e1, 7), "stable across a redelivery")
	require.Equal(t, 2, strings.Count(a, "."), "two separators: a capture id has one")
	require.True(t, strings.HasPrefix(a, "acme."+mqttMessageStoreSourceTag), a)
}

// The startup reconcile does not retry a refusal no retry can change.
func TestASharedBrokerIsRefusedAtOnceNotRetried(t *testing.T) {
	b := startGuardBroker(t)
	_, err := b.js.AddStream(&nats.StreamConfig{Name: StreamName("otherinst", streams.InboundEvents),
		Subjects: []string{"otherinst.*." + streams.InboundEvents}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	start := time.Now()
	_, err = failClosedManager(t, b.nc, b.js, guardInstance, true).ReconcileMqttFailClosed(context.Background())
	require.True(t, errors.Is(err, ErrMqttFailClosedSharedBroker))
	require.Less(t, time.Since(start), time.Second, "the refusal waited out the retry schedule")
}

// The stream listing is bounded even when the caller's context is not. Both callers pass
// one with no deadline, so an API request the broker takes and never answers (a meta-leader
// election in a cluster) would otherwise hold the start, or the watch, for good. Here a
// subscriber takes the request and never replies.
//
// GUARD: kills a listing that waits on the caller's context alone.
func TestTheStreamListingDoesNotWaitForeverOnABrokerThatNeverAnswers(t *testing.T) {
	// A broker without JetStream, so the only taker of the request is the subscriber.
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second))
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	_, err = nc.Subscribe("$JS.API.STREAM.NAMES", func(*nats.Msg) {})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	saved := mqttFailClosedRequestTimeout
	mqttFailClosedRequestTimeout = 200 * time.Millisecond
	t.Cleanup(func() { mqttFailClosedRequestTimeout = saved })

	done := make(chan error, 1)
	go func() {
		_, err := streamNamesStrict(context.Background(), nc)
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err, "a listing nobody answered must fail, not read as no other instance")
	case <-time.After(10 * time.Second):
		t.Fatal("the stream listing waited on a broker that never answers")
	}
}

// Another instance's stream on a later page of the listing is still seen. The server pages
// STREAM.NAMES (1024 names a page); a look that read only the first page would read a broker
// with many streams as serving no other instance.
//
// GUARD: kills a listing that stops after one page.
func TestAnotherInstanceOnALaterPageOfTheListingIsSeen(t *testing.T) {
	b := startGuardBroker(t)
	const filler = 1100
	for i := 0; i < filler; i++ {
		_, err := b.js.AddStream(&nats.StreamConfig{Name: fmt.Sprintf("a%04d", i),
			Subjects: []string{fmt.Sprintf("filler.%d", i)}, Storage: nats.MemoryStorage})
		require.NoError(t, err)
	}
	// Names are listed sorted, so "zz..." lands on the second page.
	_, err := b.js.AddStream(&nats.StreamConfig{Name: StreamName("zzother", streams.InboundEvents),
		Subjects: []string{"zzother.*." + streams.InboundEvents}, Storage: nats.MemoryStorage})
	require.NoError(t, err)

	names, err := streamNamesStrict(context.Background(), b.nc)
	require.NoError(t, err)
	require.Len(t, names, filler+1, "every stream name, across pages")
	_, err = failClosedManager(t, b.nc, b.js, guardInstance, true).ReconcileMqttFailClosed(context.Background())
	require.ErrorIs(t, err, ErrMqttFailClosedSharedBroker)
	require.Contains(t, err.Error(), "zzother")
}
