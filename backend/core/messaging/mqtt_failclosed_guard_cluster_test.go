// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"bytes"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-microservice/test/mqttack"
)

// The cluster half of the fail-closed MQTT guard (see mqtt_failclosed_guard_test.go for what
// is pinned and why): a replicated $MQTT_msgs, a replicated platform durable, and the stream's
// leader moved while devices publish.
//
// What a leader move is allowed to cost is DUPLICATES: a store that was in flight when the
// leader changed times out, the gateway closes the connection without a PUBACK, and the device
// re-sends. What it may never cost is a message the device WAS told was stored, and that is
// what both tests count.

// guardCluster is a three-node JetStream cluster with the MQTT gateway on every node.
type guardCluster struct {
	servers  []*natsserver.Server
	mqtt     []int
	nc       *nats.Conn
	js       nats.JetStreamContext
	moves    atomic.Int64
	timeline *moveLog
	pending  sync.WaitGroup
}

func startGuardCluster(t *testing.T) *guardCluster {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the in-process cluster fixture holds its refusing route addresses with unix sockets")
	}
	gc := &guardCluster{timeline: newMoveLog()}
	gc.servers, _ = dctest.StartJetStreamClusterConfigured(t, 3, func(i int, o *natsserver.Options) {
		o.MQTT = natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1, StreamReplicas: 3}
	})
	gc.mqtt = clusterMQTTPorts(t, gc.servers)
	nc, err := nats.Connect(gc.servers[0].ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	gc.nc, gc.js = nc, js

	cfg := guardStoreConfig(nats.DiscardNew, 100_000, 3)
	require.Eventually(t, func() bool {
		_, err := js.AddStream(cfg)
		return err == nil
	}, 30*time.Second, 200*time.Millisecond, "the replicated message store was never created")
	return gc
}

// clusterMQTTPorts reads back the MQTT port each server bound, through dctest.MQTTPort. The
// listener must be configured with Port -1 (see the cluster fixtures' configure callbacks):
// a port picked beforehand by a probe listener and released is free only until something
// else binds it, and a cluster's own routes, proxies and dials bind ports in between.
func clusterMQTTPorts(t *testing.T, servers []*natsserver.Server) []int {
	t.Helper()
	ports := make([]int, len(servers))
	for i, s := range servers {
		ports[i] = dctest.MQTTPort(t, s)
	}
	return ports
}

func (gc *guardCluster) mqttURL(i int) string { return fmt.Sprintf("tcp://127.0.0.1:%d", gc.mqtt[i]) }

// stepDown asks the message store's current leader to step down, which moves leadership,
// and counts the moves the server confirmed so a test can show its fault happened.
func (gc *guardCluster) stepDown() {
	msg, err := gc.nc.Request("$JS.API.STREAM.LEADER.STEPDOWN."+MqttMessageStore, nil, 3*time.Second)
	if err != nil {
		return
	}
	if !bytes.Contains(msg.Data, []byte(`"error"`)) {
		gc.moves.Add(1)
		gc.timeline.add(MqttMessageStore)
	}
}

// stepDownAsync is stepDown on its own goroutine, waited for before a test reads the count.
func (gc *guardCluster) stepDownAsync() {
	gc.pending.Add(1)
	go func() {
		defer gc.pending.Done()
		gc.stepDown()
	}()
}

// guardCollector reads a durable continuously, through leader moves, and keeps every
// delivery, duplicates included.
type guardCollector struct {
	mu         sync.Mutex
	seen       map[string]int
	deliveries *deliveryLog
	stop       chan struct{}
	done       chan struct{}
}

func startGuardCollector(t *testing.T, js nats.JetStreamContext, durable, filter string) *guardCollector {
	t.Helper()
	sub, err := js.PullSubscribe(filter, durable, nats.Bind(MqttMessageStore, durable))
	require.NoError(t, err)
	c := &guardCollector{seen: map[string]int{}, deliveries: newDeliveryLog(), stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		defer sub.Unsubscribe()
		for {
			select {
			case <-c.stop:
				return
			default:
			}
			// Errors are expected while leadership moves; the next fetch tries again.
			msgs, _ := sub.Fetch(64, nats.MaxWait(300*time.Millisecond))
			for _, m := range msgs {
				c.mu.Lock()
				c.seen[string(m.Data)]++
				c.mu.Unlock()
				if md, err := m.Metadata(); err == nil {
					c.deliveries.add(md.Sequence.Stream, string(m.Data))
				}
				_ = m.AckSync()
			}
		}
	}()
	t.Cleanup(c.close)
	return c
}

func (c *guardCollector) close() {
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
	<-c.done
}

// lostOf reports which of the PUBACKed payloads have not been read.
func (c *guardCollector) lostOf(acked []string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var lost []string
	for _, a := range acked {
		if c.seen[a] == 0 {
			lost = append(lost, a)
		}
	}
	return lost
}

func (c *guardCollector) duplicates() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := 0
	for _, n := range c.seen {
		d += n - 1
	}
	return d
}

