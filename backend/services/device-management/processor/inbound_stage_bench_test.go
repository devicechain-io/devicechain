// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	dmodel "github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

// BenchmarkInboundStageOccupancy measures WHERE an inbound event waits inside
// device-management, stage by stage, and how many events each stage holds at once.
//
// It runs the real pipeline over an embedded three-server JetStream cluster: the real
// durable reader, the real hand-off channel and resolver pool, and the real ordered writer
// publishing to resolved-events. Only the device-management API is a double (latencyApi),
// which answers each lookup after a fixed delay. The events are pre-published, so the
// pipeline runs saturated from the first fetch.
//
// # What it stamps, per event
//
//	read     the reader hands the message to the read loop          (stampingReader)
//	dequeue  a resolver makes the event's first lookup               (latencyApi)
//	submit   the outbound loop hands the resolved event to the writer (stampingWriter)
//	settle   the broker's PubAck for that publish is reported         (stampingWriter)
//	ack      the source is acknowledged                              (stampingReader)
//
// "resolve" below is dequeue→submit: the lookups, AND the wait to put the result on the
// resolved channel, AND the wait for the single outbound loop to take it. The processor's own
// resolve_duration_seconds measures the first two and not the third.
//
// # What it samples, every 10 ms
//
// The broker's ack-pending count for the inbound durable (every message fetched and not yet
// acked), and the four places inside the pod the processor can report directly: the hand-off
// channel, the busy resolvers (the processor's own resolve_inflight gauge, which counts a
// resolver from dequeue until it has handed its result on, so it never overlaps the resolved
// channel), the resolved channel, and the writer's publishes in flight. What is left over is
// the reader's fetched buffer, which nothing exports.
//
// # When it refuses to report
//
//   - An event is missing a stamp, or its stamps are out of order: an instrument missed a
//     stage, and no number from the arm is meaningful.
//   - The resolved event published for an inbound sequence does not carry that sequence's
//     device: the sequence-to-event mapping the stamps rely on does not hold.
//   - The left-over (the reader's buffer) averages below zero or above one fetch: a place
//     that holds events is missing from the decomposition.
//   - Little's law does not close: the mean ack-pending divided by the rate differs by more
//     than a quarter from the mean time an event spends between read and ack plus the time
//     the left-over implies it waited in the reader's buffer.
//
// # The control
//
// The arm with slowPublish holds each publish for that long before handing it to the writer,
// so the writer, not the resolvers, is the slowest stage. It must blame the publish side. An
// instrument that blames the resolvers whatever it is shown fails there.
//
// # The latency model
//
// The resolver's lookups for a warm measurement event are, in order: the credential (one
// uncached SELECT, when the event carries a credential — every event, under the default
// "required" device-auth mode), or the device by token (a key-value read) when it does not;
// then the profile, the tracked relationships and whether any scoped group exists (three
// key-value reads). Each is given the same fixed delay. That is a MODEL of a network round
// trip, and a sleeping resolver costs no CPU, so the arms measure how the pipeline's queues
// behave when resolution waits on the network; they do not measure what a lookup costs on a
// real server. model's BenchmarkAuthenticateDeviceConcurrency measures the SELECT on a real
// PostgreSQL.
//
// Run with (one package, one process, under a timeout — it starts three NATS servers):
//
//	go test ./processor -run '^$' -bench BenchmarkInboundStageOccupancy -benchtime 1x -p 1 -count 1
func BenchmarkInboundStageOccupancy(b *testing.B) {
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	const lookup = 750 * time.Microsecond
	arms := []stageArm{
		{auth: config.AuthModeRequired, lookup: lookup, resolvers: 5, events: 20000},
		{auth: config.AuthModeRequired, lookup: lookup, resolvers: 8, events: 20000},
		{auth: config.AuthModeRequired, lookup: lookup, resolvers: 10, events: 20000},
		{auth: config.AuthModeRequired, lookup: lookup, resolvers: 16, events: 20000},
		{auth: config.AuthModeRequired, lookup: 0, resolvers: 5, events: 20000},
		{auth: config.AuthModeRequired, lookup: 0, resolvers: 10, events: 20000},
		{auth: config.AuthModeOptional, lookup: lookup, resolvers: 5, events: 20000},
		{auth: config.AuthModeOptional, lookup: lookup, resolvers: 10, events: 20000},
		// The control: the writer is the slowest stage. Fewer events, because it is slow.
		{auth: config.AuthModeRequired, lookup: 0, resolvers: 10, slowPublish: 2 * time.Millisecond, events: 3000},
	}

	parent := b
	var cluster []*natsserver.Server
	for n, arm := range arms {
		b.Run(arm.name(), func(b *testing.B) {
			if cluster == nil {
				cluster = dctest.StartJetStreamCluster(parent, 3)
			}
			for iter := 0; iter < b.N; iter++ {
				r := runStageArm(b, cluster[0], fmt.Sprintf("stage%d-%d", n, iter), arm)
				r.report(b)
				if arm.slowPublish > 0 && r.blamed != stagePublish {
					b.Fatalf("CONTROL: the writer was held %v per publish, but the decomposition blamed %q "+
						"(mean hand-off %.1f, resolved %.1f of %d): the instrument cannot tell the stages apart",
						arm.slowPublish, r.blamed, r.occ.messages, r.occ.resolved, RESOLVED_EVENT_BACKLOG_SIZE)
				}
				if arm.auth == config.AuthModeRequired && arm.lookup == lookup && arm.resolvers == 5 &&
					(r.rate < 1200 || r.rate > 2000) {
					// The model was chosen so five resolvers reproduce the ceiling measured on a
					// kind cluster (about 1.6k events a second). Outside this band the width
					// comparison is about a different system.
					b.Errorf("CALIBRATION: five resolvers at %v per lookup resolved %.0f events/s, outside "+
						"1200..2000; the latency model does not reproduce the measured ceiling", lookup, r.rate)
				}
			}
		})
	}
}

