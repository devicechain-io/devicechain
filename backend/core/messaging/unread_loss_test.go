// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
)

// These tests pin what a durable consumer LOSES when its stream discards messages it has
// not read yet.
//
// Every stream is bounded and discards its OLDEST message when full. That is the right
// policy for a platform that must keep ingesting, and it is silent by construction: the
// broker removes the message, the consumer's cursor steps over the hole on its next pull,
// and nothing anywhere says a message went unread. The stream's fill gauges show that a
// stream is FULL; they cannot show that a particular reader fell behind it, because a
// full stream whose readers are all caught up loses nothing.
//
// So loss is counted per durable, from the broker's own numbers, by the sampler every
// service already runs — and read here through the registry a scrape reads, not off a
// Go handle, so a series that is built but never registered reads as absent.
//
// 🔴 Everything below runs against a real embedded JetStream server. The quantity being
// measured is broker behaviour — which sequences a pull skips, what ConsumerInfo reports
// for a cursor that stepped over a hole, what a recreated durable reports — and a fake
// would restate the model under test rather than check it.

const unreadSuffix = streams.AlarmEvents

// unreadRig is one service reading one stream through the real constructors: a
// NatsManager from NewNatsManager, a writer from NewWriter and a reader from NewReader.
// The manager is initialised but not started, so no sampler goroutine runs; the test
// drives the sampler's own pass (sampleNow) itself, which is what lets it decide exactly
// which interval each sample covers.
type unreadRig struct {
	t       *testing.T
	nmgr    *NatsManager
	reg     *prometheus.Registry
	writer  MessageWriter
	reader  *natsReader
	stream  string
	durable string
	area    string
}

func newUnreadRig(t *testing.T, maxMsgs int64) *unreadRig {
	t.Helper()
	return newUnreadRigOn(t, startEmbeddedServer(t), uniqueArea("unread"), maxMsgs)
}

// newUnreadRigOn builds a rig on an existing server and area. A second rig on the same
// server and area is a second pod of the same service: its own manager, its own registry
// and sampler state, and the SAME durable.
func newUnreadRigOn(t *testing.T, srv *natsserver.Server, area string, maxMsgs int64) *unreadRig {
	t.Helper()
	ms := testMicroservice(t, srv, area)
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	if maxMsgs > 0 {
		ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxMsgs = maxMsgs
	}
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	if err := nmgr.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	t.Cleanup(func() { nmgr.closeConn() })
	w, err := nmgr.NewWriter(unreadSuffix)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	r, err := nmgr.NewReader(unreadSuffix)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return &unreadRig{
		t: t, nmgr: nmgr, reg: reg, writer: w, reader: r.(*natsReader),
		stream:  StreamName("test", unreadSuffix),
		durable: DurableName("test", area, unreadSuffix),
		area:    area,
	}
}

func (g *unreadRig) publish(tenant string, n int) {
	g.t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	for i := 0; i < n; i++ {
		if err := g.writer.WriteMessages(ctx, Message{Value: []byte("m")}); err != nil {
			g.t.Fatalf("publish %d for %s: %v", i, tenant, err)
		}
	}
}

// read hands out n messages and acks each, returning their stream sequences.
func (g *unreadRig) read(n int) []uint64 {
	g.t.Helper()
	seqs := make([]uint64, 0, n)
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		m, err := g.reader.ReadMessage(ctx)
		cancel()
		if err != nil {
			g.t.Fatalf("read %d of %d: %v", i+1, n, err)
		}
		if err := m.Ack(); err != nil {
			g.t.Fatalf("ack: %v", err)
		}
		seqs = append(seqs, m.StreamSeq)
	}
	return seqs
}

func (g *unreadRig) sample() {
	g.nmgr.sampleNow(context.Background())
}

