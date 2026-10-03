// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	// streamMetricsSampleInterval is how often each stream's fill is sampled. The
	// stream ceilings (ADR-023) evict the oldest messages via DiscardOld when a
	// stream fills, which is otherwise silent; this sampling surfaces the fill, and each
	// reader durable's unread backlog against it, as gauges so an operator sees a
	// backlog building BEFORE data is dropped. A 30s cadence is cheap (one StreamInfo
	// per stream, one ConsumerInfo per reader durable) and fast enough to catch a backlog well before
	// the 7-day/size window closes.
	streamMetricsSampleInterval = 30 * time.Second

	// streamNearFullThreshold is the fill fraction (bytes or messages, whichever is
	// higher) at which a stream is logged as near-full.
	streamNearFullThreshold = 0.8

	// nearFullMsg and nearFullSinkMsg are the edge-triggered near-full lines (see sample).
	nearFullMsg = "JetStream stream is near its size ceiling; once full it discards its oldest " +
		"messages, read or not (jetstream_consumer_unread_ratio says whether a consumer has not read them)"
	nearFullSinkMsg = "JetStream stream that holds records for an operator is near its size " +
		"ceiling; once full it discards its oldest records (DiscardOld), which no service processes"
)

// streamMetrics exposes per-stream JetStream fill as Prometheus gauges and logs an
// edge-triggered line when a stream nears its ceiling (a warning for a declared sink,
// an info for any other stream; see sample). Stream names are a small,
// known, platform-controlled set (one per suffix), so the {stream} label is bounded
// — unlike a tenant-derived label, it is not a cardinality risk.
type streamMetrics struct {
	usedBytes  *prometheus.GaugeVec
	limitBytes *prometheus.GaugeVec
	usedMsgs   *prometheus.GaugeVec
	limitMsgs  *prometheus.GaugeVec

	// streamSink is 1 for a stream declared as holding records for an operator rather than
	// work for a service (core/streams: DeadLetterKind NotLettered — the failed-message
	// records, the dead letters and the max-delivery capture), and 0 for every other stream,
	// set beside the fill gauges above. It exists for the one alert that may read
	// a stream's FILL as a warning of loss, JetStreamStreamNearFull: every stream keeps a
	// week of history, so a stream services read sits at its ceiling full of messages they
	// have already read, and its fill says nothing. A sink's fill is different: nothing
	// processes what it holds, so near its ceiling it is about to discard records nobody
	// has looked at.
	//
	// 🔑 A DECLARATION, NOT A LIST. It is read from the stream's declared DeadLetterKind, the
	// field that already says "this stream is itself a sink, or a report", so a new sink is
	// covered by the declaration that has to be written for it anyway, and a stream whose
	// readers are merely not deployed (resolved-events on an ingest-only install) is never
	// mistaken for one.
	streamSink *prometheus.GaugeVec

	// The replication triple (ADR-020 A0). These exist because every other check
	// that an instance is highly available is made at INSTALL time — a rendered
	// value, a tofu plan, a preflight — and install-time checks go stale by
	// construction. That staleness is the entire bug A0 closes: replication used to
	// be applied only at stream creation, so a cluster could carry a correct-looking
	// config and single-replica streams indefinitely with nothing to say so.
	//
	// An HA claim is therefore asserted from BROKER STATE, never from config.
	// desired is what the operator asked for; actual is what JetStream reports; and
	// peersCurrent is the one that survives contact with reality — a stream can
	// report Replicas:3 while two of its peers are stale or offline, which is not
	// replication, it is a label. Alert on desired != actual, and on
	// peersCurrent < actual.
	replicasDesired *prometheus.GaugeVec
	replicasActual  *prometheus.GaugeVec
	peersCurrent    *prometheus.GaugeVec

	// brokerClustered is the fourth number, and without it the other three cannot
	// see the very state A0 exists to close.
	//
	// The triple above is (config, stream state, peer health). All three read 1 on a
	// healthy single-node install — and all three ALSO read 1 on a 3-node RAFT
	// cluster whose streams were every one of them created single-replica. Those two
	// worlds are bit-identical in the metrics, and they are the two worlds A0 is
	// about: one is correct and cheap, the other pays for three servers, three
	// volumes and a quorum round-trip to store exactly one copy of everything and
	// survive exactly zero node losses.
	//
	// Nothing else catches it either. The clamp only degrades a factor the broker
	// cannot satisfy, so it is silent in this direction; dcctl's preflight only
	// asserts servers >= replicas, which holds; and the shipped chart default is
	// streamReplicas: 1, so this is what a `tofu apply -var ha=true` followed by a
	// plain `helm install` produces — the supported direct-use path.
	//
	// So the broker's own topology has to be exported, not just the streams'. 1 when
	// the connected server reports a cluster name, 0 otherwise. Clustered AND
	// desired == 1 is the false-HA state, stated as an alertable fact.
	brokerClustered prometheus.Gauge

	// heldPastAckWait counts messages a CAPACITY READER held past their AckWait, by durable
	// and by where the message was when the clock ran out (capacity.go): stage=worker means a
	// handler was still working on a message the broker had already redelivered, so it may be
	// handled twice; stage=buffer means the reader dropped a message it had fetched but not
	// yet handed out. Each pod counts its own messages, so the alert sums across pods.
	heldPastAckWait *prometheus.CounterVec

	// maxDeliveryRecords counts what the max-delivery recorder did with each advisory it
	// handled, by the ORIGINAL message's stream and the outcome (recorder.go). Every replica
	// of an area shares one recorder durable, so each advisory is counted by exactly one pod.
	maxDeliveryRecords *prometheus.CounterVec

	// connectionDead counts the broker connections this service gave up because they had
	// died WITHOUT being closed (broker_liveness.go), by what detected it: ping, the server
	// left too many pings unanswered; write, a write made no progress for BrokerWriteTimeout.
	// Each pod counts its own connection. Both series are created at 0 with the metrics.
	//
	// connectionDeadLast is when the last one was counted, in Unix seconds (0: never), and it
	// is the one the alert reads. A counter's increase() misses an event whose first sample
	// Prometheus never scraped, and the incident this exists for is a lost NODE, which is
	// where Prometheus itself may have been running; a timestamp says "within the last 15
	// minutes" from any one sample taken in that window.
	connectionDead     *prometheus.CounterVec
	connectionDeadLast prometheus.Gauge

	// warned tracks whether a stream is currently above the near-full threshold, so
	// the near-full line fires once on the way up (and an info once on the way back down)
	// rather than every sample. Accessed only from the single sampler goroutine.
	warned map[string]bool

	// Unread loss, per durable. A full stream discards its OLDEST message (DiscardOld),
	// and the fill gauges above say only that the stream is full — not whether any
	// reader was still behind the message that went. A reader that had not reached it
	// never sees it: its cursor steps over the hole on the next pull and nothing anywhere
	// records that a message went unread. These two instruments are that record.
	//
	// unreadSkipped counts, between consecutive samples, the stream sequences a durable's
	// cursor moved past without a delivery, less the growth in the stream's interior
	// deletes over the same interval (a tenant purge removing one tenant's messages from
	// the middle of the stream is not a reader falling behind). That subtraction only
	// cancels a purge whose holes the cursor crosses in the SAME interval as the purge; a
	// purge ahead of a durable that is sampled before it reaches the holes is counted as
	// loss when it does. See sampleDurable for the arithmetic and why it is a lower bound.
	//
	// unreadGap is the stalled case the counter cannot see yet: a durable that is not
	// pulling does not move its cursor, so it crosses no hole — but the messages ahead of
	// it are already gone. It is FirstSeq-1 minus the cursor, reported ONLY for a durable
	// that was handed nothing since the previous sample, and 0 otherwise. The condition is
	// what makes it mean "stalled" rather than "behind": a live reader slower than its
	// producer also has messages evicted ahead of its cursor between its pulls, and that
	// loss is the counter's to report, not a stall. It reads 0 again as soon as the
	// durable is handed a message.
	//
	// The {durable} label is as bounded as {stream}: one durable per reader a service
	// constructs, named from the instance, area and suffix — never from a tenant.
	unreadSkipped *prometheus.CounterVec
	unreadGap     *prometheus.GaugeVec

	// consumerPending / consumerAckPending are each reader durable's backlog, from the
	// ConsumerInfo sampleDurable already fetches: messages in the stream not yet handed to
	// the durable (NumPending), and messages handed out but not yet acknowledged
	// (NumAckPending). Together they say how far behind its stream a consumer is, which the
	// unread pair cannot: a consumer can lose nothing and still be an hour behind.
	//
	// 🔴 A PRESENT SERIES IS A MEASUREMENT, AND ONLY A MEASUREMENT. So, unlike the unread
	// pair, they are NOT created at 0 — a zero there is a claim ("no loss yet") that is true
	// from the moment the reader exists, while a zero here would claim "no backlog" for the
	// 30 s before the first sample, which is exactly when a restarted pod attaching to a large
	// backlog is reporting. And they are WITHDRAWN when a sample fails (the durable or its
	// stream could not be read) rather than left at the last value: a gauge frozen at an old
	// small number reads as a current one, and the backlog alert would be silent over a
	// consumer nobody is measuring. Absent means "not measured", never "nothing waiting".
	consumerPending    *prometheus.GaugeVec
	consumerAckPending *prometheus.GaugeVec

	// consumerUnreadRatio is each reader durable's unread backlog (pending plus
	// unacknowledged) over what its stream can hold, computed by unreadRatio — the SAME
	// function the backpressure gate decides on, so the warning and the refusal can never
	// disagree about what "unread" means. It exists because a stream's fill cannot say it:
	// every stream is Limits retention with a week of history, so a busy one sits near its
	// ceiling full of messages every consumer has ALREADY read, and that loses nothing.
	// What loses something is a consumer whose UNREAD backlog reaches the ceiling, because
	// DiscardOld then evicts messages it has not reached.
	//
	// NOT exported for a durable that gates its stream (streams.Stream.BackpressureReaders):
	// for those the stream refuses writers before it discards, and the writers already
	// export the same quantity as jetstream_backpressure_unread_ratio, which is what the
	// refusal is decided on. Absent there means "measured elsewhere", and the two alerts
	// stay disjoint by construction instead of by a PromQL exclusion.
	//
	// Absent until the first sample and withdrawn with the backlog pair (forgetBacklog), for
	// the reason given on consumerPending.
	consumerUnreadRatio *prometheus.GaugeVec

	// durables holds each durable's previous sample, which the counter is the difference
	// against. Accessed only from the single sampler goroutine, like warned.
	durables map[durableRef]durableSample

	// publishLatency is how long each JetStream publish took, by the stream suffix it went
	// to and by the writer's mode. Both labels are bounded: suffixes are the platform's
	// declared set (ensureStream refuses anything else) and mode has two values. It carries
	// no tenant and no subject.
	//
	// 🔑 THE TWO MODES MEASURE DIFFERENT THINGS, which is why mode is a label rather than
	// being folded away. publishModeSync is one request's round trip. publishModePipelined
	// is from the send to the moment the outcome is acted on, which is after every publish
	// submitted before it has settled, and after any failure backoff one of them started — so
	// a slow, timed-out or failed publish at the head of the window lifts the samples of
	// everything queued behind it. Comparing the two is
	// comparing a latency with a latency-plus-queueing.
	publishLatency *prometheus.HistogramVec

	// The key-value caches (cache.go), by the cache's name in the kv inventory — a
	// platform-declared set of a handful, never a tenant. They answer the question a cache
	// that has stopped answering raises: is it being skipped (cacheUnavailable), since when
	// and why (cacheFailures), what that is costing the database (cacheBypassed), and how
	// long the operations that did run took (cacheLatency).
	cacheLatency     *prometheus.HistogramVec
	cacheFailures    *prometheus.CounterVec
	cacheBypassed    *prometheus.CounterVec
	cacheUnavailable *prometheus.GaugeVec

	// Backpressure (backpressure.go). unreadRatio is each GATING durable's unread backlog over
	// its stream's capacity, as the writers of that stream measure it. It is not a copy of
	// consumerPending: that series is exported by the service that READS the durable, and
	// only for its own readers; this one is exported by every service that WRITES the stream,
	// for the durables that gate it, which is what the refusal is decided on. Absent while
	// not measured, like consumerPending. historyRunway is the runway rule's quantity for the
	// same durables, measured by the same writers and withdrawn with unreadRatio.
	//
	// publishRefused counts messages a writer refused under backpressure, by stream, created
	// at 0 when a writer on a gated stream is built. engaged is the scrape-time gauge of each
	// gate (engagedCollector); nil on a microservice with no registry.
	unreadRatio    *prometheus.GaugeVec
	historyRunway  *prometheus.GaugeVec
	publishRefused *prometheus.CounterVec
	engaged        *engagedCollector

	// The in-process tier in front of each cache (cache_local.go), by the same cache name.
	// They exist only for a cache whose in-process tier is on: an absent series means the
	// tier is off, not that nothing was counted. cacheLocalLookups says how many reads never
	// reached the bucket (hit) and how many went on to it (miss), which is what
	// cacheLatency{op="get"} now counts: only the reads memory could not answer.
	cacheLocalLookups   *prometheus.CounterVec
	cacheLocalEvictions *prometheus.CounterVec
	cacheLocalEntries   *prometheus.GaugeVec
	cacheLocalBytes     *prometheus.GaugeVec
	// The bounds each in-process copy was built with, so its fill can be read against them.
	cacheLocalMaxEntries *prometheus.GaugeVec
	cacheLocalMaxBytes   *prometheus.GaugeVec

	// cacheEvictBroadcasts counts the messages of each EvictionBroadcast (cache_evict.go),
	// by cache and result.
	cacheEvictBroadcasts *prometheus.CounterVec
}