// stageArm is one configuration of BenchmarkInboundStageOccupancy.
type stageArm struct {
	auth        string        // config.AuthModeRequired: events carry a credential; Optional: they do not
	lookup      time.Duration // the delay of every lookup resolution makes
	resolvers   int           // the resolver pool width
	slowPublish time.Duration // held before each publish reaches the writer (the control)
	events      int
}

func (a stageArm) name() string {
	n := fmt.Sprintf("auth=%s/lookup=%v/resolvers=%d", a.auth, a.lookup, a.resolvers)
	if a.slowPublish > 0 {
		n += fmt.Sprintf("/slowPublish=%v", a.slowPublish)
	}
	return n
}

// readerFetchBatch is the most messages the inbound reader fetches at once
// (core/messaging fetchBatch). The reader fetches again only once its buffer is empty, so
// its buffer holds between 0 and this many, plus the one message the read loop holds while it
// waits on a full hand-off channel.
const readerFetchBatch = 64

// Stage names a place an event can wait, for the verdict.
const (
	stagePublish   = "publish"   // the resolved channel is filling: the writer is the slowest stage
	stageResolvers = "resolvers" // the hand-off channel is filling: the resolvers are the slowest stage
	stageReader    = "reader"    // neither: events are not arriving fast enough to queue in the pod
)

// stageStamps holds each event's stamps, indexed by event (inbound sequence - 1), as
// nanoseconds since base. Zero means not stamped. The first stamp wins.
type stageStamps struct {
	base                               time.Time
	read, dequeue, submit, settle, ack []atomic.Int64
	acked                              atomic.Int64 // sources acked
	writerInflight                     atomic.Int64 // publishes handed to the writer and not yet settled
	faults                             atomic.Int64 // an index out of range, or a publish carrying the wrong device
	faultMu                            sync.Mutex
	firstFault                         string
}

func newStageStamps(events int) *stageStamps {
	return &stageStamps{
		base:    time.Now(),
		read:    make([]atomic.Int64, events),
		dequeue: make([]atomic.Int64, events),
		submit:  make([]atomic.Int64, events),
		settle:  make([]atomic.Int64, events),
		ack:     make([]atomic.Int64, events),
	}
}

func (s *stageStamps) stamp(slot []atomic.Int64, i int) bool {
	if i < 0 || i >= len(slot) {
		s.fault(fmt.Sprintf("event index %d is outside 0..%d", i, len(slot)-1))
		return false
	}
	return slot[i].CompareAndSwap(0, int64(time.Since(s.base))+1)
}

