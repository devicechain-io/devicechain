// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package messaging

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// LOAD MEASUREMENTS OF THE FAIL-CLOSED MESSAGE STORE, run by hand. They are skipped unless
// DC_FCPERF is set, and they report numbers rather than assert them: what they measure depends
// on the machine. Run one in a Linux container (the cluster fixture needs unix sockets), e.g.
//
//	DC_FCPERF=1 FCP_SHAPE=store go test -run TestPerfRawDrain -v -count=1 .
//
// Knobs are FCP_* environment variables, read where each test uses them. What they found, on a
// three-node in-process cluster limited to six CPUs, 3,000 device subjects, 150-byte payloads:
//
//   - TestPerfRawDrain: a 300k backlog drained at ~11k/s through a wildcard-filtered durable
//     and ~117k/s through an unfiltered one (FCP_EVFILTER=all, FCP_EVFILTER_ALL=""), the same as
//     the capture stream's ~114k/s. The filtered cost is the broker's per-subject index walk
//     (MqttFailClosedDurable.Filter).
//   - TestPerfRawDrain's fill: ~26k/s into the store against ~179k/s into the capture stream.
//     A clustered discard-new stream with a byte limit sums its in-flight proposals per subject
//     on every publish (FCP_DEVICES 1: ~174k/s, 3,000: ~39k/s, 10,000: ~18k/s; FCP_DISCOLD:
//     ~171k/s), a broker cost no setting here removes.
//   - TestPerfStoreReaderSustained: the real store reader, starting 200k behind at 8k/s
//     offered, caught up at ~12k/s filtered and ~48k/s unfiltered.
//   - TestPerfStoreMemory: 1.5M messages held cost ~25-35 MiB of heap per server.

var pubProf func()

type perfTimedJS struct {
	nats.JetStreamContext
	n, ns atomic.Int64
}

func (j *perfTimedJS) StreamInfo(stream string, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	t0 := time.Now()
	if d := perfEnvInt("FCP_LOOK_DELAY_MS", 0); d > 0 && stream == MqttMessageStore {
		time.Sleep(time.Duration(d) * time.Millisecond)
	}
	si, err := j.JetStreamContext.StreamInfo(stream, opts...)
	if stream == MqttMessageStore {
		j.n.Add(1)
		j.ns.Add(int64(time.Since(t0)))
	}
	return si, err
}

func perfEnvInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return def
}