// cacheBuckets spans a loopback KV answer to the Get/Set budget. Nothing lands above it
// except a Delete, whose budget is longer (see cacheDeleteTimeout).
var cacheBuckets = []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1}

const (
	// publishModeSync is a MessageWriter publish: one request, waited on.
	publishModeSync = "sync"
	// publishModePipelined is an OrderedWriter publish: one of a window in flight at once,
	// settled in submission order.
	publishModePipelined = "pipelined"
)

// publishBuckets spans a loopback PubAck (well under a millisecond) through a replicated
// stream's quorum commit to the publish ceiling. The top finite bucket IS publishWait, so
// for publishModeSync the count above it is the publishes that ran into the ceiling. NOT for
// publishModePipelined: its sample also holds the settlement of every earlier publish and
// the failure backoff after one (see publishLatency), so a publish the broker acknowledged at
// once can be counted above it.
var publishBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

// durableRef names one durable consumer on one stream: a reader this service created.
type durableRef struct {
	stream, durable string
	// gates is set when this durable's unread backlog makes its stream refuse writers
	// (gatesItsStream); its unread ratio is then the writers' to export, not the reader's.
	gates bool
}

// durableSample is what the unread counter differences between two samples: the
// durable's cursor (the last stream sequence it was handed), how many deliveries it has
// made, and the stream's interior-delete count at the same moment.
type durableSample struct {
	deliveredStream   uint64
	deliveredConsumer uint64
	numDeleted        uint64
}

