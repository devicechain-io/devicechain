// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-event-sources/presence"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	corepresence "github.com/devicechain-io/dc-microservice/presence"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-microservice/test/mqttack"
)

// THE FAIL-CLOSED MQTT ACKNOWLEDGEMENT ON A THREE-NODE CLUSTER, UNDER LOAD: what the setting
// promises, held where the guarantee is stated (a replicated broker) and under the faults that
// lost PUBACKed events before it existed.
//
// Devices publish QoS 1 continuously over the MQTT gateways of all three nodes, through two
// event-sources pods with the setting on, while the leaders of the message store and of the
// device-events capture stream are moved again and again (a capture leader that stepped down
// under load lost 59,964 PUBACKed events in five seconds in a benchmark round, with no counter
// and no alert). The store is then filled by stopping its readers, so it refuses and the devices
// are pushed back; the setting is turned OFF by a rollout and back ON by another; and through
// all of it the broker's connection advisories are fed to the presence tap.
//
// What is held:
//
//   - ZERO LOSS: every message a device was PUBACKed reaches inbound-events (the platform's
//     PubAck on it), whichever pod, path or phase carried it. Duplicates are allowed and counted:
//     QoS 1 is at-least-once, and a leader move or a hand-over resends.
//   - PUSHED BACK, NOT DROPPED: with the store full, devices are refused (the connection closed
//     with no PUBACK), and their messages arrive once the store is read again.
//   - PRESENCE DOES NOT FLAP: the tap reports no device offline, although the broker closed
//     connections for refused or unacknowledged publishes many times.
//
// While the setting is OFF the faults are paused: off, a QoS 1 publish reaches the platform only
// through the capture stream, which is the configuration the setting exists to replace, and a
// capture leader move under load is allowed to lose what it held. The load continues, and what
// is PUBACKed then may be lost too, without any fault: at this load the capture stream's ceiling
// (clusterHot, the store's own bound) holds well under a second of publishes, so a reader that
// falls behind for a moment, as it does whenever the machine is busy, overflows it and the
// stream discards its oldest. That is the setting-off behaviour, documented as such. So the span
// in which the store is not held fail-closed (from its release by the pod with the setting off
// until the durables of the pod turning it back on have existed for openMargin) is the one span
// whose losses are counted and reported rather than failed. Its ends are read from the broker
// (the release time on the store, the durable's creation time), not from the test's phases.

const (
	clusterDevices = 9
	// clusterHot is the hot ceiling the pods run at, which the store and the capture stream take:
	// small enough that the store fills within a second or two of its readers stopping.
	clusterHot = 2 << 20
)

// clusterAccounts gives every node an application account with JetStream, which the platform
// and the devices use, and a system account the presence tap reads the advisories on.
const clusterAccounts = `
accounts {
  APP: { jetstream: enabled, users: [ {user: dc_service, password: svc} ] }
  SYS: { users: [ {user: dc_sys, password: sys} ] }
}
system_account: SYS
no_auth_user: dc_service
`

// ledger is what was PUBACKed to the devices and what reached inbound-events.
type ledger struct {
	mu        sync.Mutex
	acked     map[string]bool
	delivered map[string]int
	// phase is the test's phase now, and phaseOf the phase each id was PUBACKed in: what a
	// failure reports, so a loss points at the transition that caused it.
	phase   string
	phaseOf map[string]string
	// ackAt is when each id was PUBACKed, and [openFrom, openTo) the span in which the store was
	// not held fail-closed (see the test comment): a PUBACK in it is the setting-off one.
	ackAt            map[string]time.Time
	openFrom, openTo time.Time
}

// openMargin is how long after the re-created events durable a PUBACK may still be for a
// message the store took before the durable existed (and so kept for nobody): the time from a
// store write to its PUBACK reaching the device.
const openMargin = 2 * time.Second

func newLedger() *ledger {
	return &ledger{acked: map[string]bool{}, delivered: map[string]int{}, phaseOf: map[string]string{},
		ackAt: map[string]time.Time{}, phase: "start"}
}

func (l *ledger) setPhase(p string) {
	l.mu.Lock()
	l.phase = p
	l.mu.Unlock()
}

func (l *ledger) ack(id string) {
	l.mu.Lock()
	l.acked[id] = true
	l.phaseOf[id] = l.phase
	l.ackAt[id] = time.Now()
	l.mu.Unlock()
}