// series reads one per-durable series from the registry the service's /metrics serves.
// found=false is ABSENCE, which every caller treats as a failure in its own right: a
// counter that does not exist and a counter at zero are different claims.
func (g *unreadRig) series(suffix string) (value float64, found bool) {
	g.t.Helper()
	families, err := g.reg.Gather()
	if err != nil {
		g.t.Fatalf("gather: %v", err)
	}
	want := "devicechain_" + strings.ReplaceAll(g.area, "-", "") + "_" + suffix
	for _, f := range families {
		if f.GetName() != want {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["stream"] != g.stream || labels["durable"] != g.durable {
				continue
			}
			if c := m.GetCounter(); c != nil {
				return c.GetValue(), true
			}
			return m.GetGauge().GetValue(), true
		}
	}
	return 0, false
}

func (g *unreadRig) mustSeries(suffix string) float64 {
	g.t.Helper()
	v, ok := g.series(suffix)
	if !ok {
		g.t.Fatalf("series %s{stream=%q,durable=%q} is not exported at all: a reader that "+
			"loses messages it never read would leave no trace on /metrics", suffix, g.stream, g.durable)
	}
	return v
}

const (
	skippedSeries = "jetstream_consumer_unread_skipped_total"
	gapSeries     = "jetstream_consumer_unread_gap_messages"
)

func (g *unreadRig) streamInfo() *nats.StreamInfo {
	g.t.Helper()
	info, err := g.nmgr.js.StreamInfo(g.stream)
	if err != nil {
		g.t.Fatalf("stream info: %v", err)
	}
	return info
}

// A reader that is ALIVE and pulling, but slower than its producer, against a full
// stream. This is the ordinary way messages go unread: nothing is stalled, every pull
// succeeds, and the cursor quietly steps over whatever the stream evicted between two
// pulls.
//
// It is also a case the gap gauge must NOT report. Between two pulls of a live reader the
// stream keeps evicting ahead of its cursor, so "how far is the cursor behind the stream's
// first sequence" is above zero at almost any moment a sample lands — for a reader that
// never stopped. That is a lagging reader, not a stalled one: its loss is the counter's,
// counted as the cursor crosses it, and the gauge reports only a durable that was handed
// nothing since the previous sample. So every sample taken while this reader is working
// must read a gap of 0.
func TestLiveLaggingDurableLossIsCounted(t *testing.T) {
	const published = 2000
	g := newUnreadRig(t, 10)
	g.sample() // baseline

	// gaps records the gap gauge at every sample the sampler takes while the reader is
	// between its first delivery and the head, and evictedAhead how many of those samples
	// found the stream's first sequence past the cursor — the premise that the check on
	// gaps is not vacuous.
	var (
		gaps         []float64
		evictedAhead int
	)

	var (
		mu        sync.Mutex
		delivered = map[uint64]bool{}
	)
	ctx, stop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // the slow reader
		defer wg.Done()
		for {
			m, err := g.reader.ReadMessage(ctx)
			if err != nil {
				return
			}
			_ = m.Ack()
			mu.Lock()
			delivered[m.StreamSeq] = true
			mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	// The sampler, on a cadence much finer than production's but coarser than the time
	// a fetched batch takes to clear: the stream holds at most 10 messages, so a batch is
	// at most 10 x 20ms, and every interval spans at least one pull.
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(400 * time.Millisecond):
				mu.Lock()
				working := len(delivered) > 0 && !delivered[published]
				mu.Unlock()
				g.sample()
				if !working {
					continue
				}
				// Not the rig's t.Fatal helpers: this is not the test goroutine. An absent
				// series is recorded as -1, which the check below reports as nonzero.
				ci, cerr := g.nmgr.js.ConsumerInfo(g.stream, g.durable)
				si, serr := g.nmgr.js.StreamInfo(g.stream)
				if cerr == nil && serr == nil && si.State.FirstSeq > ci.Delivered.Stream+1 {
					evictedAhead++
				}
				v, ok := g.series(gapSeries)
				if !ok {
					v = -1
				}
				gaps = append(gaps, v)
			}
		}
	}()

	// Paced, so the run spans many sample intervals rather than the fraction of a second an
	// unpaced publish takes; still far faster than the reader's ~50 messages a second.
	for i := 0; i < published/100; i++ {
		g.publish("acme", 100)
		time.Sleep(150 * time.Millisecond)
	}

	// Let the reader reach the head, so every sequence has been either delivered or
	// stepped over and the count is complete.
	deadline := time.Now().Add(30 * time.Second)
	for {
		mu.Lock()
		done := delivered[published]
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			stop()
			wg.Wait()
			t.Fatal("the reader never reached the last published sequence")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	wg.Wait()
	g.sample()

	mu.Lock()
	lost := published - len(delivered)
	mu.Unlock()
	if lost < published/2 {
		t.Fatalf("only %d of %d messages went unread; the reader was not slow enough for this "+
			"test to say anything about loss", lost, published)
	}
	got := g.mustSeries(skippedSeries)
	t.Logf("published %d, delivered %d distinct, counted %v unread", published, published-lost, got)
	if got > float64(lost) || got < 0.9*float64(lost) {
		t.Errorf("unread-skipped counter = %v, want within [%v, %d]: %d messages were published "+
			"and the reader was handed %d distinct ones, so %d were removed before it read them",
			got, 0.9*float64(lost), lost, published, published-lost, lost)
	}

	t.Logf("gap gauge while the reader was working: %v (%d samples found evictions ahead of the cursor)",
		gaps, evictedAhead)
	if evictedAhead == 0 {
		t.Fatalf("no sample taken while the reader was working found the stream evicting ahead of "+
			"its cursor (%d samples); the gap check below would pass for any gauge", len(gaps))
	}
	for i, v := range gaps {
		if v != 0 {
			t.Errorf("unread-gap gauge = %v at sample %d while the reader was pulling continuously; "+
				"want 0: a reader that is slower than its producer is lagging, not stalled, and the "+
				"stalled alert would send the operator looking for pods that are not reading", v, i)
		}
	}
}