func newStreamMetrics(ms *core.Microservice) *streamMetrics {
	m := &streamMetrics{
		usedBytes:  ms.NewGaugeVec("jetstream_stream_used_bytes", "Current on-disk bytes stored in a JetStream stream.", []string{"stream"}),
		limitBytes: ms.NewGaugeVec("jetstream_stream_limit_bytes", "Configured MaxBytes ceiling for a JetStream stream.", []string{"stream"}),
		usedMsgs:   ms.NewGaugeVec("jetstream_stream_used_messages", "Current message count in a JetStream stream.", []string{"stream"}),
		limitMsgs:  ms.NewGaugeVec("jetstream_stream_limit_messages", "Configured MaxMsgs ceiling for a JetStream stream.", []string{"stream"}),
		streamSink: ms.NewGaugeVec("jetstream_stream_sink",
			"1 for a stream that holds records for an operator rather than messages for a service to process "+
				"(failed-message records, dead letters, max-delivery notices), 0 for any other stream.",
			[]string{"stream"}),
		replicasDesired: ms.NewGaugeVec("jetstream_replicas_desired",
			"Replica count this instance is configured for (instance.config.infrastructure.nats.streamReplicas).",
			[]string{"stream"}),
		replicasActual: ms.NewGaugeVec("jetstream_replicas_actual",
			"Replica count JetStream reports for this stream or KV bucket.", []string{"stream"}),
		peersCurrent: ms.NewGaugeVec("jetstream_peers_current",
			"RAFT peers currently caught up and online for this stream or KV bucket, including the leader. "+
				"Below jetstream_replicas_actual means the stream is labelled replicated but is not.",
			[]string{"stream"}),
		brokerClustered: ms.NewGauge("jetstream_broker_clustered",
			"1 when the connected NATS server reports a cluster, 0 otherwise. Paired with "+
				"jetstream_replicas_desired == 1 this is the false-HA state: a replicated broker "+
				"storing one copy of everything."),
		unreadSkipped: ms.NewCounterVec("jetstream_consumer_unread_skipped_total",
			"Stream sequences this durable's cursor moved past without a delivery: messages removed "+
				"before it read them. A lower bound (redeliveries and deletes behind the cursor reduce it).",
			[]string{"stream", "durable"}),
		unreadGap: ms.NewGaugeVec("jetstream_consumer_unread_gap_messages",
			"Messages removed ahead of this durable's cursor, reported while the durable has been handed "+
				"nothing since the previous sample (0 while it is reading).",
			[]string{"stream", "durable"}),
		consumerPending: ms.NewGaugeVec("jetstream_consumer_pending_messages",
			"Messages in the stream this durable has not been handed yet, as of the last 30 s sample. "+
				"Absent until the first sample, and while the durable cannot be read.",
			[]string{"stream", "durable"}),
		consumerAckPending: ms.NewGaugeVec("jetstream_consumer_ack_pending_messages",
			"Messages handed to this durable and not yet acknowledged, as of the last 30 s sample. "+
				"Absent until the first sample, and while the durable cannot be read.",
			[]string{"stream", "durable"}),
		consumerUnreadRatio: ms.NewGaugeVec("jetstream_consumer_unread_ratio",
			"This durable's unread backlog (pending plus unacknowledged) over what its stream can hold, in "+
				"messages or bytes, whichever limit is tighter, as of the last 30 s sample. At 1 the stream "+
				"discards messages it has not read. Not exported for a durable whose backlog makes the stream "+
				"refuse writers instead (see jetstream_backpressure_unread_ratio). Absent until the first "+
				"sample, and while the durable cannot be read.",
			[]string{"stream", "durable"}),
		heldPastAckWait: ms.NewCounterVec("reader_held_past_ack_wait_total",
			"Messages a capacity-bounded reader held past their acknowledgement window, so the broker "+
				"redelivered them. stage=worker: a handler was still working on one; stage=buffer: the "+
				"reader dropped one it had fetched but not yet handed out.",
			[]string{"durable", "stage"}),
		maxDeliveryRecords: ms.NewCounterVec("max_delivery_records_total",
			"Messages whose every delivery ran out, as recorded from the broker's max-delivery advisory, "+
				"by the original's stream and what was done: lettered (a dead letter was written), gone "+
				"(the stream no longer held it), unattributable (no tenant), tenant-deleted, not-lettered "+
				"(a dead-letter reader's own give-up: counted as lost), lost (the letter could not be "+
				"written), malformed (not a max-delivery advisory for one of this service's durables), "+
				"replay-covered (not lettered: this service re-reads the stream from its own checkpoint, "+
				"so the message is not lost, but that checkpoint has been failing).",
			[]string{"stream", "outcome"}),
		connectionDead: ms.NewCounterVec("nats_connection_dead_total",
			fmt.Sprintf("Times this service gave up a connection to the message broker that had died without "+
				"being closed, and reconnected: detected_by=ping, the server left %d pings sent %s apart "+
				"unanswered; detected_by=write, a write made no progress for %s. A lost or restarted node, a "+
				"network partition, or a server that stopped reading.",
				BrokerMaxPingsOutstanding, BrokerPingInterval, BrokerWriteTimeout),
			[]string{"detected_by"}),
		connectionDeadLast: ms.NewGauge("nats_connection_dead_last_timestamp_seconds",
			"When this service last gave up a broker connection that had died without being closed "+
				"(see nats_connection_dead_total), in Unix seconds; 0 if it has not since it started."),
		publishLatency: ms.NewHistogramVec("jetstream_publish_duration_seconds",
			"Time from sending a JetStream publish to acting on its acknowledgement or failure, by stream "+
				"suffix and mode. mode=sync is one request's round trip; mode=pipelined includes any wait "+
				"behind an earlier publish still in flight. A publish the broker never answered is counted "+
				"at the 5 s ceiling.",
			[]string{"suffix", "mode"}, publishBuckets),
		cacheLatency: ms.NewHistogramVec("kv_cache_request_duration_seconds",
			"Time a key-value cache operation took at the bucket. A get or set is cut off at 0.5 s; a delete at "+
				"5 s. A get answered from process memory never reaches the bucket and is not counted here.",
			[]string{"cache", "op"}, cacheBuckets),
		cacheFailures: ms.NewCounterVec("kv_cache_failures_total",
			"Key-value cache operations that timed out (reason=timeout) or failed (reason=error). A timeout, "+
				"or a cache nobody answers for, opens the cache's 5 s bypass.",
			[]string{"cache", "op", "reason"}),
		cacheBypassed: ms.NewCounterVec("kv_cache_bypassed_total",
			"Key-value cache reads and writes skipped because the cache was bypassed; they went to the database.",
			[]string{"cache", "op"}),
		cacheUnavailable: ms.NewGaugeVec("kv_cache_unavailable",
			"1 while the key-value cache is bypassed after an operation timed out or could not reach it.",
			[]string{"cache"}),
		unreadRatio: ms.NewGaugeVec("jetstream_backpressure_unread_ratio",
			"A gating consumer's unread backlog (pending plus unacknowledged) over what its stream can hold, "+
				"as this service measures it before writing to the stream. At 0.9 the stream refuses new "+
				"messages until it is below 0.8. Absent while not measured.",
			[]string{"stream", "durable"}),
		historyRunway: ms.NewGaugeVec("jetstream_backpressure_history_runway_seconds",
			"How long the messages a gating consumer has already read, still held ahead of its unread ones, "+
				"would last at the rate the full stream has recently been discarding them, as this service "+
				"measures it before writing to the stream. +Inf while the stream is not at its ceiling, is "+
				"discarding none of them, or the consumer has read everything. Below 30 the stream refuses new "+
				"messages until it is 60 or more, or the consumer has read everything. Absent while not measured.",
			[]string{"stream", "durable"}),
		publishRefused: ms.NewCounterVec("jetstream_publish_refused_total",
			"Messages this service did not publish because the stream was applying backpressure.",
			[]string{"stream"}),
		engaged: newEngagedCollector(ms),
		cacheLocalLookups: ms.NewCounterVec("kv_cache_local_lookups_total",
			"Key-value cache reads answered from process memory (result=hit) or passed on to the bucket "+
				"(result=miss). A value is kept in memory for up to 5 s, never longer than the cache's TTL.",
			[]string{"cache", "result"}),
		cacheLocalEvictions: ms.NewCounterVec("kv_cache_local_evictions_total",
			"Entries removed from a key-value cache's in-process copy: reason=expired (found past its "+
				"time in memory), capacity (the copy was full), deleted (the entry was evicted on a change).",
			[]string{"cache", "reason"}),
		cacheLocalEntries: ms.NewGaugeVec("kv_cache_local_entries",
			"Entries held in a key-value cache's in-process copy, including expired ones not yet removed "+
				"by a lookup or a newer store.",
			[]string{"cache"}),
		cacheLocalBytes: ms.NewGaugeVec("kv_cache_local_bytes",
			"Approximate bytes held in a key-value cache's in-process copy (keys, values and a fixed "+
				"per-entry overhead), including expired entries not yet removed.",
			[]string{"cache"}),
		cacheLocalMaxEntries: ms.NewGaugeVec("kv_cache_local_max_entries",
			"The most entries a key-value cache's in-process copy holds before it drops the least recently used.",
			[]string{"cache"}),
		cacheLocalMaxBytes: ms.NewGaugeVec("kv_cache_local_max_bytes",
			"The most bytes, counted as kv_cache_local_bytes counts them, that a key-value cache's in-process "+
				"copy holds before it drops the least recently used.",
			[]string{"cache"}),
		cacheEvictBroadcasts: ms.NewCounterVec("cache_eviction_broadcasts_total",
			"In-process cache eviction messages this replica broadcast (result=published, or publish_failed: "+
				"other replicas then drop the entries only when they expire) or received from the broker "+
				"(result=received, or malformed: dropped unread).",
			[]string{"cache", "result"}),
		warned:   map[string]bool{},
		durables: map[durableRef]durableSample{},
	}
	// Created at 0 with the metrics, before any connection, for the reason initDurable gives.
	m.connectionDead.WithLabelValues(deadByPing).Add(0)
	m.connectionDead.WithLabelValues(deadByWrite).Add(0)
	return m
}