// setOpen records where the span in which the store was not held fail-closed begins (from) or
// ends (to); a zero time leaves that end as it was.
func (l *ledger) setOpen(from, to time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !from.IsZero() {
		l.openFrom = from
	}
	if !to.IsZero() {
		l.openTo = to
	}
}

// inOpen reports whether id was PUBACKed in the span in which the store was not held
// fail-closed. l.mu is held.
func (l *ledger) inOpen(id string) bool {
	at := l.ackAt[id]
	return !l.openFrom.IsZero() && !at.Before(l.openFrom) && (l.openTo.IsZero() || at.Before(l.openTo))
}

// lostByPhase counts the lost messages by the phase they were PUBACKed in, with a few ids.
func (l *ledger) lostByPhase() map[string][]string {
	lost := l.lost()
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string][]string{}
	for _, id := range lost {
		ph := l.phaseOf[id]
		out[ph] = append(out[ph], id)
	}
	return out
}

func (l *ledger) deliver(id string) {
	l.mu.Lock()
	l.delivered[id]++
	l.mu.Unlock()
}

// lost is the messages PUBACKed while the store was held fail-closed that have not reached
// inbound-events.
func (l *ledger) lost() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for id := range l.acked {
		if l.delivered[id] == 0 && !l.inOpen(id) {
			out = append(out, id)
		}
	}
	return out
}

// lostOpen counts the messages PUBACKed while the store was not held fail-closed, and how many
// of those have not reached inbound-events.
func (l *ledger) lostOpen() (acked, lost int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id := range l.acked {
		if l.inOpen(id) {
			acked++
			if l.delivered[id] == 0 {
				lost++
			}
		}
	}
	return acked, lost
}

func (l *ledger) counts() (acked, delivered, duplicates int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, n := range l.delivered {
		delivered += n
		if l.acked[id] && n > 1 {
			duplicates += n - 1
		}
	}
	return len(l.acked), delivered, duplicates
}

// ledgerWriter records each message an inbound writer published and the stream acknowledged.
type ledgerWriter struct {
	messaging.OrderedWriter
	l *ledger
}

func (w *ledgerWriter) Publish(ctx context.Context, msg messaging.Message, done func(error)) {
	id := string(msg.Value)
	w.OrderedWriter.Publish(ctx, msg, func(err error) {
		if err == nil {
			w.l.deliver(id)
		}
		done(err)
	})
}

// presenceLedger is the presence tap's emitter, folded per device the way the presence
// projection folds it (corepresence.Decide): a disconnect the tap held back and emitted after
// the device had reconnected carries the older session, and is refused there. What it counts
// is what a reader of the projection would see: each time a device FLIPPED to offline.
type presenceLedger struct {
	mu       sync.Mutex
	online   int
	prior    map[string]corepresence.Prior
	offline  map[string]int
	refusals atomic.Int64
}

func (p *presenceLedger) EmitPresence(_ context.Context, _, _, device string, ev adapter.PresenceEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	claim := corepresence.ClaimDisconnected
	if ev.Connected {
		claim = corepresence.ClaimConnected
		p.online++
	}
	prior := p.prior[device]
	d := corepresence.Decide(prior, corepresence.Incoming{SessionId: ev.SessionId, OccurredAt: ev.OccurredAt, Claim: claim})
	if !d.Ordered {
		return nil
	}
	if d.Flipped && !ev.Connected {
		p.offline[device]++
	}
	p.prior[device] = corepresence.Prior{SessionId: ev.SessionId, Time: ev.OccurredAt, HasTime: true, Connected: ev.Connected}
	return nil
}

func (p *presenceLedger) EmitPresenceDemotion(context.Context, string, string, string, adapter.DemotionEvent) error {
	return nil
}

// snapshot is how many connects were emitted, each device's offline flips, and the devices
// the projection would read offline now.
func (p *presenceLedger) snapshot() (online int, flips map[string]int, offlineNow []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	flips = map[string]int{}
	for k, v := range p.offline {
		flips[k] = v
	}
	for dev, pr := range p.prior {
		if !pr.Connected {
			offlineNow = append(offlineNow, dev)
		}
	}
	return p.online, flips, offlineNow
}

// clusterDevice publishes QoS 1 continuously, each message waited for until PUBACKed. Its
// session is persistent and it reconnects on its own, so a publish the broker closed the
// connection on is sent again by the client library, as a device's would be.
type clusterDevice struct {
	token    string
	c        mqtt.Client
	store    *mqttack.Store
	pushback atomic.Int64
	answered atomic.Int64
	errs     chan error
}