// A durable that has STOPPED reading. Its cursor does not move, so it crosses no hole
// and the counter cannot see the loss yet — but the messages are already gone. The gap
// gauge is what states that while it is happening; the counter takes over the moment the
// reader resumes and steps over the hole.
func TestStalledDurableGapIsGauged(t *testing.T) {
	g := newUnreadRig(t, 10)
	g.sample() // baseline

	g.publish("acme", 2)
	if seqs := g.read(2); seqs[1] != 2 {
		t.Fatalf("read sequences %v, want 1 and 2", seqs)
	}
	// The interval in which the durable read 1 and 2: it moved, so it is not stalled, and
	// the next interval is the first in which it is handed nothing.
	g.sample()
	// 30 published in all: the stream now holds 21..30, and 3..20 were removed while
	// this durable, whose cursor sits at 2, was not reading.
	g.publish("acme", 28)
	if first := g.streamInfo().State.FirstSeq; first != 21 {
		t.Fatalf("stream FirstSeq = %d, want 21; the ceiling did not evict as this test assumes", first)
	}
	g.sample()
	if got := g.mustSeries(gapSeries); got != 18 {
		t.Errorf("unread-gap gauge = %v, want 18 (sequences 3..20 removed ahead of a cursor at 2)", got)
	}
	g.sample()
	if got := g.mustSeries(skippedSeries); got != 0 {
		t.Errorf("unread-skipped counter = %v while the durable is stalled, want 0: its cursor "+
			"has not moved, so it has not yet crossed the removed range", got)
	}

	// Resume. The first pull steps the cursor over 3..20.
	if seqs := g.read(1); seqs[0] != 21 {
		t.Fatalf("the resumed reader was handed sequence %d, want 21 (the stream's first)", seqs[0])
	}
	g.sample()
	if got := g.mustSeries(skippedSeries); got != 18 {
		t.Errorf("unread-skipped counter = %v after the durable resumed, want 18", got)
	}
	if got := g.mustSeries(gapSeries); got != 0 {
		t.Errorf("unread-gap gauge = %v after the durable resumed past the gap, want 0", got)
	}
}