// The detected_by values of nats_connection_dead_total.
const (
	deadByPing  = "ping"
	deadByWrite = "write"
)

// countDeadConnection records one broker connection given up for having died without being
// closed. A no-op on a manager with no metrics (one assembled by hand in a unit test).
func (m *streamMetrics) countDeadConnection(by string) {
	if m == nil || m.connectionDead == nil {
		return
	}
	m.connectionDead.WithLabelValues(by).Inc()
	m.connectionDeadLast.SetToCurrentTime()
}

// evictionBroadcastInit creates one EvictionBroadcast series at 0.
func (m *streamMetrics) evictionBroadcastInit(cache, result string) {
	if m == nil || m.cacheEvictBroadcasts == nil {
		return
	}
	m.cacheEvictBroadcasts.WithLabelValues(cache, result).Add(0)
}

// evictionBroadcast counts one EvictionBroadcast message.
func (m *streamMetrics) evictionBroadcast(cache, result string) {
	if m == nil || m.cacheEvictBroadcasts == nil {
		return
	}
	m.cacheEvictBroadcasts.WithLabelValues(cache, result).Inc()
}

// newEngagedCollector builds and registers the scrape-time engaged gauge, or returns nil for
// a microservice with no registry (one assembled as a struct literal).
func newEngagedCollector(ms *core.Microservice) *engagedCollector {
	reg := ms.MetricsRegisterer()
	if reg == nil {
		return nil
	}
	c := &engagedCollector{desc: prometheus.NewDesc(
		prometheus.BuildFQName(core.METRICS_NAMESPACE, ms.MetricsSubsystem(), "jetstream_backpressure_engaged"),
		"1 while this service is refusing new messages on the stream (backpressure), including while it "+
			"cannot measure the stream's backlog; 0 otherwise. Evaluated at scrape time.",
		[]string{"stream"}, nil)}
	reg.MustRegister(c)
	return c
}