func connectClusterDevice(t *testing.T, url, token string) *clusterDevice {
	t.Helper()
	clientID, err := messaging.DeviceClientID(e2eInstance, e2eTenant, token)
	require.NoError(t, err)
	d := &clusterDevice{token: token, store: mqttack.NewStore(), errs: make(chan error, 1)}
	opts := mqtt.NewClientOptions().AddBroker(url).SetClientID(clientID).
		SetUsername("dc_service").SetPassword("svc").
		SetCleanSession(false).SetStore(d.store).
		SetAutoReconnect(true).SetConnectRetry(true).SetConnectRetryInterval(250 * time.Millisecond).
		SetMaxReconnectInterval(500 * time.Millisecond).SetConnectTimeout(10 * time.Second).
		SetConnectionLostHandler(func(mqtt.Client, error) { d.pushback.Add(1) })
	d.c = mqtt.NewClient(opts)
	tok := d.c.Connect()
	require.True(t, tok.WaitTimeout(60*time.Second), "device %s never connected", token)
	require.NoError(t, tok.Error())
	t.Cleanup(func() { d.c.Disconnect(100) })
	return d
}

func (d *clusterDevice) body(id string) string {
	return fmt.Sprintf(`{"device":%q,"altId":%q,"eventType":"Measurement","pad":%q,`+
		`"payload":{"entries":[{"measurements":{"t":"1"}}]}}`, d.token, id, e2ePad)
}

// run publishes until stop is closed, recording each PUBACKed id. A publish not answered in
// two minutes is reported on errs and ends the device: the broker is meant to answer one way
// or the other, and a refused publish is answered by the resend that is finally acknowledged.
func (d *clusterDevice) run(l *ledger, stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	topic := fmt.Sprintf("%s/%s/devices/%s/events", e2eInstance, e2eTenant, d.token)
	for i := 0; ; i++ {
		select {
		case <-stop:
			return
		default:
		}
		id := fmt.Sprintf("%s-%06d", d.token, i)
		tok := d.c.Publish(topic, 1, false, d.body(id))
		answered := time.Now().Add(2 * time.Minute)
		if !tok.WaitTimeout(2 * time.Minute) {
			select {
			case d.errs <- fmt.Errorf("%s was never answered", id):
			default:
			}
			return
		}
		if tok.Error() != nil {
			continue
		}
		// A completed token is not a PUBACK: a reconnect completes the token of every publish it
		// re-sends (see mqttack). The id is PUBACKed when the store says so, for either send.
		mid := tok.(*mqtt.PublishToken).MessageID()
		for !d.store.PubAcked(mid) && time.Now().Before(answered) {
			time.Sleep(10 * time.Millisecond)
		}
		if !d.store.PubAcked(mid) {
			select {
			case d.errs <- fmt.Errorf("%s was re-sent and its re-send never answered", id):
			default:
			}
			return
		}
		l.ack(id)
		d.answered.Add(1)
	}
}

// clusterFaults moves the leaders of the message store and of the capture stream in turn,
// while it is not paused, and counts the moves the broker confirmed for each.
type clusterFaults struct {
	nc     *nats.Conn
	paused atomic.Bool
	moves  map[string]*atomic.Int64
	stop   chan struct{}
	done   chan struct{}
}

func startClusterFaults(nc *nats.Conn, every time.Duration, streamNames ...string) *clusterFaults {
	f := &clusterFaults{nc: nc, moves: map[string]*atomic.Int64{}, stop: make(chan struct{}), done: make(chan struct{})}
	for _, s := range streamNames {
		f.moves[s] = &atomic.Int64{}
	}
	go func() {
		defer close(f.done)
		for i := 0; ; i++ {
			select {
			case <-f.stop:
				return
			case <-time.After(every):
			}
			if f.paused.Load() {
				continue
			}
			s := streamNames[i%len(streamNames)]
			msg, err := nc.Request("$JS.API.STREAM.LEADER.STEPDOWN."+s, nil, 5*time.Second)
			if err == nil && !bytes.Contains(msg.Data, []byte(`"error"`)) {
				f.moves[s].Add(1)
			}
		}
	}()
	return f
}

func (f *clusterFaults) close() {
	select {
	case <-f.stop:
	default:
		close(f.stop)
	}
	<-f.done
}