// A reader that keeps up loses nothing, and must count nothing — even though its cursor
// moves by every message published.
func TestCaughtUpDurableCountsNothing(t *testing.T) {
	g := newUnreadRig(t, 0)
	g.sample() // baseline
	for round := 0; round < 3; round++ {
		g.publish("acme", 20)
		g.read(20)
		g.sample()
	}
	if got := g.mustSeries(skippedSeries); got != 0 {
		t.Errorf("unread-skipped counter = %v for a reader that read all 60 messages, want 0", got)
	}
	if got := g.mustSeries(gapSeries); got != 0 {
		t.Errorf("unread-gap gauge = %v for a caught-up reader, want 0", got)
	}
}

// A tenant purge removes one tenant's messages from the MIDDLE of a stream. The cursor
// then steps over those holes too — but they are counted in the stream's deleted count,
// and a message the purge removed on purpose is not a message this reader failed to read
// because it fell behind.
//
// This pins the case the subtraction covers: the purge and the cursor crossing its holes
// fall in the SAME sample interval. A purge ahead of a durable that is sampled before it
// reaches the holes is counted as loss when it does — an accepted residual, documented on
// sampleDurable and named in the alert text.
func TestInteriorPurgeIsNotCountedAsLoss(t *testing.T) {
	g := newUnreadRig(t, 0)
	g.sample() // baseline

	// Interleaved, starting and ending with the tenant that stays, so every t1 message is
	// interior and none sits at the head where a removal moves FirstSeq instead.
	for i := 0; i < 10; i++ {
		g.publish("t2", 1)
		g.publish("t1", 1)
	}
	g.publish("t2", 1)

	purge := &nats.StreamPurgeRequest{Subject: TenantSubjectFilter("test", "t1", unreadSuffix)}
	if err := g.nmgr.js.PurgeStream(g.stream, purge); err != nil {
		t.Fatalf("purge t1: %v", err)
	}
	if d := g.streamInfo().State.NumDeleted; d != 10 {
		t.Fatalf("stream NumDeleted = %d after the purge, want 10 interior deletes", d)
	}

	g.read(11)
	g.sample()
	if got := g.mustSeries(skippedSeries); got != 0 {
		t.Errorf("unread-skipped counter = %v after a reader stepped over 10 purged messages, "+
			"want 0: a deliberate interior delete is not unread loss", got)
	}
}

// A durable that is deleted and recreated starts a new cursor. JetStream reports a new
// DeliverAll durable's stream cursor at the stream's first sequence minus one, not 0, and
// its consumer count from 0 — so without treating the drop as a new baseline, the jump
// in the stream cursor would be counted as loss against a consumer count that went
// backwards.
func TestRecreatedConsumerIsANewBaseline(t *testing.T) {
	g := newUnreadRig(t, 10)
	g.sample() // baseline
	g.publish("acme", 5)
	g.read(5)
	g.sample()

	if err := g.nmgr.js.DeleteConsumer(g.stream, g.durable); err != nil {
		t.Fatalf("delete durable: %v", err)
	}
	g.publish("acme", 30) // the stream now holds 26..35
	// Recreated through the reader's own self-heal path, as a live reader would.
	if err := g.reader.bind(); err != nil {
		t.Fatalf("recreate durable: %v", err)
	}
	ci, err := g.nmgr.js.ConsumerInfo(g.stream, g.durable)
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	if ci.Delivered.Stream != 25 || ci.Delivered.Consumer != 0 {
		t.Fatalf("recreated durable reports Delivered{Stream:%d, Consumer:%d}, want {25, 0}; "+
			"the premise this test pins has moved", ci.Delivered.Stream, ci.Delivered.Consumer)
	}
	g.sample()
	if got := g.mustSeries(skippedSeries); got != 0 {
		t.Errorf("unread-skipped counter = %v after the durable was recreated, want 0: a new "+
			"consumer is a new baseline", got)
	}
	g.read(10)
	g.sample()
	if got := g.mustSeries(skippedSeries); got != 0 {
		t.Errorf("unread-skipped counter = %v after the recreated durable read everything the "+
			"stream holds, want 0", got)
	}
}