func (s *stageStamps) fault(what string) {
	if s.faults.Add(1) == 1 {
		s.faultMu.Lock()
		s.firstFault = what
		s.faultMu.Unlock()
	}
}

// indexFromSuffix parses the event index from a token or credential id "<prefix>-<i>".
func indexFromSuffix(s string) int {
	i, err := strconv.Atoi(s[strings.LastIndexByte(s, '-')+1:])
	if err != nil {
		return -1
	}
	return i
}

// latencyApi answers the lookups resolution makes for a warm measurement event, each after d,
// and stamps the event's dequeue at the first of them. Event i's credential is "cred-i" and
// its device "dev-i", so a lookup names its event.
type latencyApi struct {
	instantApi
	d  time.Duration
	st *stageStamps
}

func (a latencyApi) wait() {
	if a.d > 0 {
		time.Sleep(a.d)
	}
}

func (a latencyApi) AuthenticateDevice(_ context.Context, presented *dmodel.PresentedCredential, _ time.Time) (*dmodel.Device, error) {
	i := indexFromSuffix(presented.CredentialId)
	a.st.stamp(a.st.dequeue, i)
	a.wait()
	return &dmodel.Device{
		Model:          gorm.Model{ID: 1},
		TokenReference: rdb.TokenReference{Token: fmt.Sprintf("dev-%d", i)},
		DeviceTypeId:   7,
	}, nil
}

func (a latencyApi) DevicesByToken(ctx context.Context, tokens []string) ([]*dmodel.Device, error) {
	for _, t := range tokens {
		a.st.stamp(a.st.dequeue, indexFromSuffix(t))
	}
	a.wait()
	return a.instantApi.DevicesByToken(ctx, tokens)
}

func (a latencyApi) ProfileResolutionByDeviceType(ctx context.Context, id uint) (*dmodel.ProfileResolution, error) {
	a.wait()
	return a.instantApi.ProfileResolutionByDeviceType(ctx, id)
}

func (a latencyApi) TrackedRelationshipsForDevice(ctx context.Context, id uint) (*dmodel.EntityRelationshipSearchResults, error) {
	a.wait()
	return a.instantApi.TrackedRelationshipsForDevice(ctx, id)
}

func (a latencyApi) AnyScopedGroups(ctx context.Context) (bool, error) {
	a.wait()
	return a.instantApi.AnyScopedGroups(ctx)
}

// stampingReader stamps each message's read and, through its acknowledger, its ack. The
// message it hands on is the one it read, rebuilt around that acknowledger; the inbound
// reader is a plain one (no capacity), so the message carries no slot to lose.
type stampingReader struct {
	inner    messaging.MessageReader
	st       *stageStamps
	consumer atomic.Pointer[string]
}

func (r *stampingReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	m, err := r.inner.ReadMessage(ctx)
	if err != nil {
		return m, err
	}
	if r.consumer.Load() == nil {
		c := m.Origin().Consumer
		r.consumer.Store(&c)
	}
	i := int(m.StreamSeq) - 1
	r.st.stamp(r.st.read, i)
	out := messaging.NewConsumedMessage(m.Subject, m.Value, m.NumDelivered, m.Headers, stampedAck{m: m, st: r.st, i: i})
	out.StreamSeq = m.StreamSeq
	out.AppendTime = m.AppendTime
	return out.WithOrigin(m.Origin()), nil
}

func (r *stampingReader) HandleResponse(err error) { r.inner.HandleResponse(err) }

type stampedAck struct {
	m  messaging.Message
	st *stageStamps
	i  int
}

func (a stampedAck) Ack() error {
	if a.st.stamp(a.st.ack, a.i) {
		a.st.acked.Add(1)
	}
	return a.m.Ack()
}

// stampingWriter stamps each resolved publish's submission and settlement, and counts the
// publishes in flight. With slow set it holds each publish that long before handing it on;
// Publish is called only by the one outbound loop, so the hold serializes like a slow broker
// and keeps the submission order.
type stampingWriter struct {
	inner messaging.OrderedWriter
	st    *stageStamps
	slow  time.Duration
}

