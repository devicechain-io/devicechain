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
	servers []*natsserver.Server
	mqtt    []int
	nc      *nats.Conn
	js      nats.JetStreamContext
	moves   atomic.Int64
	pending sync.WaitGroup
}

func startGuardCluster(t *testing.T) *guardCluster {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the in-process cluster fixture holds its refusing route addresses with unix sockets")
	}
	gc := &guardCluster{mqtt: make([]int, 3)}
	for i := range gc.mqtt {
		gc.mqtt[i] = dctest.FreeTCPPort(t)
	}
	gc.servers, _ = dctest.StartJetStreamClusterConfigured(t, 3, func(i int, o *natsserver.Options) {
		o.MQTT = natsserver.MQTTOpts{Host: "127.0.0.1", Port: gc.mqtt[i], StreamReplicas: 3}
	})
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
	mu   sync.Mutex
	seen map[string]int
	stop chan struct{}
	done chan struct{}
}

func startGuardCollector(t *testing.T, js nats.JetStreamContext, durable, filter string) *guardCollector {
	t.Helper()
	sub, err := js.PullSubscribe(filter, durable, nats.Bind(MqttMessageStore, durable))
	require.NoError(t, err)
	c := &guardCollector{seen: map[string]int{}, stop: make(chan struct{}), done: make(chan struct{})}
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

// publishThrough publishes n messages from one auto-reconnecting device. A publish whose
// connection the broker closed is re-sent by the client library on reconnect (its session is
// persistent), exactly as a device's would be, and the PUBACK it finally gets is the one
// counted.
func publishThrough(t *testing.T, url, device, topic, prefix string, n int) []string {
	t.Helper()
	clientID, err := DeviceClientID(guardInstance, guardTenant, device)
	require.NoError(t, err)
	// A CONNECT that lands on a leader move can be refused or dropped (the gateway looks its
	// streams up at CONNECT, and "non-CONNACK first packet" is how that shows), so a device
	// tries again. It does so for a bounded TIME, as a real device would, not a bounded count:
	// a run of step-downs back to back can refuse more attempts than any fixed count.
	var g *guardClient
	deadline := time.Now().Add(30 * time.Second)
	for g == nil {
		var err error
		if g, err = tryConnectGuardClient(t, url, clientID, mqtt.NewMemoryStore(), true); err != nil {
			require.True(t, time.Now().Before(deadline), "connecting %q kept failing for 30s: %v", clientID, err)
			time.Sleep(500 * time.Millisecond)
		}
	}
	var acked []string
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("%s-%03d", prefix, i)
		tok := g.c.Publish(topic, 1, false, p)
		select {
		case <-tok.Done():
			if tok.Error() == nil {
				acked = append(acked, p)
			}
		case <-time.After(45 * time.Second):
			t.Errorf("publish %q was never answered", p)
			return acked
		}
	}
	return acked
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

	stopMoves := make(chan struct{})
	var moves sync.WaitGroup
	moves.Add(1)
	go func() {
		defer moves.Done()
		// A bounded number of moves: enough to land among the publishes, not so many that
		// the cluster does nothing else.
		for i := 0; i < 15; i++ {
			select {
			case <-stopMoves:
				return
			case <-time.After(150 * time.Millisecond):
				gc.stepDown()
			}
		}
	}()

	const devices, perDevice = 6, 200
	var (
		mu    sync.Mutex
		acked []string
		wg    sync.WaitGroup
	)
	for d := 0; d < devices; d++ {
		wg.Add(1)
		go func(d int) {
			defer wg.Done()
			dev := fmt.Sprintf("load%d", d)
			got := publishThrough(t, gc.mqttURL(d%3), dev, guardTopic(guardInstance, guardTenant, dev), dev, perDevice)
			mu.Lock()
			acked = append(acked, got...)
			mu.Unlock()
		}(d)
	}
	wg.Wait()
	close(stopMoves)
	moves.Wait()

	require.NotEmpty(t, acked)
	require.Greater(t, gc.moves.Load(), int64(2), "the leader was never moved, so nothing was tested")
	require.Eventually(t, func() bool { return len(col.lostOf(acked)) == 0 }, 30*time.Second, 200*time.Millisecond,
		"PUBACKed messages were never read from the durable: lost %v", col.lostOf(acked))
	t.Logf("%d PUBACKed, 0 lost, %d duplicate deliveries across %d leader moves", len(acked), col.duplicates(), gc.moves.Load())
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
		acked := publishThrough(t, gc.mqttURL(r%3), device, guardTopic(guardInstance, guardTenant, device), device, burst)
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
