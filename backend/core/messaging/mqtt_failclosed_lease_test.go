// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// THE STORE ACROSS A ROLLING UPDATE. Every event-sources pod runs a fail-closed pass at start
// and then every 30 s, and during a rollout the pods disagree: some have the setting on and
// some off, or on with different bounds, and any of them can restart in the middle. These
// simulate the pods as managers on one broker, sharing one lease clock, and run them through
// rollouts in both directions with a pod of the OLD configuration restarting mid-rollout. After
// every round of passes they hold the store to two things:
//
//   - it is never fail-closed (discard new) with nobody reading its durables and nobody
//     exporting its metrics: some live pod has the setting on, or has the setting off and has
//     seen the store marked (and so reads its durables);
//   - it is coherent: discard new exactly when this instance's owner or released mark is on
//     it; owned with all three durables and a from mark for each direct stream; released
//     without the owner mark (and still discard new: its durables are being drained, and
//     discard old would evict the copies they hold).
//
// and, once the old pods are gone, to having converged on what the remaining pods want.

// fcPod is one simulated event-sources pod.
type fcPod struct {
	t    *testing.T
	name string
	on   bool
	hot  int64
	nmgr *NatsManager
	m    *MqttFailClosedMetrics
	live bool
}

func startFcPod(t *testing.T, b *guardBroker, clock *fcClock, name string, on bool, hot int64) *fcPod {
	t.Helper()
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, on)
	if hot != 0 {
		nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytes = hot
		nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytesCold = hot
	}
	nmgr.mqttClock = clock.now
	p := &fcPod{t: t, name: name, on: on, hot: hot, nmgr: nmgr, live: true}
	if on {
		p.m = fcMetrics(guardInstance)
		_, err := nmgr.ReconcileMqttFailClosed(context.Background())
		require.NoErrorf(t, err, "starting %s", name)
	} else {
		n := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
		require.NoErrorf(t, nmgr.ReconcileMqttStores(context.Background(), n.MqttMessageStoreMaxBytes(), n.MqttQoS2StoreMaxBytes),
			"starting %s", name)
	}
	return p
}

func (p *fcPod) pass() mqttFailClosedReport {
	p.t.Helper()
	rep, err := p.nmgr.checkMqttFailClosed(context.Background(), p.m)
	require.NoErrorf(p.t, err, "a pass of %s", p.name)
	return rep
}

// reads reports whether the pod reads the store's durables: it has the setting on, or it has
// seen the store marked for this instance (event-sources then starts its store readers).
func (p *fcPod) reads() bool { return p.live && (p.on || p.nmgr.MqttMessageStoreMarked()) }

type fcFleet struct {
	t     *testing.T
	b     *guardBroker
	clock *fcClock
	pods  []*fcPod
}

func (f *fcFleet) start(name string, on bool, hot int64) *fcPod {
	p := startFcPod(f.t, f.b, f.clock, name, on, hot)
	f.pods = append(f.pods, p)
	f.check(fmt.Sprintf("after %s started", name))
	return p
}

func (f *fcFleet) stop(p *fcPod) {
	p.live = false
}

// round runs one pass of every live pod, advances the clock by d, and checks the store.
func (f *fcFleet) round(d time.Duration, what string) {
	f.t.Helper()
	for _, p := range f.pods {
		if p.live {
			p.pass()
		}
	}
	f.check(what)
	f.clock.advance(d)
}

func (f *fcFleet) store() *nats.StreamInfo {
	f.t.Helper()
	si, err := f.b.js.StreamInfo(MqttMessageStore)
	if err != nil {
		require.ErrorIs(f.t, err, nats.ErrStreamNotFound)
		return nil
	}
	return si
}

// check holds the store to the two properties in the file comment.
func (f *fcFleet) check(what string) {
	f.t.Helper()
	si := f.store()
	if si == nil {
		return
	}
	md := si.Config.Metadata
	owned := md[mqttFailClosedOwnerKey] == guardInstance
	released := md[mqttFailClosedReleasedKey] == guardInstance
	failClosed := si.Config.Discard == nats.DiscardNew
	require.Equalf(f.t, owned || released, failClosed, "%s: discard new without a mark of this instance, or the reverse: %v", what, md)
	if owned {
		require.Falsef(f.t, released, "%s: owned and released at once", what)
		require.Lenf(f.t, mqttFailClosedFromMarks(md), len(mqttFailClosedDirectSuffix), "%s: owned without a from mark per stream", what)
		names := consumerNames(f.t, f.b.js, MqttMessageStore)
		for _, d := range MqttFailClosedDurables(guardInstance) {
			require.Containsf(f.t, names, d.Name, "%s: fail-closed without its %s durable", what, d.Kind)
		}
		reader, exporter := false, false
		for _, p := range f.pods {
			reader = reader || p.reads()
			exporter = exporter || (p.live && p.on)
		}
		require.Truef(f.t, reader || exporter, "%s: the store is fail-closed and no live pod reads its durables or exports its metrics", what)
	}
	if released {
		require.NotContainsf(f.t, md, mqttFailClosedOwnerKey, "%s: released but still owned", what)
		reader := false
		for _, p := range f.pods {
			reader = reader || p.reads()
		}
		require.Truef(f.t, reader, "%s: the store is released (still discard new) and no live pod reads its durables", what)
	}
}