func (w *stampingWriter) Publish(ctx context.Context, msg messaging.Message, done func(error)) {
	// DedupID is resolved:<tenant>:<inbound seq>:<fan-out index> (resolvedDedupID).
	parts := strings.Split(msg.DedupID, ":")
	i := -1
	if len(parts) == 4 {
		if seq, err := strconv.Atoi(parts[2]); err == nil {
			i = seq - 1
		}
	}
	if want := fmt.Sprintf("dev-%d", i); string(msg.Key) != want {
		w.st.fault(fmt.Sprintf("the publish for inbound sequence %d carries device %q, want %q", i+1, msg.Key, want))
	}
	w.st.stamp(w.st.submit, i)
	w.st.writerInflight.Add(1)
	if w.slow > 0 {
		time.Sleep(w.slow)
	}
	w.inner.Publish(ctx, msg, func(err error) {
		w.st.stamp(w.st.settle, i)
		w.st.writerInflight.Add(-1)
		done(err)
	})
}

func (w *stampingWriter) Fail(err error, done func(error)) { w.inner.Fail(err, done) }
func (w *stampingWriter) Draining()                        { w.inner.Draining() }
func (w *stampingWriter) Close()                           { w.inner.Close() }

// occupancy is the mean number of events each place held.
type occupancy struct {
	ackPending, messages, resolvers, resolved, writer, readerBuffer float64
}

// stageResult is what one arm measured.
type stageResult struct {
	arm       stageArm
	rate      float64
	stages    map[string]stageStats
	occ       occupancy // over the samples taken while anything was pending
	littleMs  float64   // mean ack-pending (all samples) / rate
	inPodMs   float64   // mean read->ack + the reader-buffer wait the left-over implies
	reorderMs stageStats
	blamed    string
}

type stageStats struct{ mean, p50, p99, max float64 } // milliseconds

func statsOf(ms []float64) stageStats {
	if len(ms) == 0 {
		return stageStats{}
	}
	sort.Float64s(ms)
	sum := 0.0
	for _, v := range ms {
		sum += v
	}
	q := func(p float64) float64 { return ms[int(math.Ceil(p*float64(len(ms))))-1] }
	return stageStats{mean: sum / float64(len(ms)), p50: q(0.5), p99: q(0.99), max: ms[len(ms)-1]}
}

// stageOrder names the stamped intervals, in pipeline order, for reporting.
var stageOrder = []string{"handoff", "resolve", "publish", "ack", "inpod"}

func (r stageResult) report(b *testing.B) {
	b.ReportMetric(r.rate, "ev/s")
	for _, name := range stageOrder {
		s := r.stages[name]
		b.ReportMetric(s.mean, name+"-mean-ms")
		b.ReportMetric(s.p99, name+"-p99-ms")
	}
	b.ReportMetric(r.occ.ackPending, "ackpending-mean")
	b.ReportMetric(r.occ.readerBuffer, "readerbuf-mean")
	b.ReportMetric(r.occ.messages, "handoff-mean")
	b.ReportMetric(r.occ.resolvers, "resolvers-mean")
	b.ReportMetric(r.occ.resolved, "resolved-mean")
	b.ReportMetric(r.occ.writer, "writer-mean")
	b.ReportMetric(r.littleMs, "L/λ-ms")
	b.ReportMetric(r.reorderMs.p99, "reorder-p99-ms")
	b.ReportMetric(r.reorderMs.max, "reorder-max-ms")
	line := fmt.Sprintf("%s: %.0f ev/s, blamed %s | in pod (ack-pending %.1f): reader buffer %.1f, hand-off %.1f, "+
		"resolvers %.1f, resolved %.1f, writer %.1f | L/λ %.1f ms vs stamped %.1f ms |",
		r.arm.name(), r.rate, r.blamed, r.occ.ackPending, r.occ.readerBuffer, r.occ.messages, r.occ.resolvers,
		r.occ.resolved, r.occ.writer, r.littleMs, r.inPodMs)
	for _, name := range stageOrder {
		s := r.stages[name]
		line += fmt.Sprintf(" %s %.2f/%.2f/%.2f", name, s.mean, s.p50, s.p99)
	}
	line += fmt.Sprintf(" | reorder p99 %.2f max %.2f ms", r.reorderMs.p99, r.reorderMs.max)
	b.Log(line)
}