// publishResult is what one device's round of publishes got.
type publishResult struct {
	// pubacked are the payloads the broker PUBACKed: what a device was told was stored.
	pubacked []string
	// resumed are the payloads whose token completed WITHOUT a PUBACK (see mqttack): the
	// broker never acknowledged them, so their absence is not a loss.
	resumed []string
	// early counts the tokens completed before their PUBACK: by a reconnect's re-send, since
	// paho records a real PUBACK in the store before it completes the token. It is how often the
	// path that made these tests report false losses was taken.
	early int
}

// publishThrough publishes n messages from one auto-reconnecting device. A publish whose
// connection the broker closed is re-sent by the client library on reconnect (its session is
// persistent), exactly as a device's would be, and only a PUBACK the broker sent, for either
// send, counts: a completed token is not one (see mqttack). Before disconnecting, the device
// waits for the PUBACKs of its re-sends, as a device that wants its messages delivered would.
//
// It reports failure as an error and never asserts: it runs on per-device goroutines, where
// FailNow is not allowed, and under leader moves a slow connect or a slow answer is an
// expected cost the caller decides what to do about. The messages PUBACKed before an error
// are still returned, because the device WAS told they were stored.
func publishThrough(t *testing.T, url, device, topic, prefix string, n int) ([]string, error) {
	res, err := publishThroughResult(t, url, device, topic, prefix, n)
	return res.pubacked, err
}

