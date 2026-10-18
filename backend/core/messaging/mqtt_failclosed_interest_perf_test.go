// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package messaging

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// TestPerfInterestRetention asks whether the store's interest retention can delete a message a
// platform durable has not acked, with the consumer set it really has: the unfiltered events
// durable, the two sparse filtered ones, and churn of session consumers like the ones the
// gateway creates for a clean-session reconnect (DeliverNew, filtered to one device's
// command topic, deleted again later). The store is $MQTT_msgs as the fail-closed reconcile
// declares it, on three nodes; a sustained publish the events reader cannot keep up with
// builds a backlog while the stream leader and the events durable's leader are stepped down
// in turn. It counts the PubAcked events the events durable never delivered, and asserts
// there are none. FCP_OLDFILTER gives the events durable the wildcard filter of the earlier
// build, for the comparison.
//
// The question is real because the broker removes messages from an interest stream in more
// than one place, and not all of them are replicated: an ack the stream leader judges nobody
// else needs is a replicated delete, but a deleted consumer's cleanup and the periodic
// checkInterestState compaction (to the lowest check floor of the consumers on THAT server,
// where a sparse durable's floor sits at the tail) act on each server's own copy, and a
// consumer reads from its own server's copy. Measured (2026-10, six CPUs) at 6k/s offered,
// 2k/s read, 20 sessions/s and 5 step-downs: 0 lost of 352k unfiltered, 0 of 348k with the
// old filter. The same set on three nats-server processes, with SIGKILLs of one, two and then
// the third server during a 750k backlog, also lost nothing in either mode; that harness is
// not in the tree.
func TestPerfInterestRetention(t *testing.T) {
	if os.Getenv("DC_FCPERF") == "" {
		t.Skip("DC_FCPERF not set")
	}
	rate := perfEnvInt("FCP_RATE", 6000)
	drive := time.Duration(perfEnvInt("FCP_DRIVE_S", 60)) * time.Second
	readRate := perfEnvInt("FCP_READ_RATE", 2000)
	devices := perfEnvInt("FCP_DEVICES", 1000)
	churn := perfEnvInt("FCP_CHURN", 20)          // session consumers created (and later deleted) per second
	stepEvery := perfEnvInt("FCP_STEPDOWN_S", 10) // 0: none
	sessionLife := perfEnvInt("FCP_SESSION_MS", 2000)
	oldFilter := os.Getenv("FCP_OLDFILTER") != ""

	servers := dctest.StartJetStreamCluster(t, 3)
	conn := func(i int) (*nats.Conn, nats.JetStreamContext) {
		nc, err := nats.Connect(servers[i%len(servers)].ClientURL(), nats.MaxReconnects(-1))
		require.NoError(t, err)
		t.Cleanup(nc.Close)
		js, err := nc.JetStream(nats.PublishAsyncMaxPending(4096), nats.PublishAsyncTimeout(5*time.Second))
		require.NoError(t, err)
		return nc, js
	}
	_, adm := conn(0)
	_, err := adm.AddStream(&nats.StreamConfig{Name: MqttMessageStore, Subjects: []string{mqttMsgsSubjectPrefix + ">"},
		Storage: nats.FileStorage, Retention: nats.InterestPolicy, Replicas: 3, Discard: nats.DiscardNew, MaxBytes: 1 << 30})
	require.NoError(t, err)
	durs := MqttFailClosedDurables("inst")
	if oldFilter {
		durs[0].Filter = durs[0].Forwards
	}
	for _, d := range durs {
		_, err := adm.AddConsumer(MqttMessageStore, MqttFailClosedConsumerConfig(d, 3))
		require.NoError(t, err)
	}
	evSubj := func(d int) string { return fmt.Sprintf("%sinst.t1.devices.d%d.events", mqttMsgsSubjectPrefix, d) }
	kindSubj := func(d int, k string) string {
		return fmt.Sprintf("%sinst.t1.devices.d%d.%s", mqttMsgsSubjectPrefix, d, k)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup

	// Publisher: unique ids; an id counts as PUBACKed only on a successful ack.
	var acked sync.Map
	var nAcked, nFailed atomic.Int64
	pubDone := make(chan struct{})
	_, pjs := conn(1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(pubDone)
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		end := time.Now().Add(drive)
		per := rate / 100
		id := 0
		for time.Now().Before(end) {
			<-tick.C
			for j := 0; j < per; j++ {
				id++
				d := id % devices
				subj := evSubj(d)
				if id%100 == 0 {
					subj = kindSubj(d, SubjectCommandResponses)
				} else if id%100 == 50 {
					subj = kindSubj(d, SubjectDeviceReports)
				}
				m := nats.NewMsg(subj)
				m.Header.Set(mqttGatewayHeader, "1")
				m.Data = []byte(strconv.Itoa(id))
				f, err := pjs.PublishMsgAsync(m)
				if err != nil {
					nFailed.Add(1)
					continue
				}
				myID := id
				isEvent := strings.HasSuffix(subj, ".events")
				go func() {
					select {
					case <-f.Ok():
						if isEvent {
							acked.Store(myID, true)
						}
						nAcked.Add(1)
					case <-f.Err():
						nFailed.Add(1)
					}
				}()
			}
		}
		select {
		case <-pjs.PublishAsyncComplete():
		case <-time.After(30 * time.Second):
		}
	}()

	// Events reader: rate-limited, so it falls behind; acks each message, drops non-events.
	var got sync.Map
	var nGot, nDropped atomic.Int64
	_, ejs := conn(2)
	ev := durs[0]
	esub, err := ejs.PullSubscribe(ev.Filter, ev.Name, nats.Bind(MqttMessageStore, ev.Name))
	require.NoError(t, err)
	var draining atomic.Bool
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		budget := time.Now()
		for ctx.Err() == nil {
			msgs, err := esub.Fetch(256, nats.MaxWait(time.Second))
			if err != nil && len(msgs) == 0 {
				continue
			}
			for _, m := range msgs {
				if !storeSubjectMatches(ev.Forwards, m.Subject) {
					nDropped.Add(1)
				} else {
					id, _ := strconv.Atoi(string(m.Data))
					if _, dup := got.LoadOrStore(id, true); !dup {
						nGot.Add(1)
					}
				}
				_ = m.Ack()
			}
			if !draining.Load() {
				budget = budget.Add(time.Duration(len(msgs)) * time.Second / time.Duration(readRate))
				if d := time.Until(budget); d > 0 {
					time.Sleep(d)
				} else {
					budget = time.Now()
				}
			}
		}
	}()
	// Sparse readers keep up.
	for _, d := range durs[1:] {
		d := d
		_, sjs := conn(0)
		sub, err := sjs.PullSubscribe(d.Filter, d.Name, nats.Bind(MqttMessageStore, d.Name))
		require.NoError(t, err)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				msgs, _ := sub.Fetch(256, nats.MaxWait(time.Second))
				for _, m := range msgs {
					_ = m.Ack()
				}
			}
		}()
	}

	// Session-consumer churn: what a clean-session MQTT reconnect does on $MQTT_msgs, a
	// DeliverNew push durable filtered to the device's command topic, deleted again later.
	var nCreated, nDeleted atomic.Int64
	if churn > 0 {
		nc, cjs := conn(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			tick := time.NewTicker(time.Second / time.Duration(churn))
			defer tick.Stop()
			for i := 0; ; i++ {
				select {
				case <-pubDone:
					return
				case <-tick.C:
				}
				name := fmt.Sprintf("sess_%d", i)
				inbox := nc.NewRespInbox()
				_, err := cjs.AddConsumer(MqttMessageStore, &nats.ConsumerConfig{Durable: name, DeliverSubject: inbox,
					AckPolicy: nats.AckExplicitPolicy, DeliverPolicy: nats.DeliverNewPolicy,
					FilterSubject: fmt.Sprintf("%sinst.t1.devices.d%d.commands", mqttMsgsSubjectPrefix, rand.Intn(devices)),
					AckWait:       30 * time.Second, MaxAckPending: 1024})
				if err != nil {
					continue
				}
				nCreated.Add(1)
				time.AfterFunc(time.Duration(sessionLife)*time.Millisecond, func() {
					if cjs.DeleteConsumer(MqttMessageStore, name) == nil {
						nDeleted.Add(1)
					}
				})
			}
		}()
	}

	// Step-downs: alternate the stream leader and the events durable's leader.
	var nStep atomic.Int64
	if stepEvery > 0 {
		snc, _ := conn(0)
		wg.Add(1)
		go func() {
			defer wg.Done()
			tick := time.NewTicker(time.Duration(stepEvery) * time.Second)
			defer tick.Stop()
			for i := 0; ; i++ {
				select {
				case <-pubDone:
					return
				case <-tick.C:
				}
				subj := "$JS.API.STREAM.LEADER.STEPDOWN." + MqttMessageStore
				if i%2 == 1 {
					subj = "$JS.API.CONSUMER.LEADER.STEPDOWN." + MqttMessageStore + "." + ev.Name
				}
				if _, err := snc.Request(subj, nil, 5*time.Second); err == nil {
					nStep.Add(1)
				}
			}
		}()
	}

	logTick := time.NewTicker(5 * time.Second)
	defer logTick.Stop()
	report := func(tag string) {
		ci, _ := adm.ConsumerInfo(MqttMessageStore, ev.Name)
		si, _ := adm.StreamInfo(MqttMessageStore)
		var pend, ackp uint64
		var msgs uint64
		if ci != nil {
			pend, ackp = ci.NumPending, uint64(ci.NumAckPending)
		}
		if si != nil {
			msgs = si.State.Msgs
		}
		t.Logf("%s acked=%d failed=%d got=%d dropped=%d pending=%d ackpending=%d stored=%d sessions +%d/-%d stepdowns=%d",
			tag, nAcked.Load(), nFailed.Load(), nGot.Load(), nDropped.Load(), pend, ackp, msgs, nCreated.Load(), nDeleted.Load(), nStep.Load())
	}
wait:
	for {
		select {
		case <-pubDone:
			break wait
		case <-logTick.C:
			report("drive")
		}
	}
	time.Sleep(time.Duration(sessionLife+500) * time.Millisecond)
	draining.Store(true)
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		ci, err := adm.ConsumerInfo(MqttMessageStore, ev.Name)
		if err == nil && ci.NumPending == 0 && ci.NumAckPending == 0 {
			break
		}
		select {
		case <-logTick.C:
			report("drain")
		default:
			time.Sleep(200 * time.Millisecond)
		}
	}
	time.Sleep(2 * time.Second)
	report("end")
	cancel()
	<-readerDone

	var lost, total int
	var sample []int
	acked.Range(func(k, _ any) bool {
		total++
		if _, ok := got.Load(k); !ok {
			lost++
			if len(sample) < 20 {
				sample = append(sample, k.(int))
			}
		}
		return true
	})
	t.Logf("RESULT filter=%v acked events=%d lost=%d sample=%v", map[bool]string{true: "old", false: "none"}[oldFilter], total, lost, sample)
	require.Zero(t, lost, "PUBACKed events the events durable never delivered")
}