// pristine reports the store as a never-enabled install has it: no mark of this instance and
// none of its durables.
func (f *fcFleet) pristine() bool {
	si := f.store()
	if si == nil {
		return true
	}
	for k := range si.Config.Metadata {
		if strings.HasPrefix(k, mqttFailClosedKeyPrefix) {
			return false
		}
	}
	names := consumerNames(f.t, f.b.js, MqttMessageStore)
	for _, d := range MqttFailClosedDurables(guardInstance) {
		for _, n := range names {
			if n == d.Name {
				return false
			}
		}
	}
	return si.Config.Discard == nats.DiscardOld
}

func newFcFleet(t *testing.T) *fcFleet {
	fastMqttStoreLookup(t)
	b := startGuardBroker(t)
	// A device connects first, so the gateway has made its own store: the rollouts below act on
	// a store that already existed, as on any installed instance.
	connectDevice(t, b.mqttURL(), guardTenant, "d0")
	return &fcFleet{t: t, b: b, clock: newFcClock()}
}

// ON -> OFF, with an old pod (setting on) restarting after the first new pod is up. Until the
// last pod with the setting on has gone the store stays fail-closed, read by every pod; then
// the pods with it off release it and, once drained, remove its durables and marks.
//
// GUARD: kills a release while a live pod still holds the store (the old pods' durables would
// go unread behind a discard-new store, or be removed under them), a lease that does not let a
// restarted old pod hold it again, and a fleet that does not converge on the pristine store.
func TestARolloutTurningTheSettingOffConvergesOnceTheOldPodsAreGone(t *testing.T) {
	f := newFcFleet(t)
	a1 := f.start("A1(on)", true, 0)
	a2 := f.start("A2(on)", true, 0)
	f.round(10*time.Second, "both old pods")

	b1 := f.start("B1(off)", false, 0)
	require.True(t, b1.reads(), "a new pod with the setting off reads the durables of a store it finds held")
	f.round(10*time.Second, "B1 up")
	f.stop(a1)
	f.round(10*time.Second, "A1 gone")

	// The old ReplicaSet's last pod restarts mid-rollout.
	f.stop(a2)
	a2r := f.start("A2'(on, restarted)", true, 0)
	f.round(10*time.Second, "A2 restarted")
	require.Equal(t, nats.DiscardNew, f.store().Config.Discard, "the restarted old pod holds the store")

	f.start("B2(off)", false, 0)
	f.round(10*time.Second, "B2 up")
	require.Equal(t, nats.DiscardNew, f.store().Config.Discard, "released while an old pod was alive")
	f.stop(a2r)

	for i := 0; i < 12 && !f.pristine(); i++ {
		f.round(MqttFailClosedWatchInterval, fmt.Sprintf("only new pods, round %d", i))
	}
	require.True(t, f.pristine(), "the fleet of pods with the setting off did not converge on the gateway's store")
	si := f.store()
	require.EqualValues(t, b1.nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.MqttStoreMaxBytes, si.Config.MaxBytes)
}