func publishThroughResult(t *testing.T, url, device, topic, prefix string, n int) (publishResult, error) {
	t.Helper()
	var res publishResult
	clientID, err := DeviceClientID(guardInstance, guardTenant, device)
	if err != nil {
		return res, err
	}
	// A CONNECT that lands on a leader move can be refused or dropped (the gateway looks its
	// streams up at CONNECT, and "non-CONNACK first packet" is how that shows), so a device
	// tries again. It does so for a bounded TIME, as a real device would, not a bounded count:
	// a run of step-downs back to back can refuse more attempts than any fixed count.
	var g *guardClient
	var store *mqttack.Store
	deadline := time.Now().Add(30 * time.Second)
	for g == nil {
		var err error
		store = mqttack.NewStore()
		if g, err = tryConnectGuardClient(t, url, clientID, store, true); err != nil {
			if !time.Now().Before(deadline) {
				return res, fmt.Errorf("connecting %q kept failing for 30s: %w", clientID, err)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	// One device, one live connection: a later round of the same device must not fight this
	// one for the client ID.
	defer g.c.Disconnect(100)
	type sent struct {
		payload string
		mid     uint16
	}
	var done []sent
	collect := func() {
		for _, s := range done {
			if store.PubAcked(s.mid) {
				res.pubacked = append(res.pubacked, s.payload)
			} else {
				res.resumed = append(res.resumed, s.payload)
			}
		}
	}
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("%s-%03d", prefix, i)
		tok := g.c.Publish(topic, 1, false, p)
		select {
		case <-tok.Done():
			if tok.Error() == nil {
				mid := tok.(*mqtt.PublishToken).MessageID()
				if !store.PubAcked(mid) {
					res.early++
				}
				done = append(done, sent{p, mid})
			}
		case <-time.After(45 * time.Second):
			collect()
			return res, fmt.Errorf("publish %q was never answered", p)
		}
	}
	// The re-sends a reconnect queued are answered on tokens nobody holds: wait for their
	// PUBACKs here, or the disconnect below cuts them off.
	settle := time.Now().Add(45 * time.Second)
	for store.Outstanding() > 0 && time.Now().Before(settle) {
		time.Sleep(50 * time.Millisecond)
	}
	collect()
	return res, nil
}

// A leader move of $MQTT_msgs while devices publish never loses a message a device was told
// was stored. The platform durable survives the move and the messages in flight at the move
// come back as re-sends, so the consumer may see some twice.
//
// GUARD: kills a nats-server whose store acknowledges a message a new leader then does not
// have, or whose durable does not follow the stream to a new leader.
func TestAMessageStoreLeaderMoveUnderLoadDuplicatesButNeverLosesAPubackedMessage(t *testing.T) {
	gc := startGuardCluster(t)
	require.NoError(t, guardAddDurable(gc.js, guardDurable, guardFilter(guardInstance), 3))
	col := startGuardCollector(t, gc.js, guardDurable, guardFilter(guardInstance))

	// The leader is stepped down until the test has CONFIRMED enough moves, not a fixed number
	// of times: a step-down is refused while a new leader is still being elected, and on a fast
	// machine the devices finished before a fixed fifteen had confirmed more than one. The
	// devices keep publishing (another round each) until that many moves have happened, so the
	// load spans all of them.
	const requiredMoves = 3
	stopMoves := make(chan struct{})
	var moves sync.WaitGroup
	moves.Add(1)
	go func() {
		defer moves.Done()
		for {
			select {
			case <-stopMoves:
				return
			case <-time.After(150 * time.Millisecond):
				gc.stepDown()
			}
		}
	}()

	const devices, perDevice = 6, 200
	loadDeadline := time.Now().Add(90 * time.Second)
	var (
		mu      sync.Mutex
		acked   []string
		resumed []string // completed with no PUBACK: not acknowledged, so not owed
		early   int
		errs    []error
		wg      sync.WaitGroup
	)
	for d := 0; d < devices; d++ {
		wg.Add(1)
		go func(d int) {
			defer wg.Done()
			dev := fmt.Sprintf("load%d", d)
			for round := 0; round == 0 || (gc.moves.Load() < requiredMoves && time.Now().Before(loadDeadline)); round++ {
				got, err := publishThroughResult(t, gc.mqttURL(d%3), dev, guardTopic(guardInstance, guardTenant, dev),
					fmt.Sprintf("%s-r%d", dev, round), perDevice)
				mu.Lock()
				acked = append(acked, got.pubacked...)
				resumed = append(resumed, got.resumed...)
				early += got.early
				if err != nil {
					errs = append(errs, err)
				}
				mu.Unlock()
				if err != nil {
					return
				}
			}
		}(d)
	}
	wg.Wait()
	close(stopMoves)
	moves.Wait()

	require.Empty(t, errs, "a device could not finish its publishes")
	require.NotEmpty(t, acked)
	require.GreaterOrEqual(t, gc.moves.Load(), int64(requiredMoves), "the leader was hardly moved, so little was tested")
	// A message delivered and lost on the way at a move is redelivered only once the durable's
	// AckWait has run out, so the bound is past it: what is still unread then is not late.
	ci, err := gc.js.ConsumerInfo(MqttMessageStore, guardDurable)
	require.NoError(t, err)
	deadline := time.Now().Add(ci.Config.AckWait + 30*time.Second)
	for len(col.lostOf(acked)) > 0 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if unread := col.lostOf(acked); len(unread) > 0 {
		absent := unreadReport(t, gc.js, guardDurable, guardFilter(guardInstance), unread, gc.timeline)
		if si, err := gc.js.StreamInfo(MqttMessageStore); err == nil {
			col.deliveries.report(t, si.State.LastSeq, unread)
		}
		t.Fatalf("%d PUBACKed messages were never read from the durable, %d of them absent from the store", len(unread), len(absent))
	}
	t.Logf("%d PUBACKed, 0 lost, %d duplicate deliveries across %d leader moves; %d tokens completed before their PUBACK, %d publishes never PUBACKed (%d of them never read)",
		len(acked), col.duplicates(), gc.moves.Load(), early, len(resumed), len(col.lostOf(resumed)))
}

// The leader of $MQTT_msgs moves while the platform durable is being created, and a burst
// published the moment the creation is answered is still wholly read. The risk it probes: the
// stream's interest is decided on each replica when the write is applied, so a follower that
// has not yet applied the consumer could skip a message the leader stored, and a later leader
// move would lose something already PUBACKed.
//
// Messages published BEFORE the durable exists are skipped by design (see the no-consumer
// test) and are not counted; the burst starts when the broker has answered the creation.
//
// GUARD: kills a nats-server in which an acknowledged consumer creation does not yet give
// every replica interest.
func TestAMessageStoreLeaderMoveDuringDurableCreationNeverLosesAPubackedMessage(t *testing.T) {
	gc := startGuardCluster(t)
	const rounds, burst = 4, 40
	var total, dups int
	for r := 0; r < rounds; r++ {
		device := fmt.Sprintf("born%d", r)
		filter := mqttMsgsSubjectPrefix + guardInstance + "." + guardTenant + ".devices." + device + ".events"
		name := fmt.Sprintf("%s-%d", guardDurable, r)

		gc.stepDownAsync()
		require.Eventually(t, func() bool { return guardAddDurable(gc.js, name, filter, 3) == nil },
			30*time.Second, 100*time.Millisecond, "durable %s was never created", name)
		col := startGuardCollector(t, gc.js, name, filter)

		gc.stepDownAsync()
		acked, err := publishThrough(t, gc.mqttURL(r%3), device, guardTopic(guardInstance, guardTenant, device), device, burst)
		require.NoError(t, err, "round %d", r)
		require.Len(t, acked, burst, "round %d: every publish was eventually PUBACKed", r)
		require.Eventually(t, func() bool { return len(col.lostOf(acked)) == 0 }, 30*time.Second, 200*time.Millisecond,
			"round %d: PUBACKed messages lost after the durable's creation: %v", r, col.lostOf(acked))
		total += len(acked)
		dups += col.duplicates()
		col.close()
	}
	gc.pending.Wait()
	require.Greater(t, gc.moves.Load(), int64(0), "the leader was never moved, so nothing was tested")
	t.Logf("%d PUBACKed over %d rounds, 0 lost, %d duplicate deliveries, %d leader moves", total, rounds, dups, gc.moves.Load())
}