func TestFailClosedOnAClusterUnderLoadLosesNoPubackedMessage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the in-process cluster fixture holds its refusing route addresses with unix sockets")
	}
	if testing.Short() {
		t.Skip("a minutes-long load run on a three-node cluster")
	}
	conf := filepath.Join(t.TempDir(), "accounts.conf")
	require.NoError(t, os.WriteFile(conf, []byte(clusterAccounts), 0o600))
	servers, _ := dctest.StartJetStreamClusterConfigured(t, 3, func(i int, o *natsserver.Options) {
		parsed, err := natsserver.ProcessConfigFile(conf)
		if err != nil {
			panic(err)
		}
		o.Accounts, o.Users, o.SystemAccount, o.NoAuthUser = parsed.Accounts, parsed.Users, parsed.SystemAccount, parsed.NoAuthUser
		// Port -1: the server binds a port of the kernel's choosing and keeps it. A port picked
		// beforehand by a probe listener and released is free only until something else binds
		// it, and this fixture's own routes and listeners bind ports in between: a server
		// whose MQTT listener lost that race fails its start, and no device can connect to it.
		o.MQTT = natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1, StreamReplicas: 3}
	})
	mqttPorts := make([]int, len(servers))
	for i, s := range servers {
		vz, err := s.Varz(nil)
		require.NoError(t, err)
		require.NotZerof(t, vz.MQTT.Port, "server %d reports no MQTT port", i)
		mqttPorts[i] = vz.MQTT.Port
	}
	nc, err := nats.Connect(servers[0].ClientURL(), nats.UserInfo("dc_service", "svc"))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)

	var clockMu sync.Mutex
	var skew time.Duration
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return time.Now().Add(skew) }
	advance := func(d time.Duration) { clockMu.Lock(); skew += d; clockMu.Unlock() }

	l := newLedger()
	record := func(w messaging.OrderedWriter) messaging.OrderedWriter { return &ledgerWriter{OrderedWriter: w, l: l} }
	opts := e2ePodOpts{wrapCapture: record, productionSettle: true, nats: func(n *mscfg.NatsConfiguration) {
		n.StreamMaxBytes, n.StreamMaxBytesCold, n.StreamReplicas = clusterHot, clusterHot, 3
		n.Auth.User, n.Auth.Password = "dc_service", "svc"
	}}
	pod := func(name string, server int, on bool) *e2ePod {
		p := startE2EPodWith(t, servers[server].ClientURL(), name, on, clock, record, opts)
		if on {
			p.startStore()
		}
		return p
	}

	// The presence tap, fed the broker's connection advisories from the system account.
	sys, err := nats.Connect(servers[0].ClientURL(), nats.UserInfo("dc_sys", "sys"))
	require.NoError(t, err)
	t.Cleanup(sys.Close)
	seen := &presenceLedger{prior: map[string]corepresence.Prior{}, offline: map[string]int{}}
	tap := presence.NewTap(e2eInstance, "mqtt1", seen, nil, presence.Metrics{})
	t.Cleanup(tap.StopDeferred)
	var maxDeferred atomic.Int64
	for _, sub := range []struct {
		subject   string
		connected bool
	}{{"$SYS.ACCOUNT.*.CONNECT", true}, {"$SYS.ACCOUNT.*.DISCONNECT", false}} {
		connected := sub.connected
		_, err := sys.Subscribe(sub.subject, func(m *nats.Msg) {
			if !connected {
				var adv struct {
					Reason string `json:"reason"`
				}
				if json.Unmarshal(m.Data, &adv) == nil && adv.Reason == presence.RefusalCloseReason {
					seen.refusals.Add(1)
				}
			}
			tap.Handle(context.Background(), connected, m.Data)
			if n := int64(tap.DeferredDeaths()); n > maxDeferred.Load() {
				maxDeferred.Store(n)
			}
		})
		require.NoError(t, err)
	}
	require.NoError(t, sys.Flush())

	// Two pods with the setting on, as an HA install runs.
	a1, a2 := pod("A1-on", 0, true), pod("A2-on", 1, true)

	devices := make([]*clusterDevice, clusterDevices)
	for i := range devices {
		devices[i] = connectClusterDevice(t, fmt.Sprintf("tcp://127.0.0.1:%d", mqttPorts[i%3]), fmt.Sprintf("dev%d", i))
	}
	stopLoad := make(chan struct{})
	var load sync.WaitGroup
	for _, d := range devices {
		load.Add(1)
		go d.run(l, stopLoad, &load)
	}
	answered := func() int64 {
		var n int64
		for _, d := range devices {
			n += d.answered.Load()
		}
		return n
	}
	pushedBack := func() int64 {
		var n int64
		for _, d := range devices {
			n += d.pushback.Load()
		}
		return n
	}
	deviceErr := func() error {
		for _, d := range devices {
			select {
			case err := <-d.errs:
				return err
			default:
			}
		}
		return nil
	}
	steady := func(what string, d time.Duration) {
		t.Helper()
		before := answered()
		time.Sleep(d)
		require.NoError(t, deviceErr(), what)
		require.Greaterf(t, answered(), before, "%s: no device publish was acknowledged", what)
		acked, _, _ := l.counts()
		t.Logf("%s: %d PUBACKed so far, %d connections closed on devices", what, acked, pushedBack())
	}

	storeName := messaging.MqttMessageStore
	captureName := messaging.StreamName(e2eInstance, streams.DeviceEventsCapture)
	captureInfo, err := js.StreamInfo(captureName)
	require.NoError(t, err)
	captureFirst := captureInfo.State.FirstSeq
	var eventsDurable string
	for _, d := range messaging.MqttFailClosedDurables(e2eInstance) {
		if d.Kind == messaging.MqttFailClosedEvents {
			eventsDurable = d.Name
		}
	}
	faults := startClusterFaults(nc, 700*time.Millisecond, storeName, captureName)
	t.Cleanup(faults.close)

	// 1. Load with the leaders moving.
	l.setPhase("on")
	steady("on, leaders moving", 20*time.Second)

	// 2. A full store: nobody reads it, it refuses, and the devices are pushed back.
	l.setPhase("full")
	a1.stopStore()
	a2.stopStore()
	closedBefore := pushedBack()
	require.Eventually(t, func() bool {
		si, err := js.StreamInfo(storeName)
		return err == nil && si.State.Bytes > clusterHot*8/10 && pushedBack() >= closedBefore+clusterDevices
	}, 60*time.Second, 100*time.Millisecond, "the store never filled and refused the devices")
	full := pushedBack() - closedBefore
	time.Sleep(3 * time.Second) // the devices keep being refused while it stays full
	a1.startStore()
	a2.startStore()
	l.setPhase("on-after-full")
	steady("on, after the store was full", 15*time.Second)

	// 3. A rollout turning the setting OFF: a pod with it off comes up, both pods with it on go,
	// it releases the store once their heartbeat is stale and removes the drained durables.
	faults.paused.Store(true)
	l.setPhase("turning-off")
	b := pod("B-off", 2, false)
	require.Eventually(t, b.nmgr.MqttMessageStoreMarked, 10*time.Second, 50*time.Millisecond,
		"the pod with the setting off never saw the store held")
	a1.stop()
	a2.stop()
	advance(2 * time.Minute)
	var releasedAt string
	require.Eventually(t, func() bool {
		si, err := js.StreamInfo(storeName)
		if err != nil {
			return false
		}
		_, owned := si.Config.Metadata["io.devicechain.mqtt-fail-closed.owner"]
		releasedAt = si.Config.Metadata["io.devicechain.mqtt-fail-closed.released-at"]
		return !owned
	}, 30*time.Second, 100*time.Millisecond, "the pod with the setting off never released the store")
	// The release time is the pod's clock, which runs skew ahead of the test's. The grace
	// before the durables can go has not passed on that clock yet, so the store still carries it.
	relNanos, err := strconv.ParseInt(releasedAt, 10, 64)
	require.NoErrorf(t, err, "the released store carries no release time (%q)", releasedAt)
	clockMu.Lock()
	l.setOpen(time.Unix(0, relNanos).Add(-skew), time.Time{})
	clockMu.Unlock()
	advance(2 * time.Minute)
	require.Eventually(t, func() bool {
		for n := range js.ConsumerNames(storeName) {
			for _, d := range messaging.MqttFailClosedDurables(e2eInstance) {
				if n == d.Name {
					return false
				}
			}
		}
		return true
	}, 60*time.Second, 200*time.Millisecond, "the drained durables were never removed")
	l.setPhase("off")
	steady("off", 10*time.Second)

	// 4. And back ON while the pod with it off still runs; then that pod goes.
	l.setPhase("turning-on")
	a3 := pod("A3-on", 0, true)
	ci, err := js.ConsumerInfo(storeName, eventsDurable)
	require.NoError(t, err, "the pod turning the setting back on created no events durable")
	l.setOpen(time.Time{}, ci.Created.Add(openMargin))
	faults.paused.Store(false)
	steady("on again, beside the pod with it off", 10*time.Second)
	b.stop()
	l.setPhase("on-alone")
	steady("on again, alone", 15*time.Second)

	// The end: stop the load and the faults, and let what is in flight land.
	close(stopLoad)
	load.Wait()
	faults.close()
	require.NoError(t, deviceErr())
	l.setPhase("drain")
	deadline := time.Now().Add(90 * time.Second)
	for len(l.lost()) > 0 && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	if lost := l.lostByPhase(); len(lost) > 0 {
		for ph, ids := range lost {
			sort.Strings(ids)
			t.Errorf("%d PUBACKed in phase %q never reached inbound-events, e.g. %v", len(ids), ph, ids[:min(8, len(ids))])
		}
		if si, err := js.StreamInfo(storeName); err == nil {
			t.Logf("store: %d msgs, first %d last %d, subjects %v, discard %v, metadata %v", si.State.Msgs,
				si.State.FirstSeq, si.State.LastSeq, si.Config.Subjects, si.Config.Discard, si.Config.Metadata)
		}
		for n := range js.ConsumerNames(storeName) {
			if ci, err := js.ConsumerInfo(storeName, n); err == nil {
				t.Logf("store consumer %s: pending %d ack-pending %d floor %d delivered %d", n, ci.NumPending,
					ci.NumAckPending, ci.AckFloor.Stream, ci.Delivered.Stream)
			}
		}
		logCapture(t, js, captureName, captureFirst)
		t.FailNow()
	}
	logCapture(t, js, captureName, captureFirst)
	openAcked, openLost := l.lostOpen()
	t.Logf("while the store was not held fail-closed (the setting off): %d PUBACKed, %d of them lost", openAcked, openLost)

	// Every held disconnect has fired by now (the debounce is 30 s), and been refused or not.
	require.Eventually(t, func() bool { return tap.DeferredDeaths() == 0 }, 45*time.Second, 250*time.Millisecond,
		"the tap still holds disconnects back")
	online, flips, offlineNow := seen.snapshot()
	acked, delivered, dups := l.counts()
	t.Logf("%d PUBACKed, 0 lost, %d delivered into inbound-events (%d duplicates); store moves %d, capture moves %d; "+
		"%d connections closed on devices (%d while the store was full); %d refusal-close disconnects, at most %d held "+
		"back at once; presence emitted %d connects, %d devices flipped offline, %d read offline now",
		acked, delivered, dups, faults.moves[storeName].Load(), faults.moves[captureName].Load(),
		pushedBack(), full, seen.refusals.Load(), maxDeferred.Load(), online, len(flips), len(offlineNow))
	require.GreaterOrEqual(t, faults.moves[storeName].Load(), int64(5), "the store's leader was hardly moved")
	require.GreaterOrEqual(t, faults.moves[captureName].Load(), int64(5), "the capture stream's leader was hardly moved")
	require.GreaterOrEqual(t, full, int64(clusterDevices), "premise: the full store pushed the devices back")
	require.Greater(t, seen.refusals.Load(), int64(0), "premise: the broker closed connections for refused publishes")
	require.Greater(t, maxDeferred.Load(), int64(0), "premise: the tap held refusal-closes back")
	require.Empty(t, flips, "presence read devices offline that were only refused and reconnected")
	require.Empty(t, offlineNow, "presence reads connected devices offline")
	_ = a3
}

// logCapture reports the capture stream and its platform durable: how many messages the stream
// took since first (its first sequence when the test began), and how many the durable was
// handed. With nothing left in the stream, a gap between the two is what the stream discarded
// before anyone read it.
func logCapture(t *testing.T, js nats.JetStreamContext, name string, first uint64) {
	t.Helper()
	si, err := js.StreamInfo(name)
	if err != nil {
		t.Logf("capture: unreadable: %v", err)
		return
	}
	t.Logf("capture: %d msgs now, first %d last %d: %d taken during the test", si.State.Msgs, si.State.FirstSeq,
		si.State.LastSeq, si.State.LastSeq-first+1)
	for n := range js.ConsumerNames(name) {
		if ci, err := js.ConsumerInfo(name, n); err == nil {
			t.Logf("capture consumer %s: handed %d (last stream seq %d), ack floor %d, pending %d, ack-pending %d, "+
				"redelivered %d", n, ci.Delivered.Consumer, ci.Delivered.Stream, ci.AckFloor.Stream, ci.NumPending,
				ci.NumAckPending, ci.NumRedelivered)
		}
	}
}