// setUnreadRatio records one gating durable's unread ratio.
func (m *streamMetrics) setUnreadRatio(stream, durable string, r float64) {
	if m == nil || m.unreadRatio == nil {
		return
	}
	m.unreadRatio.WithLabelValues(stream, durable).Set(r)
}

// setHistoryRunway records how long one gating durable's read history would last (see
// runwaySeconds).
func (m *streamMetrics) setHistoryRunway(stream, durable string, seconds float64) {
	if m == nil || m.historyRunway == nil {
		return
	}
	m.historyRunway.WithLabelValues(stream, durable).Set(seconds)
}

// forgetBackpressure withdraws a durable's gate series, the ratio and the runway: it was not
// measured.
func (m *streamMetrics) forgetBackpressure(stream, durable string) {
	if m == nil {
		return
	}
	if m.unreadRatio != nil {
		m.unreadRatio.DeleteLabelValues(stream, durable)
	}
	if m.historyRunway != nil {
		m.historyRunway.DeleteLabelValues(stream, durable)
	}
}

// initRefused creates a gated stream's refused counter at 0, for the reason initDurable gives.
func (m *streamMetrics) initRefused(stream string) {
	if m == nil || m.publishRefused == nil {
		return
	}
	m.publishRefused.WithLabelValues(stream).Add(0)
}

// countRefused counts n refused messages.
func (m *streamMetrics) countRefused(stream string, n int) {
	if m == nil || m.publishRefused == nil {
		return
	}
	m.publishRefused.WithLabelValues(stream).Add(float64(n))
}

// cacheObserver records one Cache's metrics. Every method is a no-op on a nil receiver,
// which is what a Cache built over a test double, or by a manager with no metrics, has.
type cacheObserver struct {
	m     *streamMetrics
	cache string

	// The in-process tier's per-lookup counters, resolved once by initLocal rather than by
	// label on every read: a hit is meant to cost well under a microsecond, and a label
	// lookup is a good share of that. Nil until initLocal, so a cache whose tier is off
	// records nothing.
	localHit, localMiss prometheus.Counter
}

// cacheObserver returns the recorder for the named cache, or nil when there are no metrics.
func (m *streamMetrics) cacheObserver(name string) *cacheObserver {
	if m == nil || m.cacheLatency == nil {
		return nil
	}
	return &cacheObserver{m: m, cache: name}
}

// init creates the cache's series at 0 when the cache is opened, for the reason
// initDurable gives: a counter that first appears at a nonzero value has nothing for
// increase() to start from, and "none yet" should not read the same as "not measured".
func (o *cacheObserver) init() {
	if o == nil {
		return
	}
	for _, op := range []string{"get", "set", "delete"} {
		o.m.cacheLatency.WithLabelValues(o.cache, op)
		for _, reason := range []string{"timeout", "error"} {
			o.m.cacheFailures.WithLabelValues(o.cache, op, reason).Add(0)
		}
	}
	for _, op := range []string{"get", "set"} {
		o.m.cacheBypassed.WithLabelValues(o.cache, op).Add(0)
	}
	o.m.cacheUnavailable.WithLabelValues(o.cache).Set(0)
}

// initLocal creates the in-process tier's series at 0, for the reason init gives, and
// records the bounds it was built with. Called only for a cache whose in-process tier is on.
func (o *cacheObserver) initLocal(maxEntries, maxBytes int) {
	if o == nil || o.m.cacheLocalLookups == nil {
		return
	}
	o.m.cacheLocalMaxEntries.WithLabelValues(o.cache).Set(float64(maxEntries))
	o.m.cacheLocalMaxBytes.WithLabelValues(o.cache).Set(float64(maxBytes))
	o.localHit = o.m.cacheLocalLookups.WithLabelValues(o.cache, "hit")
	o.localMiss = o.m.cacheLocalLookups.WithLabelValues(o.cache, "miss")
	o.localHit.Add(0)
	o.localMiss.Add(0)
	for _, reason := range []string{"expired", "capacity", "deleted"} {
		o.m.cacheLocalEvictions.WithLabelValues(o.cache, reason).Add(0)
	}
	o.localSize(0, 0)
}

func (o *cacheObserver) localLookup(hit bool) {
	if o == nil || o.localHit == nil {
		return
	}
	if hit {
		o.localHit.Inc()
	} else {
		o.localMiss.Inc()
	}
}

func (o *cacheObserver) localEvicted(reason string) {
	if o == nil || o.localHit == nil {
		return
	}
	o.m.cacheLocalEvictions.WithLabelValues(o.cache, reason).Inc()
}

func (o *cacheObserver) localSize(entries, bytes int) {
	if o == nil || o.localHit == nil {
		return
	}
	o.m.cacheLocalEntries.WithLabelValues(o.cache).Set(float64(entries))
	o.m.cacheLocalBytes.WithLabelValues(o.cache).Set(float64(bytes))
}

func (o *cacheObserver) observe(op string, d time.Duration) {
	if o == nil {
		return
	}
	o.m.cacheLatency.WithLabelValues(o.cache, op).Observe(d.Seconds())
}