// OFF -> ON, with an old pod (setting off) restarting after the first new pod has taken the
// store. The old pods never release it while the new ones hold it, and the restarted one
// reads its durables from its start.
//
// GUARD: kills a flag-off start or pass that releases a store whose holders are alive (the
// two kinds of pod would undo each other on every pass).
func TestARolloutTurningTheSettingOnConvergesAndTheOldPodsLeaveItAlone(t *testing.T) {
	f := newFcFleet(t)
	b1 := f.start("B1(off)", false, 0)
	b2 := f.start("B2(off)", false, 0)
	f.round(10*time.Second, "old pods only")
	require.True(t, f.pristine(), "pods with the setting off changed a store with no mark of this instance")

	a1 := f.start("A1(on)", true, 0)
	f.round(10*time.Second, "A1 up")
	require.True(t, b1.reads(), "an old pod's pass did not see the store marked")
	f.stop(b1)
	f.round(10*time.Second, "B1 gone")

	f.stop(b2)
	b2r := f.start("B2'(off, restarted)", false, 0)
	require.True(t, b2r.reads(), "the restarted old pod reads the durables from its start")
	f.round(10*time.Second, "B2 restarted")

	a2 := f.start("A2(on)", true, 0)
	f.round(10*time.Second, "A2 up")
	f.stop(b2r)
	for i := 0; i < 8; i++ {
		f.round(MqttFailClosedWatchInterval, fmt.Sprintf("only new pods, round %d", i))
		require.Equal(t, nats.DiscardNew, f.store().Config.Discard, "the store was released under its holders")
	}
	require.Equal(t, mqttFailClosedSignature(fcHotBytes, 1), f.store().Config.Metadata[mqttFailClosedHolderKey])
	_ = a1
	_ = a2
}

// OFF -> ON where the new pod dies and the rollout is rolled back: the old pods with the
// setting off are what remain, and they hand the store back once its holder's heartbeat is
// stale. Nothing is left fail-closed for want of a pod with the setting on.
//
// GUARD: kills a fleet in which only a pod with the setting on can ever release the store.
func TestTheStoreIsReleasedWhenTheOnlyPodWithTheSettingOnDies(t *testing.T) {
	f := newFcFleet(t)
	f.start("B1(off)", false, 0)
	f.start("B2(off)", false, 0)
	a1 := f.start("A1(on)", true, 0)
	f.round(10*time.Second, "A1 up")
	f.stop(a1)
	for i := 0; i < 12 && !f.pristine(); i++ {
		f.round(MqttFailClosedWatchInterval, fmt.Sprintf("A1 dead, round %d", i))
	}
	require.True(t, f.pristine(), "the old pods never handed the store back")
}

// 🔑 L3: pods with the setting on and DIFFERENT bounds (a rollout that changes the hot
// ceiling) do not rewrite the store in turn every 30 s. The pods holding it keep it at their
// bound; the others stand by, reading it, and take it over once the holders are gone. A
// take-over is a decision, not a repair, so it raises no repair count.
//
// GUARD: kills a lease that lets a standby pod write its bound while the holders are alive
// (the bound would flip every pass), and a take-over counted as the store having been changed
// out of band (it would page on every config change).
func TestPodsWithDifferentBoundsDoNotFightOverTheStore(t *testing.T) {
	f := newFcFleet(t)
	old := f.start("A(on, 16 KiB)", true, fcHotBytes)
	neu := f.start("C(on, 32 KiB)", true, 2*fcHotBytes)
	require.Equal(t, mqttStandingStandby, neu.pass().standing)
	for i := 0; i < 6; i++ {
		f.round(MqttFailClosedWatchInterval, fmt.Sprintf("both bounds, round %d", i))
		si := f.store()
		require.EqualValuesf(t, fcHotBytes, si.Config.MaxBytes, "round %d: the bound moved while its holder was alive", i)
		require.Equal(t, mqttFailClosedSignature(fcHotBytes, 1), si.Config.Metadata[mqttFailClosedHolderKey])
	}
	f.stop(old)
	for i := 0; i < 6 && f.store().Config.MaxBytes != 2*fcHotBytes; i++ {
		f.round(MqttFailClosedWatchInterval, fmt.Sprintf("new bound only, round %d", i))
	}
	si := f.store()
	require.EqualValues(t, 2*fcHotBytes, si.Config.MaxBytes, "the remaining pods' bound never took over")
	require.Equal(t, mqttFailClosedSignature(2*fcHotBytes, 1), si.Config.Metadata[mqttFailClosedHolderKey])
	for _, s := range mqttFailClosedSettings {
		require.EqualValuesf(t, 0, testutil.ToFloat64(neu.m.storeRepaired.WithLabelValues(s)),
			"the take-over counted %s as repaired", s)
	}
}