// runStageArm runs one arm to completion and returns its measurements, refusing (b.Fatalf)
// when its instruments do not add up.
func runStageArm(b *testing.B, srv *natsserver.Server, instance string, arm stageArm) stageResult {
	b.Helper()
	b.StopTimer()
	st := newStageStamps(arm.events)
	reg := prometheus.NewRegistry()
	reader := &stampingReader{st: st}
	p := startResolvePipeline(b, pipelineSpec{
		srv: srv, instance: instance, replicas: 3, resolvers: arm.resolvers, window: PUBLISH_WINDOW,
		events: arm.events, api: latencyApi{d: arm.lookup, st: st}, authMode: arm.auth, registry: reg,
		event: func(i int) *esmodel.UnresolvedEvent {
			e := benchMeasurement(i)
			if arm.auth == config.AuthModeRequired {
				ctype, cid := string(dmodel.CredentialAccessToken), fmt.Sprintf("cred-%d", i)
				e.CredentialType, e.CredentialId = &ctype, &cid
			}
			return e
		},
		wrapReader: func(r messaging.MessageReader) messaging.MessageReader {
			reader.inner = r
			return reader
		},
		wrapResolved: func(w messaging.OrderedWriter) messaging.OrderedWriter {
			return &stampingWriter{inner: w, st: st, slow: arm.slowPublish}
		},
	})
	info, err := p.js.StreamInfo(p.inboundStream)
	if err != nil {
		b.Fatal(err)
	}
	if info.State.FirstSeq != 1 || info.State.Msgs != uint64(arm.events) {
		b.Fatalf("inbound stream holds %d from sequence %d, want %d from 1: event i is not at sequence i+1",
			info.State.Msgs, info.State.FirstSeq, arm.events)
	}
	iproc := p.iproc

	var samples []occupancy
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			consumer := reader.consumer.Load()
			if consumer == nil {
				continue // nothing fetched yet
			}
			ci, err := p.js.ConsumerInfo(p.inboundStream, *consumer)
			if err != nil {
				continue
			}
			o := occupancy{
				ackPending: float64(ci.NumAckPending),
				messages:   float64(len(iproc.messages)),
				resolvers:  gaugeValue(reg, "devicechain_devicemanagement_resolve_inflight"),
				resolved:   float64(len(iproc.resolved)),
				writer:     float64(st.writerInflight.Load()),
			}
			o.readerBuffer = o.ackPending - o.messages - o.resolvers - o.resolved - o.writer
			samples = append(samples, o)
		}
	}()

	b.StartTimer()
	started := time.Since(st.base)
	if err := iproc.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for st.acked.Load() < int64(arm.events) {
		if time.Now().After(deadline) {
			b.Fatalf("only %d of %d sources were acked", st.acked.Load(), arm.events)
		}
		time.Sleep(2 * time.Millisecond)
	}
	b.StopTimer()
	close(stop)
	<-sampled
	_ = iproc.Stop(context.Background())

	if n := st.faults.Load(); n > 0 {
		b.Fatalf("%d instrument faults; the first: %s", n, st.firstFault)
	}
	return decompose(b, arm, st, started, samples)
}