func (o *cacheObserver) failure(op, reason string) {
	if o == nil {
		return
	}
	o.m.cacheFailures.WithLabelValues(o.cache, op, reason).Inc()
}

func (o *cacheObserver) bypassed(op string) {
	if o == nil {
		return
	}
	o.m.cacheBypassed.WithLabelValues(o.cache, op).Inc()
}

func (o *cacheObserver) setUnavailable(unavailable bool) {
	if o == nil {
		return
	}
	v := 0.0
	if unavailable {
		v = 1
	}
	o.m.cacheUnavailable.WithLabelValues(o.cache).Set(v)
}

// initPublish creates a writer's latency series at count 0 when the writer is built, for the
// reason initDurable gives: a series that first appears with samples has nothing earlier to
// compare against, and "no publishes yet" should not read the same as "not measured". A
// no-op on a manager with no metrics (one assembled by hand in a unit test).
func (m *streamMetrics) initPublish(suffix, mode string) {
	if m == nil || m.publishLatency == nil {
		return
	}
	m.publishLatency.WithLabelValues(suffix, mode)
}

// observePublish records one publish's latency. See publishLatency for what each mode means.
func (m *streamMetrics) observePublish(suffix, mode string, d time.Duration) {
	if m == nil || m.publishLatency == nil {
		return
	}
	m.publishLatency.WithLabelValues(suffix, mode).Observe(d.Seconds())
}

// initDurable creates a durable's per-reader series at 0: the two unread series for every
// reader, and — for a capacity reader — both stages of the held-past-AckWait counter.
//
// It is called when the reader is created, not at its first sample or first count, and that
// ordering is what makes the first loss visible. The alerts are increase() over counters, and
// a counter that first APPEARS at a nonzero value has no earlier sample to increase from —
// so a loss in a durable's first interval would be exported and never alerted on. A
// series that exists at 0 from creation also keeps "no loss" and "not measured" from
// reading the same on a dashboard. The held-past series exist only for a capacity reader,
// because no other reader can count one: a 0 there would claim a measurement nothing makes.
func (m *streamMetrics) initDurable(stream, durable string, capacity bool) {
	m.unreadSkipped.WithLabelValues(stream, durable).Add(0)
	m.unreadGap.WithLabelValues(stream, durable).Set(0)
	if capacity {
		m.heldPastAckWait.WithLabelValues(durable, stageBuffer).Add(0)
		m.heldPastAckWait.WithLabelValues(durable, stageWorker).Add(0)
	}
}

// heldPastAckWaitFor returns the held-past-AckWait recorder for one capacity reader's
// durable. Its series are created at 0 by initDurable, once the reader exists. It returns
// nil on a manager with no metrics (one assembled by hand in a unit test).
func (m *streamMetrics) heldPastAckWaitFor(durable string) func(stage string) {
	if m == nil || m.heldPastAckWait == nil {
		return nil
	}
	return func(stage string) { m.heldPastAckWait.WithLabelValues(durable, stage).Inc() }
}

// initMaxDeliveryRecords creates stream's max-delivery series at zero for every outcome the
// recorder can count for it, so an increase() over one reads the first record rather than
// missing it. replayCovered selects the replay-covered set (see replayCoveredOutcomes). A no-op
// on a manager with no metrics (one assembled by hand in a unit test).
func (m *streamMetrics) initMaxDeliveryRecords(stream string, replayCovered bool) {
	if m == nil || m.maxDeliveryRecords == nil {
		return
	}
	outcomes := maxDeliveryOutcomes
	if replayCovered {
		outcomes = replayCoveredOutcomes
	}
	for _, o := range outcomes {
		m.maxDeliveryRecords.WithLabelValues(stream, string(o)).Add(0)
	}
}

// countMaxDelivery counts one recorded advisory.
func (m *streamMetrics) countMaxDelivery(stream string, outcome MaxDeliveryOutcome) {
	if m == nil || m.maxDeliveryRecords == nil {
		return
	}
	m.maxDeliveryRecords.WithLabelValues(stream, string(outcome)).Inc()
}

// sampleReplication records the replication triple for one stream or KV bucket.
// Split out because KV buckets get ONLY this, not the fill gauges — see sample.
func (m *streamMetrics) sampleReplication(name string, info *nats.StreamInfo, desired int) {
	actual := info.Config.Replicas
	if actual < 1 {
		actual = 1
	}
	m.replicasDesired.WithLabelValues(name).Set(float64(desired))
	m.replicasActual.WithLabelValues(name).Set(float64(actual))
	m.peersCurrent.WithLabelValues(name).Set(float64(currentPeers(info)))
}

// forgetReplication drops a stream's replication series when it cannot be read.
//
// Leaving the last healthy values in place would be worse than reporting nothing:
// the alerting condition is peersCurrent < replicasActual, so a pod that can no
// longer reach the JetStream API would keep exporting a scrapable, plausible,
// stale "everything is fine" — and the one condition that would have fired can
// never fire while the thing it watches is unreachable. Absence of a series is
// detectable; a frozen series is not. The chart's JetStreamReplicationUnobserved
// alert is what detects it: a stream this pod reported earlier and reports no
// longer, while its jetstream_broker_clustered gauge says the pod is still sampling.
// (Prometheus `absent()` cannot, because it needs the stream names written into the
// rule.)
func (m *streamMetrics) forgetReplication(name string) {
	m.replicasDesired.DeleteLabelValues(name)
	m.replicasActual.DeleteLabelValues(name)
	m.peersCurrent.DeleteLabelValues(name)
}

// currentPeers counts the RAFT peers that are caught up AND online, including the
// leader.
//
// The leader is counted from the Leader field rather than from the Replicas slice,
// because JetStream reports the OTHER peers there — a 3-replica stream lists two.
// An unclustered broker reports no cluster block at all, and there the single
// server holding the stream is the one current peer.
func currentPeers(info *nats.StreamInfo) int {
	if info.Cluster == nil {
		return 1
	}
	n := 0
	if info.Cluster.Leader != "" {
		n++
	}
	for _, p := range info.Cluster.Replicas {
		if p.Current && !p.Offline {
			n++
		}
	}
	return n
}

// sampleFailureLog is the level a failed broker sample is logged at: warn, because a
// sample that silently stops arriving is broker trouble an operator should see, unless
// ctx has been cancelled — a sampler told to stop fails every request in flight, and a
// warning per stream on every rollout would teach operators to ignore the real ones.
func sampleFailureLog(ctx context.Context) *zerolog.Event {
	if ctx.Err() != nil {
		return log.Debug()
	}
	return log.Warn()
}