// TestPerfRawDrain is the raw broker cost, with no platform code: a backlog stored over many
// device subjects, then drained by one pull consumer acking each message. FCP_SHAPE=store is
// $MQTT_msgs as the fail-closed reconcile declares it (interest, discard new, 1 GiB, three
// durables); FCP_SHAPE=capture is a work-queue stream whose durable filter is its own subject.
func TestPerfRawDrain(t *testing.T) {
	if os.Getenv("DC_FCPERF") == "" {
		t.Skip("DC_FCPERF not set")
	}
	size := perfEnvInt("FCP_SERVERS", 3)
	prefill := perfEnvInt("FCP_PREFILL", 300_000)
	devices := perfEnvInt("FCP_DEVICES", 3000)
	shape := os.Getenv("FCP_SHAPE")
	var servers []*natsserver.Server
	if size == 1 {
		s, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
		require.NoError(t, err)
		go s.Start()
		require.True(t, s.ReadyForConnections(10*time.Second))
		t.Cleanup(s.Shutdown)
		servers = []*natsserver.Server{s}
	} else {
		servers = dctest.StartJetStreamCluster(t, size)
	}
	nc, err := nats.Connect(servers[0].ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream(nats.PublishAsyncMaxPending(4096))
	require.NoError(t, err)
	reps := size
	var stream, filter string
	var subj func(d int) string
	switch shape {
	case "capture":
		stream = "CAP"
		filter = "inst.*.devices.*.events"
		_, err = js.AddStream(&nats.StreamConfig{Name: stream, Subjects: []string{filter}, Storage: nats.FileStorage,
			Retention: nats.WorkQueuePolicy, Replicas: reps, Duplicates: 2 * time.Minute})
		require.NoError(t, err)
		subj = func(d int) string { return fmt.Sprintf("inst.t1.devices.d%d.events", d) }
		_, err = js.AddConsumer(stream, &nats.ConsumerConfig{Durable: "r", FilterSubject: filter, AckPolicy: nats.AckExplicitPolicy,
			MaxAckPending: 65536, Replicas: reps})
		require.NoError(t, err)
	default:
		stream = MqttMessageStore
		filter = "$MQTT.msgs.inst.*.devices.*.events"
		ret := nats.InterestPolicy
		if os.Getenv("FCP_RET") == "wq" {
			ret = nats.WorkQueuePolicy
		}
		disc, maxb := nats.DiscardNew, int64(1<<30)
		if os.Getenv("FCP_NOMAX") != "" {
			disc, maxb = nats.DiscardOld, -1
		}
		if os.Getenv("FCP_DISCOLD") != "" {
			disc = nats.DiscardOld
		}
		if os.Getenv("FCP_UNBOUNDED") != "" {
			maxb = -1
		}
		_, err = js.AddStream(&nats.StreamConfig{Name: stream, Subjects: []string{"$MQTT.msgs.>"}, Storage: nats.FileStorage,
			Retention: ret, Replicas: reps, Discard: disc, MaxBytes: maxb})
		require.NoError(t, err)
		subj = func(d int) string { return fmt.Sprintf("$MQTT.msgs.inst.t1.devices.d%d.events", d) }
		filters := []string{filter, "$MQTT.msgs.inst.*.devices.*.command-responses", "$MQTT.msgs.inst.*.devices.*.reports"}
		filters = filters[:perfEnvInt("FCP_NCONS", 3)]
		if os.Getenv("FCP_EVFILTER") == "all" {
			filters[0] = os.Getenv("FCP_EVFILTER_ALL")
			filter = filters[0]
		}
		for i, f := range filters {
			_, err = js.AddConsumer(stream, &nats.ConsumerConfig{Durable: fmt.Sprintf("r%d", i), FilterSubject: f, AckPolicy: nats.AckExplicitPolicy,
				MaxAckPending: 65536, Replicas: reps})
			require.NoError(t, err)
		}
	}
	payload := make([]byte, perfEnvInt("FCP_PAYLOAD", 300))
	if pf := os.Getenv("FCP_PROF_PUB"); pf != "" {
		f, err := os.Create(pf)
		require.NoError(t, err)
		require.NoError(t, pprof.StartCPUProfile(f))
		pubProf = func() { pprof.StopCPUProfile(); f.Close(); pubProf = nil }
	}
	start := time.Now()
	for i := 0; i < prefill; i++ {
		m := nats.NewMsg(subj(i % devices))
		m.Data = payload
		if shape != "capture" && os.Getenv("FCP_NOHDR") == "" {
			m.Header.Set(mqttGatewayHeader, "1")
		}
		for {
			if _, err := js.PublishMsgAsync(m); err == nil {
				break
			}
			select {
			case <-js.PublishAsyncComplete():
			case <-time.After(time.Second):
			}
		}
	}
	<-js.PublishAsyncComplete()
	if pubProf != nil {
		pubProf()
	}
	t.Logf("prefill %d in %v (%.0f/s)", prefill, time.Since(start), float64(prefill)/time.Since(start).Seconds())
	durable := "r"
	if shape != "capture" {
		durable = "r0"
	}
	sub, err := js.PullSubscribe(filter, durable, nats.Bind(stream, durable))
	require.NoError(t, err)
	var got atomic.Int64
	if pf := os.Getenv("FCP_PROF"); pf != "" {
		f, err := os.Create(pf)
		require.NoError(t, err)
		require.NoError(t, pprof.StartCPUProfile(f))
		defer func() { pprof.StopCPUProfile(); f.Close() }()
	}
	start = time.Now()
	last := start
	lastN := int64(0)
	for got.Load() < int64(prefill) {
		msgs, err := sub.Fetch(256, nats.MaxWait(2*time.Second))
		if err != nil && len(msgs) == 0 {
			t.Logf("fetch: %v", err)
			continue
		}
		for _, m := range msgs {
			_ = m.Ack()
		}
		got.Add(int64(len(msgs)))
		if time.Since(last) > 2*time.Second {
			n := got.Load()
			t.Logf("  %d drained, %.0f/s", n, float64(n-lastN)/time.Since(last).Seconds())
			last, lastN = time.Now(), n
		}
	}
	el := time.Since(start)
	t.Logf("SHAPE=%s servers=%d drained %d in %v: %.0f/s", shape, size, prefill, el, float64(prefill)/el.Seconds())
}

// TestPerfStoreReaderSustained drives the real store reader (NewMqttMessageStoreReader) over
// a three-node cluster: an initial backlog (a hiccup's worth), then a sustained publish at
// FCP_RATE into the store, the reader forwarding every message into an inbound stream under
// its dedup id and acking on the PubAck.
func TestPerfStoreReaderSustained(t *testing.T) {
	if os.Getenv("DC_FCPERF") == "" {
		t.Skip("DC_FCPERF not set")
	}
	size := perfEnvInt("FCP_SERVERS", 3)
	devices := perfEnvInt("FCP_DEVICES", 3000)
	rate := perfEnvInt("FCP_RATE", 9000)
	backlog := perfEnvInt("FCP_BACKLOG", 50_000)
	secs := perfEnvInt("FCP_SECS", 60)
	servers := dctest.StartJetStreamCluster(t, size)
	nc, err := nats.Connect(servers[0].ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream(nats.PublishAsyncMaxPending(8192))
	require.NoError(t, err)
	fastMqttStoreLookup(t)

	nmgr := failClosedManager(t, nc, js, guardInstance, true)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = uint32(size)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytes = 1 << 30
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytesCold = 1 << 30
	require.Eventually(t, func() bool { return nmgr.brokerIsClustered() }, 10*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool {
		_, err := nmgr.ReconcileMqttFailClosed(t.Context())
		return err == nil
	}, 60*time.Second, time.Second)
	si, err := js.StreamInfo(MqttMessageStore)
	require.NoError(t, err)
	t.Logf("store: replicas=%d maxBytes=%d discard=%v", si.Config.Replicas, si.Config.MaxBytes, si.Config.Discard)

	_, err = js.AddStream(&nats.StreamConfig{Name: "BENCH_inbound", Subjects: []string{"bench.inbound.>"}, Storage: nats.FileStorage,
		Retention: nats.LimitsPolicy, Replicas: size, Duplicates: 2 * time.Minute, MaxMsgs: 2_000_000})
	require.NoError(t, err)

	_, err = nmgr.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	tjs := &perfTimedJS{JetStreamContext: nmgr.js}
	nmgr.js = tjs
	r, err := nmgr.NewMqttMessageStoreReader(MqttFailClosedEvents)
	require.NoError(t, err)
	t.Cleanup(r.Close)

	payload := make([]byte, perfEnvInt("FCP_PAYLOAD", 300))
	pubOne := func(pjs nats.JetStreamContext, i int) {
		m := nats.NewMsg(mqttMsgsSubjectPrefix + DeviceEventsSubject(guardInstance, guardTenant, fmt.Sprintf("d%d", i%devices)))
		m.Data = payload
		m.Header.Set(mqttGatewayHeader, "1")
		for {
			if _, err := pjs.PublishMsgAsync(m); err == nil {
				return
			}
			select {
			case <-pjs.PublishAsyncComplete():
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	for i := 0; i < backlog; i++ {
		pubOne(js, i)
	}
	<-js.PublishAsyncComplete()
	t.Logf("backlog of %d stored", backlog)

	pnc, err := nats.Connect(servers[1%size].ClientURL())
	require.NoError(t, err)
	t.Cleanup(pnc.Close)
	pjs, err := pnc.JetStream(nats.PublishAsyncMaxPending(8192))
	require.NoError(t, err)
	var published, forwarded atomic.Int64
	stop := make(chan struct{})
	pubDone := make(chan struct{})
	go func() {
		defer close(pubDone)
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		begin := time.Now()
		i := backlog
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			want := int(time.Since(begin).Seconds() * float64(rate))
			for ; i-backlog < want; i++ {
				pubOne(pjs, i)
				published.Add(1)
			}
		}
	}()

	fnc, err := nats.Connect(servers[2%size].ClientURL())
	require.NoError(t, err)
	t.Cleanup(fnc.Close)
	fjs, err := fnc.JetStream(nats.PublishAsyncMaxPending(4096))
	require.NoError(t, err)
	type inflight struct {
		f   nats.PubAckFuture
		msg Message
	}
	window := make(chan inflight, 1024)
	go func() {
		for in := range window {
			select {
			case <-in.f.Ok():
				_ = in.msg.Ack()
				forwarded.Add(1)
			case <-in.f.Err():
			}
		}
	}()
	readCtx, cancelRead := context.WithCancel(context.Background())
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			msg, err := r.ReadMessage(readCtx)
			if err != nil {
				if readCtx.Err() != nil {
					return
				}
				continue
			}
			out := nats.NewMsg("bench.inbound." + guardTenant)
			out.Data = msg.Value
			if msg.DedupID != "" {
				out.Header.Set(nats.MsgIdHdr, msg.DedupID)
			}
			f, err := fjs.PublishMsgAsync(out)
			if err != nil {
				continue
			}
			window <- inflight{f, msg}
		}
	}()
	if pf := os.Getenv("FCP_PROF"); pf != "" {
		f, err := os.Create(pf)
		require.NoError(t, err)
		require.NoError(t, pprof.StartCPUProfile(f))
		defer func() { pprof.StopCPUProfile(); f.Close() }()
	}
	apiTotal := func() uint64 {
		n := uint64(0)
		for _, sv := range servers {
			if jz, err := sv.Jsz(nil); err == nil {
				n += jz.API.Total
			}
		}
		return n
	}
	api0 := apiTotal()
	begin := time.Now()
	lastF, lastP, lastT := int64(0), int64(0), begin
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	maxPending := uint64(0)
	peakHeap := uint64(0)
	for time.Since(begin) < time.Duration(secs)*time.Second {
		<-tick.C
		f, p := forwarded.Load(), published.Load()
		dt := time.Since(lastT).Seconds()
		si, err := js.StreamInfo(MqttMessageStore)
		msgs := uint64(0)
		if err == nil {
			msgs = si.State.Msgs
		}
		maxPending = max(maxPending, msgs)
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		peakHeap = max(peakHeap, ms.HeapInuse)
		t.Logf("t=%3.0fs published %6.0f/s forwarded %6.0f/s store=%d heapInuse=%d MiB", time.Since(begin).Seconds(), float64(p-lastP)/dt, float64(f-lastF)/dt, msgs, ms.HeapInuse>>20)
		if dir := os.Getenv("FCP_HEAPDIR"); dir != "" && ms.HeapInuse >= peakHeap {
			if hf, err := os.Create(dir + "/sustained-peak.heap"); err == nil {
				_ = pprof.WriteHeapProfile(hf)
				hf.Close()
			}
		}
		lastF, lastP, lastT = f, p, time.Now()
	}
	close(stop)
	<-pubDone
	el := time.Since(begin).Seconds()
	t.Logf("RESULT rate=%d devices=%d backlog=%d: published %.0f/s, forwarded %.0f/s over %.0fs, store max %d, JS API %.0f/s",
		rate, devices, backlog, float64(published.Load())/el, float64(forwarded.Load())/el, el, maxPending,
		float64(apiTotal()-api0)/el)
	t.Logf("RESULT peak heapInuse %d MiB", peakHeap>>20)
	t.Logf("RESULT store looks: %d (%.0f/s), mean %v", tjs.n.Load(), float64(tjs.n.Load())/el,
		time.Duration(tjs.ns.Load()/max(1, tjs.n.Load())))
	// Drain time after the drive.
	total := int64(backlog) + published.Load()
	dstart := time.Now()
	for forwarded.Load() < total && time.Since(dstart) < time.Duration(perfEnvInt("FCP_DRAIN_MAX", 120))*time.Second {
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("RESULT drain after drive: %v (forwarded %d of %d)", time.Since(dstart).Round(100*time.Millisecond), forwarded.Load(), total)
	cancelRead()
	<-readDone
	close(window)
}

// TestPerfStoreMemory measures what a backlog in the store costs the broker's heap, and what
// reading it (ConsumerInfo on each durable, as the watch does; a drain) adds.
func TestPerfStoreMemory(t *testing.T) {
	if os.Getenv("DC_FCPERF") == "" {
		t.Skip("DC_FCPERF not set")
	}
	size := perfEnvInt("FCP_SERVERS", 1)
	devices := perfEnvInt("FCP_DEVICES", 2250)
	prefill := perfEnvInt("FCP_PREFILL", 1_500_000)
	var servers []*natsserver.Server
	if size == 1 {
		s, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(),
			JetStreamMaxStore: 8 << 30})
		require.NoError(t, err)
		go s.Start()
		require.True(t, s.ReadyForConnections(10*time.Second))
		t.Cleanup(s.Shutdown)
		servers = []*natsserver.Server{s}
	} else {
		servers = dctest.StartJetStreamCluster(t, size)
	}
	nc, err := nats.Connect(servers[0].ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream(nats.PublishAsyncMaxPending(4096))
	require.NoError(t, err)
	fastMqttStoreLookup(t)
	nmgr := failClosedManager(t, nc, js, guardInstance, true)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = uint32(size)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytes = 1 << 30
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytesCold = 1 << 30
	require.Eventually(t, func() bool {
		_, err := nmgr.ReconcileMqttFailClosed(t.Context())
		return err == nil
	}, 60*time.Second, time.Second)
	if os.Getenv("FCP_OLDFILTER") != "" {
		ci, err := js.ConsumerInfo(MqttMessageStore, fcDurable(t, guardInstance, MqttFailClosedEvents).Name)
		require.NoError(t, err)
		cfg := ci.Config
		cfg.FilterSubject = mqttMsgsSubjectPrefix + DeviceEventsWildcard(guardInstance)
		_, err = js.UpdateConsumer(MqttMessageStore, &cfg)
		require.NoError(t, err)
	}
	sample := func(what string) {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		t.Logf("MEM %-28s heapInuse=%5d MiB heapSys=%5d MiB sys=%5d MiB", what, ms.HeapInuse>>20, ms.HeapSys>>20, ms.Sys>>20)
	}
	sample("empty store")
	payload := make([]byte, perfEnvInt("FCP_PAYLOAD", 300))
	for i := 0; i < prefill; i++ {
		m := nats.NewMsg(mqttMsgsSubjectPrefix + DeviceEventsSubject(guardInstance, guardTenant, fmt.Sprintf("d%d", i%devices)))
		m.Data = payload
		m.Header.Set(mqttGatewayHeader, "1")
		for {
			if _, err := js.PublishMsgAsync(m); err == nil {
				break
			}
			select {
			case <-js.PublishAsyncComplete():
			case <-time.After(100 * time.Millisecond):
			}
		}
		if i%250_000 == 0 && i > 0 {
			sample(fmt.Sprintf("stored %d", i))
		}
	}
	<-js.PublishAsyncComplete()
	si, err := js.StreamInfo(MqttMessageStore)
	require.NoError(t, err)
	t.Logf("store: %d msgs, %d MiB, fill %.0f%%", si.State.Msgs, si.State.Bytes>>20, 100*float64(si.State.Bytes)/float64(si.Config.MaxBytes))
	sample("backlog stored")
	time.Sleep(15 * time.Second)
	sample("backlog idle 15s")
	heap := func(name string) {
		if dir := os.Getenv("FCP_HEAPDIR"); dir != "" {
			f, err := os.Create(dir + "/" + name)
			require.NoError(t, err)
			require.NoError(t, pprof.WriteHeapProfile(f))
			f.Close()
		}
	}
	heap("idle.heap")
	for _, d := range MqttFailClosedDurables(guardInstance) {
		ci, err := js.ConsumerInfo(MqttMessageStore, d.Name)
		require.NoError(t, err)
		t.Logf("  %s pending=%d", d.Kind, ci.NumPending)
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Logf("MEM %-28s heapInuse=%5d MiB (no GC)", "after ConsumerInfo x3", ms.HeapInuse>>20)
	heap("consumerinfo.heap")
	sample("after ConsumerInfo x3 (GC)")
	time.Sleep(15 * time.Second)
	sample("ConsumerInfo +15s")
	// Drain half with a raw reader.
	ev := fcDurable(t, guardInstance, MqttFailClosedEvents)
	sub, err := js.PullSubscribe(ev.Filter, ev.Name, nats.Bind(MqttMessageStore, ev.Name))
	require.NoError(t, err)
	got := 0
	peak := uint64(0)
	for got < prefill/2 {
		msgs, _ := sub.Fetch(256, nats.MaxWait(2*time.Second))
		for _, m := range msgs {
			_ = m.Ack()
		}
		got += len(msgs)
		if got%(50_000) < 256 {
			runtime.ReadMemStats(&ms)
			peak = max(peak, ms.HeapInuse)
		}
	}
	t.Logf("MEM %-28s peak heapInuse=%5d MiB", "draining half", peak>>20)
	sample("drained half")
}