// 🔑 M2: a pass decides again, from a read taken under the lock, before it writes. Here a pod
// with the setting off reads the store with its holder's heartbeat stale and decides to
// release it; before it takes the lock, the holder renews the heartbeat. Under the lock the
// release is decided again, and not made. The control runs the same interleaving with no
// renewal, and the release goes through: the scenario is one that would release.
//
// GUARD: kills a pass that writes what it decided before the lock (a stale UpdateStream that
// undoes what a pass in between did).
func TestAPassDecidesAgainUnderTheLockBeforeItWrites(t *testing.T) {
	for _, renew := range []bool{true, false} {
		t.Run(fmt.Sprintf("renewed=%v", renew), func(t *testing.T) {
			f := newFcFleet(t)
			holder := f.start("A(on)", true, 0)
			off := f.start("B(off)", false, 0)
			f.clock.advance(mqttFailClosedLeaseTTL)
			decided := false
			off.nmgr.mqttPassHook = func(point string) {
				if point == "decided" && !decided {
					decided = true
					if renew {
						holder.pass()
					}
				}
			}
			rep := off.pass()
			require.True(t, decided, "premise: the pod with the setting off decided to write")
			si := f.store()
			if renew {
				require.Equal(t, mqttStandingHeldByOn, rep.standing)
				require.Equal(t, nats.DiscardNew, si.Config.Discard, "a release decided on a stale read went through")
				require.Equal(t, guardInstance, si.Config.Metadata[mqttFailClosedOwnerKey])
			} else {
				require.True(t, rep.released, "control: with no renewal the release is made")
				require.NotContains(t, si.Config.Metadata, mqttFailClosedOwnerKey)
			}
			f.check("after the interleaving")
		})
	}
}

// M2, the other half: a pass that finds the lock held writes nothing (a watch pass leaves the
// pass to the pod holding it), and a start waits for it rather than writing around it.
//
// GUARD: kills a pass that writes without the lock.
func TestAPassThatCannotTakeTheLockWritesNothing(t *testing.T) {
	f := newFcFleet(t)
	f.start("A(on)", true, 0)
	off := f.start("B(off)", false, 0)
	f.clock.advance(mqttFailClosedLeaseTTL)
	kv, err := off.nmgr.mqttFailClosedLockBucket()
	require.NoError(t, err)
	rev, err := kv.Create(kvKey(mqttFailClosedLockName), []byte("someone-else"))
	require.NoError(t, err)

	rep := off.pass()
	require.Equal(t, mqttStandingSkipped, rep.standing)
	require.Equal(t, nats.DiscardNew, f.store().Config.Discard, "released without the lock")

	saved := mqttFailClosedLockWait
	mqttFailClosedLockWait = 300 * time.Millisecond
	t.Cleanup(func() { mqttFailClosedLockWait = saved })
	_, err = off.nmgr.mqttFailClosedPass(context.Background(), true)
	require.ErrorIs(t, err, errMqttFailClosedLockBusy)
	require.Equal(t, nats.DiscardNew, f.store().Config.Discard, "a start released without the lock")

	require.NoError(t, kv.Delete(kvKey(mqttFailClosedLockName), nats.LastRevision(rev)))
	require.True(t, off.pass().released, "with the lock free the release is made")
}

// 🔑 L1: taking a store the gateway made over creates the durables BEFORE the store becomes
// discard-new and is marked. Here the durables cannot be created: the take-over fails, and the
// store is left exactly as the gateway made it, not fail-closed with nothing to hold what it
// acknowledges and an owner mark that would have every reader skip its direct copies.
//
// GUARD: kills a take-over that writes the fail-closed config or the marks before the durables.
func TestTakingAGatewayStoreOverCreatesTheDurablesFirst(t *testing.T) {
	adminNC, adminJS, nc, js := permBroker(t,
		"$JS.API.CONSUMER.CREATE."+MqttMessageStore+".>", "$JS.API.CONSUMER.DURABLE.CREATE."+MqttMessageStore+".>")
	savedRetry, savedInsufficient := mqttFailClosedRetry, mqttFailClosedInsufficientRetry
	mqttFailClosedRetry, mqttFailClosedInsufficientRetry = []time.Duration{0}, nil
	t.Cleanup(func() { mqttFailClosedRetry, mqttFailClosedInsufficientRetry = savedRetry, savedInsufficient })
	_, err := adminJS.AddStream(guardStoreConfig(nats.DiscardOld, 0, 1))
	require.NoError(t, err)
	before, err := adminJS.StreamInfo(MqttMessageStore)
	require.NoError(t, err)

	_, err = failClosedManager(t, nc, js, guardInstance, true).ReconcileMqttFailClosed(context.Background())
	require.Error(t, err, "premise: the durables cannot be created")
	require.Contains(t, err.Error(), "creating the platform durable", "premise: it failed at the durables, not before")
	after, err := adminJS.StreamInfo(MqttMessageStore)
	require.NoError(t, err)
	require.Equal(t, before.Config, after.Config, "the store was changed before its durables existed")
	_ = adminNC
}