// sample polls each stream and KV bucket once and updates its gauges, emitting an
// edge-triggered line when a stream crosses the near-full threshold. A
// per-stream StreamInfo error is logged at warn and skipped: it does not stall the
// sampler, but a sample that silently stops arriving hides exactly the broker trouble
// these gauges exist to show. A failure caused by the pass being cancelled (shutdown)
// stays at debug, so a rollout does not print a warning per stream.
//
// Buckets deliberately get the replication triple and NOT the fill gauges, even
// though a KV bucket is a stream and its fill is just as real. A Cache bucket is
// created DiscardNew and is SUPPOSED to sit near its ceiling — that is a bounded cache
// working correctly, not a backlog — so its fill is exactly the history-blind number
// the alerts were moved off, and the stream-fill series (read by dashboards, and by
// JetStreamStreamNearFull for declared sinks) would show every bounded cache as full.
// Bucket disk is already accounted for up front by the ADR-023 reservation, which
// is the right place for it. Replication is different: it is a correctness
// property, it is the same property for a bucket as for a stream, and for
// dc_leases it is the one that decides whether failover works at all.
// ctx bounds the whole pass, and it does so at the level that matters: every
// StreamInfo below carries it, so a cancellation lands on the request in flight
// rather than at the next turn of the loop. That distinction is the point — with no
// context the calls fall back to the JetStream default request wait, and against a
// broker that has stopped answering a pass over a dozen streams and buckets is that
// wait a dozen times over, all of it inside the join that shutdown blocks on.
//
// The per-name error handling is the same for a cancelled pass — a cancelled
// StreamInfo is skipped like any other failure, though logged at debug rather than
// warn (see sampleFailureLog) — but the loops then stop rather than working through
// the remaining names to fail identically on each. At most the name whose StreamInfo
// was in flight has its replication series dropped (its call failed, so
// forgetReplication ran for it); the names not reached KEEP their last values, because
// the loop returns before touching them. That is harmless where it happens -- the pass
// is cancelled only when the service is stopping, and the pod's series go with it.
//
// sinks is the subset of names declared as sinks (isSinkStream). Each reads 1 on the
// streamSink marker (every other stream 0), and its near-full log line is a warning, because nothing processes what a sink
// holds, so near its ceiling it is about to discard records nobody has looked at. Any
// other stream near its ceiling is normally full of history its readers have already
// read, which is why its line is only an info.
//
// durables are the readers this service created. Each is sampled right after its
// stream, against that stream's StreamInfo — the same snapshot the fill gauges read —
// so a durable whose stream could not be read this pass is skipped with it.
func (m *streamMetrics) sample(ctx context.Context, js nats.JetStreamContext, names []string, sinks map[string]bool,
	buckets []string, durables []durableRef, desired int, clustered bool) {
	m.brokerClustered.Set(boolGauge(clustered))
	for _, name := range buckets {
		if ctx.Err() != nil {
			return
		}
		info, err := js.StreamInfo(name, nats.Context(ctx))
		if err != nil {
			sampleFailureLog(ctx).Err(err).Str("bucket", name).Msg("KV bucket replication sample failed")
			m.forgetReplication(name)
			continue
		}
		m.sampleReplication(name, info, desired)
	}
	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		info, err := js.StreamInfo(name, nats.Context(ctx))
		if err != nil {
			sampleFailureLog(ctx).Err(err).Str("stream", name).Msg("Stream utilization sample failed")
			m.forgetReplication(name)
			// Its durables are not measured this pass either (they are sampled against
			// this StreamInfo), so their backlog is withdrawn with it.
			for _, d := range durables {
				if d.stream == name {
					m.forgetBacklog(d)
				}
			}
			continue
		}
		m.sampleReplication(name, info, desired)
		m.usedBytes.WithLabelValues(name).Set(float64(info.State.Bytes))
		m.usedMsgs.WithLabelValues(name).Set(float64(info.State.Msgs))
		m.limitBytes.WithLabelValues(name).Set(float64(info.Config.MaxBytes))
		m.limitMsgs.WithLabelValues(name).Set(float64(info.Config.MaxMsgs))
		m.streamSink.WithLabelValues(name).Set(boolGauge(sinks[name]))

		pct := streamFillRatio(info)
		switch {
		case pct >= streamNearFullThreshold && !m.warned[name]:
			m.warned[name] = true
			ev, msg := log.Info(), nearFullMsg
			if sinks[name] {
				ev, msg = log.Warn(), nearFullSinkMsg
			}
			ev.Str("stream", name).Float64("utilization", pct).
				Uint64("bytes", info.State.Bytes).Int64("maxBytes", info.Config.MaxBytes).
				Uint64("msgs", info.State.Msgs).Int64("maxMsgs", info.Config.MaxMsgs).
				Msg(msg)
		case pct < streamNearFullThreshold && m.warned[name]:
			m.warned[name] = false
			log.Info().Str("stream", name).Float64("utilization", pct).
				Msg("JetStream stream utilization recovered below the near-full threshold")
		}

		for _, d := range durables {
			if d.stream != name {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			m.sampleDurable(ctx, js, info, d)
		}
	}
}