// decompose turns an arm's stamps and samples into its result, refusing when they do not add up.
func decompose(b *testing.B, arm stageArm, st *stageStamps, started time.Duration, samples []occupancy) stageResult {
	b.Helper()
	intervals := map[string][]float64{}
	missing := map[string]int{}
	var last int64
	for i := 0; i < arm.events; i++ {
		t := map[string]int64{"read": st.read[i].Load(), "dequeue": st.dequeue[i].Load(),
			"submit": st.submit[i].Load(), "settle": st.settle[i].Load(), "ack": st.ack[i].Load()}
		complete := true
		for name, v := range t {
			if v == 0 {
				missing[name]++
				complete = false
			}
		}
		if !complete {
			continue
		}
		steps := []struct {
			name     string
			from, to int64
		}{
			{"handoff", t["read"], t["dequeue"]},
			{"resolve", t["dequeue"], t["submit"]},
			{"publish", t["submit"], t["settle"]},
			{"ack", t["settle"], t["ack"]},
			{"inpod", t["read"], t["ack"]},
		}
		for _, s := range steps {
			if s.to < s.from {
				b.Fatalf("event %d: stage %s ends %v before it starts; the stamps are not in pipeline order",
					i, s.name, time.Duration(s.from-s.to))
			}
			intervals[s.name] = append(intervals[s.name], float64(s.to-s.from)/1e6)
		}
		if t["ack"] > last {
			last = t["ack"]
		}
	}
	if len(missing) > 0 {
		b.Fatalf("stamps missing, by stage: %v; an instrument missed a stage and no number from this arm means anything", missing)
	}

	r := stageResult{arm: arm, stages: map[string]stageStats{}}
	for name, v := range intervals {
		r.stages[name] = statsOf(v)
	}
	r.rate = float64(arm.events) / (time.Duration(last) - started).Seconds()

	// Occupancy over the samples where anything was pending; the Little's-law mean over all.
	var busy, all occupancy
	nBusy := 0
	for _, o := range samples {
		all.ackPending += o.ackPending
		all.readerBuffer += o.readerBuffer
		if o.ackPending > 0 {
			nBusy++
			busy.ackPending += o.ackPending
			busy.messages += o.messages
			busy.resolvers += o.resolvers
			busy.resolved += o.resolved
			busy.writer += o.writer
			busy.readerBuffer += o.readerBuffer
		}
	}
	if nBusy < 10 {
		b.Fatalf("only %d samples saw anything pending; the arm ran too briefly to be sampled", nBusy)
	}
	n := float64(nBusy)
	if math.IsNaN(busy.resolvers) {
		b.Fatal("the resolve_inflight gauge was not found on the arm's registry; busy resolvers cannot be counted")
	}
	r.occ = occupancy{ackPending: busy.ackPending / n, messages: busy.messages / n, resolvers: busy.resolvers / n,
		resolved: busy.resolved / n, writer: busy.writer / n, readerBuffer: busy.readerBuffer / n}

	// The left-over is the reader's buffer. It cannot be negative and cannot exceed one
	// fetch plus the message the read loop holds; the sampler reads the broker's count and
	// the pod's a round trip apart, and the broker learns of an ack a round trip late, so a
	// few either way is sampling noise, not a missing place.
	const tolerance = 5
	if r.occ.readerBuffer < -tolerance || r.occ.readerBuffer > readerFetchBatch+1+tolerance {
		b.Fatalf("the left-over averaged %.1f, outside 0..%d: a place that holds events is missing from the "+
			"decomposition (ack-pending %.1f = hand-off %.1f + resolvers %.1f + resolved %.1f + writer %.1f + left-over)",
			r.occ.readerBuffer, readerFetchBatch+1, r.occ.ackPending, r.occ.messages, r.occ.resolvers,
			r.occ.resolved, r.occ.writer)
	}
	nAll := float64(len(samples))
	r.littleMs = (all.ackPending / nAll) / r.rate * 1e3
	r.inPodMs = r.stages["inpod"].mean + (all.readerBuffer/nAll)/r.rate*1e3
	if diff := math.Abs(r.littleMs-r.inPodMs) / r.inPodMs; diff > 0.25 {
		b.Fatalf("Little's law does not close: mean ack-pending / rate = %.1f ms, stamped residence = %.1f ms "+
			"(%.0f%% apart)", r.littleMs, r.inPodMs, diff*100)
	}

	// Reorder: how long after a LATER inbound event each event reached the writer.
	var lateMs []float64
	minLater := int64(math.MaxInt64)
	for i := arm.events - 1; i >= 0; i-- {
		s := st.submit[i].Load()
		if s > minLater {
			lateMs = append(lateMs, float64(s-minLater)/1e6)
		} else {
			lateMs = append(lateMs, 0)
			minLater = s
		}
	}
	r.reorderMs = statsOf(lateMs)

	// The verdict: the slowest stage is the one just after the furthest-downstream queue that
	// is filling.
	switch {
	case r.occ.resolved >= 0.5*RESOLVED_EVENT_BACKLOG_SIZE:
		r.blamed = stagePublish
	case r.occ.messages >= 0.5*MESSAGE_BACKLOG_SIZE:
		r.blamed = stageResolvers
	default:
		r.blamed = stageReader
	}
	return r
}

// gaugeValue reads one unlabelled gauge from reg, or NaN when it is not there.
func gaugeValue(reg *prometheus.Registry, name string) float64 {
	mfs, err := reg.Gather()
	if err != nil {
		return math.NaN()
	}
	for _, mf := range mfs {
		if mf.GetName() == name && len(mf.GetMetric()) == 1 {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	return math.NaN()
}