// Both series exist, at zero, from the moment the reader is created. An alert built on
// increase() over a counter that appears only when it first moves cannot see that first
// move, and "no series" and "no loss" must not read the same on a dashboard.
func TestSeriesExistAtZeroBeforeAnyLoss(t *testing.T) {
	g := newUnreadRig(t, 0)
	for _, s := range []string{skippedSeries, gapSeries} {
		if got := g.mustSeries(s); got != 0 {
			t.Errorf("%s = %v before any sample, want 0", s, got)
		}
	}
}

// The stream's NumDeleted FALLS as well as rises: once DiscardOld moves the stream's head
// past the holes a purge left, they are below FirstSeq and no longer counted as interior
// deletes. That fall is not an un-delete, and it must not be charged to a durable that
// read everything as the loss of the holes it once stepped over.
func TestEvictionPastPurgedHolesIsNotCountedAsLoss(t *testing.T) {
	g := newUnreadRig(t, 100)
	g.sample() // baseline

	for i := 0; i < 10; i++ {
		g.publish("t2", 1)
		g.publish("t1", 1)
	}
	g.publish("t2", 1)
	purge := &nats.StreamPurgeRequest{Subject: TenantSubjectFilter("test", "t1", unreadSuffix)}
	if err := g.nmgr.js.PurgeStream(g.stream, purge); err != nil {
		t.Fatalf("purge t1: %v", err)
	}
	g.read(11)
	g.sample()
	if d := g.streamInfo().State.NumDeleted; d != 10 {
		t.Fatalf("stream NumDeleted = %d after the purge, want 10 interior deletes", d)
	}

	// 100 more against a 100-message ceiling evicts the 11 survivors of the purged range,
	// so the head moves past every hole. The reader reads all 100: it loses nothing.
	g.publish("acme", 100)
	if d := g.streamInfo().State.NumDeleted; d != 0 {
		t.Fatalf("stream NumDeleted = %d after the head moved past the purged range, want 0; "+
			"the premise this test pins has moved", d)
	}
	g.read(100)
	g.sample()
	if got := g.mustSeries(skippedSeries); got != 0 {
		t.Errorf("unread-skipped counter = %v for a reader that read every message the stream "+
			"still held, want 0: the stream's deleted count fell because its head moved past the "+
			"purged holes, and that is not a loss", got)
	}
}

// A pod that restarts, or a new replica, attaches to a durable that already exists and
// carries its whole history: Delivered.Stream minus Delivered.Consumer is every sequence it
// ever skipped. The new pod's first sample is a baseline, not an interval from zero —
// otherwise every restart would count that history again and raise the critical alert for
// nothing new.
func TestRestartedPodDoesNotRecountHistory(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("unread")
	first := newUnreadRigOn(t, srv, area, 10)

	// 30 against a 10-message ceiling, then a read: the durable is handed 21..30 and its
	// cursor has stepped over 1..20.
	first.publish("acme", 30)
	first.read(10)
	ci, err := first.nmgr.js.ConsumerInfo(first.stream, first.durable)
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	if ci.Delivered.Stream != 30 || ci.Delivered.Consumer != 10 {
		t.Fatalf("durable reports Delivered{Stream:%d, Consumer:%d}, want {30, 10}: it should "+
			"carry 20 historical skips into the restart", ci.Delivered.Stream, ci.Delivered.Consumer)
	}

	// The restarted pod: a new manager and registry on the same area, so the same durable.
	restarted := newUnreadRigOn(t, srv, area, 10)
	if restarted.durable != first.durable {
		t.Fatalf("restarted pod reads durable %q, want the same durable %q", restarted.durable, first.durable)
	}
	restarted.sample()
	restarted.sample()
	if got := restarted.mustSeries(skippedSeries); got != 0 {
		t.Errorf("unread-skipped counter = %v on a restarted pod that saw no new loss, want 0: "+
			"its first sample is a baseline, and the durable's 20 historical skips were not "+
			"lost on its watch", got)
	}
	if got := restarted.mustSeries(gapSeries); got != 0 {
		t.Errorf("unread-gap gauge = %v for a caught-up durable, want 0", got)
	}
}