// sampleDurable updates one durable's unread series from its ConsumerInfo and its
// stream's StreamInfo.
//
// Between two samples, a durable's cursor (Delivered.Stream: the last stream sequence it
// was handed) moves by dS, and it made dC deliveries. Every sequence the cursor passed
// was either delivered, removed before the durable reached it, or removed on purpose from
// the middle of the stream — and the last is subtracted as dD, the growth in the stream's
// NumDeleted (which a tenant purge raises) over the same interval. So dS - dC - dD is the
// unread loss. JetStream's pull skips removed sequences and moves the cursor past them,
// which is what makes the loss visible in dS at all.
//
// 🔴 dD CANCELS A PURGE ONLY WITHIN ONE INTERVAL. It is the growth in NumDeleted between
// the two samples, so it offsets a purge only when the cursor crosses the purged holes in
// the same interval the purge happened in. A purge ahead of a durable that is lagging or
// stalled, sampled before the durable reaches the holes, is counted as loss in the later
// interval in which it crosses them. That residual is accepted rather than engineered
// away — tenant deletion is rare and operator-initiated — and it is why the alert text
// names tenant deletion as an expected cause.
//
// 🔴 dD IS CLAMPED AT 0 BECAUSE NumDeleted ALSO FALLS. DiscardOld moving the stream's head
// past interior holes removes them from the count: the holes are now below FirstSeq, not
// inside the stream. A negative dD is that, not an un-delete, and subtracting it would ADD
// the old holes as phantom loss for a durable that read everything.
//
// 🔴 IT IS A LOWER BOUND, and deliberately so. A redelivery raises dC without moving the
// cursor, and an interior delete BEHIND the cursor raises dD without being stepped over
// — both only subtract, so neither can invent a loss. It is exact when neither happens.
// The clamp of the result to zero is that same direction: a negative interval is an
// interval where the subtractions outweighed the loss, not a loss to take back.
//
// 🔴 THE FIRST SAMPLE IS A BASELINE, NEVER AN INTERVAL. A pod that restarts, or a new
// replica, attaches to a durable that already exists, whose Delivered.Stream minus
// Delivered.Consumer holds every skip in its history. Differencing that against an
// implied zero would count the whole history at every restart and fire the critical
// alert for a loss it had already reported, or one from before the alert existed.
//
// 🔴 A DROP IN Delivered.Consumer IS A NEW CONSUMER, not a negative interval. A durable
// that was deleted and recreated starts counting deliveries from 0, and JetStream reports
// a recreated DeliverAll durable's cursor at the stream's FirstSeq-1 rather than 0 — so
// differencing across the recreation would charge every sequence the stream evicted in
// between against a delivery count that went backwards. The sample becomes the new
// baseline and nothing is counted, as on the very first sample.
//
// A ConsumerInfo failure skips the durable for this pass and keeps its previous sample,
// so the next good sample covers the whole interval and nothing is lost to the counter
// by a transient broker error. The backlog gauges are the opposite case and are withdrawn
// on that failure: a kept baseline loses nothing, but a kept gauge is an old reading
// exported as a current one (see consumerPending on streamMetrics).
//
// PRECONDITION, held by construction: the durable filters on its stream's WHOLE subject.
// Every reader is made by NewReader, which filters on StreamSubject(suffix) — the same
// subject the stream captures — so every sequence in the stream is one the durable would
// have been handed. A durable with a narrower filter would count every other subject's
// messages as loss — which is exactly what the max-delivery recorder's durable is: it filters
// the shared capture stream to this area's advisory subjects. It stays out because it is not a
// reader (startRecorder keeps it on nmgr.recorder, never in nmgr.readers), and
// TestTheRecorderDurableIsNotSampledForUnreadLoss pins that from the scrape's side.
func (m *streamMetrics) sampleDurable(ctx context.Context, js nats.JetStreamContext, info *nats.StreamInfo, d durableRef) {
	ci, err := js.ConsumerInfo(d.stream, d.durable, nats.Context(ctx))
	if err != nil {
		sampleFailureLog(ctx).Err(err).Str("stream", d.stream).Str("durable", d.durable).
			Msg("Durable unread-loss sample failed")
		m.forgetBacklog(d)
		return
	}
	m.consumerPending.WithLabelValues(d.stream, d.durable).Set(float64(ci.NumPending))
	m.consumerAckPending.WithLabelValues(d.stream, d.durable).Set(float64(ci.NumAckPending))
	if !d.gates {
		m.consumerUnreadRatio.WithLabelValues(d.stream, d.durable).Set(unreadRatio(info.State, info.Config, ci))
	}
	cur := durableSample{
		deliveredStream:   ci.Delivered.Stream,
		deliveredConsumer: ci.Delivered.Consumer,
		numDeleted:        uint64(max(0, info.State.NumDeleted)),
	}
	var gap uint64
	if prev, ok := m.durables[d]; ok && cur.deliveredConsumer >= prev.deliveredConsumer {
		dS := int64(cur.deliveredStream) - int64(prev.deliveredStream)
		dC := int64(cur.deliveredConsumer) - int64(prev.deliveredConsumer)
		dD := max(0, int64(cur.numDeleted)-int64(prev.numDeleted))
		if n := dS - dC - dD; n > 0 {
			m.unreadSkipped.WithLabelValues(d.stream, d.durable).Add(float64(n))
		}
		// Stalled: handed nothing since the previous sample. A durable that is reading,
		// however slowly, reports 0 here — its loss is the counter's (see unreadGap on
		// streamMetrics). The first sample, and the first after a recreation, have no
		// interval to judge by and report 0; the next sample does.
		if dS == 0 && dC == 0 {
			gap = unreadGap(info.State.FirstSeq, ci.Delivered.Stream)
		}
	}
	m.durables[d] = cur
	m.unreadGap.WithLabelValues(d.stream, d.durable).Set(float64(gap))
}

// forgetBacklog withdraws a durable's backlog series (pending, ack pending, unread
// ratio): it was not measured this pass.
func (m *streamMetrics) forgetBacklog(d durableRef) {
	m.consumerPending.DeleteLabelValues(d.stream, d.durable)
	m.consumerAckPending.DeleteLabelValues(d.stream, d.durable)
	m.consumerUnreadRatio.DeleteLabelValues(d.stream, d.durable)
}

// isSinkStream reports whether suffix's stream is declared to hold records for an operator
// rather than work for a service: its DeadLetterKind is NotLettered, which core/streams
// documents as "the stream is itself a sink, or a report". See streamSink.
func isSinkStream(suffix string) bool {
	return streams.DeadLetterKindFor(suffix) == streams.NotLettered
}

// unreadGap is how many sequences were removed between a durable's cursor and the
// stream's first retained message: FirstSeq-1-cursor, or 0 when the cursor is at or past
// the first message (including a stream that has never held one, whose FirstSeq is 0).
// sampleDurable reports it only for a durable that was handed nothing in the interval.
func unreadGap(firstSeq, cursor uint64) uint64 {
	if firstSeq == 0 || firstSeq-1 <= cursor {
		return 0
	}
	return firstSeq - 1 - cursor
}

// boolGauge renders a bool as the 1/0 a Prometheus gauge carries.
func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// streamFillRatio returns the higher of the stream's byte- and message-fill
// fractions. A dimension whose ceiling is non-positive (unlimited) contributes 0,
// so an unbounded stream never reports as near-full.
func streamFillRatio(info *nats.StreamInfo) float64 {
	frac := func(used uint64, limit int64) float64 {
		if limit <= 0 {
			return 0
		}
		return float64(used) / float64(limit)
	}
	return math.Max(
		frac(info.State.Bytes, info.Config.MaxBytes),
		frac(info.State.Msgs, info.Config.MaxMsgs),
	)
}
