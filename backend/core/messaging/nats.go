// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/google/uuid"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

const (
	// streamMaxAge bounds how long undelivered/retained messages live in a
	// JetStream stream. Mirrors a Kafka retention window; durable consumers
	// track their own position independently.
	streamMaxAge = 7 * 24 * time.Hour

	// fetchTimeout bounds a single pull-consumer Fetch so an idle reader can
	// periodically check for shutdown instead of blocking forever.
	fetchTimeout = 1 * time.Second

	// fetchBatch is how many messages a single Fetch pulls. Batching amortizes
	// the request-reply round trip across many messages so consume throughput is
	// not capped at ~one RTT per message (ADR-022 review B1). Messages are then
	// handed to the caller one per ReadMessage from an internal buffer. The whole
	// batch starts its AckWait timer at fetch, so fetchBatch is kept well below
	// ackWait * throughput (and within the processors' MESSAGE_BACKLOG channel)
	// so the tail of a batch is not redelivered while still in the pipeline.
	fetchBatch = 64

	// ackWait is how long JetStream waits for an ack before redelivering a
	// message. It must comfortably exceed the time a fetched batch takes to clear
	// the worker pipeline (worst-case per-message DB persist latency * batch /
	// workers) so a slow-but-succeeding message is not redelivered underneath the
	// worker (ADR-022 review A4).
	ackWait = 60 * time.Second

	// MaxDeliver bounds redelivery of a poison message: after this many delivery
	// attempts the broker stops redelivering, and consumers route the message to
	// their dead-letter path (failed-events) on the final attempt rather than
	// looping forever (ADR-022 review A4). Consumers compare Message.NumDelivered
	// against this.
	MaxDeliver = 5

	// readerMaxAckPending pins the consumer's max in-flight unacked messages. It
	// matches the value the legacy PullSubscribe path set implicitly (its delivery
	// channel capacity, SubChanLen) so AddConsumer stays idempotent against durables
	// an older build created, and so freshly-created and upgraded-in-place consumers
	// do not silently diverge (an unset value would default to 1000 on the server).
	readerMaxAckPending = 65536

	// rebindBackoffMin/Max bound the self-heal retry when a reader's durable consumer
	// goes away (e.g. deleted by an older pod's Unsubscribe during a rolling update).
	// The reader re-binds with capped exponential backoff so a persistent failure does
	// not busy-loop the way a bare Fetch-error retry would.
	rebindBackoffMin = 500 * time.Millisecond
	rebindBackoffMax = 5 * time.Second

	// livenessProbeAfterTimeouts is how many consecutive empty (timed-out) fetches
	// trigger a consumer-existence probe. A deleted consumer does not always surface
	// as an explicit error on the next fetch — depending on broker/timing it can look
	// like an ordinary empty fetch — so a reader that has gone quiet cheaply confirms
	// its durable still exists (one ConsumerInfo call per ~this-many idle seconds) and
	// re-binds if it has vanished, rather than idling silently on a dead consumer.
	livenessProbeAfterTimeouts = 5

	// liveBuffer bounds a live subscription's in-flight buffer. A fan-out live
	// feed (SubscribeLive) prefers dropping under a slow client to stalling the
	// shared pipeline, so a full buffer drops (NATS slow-consumer) rather than
	// applying backpressure — history is served by the queries, not this feed.
	liveBuffer = 256
)

// natsAck adapts a JetStream *nats.Msg to the transport-neutral Acknowledger so
// the ack handle can ride the Message envelope to the worker that ultimately
// handles it (ADR-022 review A3: ack only after the message is durably handled).
type natsAck struct{ nm *nats.Msg }

func (a natsAck) Ack() error { return a.nm.Ack() }

// NatsManager manages the lifecycle of NATS JetStream interactions for a
// microservice. It mirrors the former KafkaManager's lifecycle shape so the
// service mains change minimally.
type NatsManager struct {
	Microservice *core.Microservice

	oncreate  func(*NatsManager) error
	nc        *nats.Conn
	js        nats.JetStreamContext
	readers   []*natsReader
	writers   []*natsWriter
	lifecycle core.LifecycleManager

	// streamNames is the set of streams this service has ensured (deduped), guarded
	// by streamMu because SubscribeLive can ensure a stream at runtime while the
	// metrics sampler reads the set. The sampler polls each for fill (ADR-023).
	// bucketNames is the parallel set of KV buckets, held separately because they
	// get replication metrics only (streamMetrics.sample explains why).
	streamMu    sync.Mutex
	streamNames []string
	bucketNames []string
	metrics     *streamMetrics
	stopSampler chan struct{}
	samplerWg   sync.WaitGroup

	// shuttingDown distinguishes a connection we closed from one that died.
	//
	// The ClosedHandler cannot tell them apart on its own — nc.LastError() is nil
	// for both a deliberate Close and several terminal states — and the difference
	// decides the log LEVEL. A closed connection during shutdown is the expected end
	// of a pod's life; the same event at any other time means the service is
	// permanently mute and needs a restart. Emitting the second message for the
	// first case puts an ERROR reading "the process must be restarted" into the logs
	// of every service on every rolling update, node drain and scale-down, which is
	// precisely how the one loud signal here stops meaning anything.
	//
	// Atomic because the handler runs on the client's callback goroutine while
	// ExecuteStop/ExecuteTerminate run on the lifecycle goroutine.
	shuttingDown atomic.Bool

	// warnedClamp edge-triggers the replica-clamp warning. Touched only from the
	// single sampler goroutine, like streamMetrics.warned.
	warnedClamp bool

	// connectedServer remembers the broker URL, because the disconnect callback
	// cannot ask for it. nats.Conn.ConnectedUrl() returns "" for any status other
	// than CONNECTED, and by the time DisconnectErrHandler runs the status has
	// already flipped — so reading it there yields an empty string every time,
	// which is exactly the field an operator needs to know WHICH server was lost.
	connectedServer atomic.Value
}

// NewNatsManager creates a new NATS manager. oncreate is invoked on Start to
// instantiate the service's readers/writers (mirrors KafkaManager).
func NewNatsManager(ms *core.Microservice, callbacks core.LifecycleCallbacks,
	oncreate func(*NatsManager) error) *NatsManager {
	nmgr := &NatsManager{
		Microservice: ms,
		oncreate:     oncreate,
		readers:      make([]*natsReader, 0),
		writers:      make([]*natsWriter, 0),
		metrics:      newStreamMetrics(ms),
	}
	name := fmt.Sprintf("%s-%s", ms.FunctionalArea, "nats")
	nmgr.lifecycle = core.NewLifecycleManager(name, nmgr, callbacks)
	return nmgr
}

// Conn exposes the underlying NATS connection for core (non-JetStream)
// request/reply patterns such as the ADR-025 auth-callout responder. It is nil
// until ExecuteInitialize has connected.
func (nmgr *NatsManager) Conn() *nats.Conn {
	return nmgr.nc
}

// NatsUrl returns the NATS connection url from instance configuration.
func (nmgr *NatsManager) NatsUrl() string {
	cfg := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	return fmt.Sprintf("nats://%s:%d", cfg.Hostname, cfg.Port)
}

// desiredStreamReplicas returns the CONFIGURED JetStream replica count (defaulting
// to 1 when unset) — what the operator asked for, which is not necessarily what the
// broker can deliver. Callers creating or reconciling a stream or bucket want
// effectiveStreamReplicas instead.
func (nmgr *NatsManager) desiredStreamReplicas() int {
	r := int(nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas)
	if r < 1 {
		return 1
	}
	return r
}

// effectiveStreamReplicas is the replica count actually used to create and
// reconcile streams and KV buckets: the configured count, clamped to what the
// connected broker can satisfy.
//
// The clamp exists because the two halves of an HA deployment live in different
// tools and can therefore disagree. streamReplicas is a helm-rendered instance-config
// value; whether JetStream is CLUSTERED at all is an infrastructure property of the
// NATS StatefulSet (tofu var.ha / nats_cluster_replicas). Nothing structurally
// prevents an operator — or a partial edit, or a values file copied between
// instances — from raising one without the other.
//
// What makes that worth code rather than a comment is the failure it produces.
// Asking an UNCLUSTERED broker for more than one replica is not a degraded
// configuration that limps: nats-server rejects every stream creation outright
// (JSStreamReplicasNotSupportedErr), which RetryInfraConnect converts into a
// crashloop. That would take down every stream-creating area AND user-management,
// which is KV-only and is therefore auth and JWKS for the entire platform — a
// total outage, caused by a single number being too high in a document nobody
// re-read. The blast radius is wildly out of proportion to the mistake.
//
// So the runtime posture here is deliberately NOT the fail-closed one used for
// config elsewhere: clamp, and shout. Availability is preserved, and the thing
// that is refused is the CLAIM of high availability, not the platform.
//
// Be precise about what does and does not catch this earlier, because the lenient
// posture is only defensible if something else is strict. dcctl DOES run the
// config's own Validate against the rendered chart before installing
// (bootstrap/helm.go, validateRenderedInstanceConfig), which rejects a replica
// count that is even or above the server maximum. But that is everything decidable
// from the config DOCUMENT, and this mismatch is not in the document — it is
// between the document and the broker. dcctl closes that gap for its own path:
// `--ha` sets both halves from one value, and a preflight between the infra apply
// and the helm install compares this replica factor against the server count the
// infrastructure actually reported (bootstrap/ha.go). What it cannot cover is a
// chart installed by hand, or against a broker dcctl did not provision — so this
// clamp and the desired-vs-actual gauges remain the last line, and they are the
// only line for a direct `helm install`.
//
// Note the limitation, because it decides what a green startup here does and does
// not prove: this reads clustered-NESS, not peer count. A 3-node cluster running
// on one surviving node reports a cluster name and passes this check, then fails
// the creation with JSClusterNoPeersError. That case is handled at the create/update
// call site, which treats a replica-only failure as non-fatal. Neither is a
// substitute for asserting replication from StreamInfo against a running cluster
// (the A0 Check A suite) — a service that starts clean has not demonstrated that
// anything is replicated.
func (nmgr *NatsManager) effectiveStreamReplicas() int {
	return clampReplicas(nmgr.desiredStreamReplicas(), nmgr.brokerIsClustered())
}

// clampReplicas is the clamp arithmetic, split out from the connection state so the
// decision table can be tested without a broker while brokerIsClustered is tested
// against real ones.
func clampReplicas(desired int, clustered bool) int {
	if desired <= 1 || clustered {
		return desired
	}
	return 1
}

// brokerIsClustered reports whether the broker this manager is connected to RIGHT
// NOW is part of a cluster.
//
// WHERE THE SAFETY ACTUALLY COMES FROM, because an earlier version of this
// comment got it wrong: it is ConnectedClusterName() itself, which returns "" for
// a nil receiver AND for any status other than CONNECTED. The explicit
// nil/IsConnected gate below is therefore REDUNDANT — deleting it changes no
// behaviour and no test — and it is kept only as belt-and-braces against a future
// nats.go relaxing that guarantee. What is load-bearing is that this is read LIVE
// on every call rather than cached, and getting THAT wrong is a regression
// rather than a missed improvement. Under nats.RetryOnFailedConnect — which
// ExecuteInitialize sets — nats.Connect returns a non-nil connection and a NIL
// error even when no broker is reachable: it hands back a stub in RECONNECTING
// state and dials in the background. ConnectedClusterName reports "" for any
// status other than CONNECTED. So a probe taken once at connect cannot distinguish
// "this broker is not clustered" from "I have not reached the broker yet", and on
// any ordinary bring-up where a pod starts before NATS is accepting connections —
// a helm upgrade rolling the NATS StatefulSet alongside the Deployments, a node
// drain, a cold start — it answers the second question with the first answer.
// This package already documents that library behaviour, in
// lifecycle_durability_test.go.
//
// Caching the answer would then pin an entire process to a false negative, and
// every stream and bucket it creates — dc_leases included — would be single-replica
// on a perfectly healthy three-node cluster, while the warning sent the operator to
// the one lever they had already set correctly. That is precisely the false-HA state
// this workstream exists to eliminate, reintroduced by the code meant to close it.
//
// Reading live instead is self-correcting and needs no invalidation: the create and
// reconcile paths that consume it either run while connected, or fail on the broker
// call and are retried, and the retry re-evaluates. It also handles the reverse
// order — raising streamReplicas before scaling the NATS cluster — without a
// reconnect hook, because the next pod roll simply reads the truth.
func (nmgr *NatsManager) brokerIsClustered() bool {
	if nmgr.nc == nil || !nmgr.nc.IsConnected() {
		return false
	}
	return nmgr.nc.ConnectedClusterName() != ""
}

// reportReplicaClamp logs the desired-vs-effective mismatch once at connect, naming
// BOTH levers. Naming both is the whole value of the message: the operator who sees
// it has set one of them, and the fix is always the other one — a message that
// named only the config key would send them to the file they already edited.
// reportReplicaClamp shouts once if the configured replica factor is being
// clamped away, i.e. this instance thinks it is HA and is not.
//
// 🔴 IT MUST NOT RUN AT CONNECT TIME. Under RetryOnFailedConnect, nats.Connect
// returns a non-nil connection and a nil error while it is still dialling, and
// nc.JetStream() does no I/O — so at the end of ExecuteInitialize IsConnected()
// is routinely false. brokerIsClustered() then reads false for a broker that is
// merely NOT YET REACHABLE, and a correctly configured 3-node instance gets told
// its broker is not clustered and it is not highly available, pointing the
// operator at the one lever they already set right.
//
// That is the SAME false-negative the clamp itself was fixed for, one layer over:
// a cold start where NATS and the services race, a node drain, a helm upgrade
// that rolls the NATS StatefulSet alongside the Deployments. So it is called from
// the metrics sampler instead, which runs after oncreate and therefore after the
// connection has been used, and which re-evaluates on every tick — so the message
// also stops once the situation is fixed, rather than being a one-shot at boot.
//
// warnedClamp keeps it edge-triggered: once per transition into the clamped
// state, not every 30 seconds.
func (nmgr *NatsManager) reportReplicaClamp() {
	desired := nmgr.desiredStreamReplicas()
	effective := nmgr.effectiveStreamReplicas()
	if desired == effective {
		if nmgr.warnedClamp {
			nmgr.warnedClamp = false
			log.Info().Int("replicas", desired).
				Msg("The broker now satisfies the configured JetStream replica factor; " +
					"streams and buckets created from here are replicated as configured. " +
					"Anything created while clamped stays at one replica until a restart " +
					"reconciles it upward")
		}
		return
	}
	if nmgr.warnedClamp {
		return
	}
	nmgr.warnedClamp = true
	log.Warn().Int("desired", desired).Int("effective", effective).
		Msg("This instance is configured for replicated JetStream streams and KV buckets but the broker is " +
			"NOT clustered, so every stream and bucket is being created UNREPLICATED. This instance is not " +
			"highly available. Both halves must be raised together: " +
			"instance.config.infrastructure.nats.streamReplicas (helm) sets the replica count, and tofu " +
			"var.ha / nats_cluster_replicas sizes the NATS server cluster that makes it possible. " +
			"Streams are being created at 1 replica rather than failing, so the platform stays up")
}

// streamBounds are the platform ceilings applied to every per-suffix stream
// (ADR-023): total on-disk bytes, total message count, and single-message size.
type streamBounds struct {
	maxBytes   int64
	maxMsgs    int64
	maxMsgSize int32
}

// streamBounds reads the configured ceilings for a suffix, coercing any
// non-positive value to the platform default so a stream is never created
// UNLIMITED (0 in a StreamConfig means unlimited to JetStream). ApplyDefaults
// normally does this at config load; this is belt-and-suspenders for a
// manually-built config in tests.
//
// The byte ceiling is per-SUFFIX (hot vs control-plane, see
// config.StreamMaxBytesFor) rather than per-caller. That distinction is
// load-bearing: a suffix's stream is created by whichever of its writer or
// reader starts first, and those live in DIFFERENT services. If the bound came
// from the call site, two services could disagree about the same stream and each
// restart would UpdateStream it back and forth — and per ensureStream, shrinking
// a ceiling makes DiscardOld evict the overflow immediately. Keying on the suffix
// makes every service compute the same answer, which is what keeps the reconcile
// idempotent.
func (nmgr *NatsManager) streamBounds(suffix string) streamBounds {
	c := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	b := streamBounds{maxBytes: c.StreamMaxBytesFor(suffix), maxMsgs: c.StreamMaxMsgs, maxMsgSize: c.StreamMaxMsgSize}
	if b.maxMsgs <= 0 {
		b.maxMsgs = config.DefaultStreamMaxMsgs
	}
	if b.maxMsgSize <= 0 {
		b.maxMsgSize = config.DefaultStreamMaxMsgSize
	}
	return b
}

// applyStreamBounds copies the desired ceilings onto cfg and reports whether any
// of them changed, so ensureStream issues an UpdateStream only on real drift. It
// is a pure function to keep the reconcile decision unit-testable without a broker.
func applyStreamBounds(cfg *nats.StreamConfig, b streamBounds) bool {
	changed := cfg.MaxBytes != b.maxBytes || cfg.MaxMsgs != b.maxMsgs || cfg.MaxMsgSize != b.maxMsgSize
	cfg.MaxBytes = b.maxBytes
	cfg.MaxMsgs = b.maxMsgs
	cfg.MaxMsgSize = b.maxMsgSize
	return changed
}

// applyStreamSubjects reconciles the subjects an existing stream captures to the
// shape the current build expects, reporting whether anything changed.
//
// This exists because a subject shape can CHANGE between releases — device-commands
// went from tenant-scoped to per-device — and a stream created by an older build
// keeps its original subject list forever otherwise. The failure that causes is
// silent and asymmetric: a FRESH install creates the stream with the new subject and
// works, while every EXISTING cluster keeps the old one, so publishes match no
// stream and commands stop being delivered with nothing in the logs to say why. A
// green upgrade on a fresh cluster proves nothing about an existing one.
//
// Reconciling is safe for messages already stored: JetStream keeps them, they simply
// stop being matched by a filter that no longer covers them. It is deliberately a
// full replacement rather than a union — carrying the old subject forward is how the
// tenant-wide command subject would have survived the very change that removed it.
func applyStreamSubjects(cfg *nats.StreamConfig, subjects []string) bool {
	if slices.Equal(cfg.Subjects, subjects) {
		return false
	}
	cfg.Subjects = subjects
	return true
}

// applyStreamDuplicateWindow reconciles a stream's dedup window to what
// core/streams declares, reporting whether it changed.
//
// The zero check is the whole point, and it is not a micro-optimization. A suffix
// that declares NO window must leave the broker's setting alone rather than write
// 0 into it: those are different statements, and conflating them means every
// service that ensures the stream would fight over it — one build reconciling a
// window on, the next reconciling it back off, an UpdateStream per pod per
// restart forever. Reconciling only a DECLARED window makes the operation
// idempotent across pods, which is the property ensureStream depends on.
func applyStreamDuplicateWindow(cfg *nats.StreamConfig, window time.Duration) bool {
	if window == 0 || cfg.Duplicates == window {
		return false
	}
	cfg.Duplicates = window
	return true
}

// applyStreamReplicas reconciles a stream's replica factor UPWARD only, reporting
// whether it changed and whether a downward change was refused.
//
// Upward is the direction that fixes things and the only one a starting pod is
// entitled to take. Without it, replication is set at CREATE time and nowhere else,
// which produces the asymmetry this file has been bitten by twice already: a fresh
// install comes up correctly replicated while every existing cluster silently keeps
// the single-replica streams it was born with, looking healthy and reporting
// nothing. An HA migration that only works on clusters that did not need it is not
// an HA migration.
//
// Downward is refused, and that refusal is doing real work rather than being
// conservative for its own sake. nats-server executes a scale-down with no safety
// check at all: it drops RAFT peers and remaps every durable consumer's group. The
// actor requesting it here would be a pod that just started, acting on config it
// happens to have been handed — and the ordinary mechanism that hands a pod older
// config is `helm rollback`, which an operator reaches for to make things SAFER.
// Rolling back an unrelated bad release must not quietly de-replicate the platform
// as a side effect. Scale-down stays a deliberate operator action (`nats stream
// update`), not something inferred from a deployment artifact.
//
// A 0 in an existing config means "unspecified", which JetStream treats as 1; it is
// normalized so an old stream created before the field was set is seen as the single
// replica it actually is, rather than as an incomparable zero.
func applyStreamReplicas(cfg *nats.StreamConfig, desired int) (changed, refusedDownward bool) {
	current := cfg.Replicas
	if current < 1 {
		current = 1
	}
	if desired == current {
		return false, false
	}
	if desired < current {
		return false, true
	}
	cfg.Replicas = desired
	return true, false
}

// ensureStream creates the per-suffix stream if it does not already exist, and
// reconciles its size ceilings and subjects if it does. The stream captures every
// tenant's subjects for the suffix, so a single stream backs both the scoped
// producers and the shared wildcard consumer.
func (nmgr *NatsManager) ensureStream(suffix string) (string, error) {
	// Refuse a suffix core/streams does not declare. This is the guard that makes
	// the disk budget trustworthy: JetStream reserves each stream's MaxBytes UP
	// FRONT, so a stream nobody declared reserves real disk that the budget never
	// counted — and the budget is what keeps the broker from crashlooping with
	// "insufficient storage resources available" on a fresh bring-up.
	//
	// Failing here is deliberately loud and early. The alternative is what used to
	// happen: the stream is created, works fine in a dev cluster, and the shortfall
	// only appears as a crashloop on an install whose PV was sized from the budget.
	if !streams.IsDeclared(suffix) {
		return "", fmt.Errorf("stream suffix %q is not declared in core/streams: "+
			"add it to streams.All (with its disk tier) so the reservation budget accounts for it", suffix)
	}
	name := StreamName(nmgr.Microservice.InstanceId, suffix)
	bounds := nmgr.streamBounds(suffix)
	subjects := []string{StreamSubject(nmgr.Microservice.InstanceId, suffix)}
	dupWindow := time.Duration(streams.DuplicateWindowSecondsFor(suffix)) * time.Second
	// Retry on connection/server errors so a few seconds of NATS lag on a cluster
	// restart degrades into a retry rather than a crash-loop (A6). A stream that
	// does not yet exist (ErrStreamNotFound) is the normal first-run case and is
	// handled by creating it, not retried.
	err := core.RetryInfraConnect(context.Background(), "nats jetstream", func(context.Context) error {
		info, err := nmgr.js.StreamInfo(name)
		if err == nil {
			// The stream exists — an older build (or an earlier ceiling) may have left
			// it unbounded, so reconcile in place. UpdateStream touches the byte/msg
			// ceilings, the captured subjects, the dedup window and the replica factor
			// on the existing config, leaving storage and retention untouched. Config
			// is the source of truth: an out-of-band tune (e.g. a raised MaxBytes set
			// via the nats CLI) is intentionally reverted to the config value on the
			// next restart — and shrinking a ceiling below current usage makes
			// DiscardOld immediately evict the overflow. Change the bound via config,
			// not out-of-band, to avoid that.
			//
			// On concurrency: the bounds, subject and window reconciles are idempotent
			// by construction — every pod computes the same desired value from the same
			// shared config, so concurrent updates converge and no advisory lock is
			// needed. The replica reconcile is NOT obviously in that class, because a
			// replica change is a RAFT peer-set reconfiguration rather than a config
			// write, and up to four services ensure some of these streams. It is left
			// unlocked deliberately and provisionally: the only lock available here is
			// the KV-backed DistributedLock, and taking it from inside the reconcile
			// path is not possible — NewDistributedLock calls KeyValueStore, which
			// calls reconcileKvBucket, which is this same path. Whether concurrent
			// upward updates need coordination at all is unmeasured; it is what A0's
			// Check C exists to settle, by rolling every service simultaneously during
			// the lift. If that shows contention, the coordination has to come from
			// somewhere outside this call path.
			cfg := info.Config
			boundsChanged := applyStreamBounds(&cfg, bounds)
			subjectsChanged := applyStreamSubjects(&cfg, subjects)
			dupChanged := applyStreamDuplicateWindow(&cfg, dupWindow)
			if boundsChanged || subjectsChanged || dupChanged {
				if _, err := nmgr.js.UpdateStream(&cfg); err != nil {
					return err
				}
				log.Info().Str("stream", name).Int64("maxBytes", bounds.maxBytes).
					Int64("maxMsgs", bounds.maxMsgs).Int32("maxMsgSize", bounds.maxMsgSize).
					Strs("subjects", cfg.Subjects).Bool("subjectsChanged", subjectsChanged).
					Dur("duplicateWindow", cfg.Duplicates).Bool("duplicateWindowChanged", dupChanged).
					Msg("Reconciled JetStream stream configuration")
			}
			// The replica factor is reconciled as its OWN update, and a failure here is
			// warned rather than returned. Two reasons, and both are about not letting
			// an availability improvement cause an availability regression.
			//
			// First, this is the one field whose update can fail for a reason that is
			// transient and expected during exactly the operation A0 enables: scaling
			// the NATS cluster and raising streamReplicas are two separate acts, so
			// mid-scale the broker reports a cluster name (so the clamp passes) while
			// having too few peers to place the new replicas, and rejects with
			// JSClusterNoPeersError. Returning that error would burn RetryInfraConnect
			// and then crashloop every stream-ensuring service until the cluster is
			// whole — turning a partially-completed HA rollout into an outage, which is
			// the opposite of the point. The KV path already treats the identical
			// failure as warn-and-continue.
			//
			// Second, fusing it into the update above would let a failed replica lift
			// block a subject or ceiling reconcile that would have succeeded on its own.
			// Those reconciles fix data-path correctness (a stream that captures the
			// wrong subjects drops messages); replication is a durability improvement
			// that can wait for the next restart. The lower-stakes change must not be
			// able to veto the higher-stakes one.
			//
			// 🔴 PASS cfg, NOT info.Config. cfg is the POST-update configuration;
			// info.Config is the snapshot taken before the update above. Because the
			// replica reconcile issues its own UpdateStream from whatever config it is
			// handed, passing the stale one writes the OLD ceilings and subjects back
			// over the reconcile that just succeeded — silently, and after its success
			// has already been logged. That is worse than the veto this split exists to
			// prevent: a veto at least leaves the stream alone. The KV path avoids the
			// same hazard by re-reading StreamInfo (kv.go); here the fresh config is
			// already in hand.
			nmgr.reconcileStreamReplicas(name, cfg)
			return nil
		} else if !errors.Is(err, nats.ErrStreamNotFound) {
			return err
		}
		cfg := &nats.StreamConfig{
			Name:      name,
			Subjects:  subjects,
			Storage:   nats.FileStorage,
			Retention: nats.LimitsPolicy,
			Discard:   nats.DiscardOld,
			MaxAge:    streamMaxAge,
			Replicas:  nmgr.effectiveStreamReplicas(),
		}
		applyStreamBounds(cfg, bounds)
		applyStreamDuplicateWindow(cfg, dupWindow)
		if _, err := nmgr.js.AddStream(cfg); err != nil {
			return err
		}
		log.Info().Str("stream", name).Int64("maxBytes", bounds.maxBytes).
			Int64("maxMsgs", bounds.maxMsgs).Int32("maxMsgSize", bounds.maxMsgSize).
			Msg("Created JetStream stream")
		return nil
	})
	if err != nil {
		return "", err
	}
	nmgr.trackStream(name)
	return name, nil
}

// reconcileStreamReplicas applies the replica factor to an existing stream as an
// isolated, best-effort update. Shared by ensureStream and reconcileKvBucket so a
// bucket and a message stream cannot drift apart in how they handle the same
// operation — they are the same object to JetStream, and the KV path had the
// lenient posture first.
func (nmgr *NatsManager) reconcileStreamReplicas(name string, current nats.StreamConfig) {
	desired := nmgr.desiredStreamReplicas()
	effective := nmgr.effectiveStreamReplicas()
	cfg := current
	changed, refusedDownward := applyStreamReplicas(&cfg, effective)
	if refusedDownward {
		// Log BOTH numbers. Logging only the clamped one under the name "configured"
		// is actively harmful here: when the clamp is engaged, an existing correctly
		// replicated stream reports "you configured 1" — which is false — attached to
		// advice to scale it down, which would de-replicate a healthy cluster.
		log.Warn().Str("stream", name).Int("current", current.Replicas).
			Int("configured", desired).Int("effective", effective).
			Msg("Stream or bucket has MORE replicas than this instance is configured for; leaving it " +
				"alone. A starting pod does not de-replicate — that drops RAFT peers and remaps every " +
				"durable consumer, and the usual way a pod is handed older config is a rollback of some " +
				"unrelated release. If `effective` is below `configured` the broker simply is not " +
				"clustered right now and nothing should be scaled down. Otherwise scale down " +
				"deliberately with `nats stream update`")
		return
	}
	if !changed {
		return
	}
	if _, err := nmgr.js.UpdateStream(&cfg); err != nil {
		log.Warn().Err(err).Str("stream", name).Int("from", current.Replicas).Int("to", effective).
			Msg("Could not raise the replica factor; it stays as-is until the next startup. This is " +
				"expected while the NATS cluster is still scaling — the broker reports a cluster but " +
				"cannot yet place the new replicas. The jetstream_replicas_desired/actual gauges will " +
				"disagree until it succeeds")
		return
	}
	log.Info().Str("stream", name).Int("from", current.Replicas).Int("to", effective).
		Msg("Raised the replica factor on an existing stream or bucket")
}

// trackStream records a stream this service has ensured, so the metrics sampler
// polls it. Deduped and mutex-guarded because SubscribeLive can ensure a stream at
// runtime concurrently with the sampler reading the set.
func (nmgr *NatsManager) trackStream(name string) {
	nmgr.streamMu.Lock()
	defer nmgr.streamMu.Unlock()
	for _, n := range nmgr.streamNames {
		if n == name {
			return
		}
	}
	nmgr.streamNames = append(nmgr.streamNames, name)
}

// trackedStreams returns a snapshot of the ensured stream names for the sampler.
func (nmgr *NatsManager) trackedStreams() []string {
	nmgr.streamMu.Lock()
	defer nmgr.streamMu.Unlock()
	return append([]string(nil), nmgr.streamNames...)
}

// trackKvBucket records a KV bucket this service has opened, by the name of the
// stream backing it, so the sampler can report its replication.
//
// Buckets are tracked separately from streams rather than added to streamNames
// because the two get different metrics — see streamMetrics.sample for why folding
// them together would turn a correctly-working bounded cache into a firing alert.
func (nmgr *NatsManager) trackKvBucket(bucket string) {
	name := kvStreamPrefix + bucket
	nmgr.streamMu.Lock()
	defer nmgr.streamMu.Unlock()
	for _, n := range nmgr.bucketNames {
		if n == name {
			return
		}
	}
	nmgr.bucketNames = append(nmgr.bucketNames, name)
}

// trackedBuckets returns a snapshot of the opened KV bucket stream names.
func (nmgr *NatsManager) trackedBuckets() []string {
	nmgr.streamMu.Lock()
	defer nmgr.streamMu.Unlock()
	return append([]string(nil), nmgr.bucketNames...)
}

// runStreamMetrics samples every ensured stream's fill, and every stream's and
// bucket's replication, on a fixed cadence until stopped — so both the ceilings'
// DiscardOld eviction (ADR-023) and a stream that is not actually replicated
// (ADR-020 A0) are observable rather than silent. It re-snapshots the tracked sets
// each tick to pick up anything a runtime SubscribeLive ensured after startup.
func (nmgr *NatsManager) runStreamMetrics() {
	defer nmgr.samplerWg.Done()
	ticker := time.NewTicker(streamMetricsSampleInterval)
	defer ticker.Stop()
	// Seed promptly, don't wait a full interval.
	nmgr.reportReplicaClamp()
	nmgr.metrics.sample(nmgr.js, nmgr.trackedStreams(), nmgr.trackedBuckets(), nmgr.desiredStreamReplicas(), nmgr.brokerIsClustered())
	for {
		select {
		case <-nmgr.stopSampler:
			return
		case <-ticker.C:
			nmgr.reportReplicaClamp()
			nmgr.metrics.sample(nmgr.js, nmgr.trackedStreams(), nmgr.trackedBuckets(), nmgr.desiredStreamReplicas(), nmgr.brokerIsClustered())
		}
	}
}

// ----------------
// Writer (producer)
// ----------------

// natsWriter publishes to a per-suffix subject, deriving the tenant-scoped
// subject from context at write time (fail-closed when no tenant is present).
type natsWriter struct {
	nmgr   *NatsManager
	suffix string
}

// NewWriter creates a producer for the given subject suffix. The stream backing
// the suffix is created if needed. The returned writer builds the fully-scoped
// subject ("{instance}.{tenant}.{suffix}") per message from the tenant in
// context.
func (nmgr *NatsManager) NewWriter(suffix string) (MessageWriter, error) {
	if _, err := nmgr.ensureStream(suffix); err != nil {
		return nil, err
	}
	w := &natsWriter{nmgr: nmgr, suffix: suffix}
	nmgr.writers = append(nmgr.writers, w)
	log.Info().Str("suffix", suffix).Msg("Added new NATS writer")
	return w, nil
}

// WriteMessages publishes each message to the writer's tenant-scoped subject.
// The tenant is taken from context and is the single source of the subject
// (fail-closed): a write with no tenant in context is rejected rather than
// published unscoped. All messages in one call share the caller's tenant, so
// the subject is derived once.
//
// The tenant is validated against the global token grammar (ADR-042) before it is
// spliced into the subject — the universal belt-and-suspenders behind ADR-025's
// broker grant. Every producer's tenant flows through here, and some of it
// originates from device-controlled addressing (an event-source derives it from an
// MQTT topic / HTTP path). A tenant carrying "." / "*" / ">" would shift subject
// segments or inject a cross-tenant wildcard, so a malformed tenant is rejected
// here rather than published to a corrupted subject. Legitimate tenants always
// pass: the same grammar is enforced when a tenant is created.
func (w *natsWriter) WriteMessages(ctx context.Context, msgs ...Message) error {
	// A per-device suffix has no meaningful tenant-wide subject: publishing there
	// would land outside the stream (which captures one more level) and, if it
	// somehow matched, would broadcast one device's message to every device. Refuse
	// rather than let the addressing be decided by which method a caller happened to
	// reach for.
	if IsPerDeviceSuffix(w.suffix) {
		return fmt.Errorf("messaging: %q is a per-device subject; use WriteToDevice", w.suffix)
	}
	// A capture stream's producer is the DEVICE, via the broker's MQTT gateway
	// (ADR-030 amendment). Its suffix appears in no subject, so publishing here
	// would address "{instance}.{tenant}.{suffix}" — a subject that matches no
	// stream, which JetStream reports as a publish error only because the stream
	// lookup fails, and which a caller ignoring the error would lose silently.
	// Refuse it outright: there is no correct way to write to this stream.
	if streams.ShapeOf(w.suffix) == streams.ShapeDeviceEvents {
		return fmt.Errorf("messaging: %q is a device-events capture stream written by the broker's "+
			"MQTT gateway, not by the platform; it has no publish path", w.suffix)
	}
	return w.publish(ctx, "", msgs...)
}

// WriteToDevice publishes to the per-device subject for deviceToken.
func (w *natsWriter) WriteToDevice(ctx context.Context, deviceToken string, msgs ...Message) error {
	if deviceToken == "" {
		return fmt.Errorf("messaging: refusing to publish a device-scoped message with no device token")
	}
	// The device token is spliced into the subject, so it gets the same grammar
	// check the tenant does: a token carrying "." / "*" / ">" would shift subject
	// levels and could address messages outside its own device (ADR-042 / ADR-025).
	if err := core.ValidateToken(deviceToken); err != nil {
		return fmt.Errorf("messaging: refusing to publish to a subject for an invalid device token: %w", err)
	}
	return w.publish(ctx, deviceToken, msgs...)
}

// publish resolves the subject and writes. An empty deviceToken means the
// tenant-scoped subject.
func (w *natsWriter) publish(ctx context.Context, deviceToken string, msgs ...Message) error {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return core.ErrNoTenant
	}
	if err := core.ValidateToken(tenant); err != nil {
		return fmt.Errorf("messaging: refusing to publish to a subject for an invalid tenant: %w", err)
	}
	subject := ScopedSubject(w.nmgr.Microservice.InstanceId, tenant, w.suffix)
	if deviceToken != "" {
		subject = DeviceScopedSubject(w.nmgr.Microservice.InstanceId, tenant, w.suffix, deviceToken)
	}
	for i := range msgs {
		nm := &nats.Msg{Subject: subject, Data: msgs[i].Value, Header: nats.Header{}}
		// Carry the correlation id, generating one when the producer did not
		// propagate it, so any message can be followed across the pipeline (E15).
		cid := msgs[i].CorrelationID()
		if cid == "" {
			cid = uuid.NewString()
		}
		nm.Header.Set(HeaderCorrelationID, cid)
		// The dedup id goes on before the caller's own headers are copied, and the
		// copy skips it, so a producer cannot set Nats-Msg-Id by hand through the
		// Headers map and bypass the reasoning on Message.DedupID.
		if msgs[i].DedupID != "" {
			nm.Header.Set(nats.MsgIdHdr, msgs[i].DedupID)
		}
		for k, v := range msgs[i].Headers {
			if k != HeaderCorrelationID && k != nats.MsgIdHdr {
				nm.Header.Set(k, v)
			}
		}
		if _, err := w.nmgr.js.PublishMsg(nm); err != nil {
			return err
		}
	}
	return nil
}

// HandleResponse logs the result of a write operation.
func (w *natsWriter) HandleResponse(err error) {
	if err != nil {
		log.Error().Err(err).Str("suffix", w.suffix).Msg("nats write operation failed")
	} else if log.Debug().Enabled() {
		log.Debug().Str("suffix", w.suffix).Msg("nats write operation successful")
	}
}

// ----------------
// Reader (consumer)
// ----------------

// natsReader is a durable pull-consumer over the cross-tenant wildcard subject
// for a suffix. The shared microservice consumes all tenants' messages here and
// derives the per-message tenant from the delivered subject.
type natsReader struct {
	nmgr    *NatsManager
	suffix  string
	stream  string
	subject string
	durable string
	// sub is the current pull subscription. It is written by the read-loop goroutine
	// (bind, on first attach and on self-heal re-bind) and read by the lifecycle
	// goroutine (ExecuteStop's Unsubscribe), so it is an atomic pointer rather than a
	// plain field — the self-heal made it mutable across goroutines.
	sub atomic.Pointer[nats.Subscription]
	// gate pauses consumption until the service's data plane is ready (ADR-022
	// decision 3): a degraded service parks in ReadMessage instead of draining
	// messages without live auth.
	gate *core.ReadinessGate
	// pending buffers the remainder of the last batch Fetch so ReadMessage can
	// hand messages out one at a time while fetching in batches (B1). Messages
	// are not acked here; the ack handle rides the returned Message (A3).
	pending []*nats.Msg
	// consecutiveTimeouts counts empty fetches since the last delivery, driving the
	// periodic consumer-liveness probe. Only the single read-loop goroutine touches
	// it, so it needs no synchronization.
	consecutiveTimeouts int
	// deliverNew, when set, creates the durable at the stream tail (DeliverNewPolicy)
	// instead of the default DeliverAll — see ReaderWithDeliverNew.
	deliverNew bool
}

// ReaderOption tunes a reader's durable consumer at creation time. Options only
// affect the FIRST creation of a given durable: the consumer config is frozen on the
// server, so an option that changes consumerConfig will make AddConsumer reject an
// already-created durable and crash-loop startup on a non-fresh cluster (see the
// warning on consumerConfig). Changing an option therefore rides a fresh bring-up
// (down+up) or an explicit consumer migration — the pre-GA decisive cutover.
type ReaderOption func(*natsReader)

// ReaderWithDeliverNew starts the durable at the stream tail (DeliverNewPolicy) on
// first creation instead of replaying the retained stream (the default DeliverAll).
// Use it for a consumer where replaying history on first enable would be harmful —
// e.g. the notification dispatcher, where DeliverAll would page humans about every
// alarm retained in the stream (up to streamMaxAge) the first time the service is
// enabled on a running fleet. Downtime-safety is unaffected: once the durable exists
// its ack cursor persists, so a restart still resumes from the last ack, not the tail.
func ReaderWithDeliverNew() ReaderOption {
	return func(r *natsReader) { r.deliverNew = true }
}

// consumerConfig is the durable pull-consumer configuration (A4) — explicit-ack so a
// message is only removed once a consumer acks it after durable handling (A3), a
// deliberate AckWait sized to persistence latency, a finite MaxDeliver so a poison
// message stops redelivering, and the cross-tenant wildcard filter.
//
// Every field here MUST match what the prior PullSubscribe(subject, durable,
// AckExplicit, AckWait, MaxDeliver) created, so that AddConsumer is idempotent
// against a durable an older build already created on an existing cluster (nats.go's
// checkConfig only compares fields the config sets, and rejects a mismatch). In
// particular: DeliverPolicy is left at its zero value (DeliverAll) to match, and
// MaxAckPending is pinned to the value the old PullSubscribe path used implicitly
// (its subscription channel capacity) rather than left unset — unset would inherit
// the server default (1000) on freshly-created consumers while upgraded-in-place
// durables kept the old value, a silent config fork. WARNING: changing any field
// here (or adding a newly-compared one) will make AddConsumer reject an existing
// durable and crash-loop startup on a non-fresh cluster — a config change must ride a
// fresh bring-up (down+up) or an explicit consumer migration (pre-GA: prefer the
// decisive cutover).
func (r *natsReader) consumerConfig() *nats.ConsumerConfig {
	cfg := &nats.ConsumerConfig{
		Durable:       r.durable,
		AckPolicy:     nats.AckExplicitPolicy,
		AckWait:       ackWait,
		MaxDeliver:    MaxDeliver,
		MaxAckPending: readerMaxAckPending,
		FilterSubject: r.subject,
	}
	// DeliverPolicy is otherwise left at its zero value (DeliverAll) so AddConsumer
	// stays idempotent against durables older builds created without setting it; a
	// reader that opted into ReaderWithDeliverNew pins DeliverNew on its own durable
	// (a distinct, per-service consumer, so it forks no shared config).
	if r.deliverNew {
		cfg.DeliverPolicy = nats.DeliverNewPolicy
	}
	return cfg
}

// bind (re)creates the durable consumer if needed and binds a pull subscription to
// it WITHOUT owning its lifecycle. This is the fix for the rolling-update hazard: a
// pull subscription that CREATED its consumer deletes it on Unsubscribe/Drain
// (nats.go sets the delete-consumer flag), so during a rolling update — where the
// old and new pods briefly share this one durable — the terminating pod would delete
// the consumer out from under the new pod, which then hot-spins on a dead
// subscription. Creating the consumer out-of-band (AddConsumer is idempotent for a
// matching durable) and attaching with nats.Bind leaves the client's delete flag off,
// so the durable survives every pod's shutdown. bind is also the self-heal path:
// ReadMessage calls it to re-attach if the consumer ever does go away.
func (r *natsReader) bind() error {
	// Release any stale subscription first. With a bound sub this does not delete the
	// durable; it just releases the old (dead) subscription on a re-bind. The old
	// pointer stays published until the new one is ready, so a concurrent
	// ExecuteStop always sees a non-nil, safe-to-Unsubscribe subscription.
	if old := r.sub.Load(); old != nil {
		_ = old.Unsubscribe()
	}
	if _, err := r.nmgr.js.AddConsumer(r.stream, r.consumerConfig()); err != nil {
		return err
	}
	sub, err := r.nmgr.js.PullSubscribe(r.subject, r.durable, nats.Bind(r.stream, r.durable))
	if err != nil {
		return err
	}
	r.sub.Store(sub)
	return nil
}

// NewReader creates a durable pull consumer for the given subject suffix,
// subscribing to the cross-tenant wildcard so one shared pod drains every
// tenant. The durable name is scoped to the instance + functional area + suffix
// (not the tenant), so every replica of a service shares one consumer and each
// message is delivered to exactly one of them.
func (nmgr *NatsManager) NewReader(suffix string, opts ...ReaderOption) (MessageReader, error) {
	stream, err := nmgr.ensureStream(suffix)
	if err != nil {
		return nil, err
	}
	r := &natsReader{
		nmgr:    nmgr,
		suffix:  suffix,
		stream:  stream,
		subject: StreamSubject(nmgr.Microservice.InstanceId, suffix),
		durable: DurableName(nmgr.Microservice.InstanceId, nmgr.Microservice.FunctionalArea, suffix),
		gate:    nmgr.Microservice.Readiness,
	}
	for _, opt := range opts {
		opt(r)
	}
	if err := r.bind(); err != nil {
		return nil, err
	}
	nmgr.readers = append(nmgr.readers, r)
	log.Info().Str("durable", r.durable).Str("subject", r.subject).Msg("Added new NATS reader")
	return r, nil
}

// natsReplayReader is the JetStream implementation of ReplayReader: an EPHEMERAL
// pull consumer pinned to a start sequence, drained in order up to a fixed head
// sequence. Unlike the durable reader it owns and acks its own consumer (the acks
// only advance the throwaway ephemeral, which is deleted on Close / by
// InactiveThreshold) — it exists purely to feed the engine the exact, ordered
// prefix it missed, then it is discarded and the durable live reader takes over.
type natsReplayReader struct {
	sub      *nats.Subscription
	js       nats.JetStreamContext
	stream   string
	head     uint64
	doneSeq  uint64
	pending  []*nats.Msg
	timeouts int
}

// replayInactiveThreshold auto-deletes the ephemeral replay consumer if the reader
// dies without Close (crash mid-replay), so orphaned consumers do not accumulate.
const replayInactiveThreshold = 5 * time.Minute

// maxReplayStuckFetches bounds how long replay waits on a broker that reports
// messages retained up to the head yet delivers none — a stuck/broken broker, not a
// drained one. Reaching it fails startup LOUDLY rather than silently ending replay
// short of the head (which would reintroduce out-of-order live delivery for the
// unread tail). At fetchTimeout per fetch this is ~tens of seconds.
const maxReplayStuckFetches = 30

// NewReplayReader opens an ephemeral, in-order read of the suffix's stream from
// startSeq up to the current head, returning the reader and that head sequence. When
// startSeq is already past the head (nothing to replay, or a snapshot ahead of a
// re-created/truncated stream) it returns a reader that immediately reports io.EOF
// and creates no consumer. The head is captured once, at open, so live messages
// published during replay are left for the durable reader.
func (nmgr *NatsManager) NewReplayReader(suffix string, startSeq uint64) (ReplayReader, uint64, error) {
	name := StreamName(nmgr.Microservice.InstanceId, suffix)
	info, err := nmgr.js.StreamInfo(name)
	if err != nil {
		if errors.Is(err, nats.ErrStreamNotFound) {
			return &natsReplayReader{}, 0, nil // empty/absent stream: nothing to replay
		}
		return nil, 0, err
	}
	head := info.State.LastSeq
	if startSeq > head {
		return &natsReplayReader{head: head, doneSeq: head}, head, nil
	}
	subject := StreamSubject(nmgr.Microservice.InstanceId, suffix)
	sub, err := nmgr.js.PullSubscribe(subject, "", // empty durable => ephemeral consumer
		nats.BindStream(name),
		nats.StartSequence(startSeq),
		nats.AckExplicit(),
		nats.InactiveThreshold(replayInactiveThreshold))
	if err != nil {
		return nil, 0, err
	}
	log.Info().Str("stream", name).Uint64("startSeq", startSeq).Uint64("head", head).
		Msg("Opened ordered replay reader")
	return &natsReplayReader{sub: sub, js: nmgr.js, stream: name, head: head}, head, nil
}

// NewReplayReaderFromTime opens an ephemeral, in-order read of the suffix's stream from the
// first message published at/after startTime up to the current head, for the replay-preview
// harness (ADR-053 slice 9d). It mirrors NewReplayReader's isolation exactly — an ephemeral
// pull consumer (empty durable, InactiveThreshold-reaped) that acks only its own throwaway
// consumer, never the production durable — but starts by JetStream publish TIME rather than a
// sequence, since a preview names a time window, not a sequence.
//
// It returns the stream's earliest retained message time (FirstTime) so the caller can DETECT
// the aged-out case: when the requested startTime predates FirstTime, JetStream silently clamps
// the start to the first retained message, so the caller compares startTime to the returned
// FirstTime and reports a degraded (history-evicted) preview rather than a misleadingly-empty
// one. When the stream is absent/empty, or startTime is past the last retained message (an empty
// window), it returns a reader that immediately reports io.EOF and creates no consumer — so a
// future-dated or empty window degrades to zero firings cleanly instead of stalling on a
// consumer that will never deliver.
//
// NB: JetStream StartTime keys on the message's PUBLISH time (≈ the event's processed/arrival
// time), not its logical OccurredTime — so the caller must still filter delivered events to its
// occurred-time window; this only bounds the scan's lower edge.
func (nmgr *NatsManager) NewReplayReaderFromTime(suffix string, startTime time.Time) (ReplayReader, time.Time, error) {
	name := StreamName(nmgr.Microservice.InstanceId, suffix)
	info, err := nmgr.js.StreamInfo(name)
	if err != nil {
		if errors.Is(err, nats.ErrStreamNotFound) {
			return &natsReplayReader{}, time.Time{}, nil // empty/absent stream: nothing to replay
		}
		return nil, time.Time{}, err
	}
	head := info.State.LastSeq
	firstTime := info.State.FirstTime
	// Empty stream, or a window that starts after the last retained event: nothing to replay.
	// Returning a doneSeq==head reader makes the first Read report io.EOF without a consumer,
	// so an empty/future window can never stall on a consumer that will never deliver.
	if head == 0 || startTime.After(info.State.LastTime) {
		return &natsReplayReader{head: head, doneSeq: head}, firstTime, nil
	}
	subject := StreamSubject(nmgr.Microservice.InstanceId, suffix)
	sub, err := nmgr.js.PullSubscribe(subject, "", // empty durable => ephemeral consumer
		nats.BindStream(name),
		nats.StartTime(startTime),
		nats.AckExplicit(),
		nats.InactiveThreshold(replayInactiveThreshold))
	if err != nil {
		return nil, time.Time{}, err
	}
	log.Info().Str("stream", name).Time("startTime", startTime).Uint64("head", head).
		Msg("Opened time-started replay reader (preview)")
	return &natsReplayReader{sub: sub, js: nmgr.js, stream: name, head: head}, firstTime, nil
}

// Read returns the next message in ascending stream order, or io.EOF once every
// message through the head has been delivered. A message whose sequence is past the
// head is a live publish that arrived after open; it is NOT delivered here (the
// durable live reader owns it), so replay stays a clean, finite prefix.
func (r *natsReplayReader) Read(ctx context.Context) (Message, error) {
	if r.doneSeq >= r.head || r.sub == nil {
		return Message{}, io.EOF
	}
	for {
		if err := ctx.Err(); err != nil {
			return Message{}, io.EOF
		}
		if len(r.pending) == 0 {
			msgs, err := r.sub.Fetch(fetchBatch, nats.MaxWait(fetchTimeout))
			if err != nil {
				if errors.Is(err, nats.ErrTimeout) {
					// A timeout means nothing is deliverable this instant. Distinguish a
					// genuinely DRAINED range (the messages up to head aged out, MaxAge)
					// from transient broker slowness by consulting the stream: if its
					// earliest retained sequence is already past the head, the whole replay
					// range is gone — end replay cleanly. Otherwise messages in range still
					// exist, so keep waiting; NEVER end replay early on transient slowness,
					// which would leave a tail for the (unordered) live reader and
					// reintroduce the out-of-order hazard this replay exists to prevent. A
					// broker that retains through head yet delivers nothing for
					// maxReplayStuckFetches fails startup loudly rather than truncating.
					if info, ierr := r.js.StreamInfo(r.stream); ierr == nil && info.State.FirstSeq > r.head {
						log.Warn().Uint64("doneSeq", r.doneSeq).Uint64("head", r.head).
							Uint64("firstSeq", info.State.FirstSeq).
							Msg("Replay range fully evicted (aged out below head); ending replay.")
						r.doneSeq = r.head
						return Message{}, io.EOF
					}
					r.timeouts++
					if r.timeouts > maxReplayStuckFetches {
						return Message{}, fmt.Errorf("replay stalled: stream %s retains messages through head %d but delivered none in %d fetches",
							r.stream, r.head, r.timeouts)
					}
					continue
				}
				return Message{}, err
			}
			r.timeouts = 0
			r.pending = msgs
		}
		nm := r.pending[0]
		r.pending = r.pending[1:]
		seq, deliv, appended := msgMeta(nm)
		if seq > r.head {
			// A live message past the captured head: stop replay here and leave it (and
			// everything after) to the durable reader. Do not ack — it is not ours.
			r.doneSeq = r.head
			return Message{}, io.EOF
		}
		// Ack advances (and lets InactiveThreshold reap) the throwaway ephemeral.
		_ = nm.Ack()
		r.doneSeq = seq
		msg := NewConsumedMessage(nm.Subject, nm.Data, deliv, natsHeaders(nm), nil)
		msg.StreamSeq = seq
		msg.AppendTime = appended
		return msg, nil
	}
}

// Close releases the ephemeral consumer (best-effort; InactiveThreshold reaps it
// anyway if this is missed).
func (r *natsReplayReader) Close() error {
	if r.sub != nil {
		return r.sub.Unsubscribe()
	}
	return nil
}

// isConsumerGone reports whether a Fetch error means the durable consumer no longer
// exists (deleted, not found, inactive) or is unreachable (no responders) — the
// conditions the reader self-heals from by re-binding, as opposed to a transient
// timeout or a shutdown-time connection close.
func isConsumerGone(err error) bool {
	return errors.Is(err, nats.ErrConsumerDeleted) ||
		errors.Is(err, nats.ErrConsumerNotFound) ||
		errors.Is(err, nats.ErrConsumerNotActive) ||
		errors.Is(err, nats.ErrNoResponders)
}

// rebindWithBackoff re-attaches the reader to its durable consumer (recreating it if
// needed), retrying with capped exponential backoff until it succeeds or ctx is
// cancelled (shutdown). It never gives up on its own: a consumer the service is
// meant to be draining should be restored, not abandoned.
func (r *natsReader) rebindWithBackoff(ctx context.Context) error {
	backoff := rebindBackoffMin
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if err := r.bind(); err != nil {
			// A closing/draining connection means the service is shutting down (or the
			// broker went away): stop trying and let the caller unwind as EOF, rather
			// than looping forever — some readers stop by draining the connection
			// instead of cancelling this context (e.g. command-delivery).
			if errors.Is(err, nats.ErrConnectionClosed) || errors.Is(err, nats.ErrConnectionDraining) {
				return err
			}
			log.Error().Err(err).Str("durable", r.durable).Msg("Re-bind of durable consumer failed; will retry")
			if backoff *= 2; backoff > rebindBackoffMax {
				backoff = rebindBackoffMax
			}
			continue
		}
		log.Info().Str("durable", r.durable).Msg("Re-bound durable consumer")
		return nil
	}
}

// ReadMessage returns the next message, blocking until one is available, the
// context is cancelled, or the subscription closes. Messages are fetched in
// batches (B1) and buffered, so most calls return from the buffer without a
// round trip. The message is NOT acked here (A3): its ack handle rides the
// returned envelope so the consumer can Ack only after durably handling it. A
// transient failure is retried by NOT acking, so AckWait paces redelivery (there
// is deliberately no Nak — see Message). On shutdown (ctx cancelled or
// subscription/connection closed) it returns io.EOF so the existing processor EOF
// handling applies.
func (r *natsReader) ReadMessage(ctx context.Context) (Message, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Message{}, io.EOF
		}
		// Stay parked until the data plane is released (ADR-022 decision 3). A
		// cancelled context here means shutdown, which surfaces as EOF below.
		if r.gate != nil {
			if err := r.gate.WaitReady(ctx); err != nil {
				return Message{}, io.EOF
			}
		}
		if len(r.pending) == 0 {
			msgs, err := r.sub.Load().Fetch(fetchBatch, nats.MaxWait(fetchTimeout))
			if err != nil {
				if errors.Is(err, nats.ErrTimeout) {
					// An empty fetch. A run of them can also mean the consumer was
					// deleted without the next fetch surfacing an explicit error, so
					// periodically confirm it still exists and re-bind if it has gone.
					r.consecutiveTimeouts++
					if r.consecutiveTimeouts >= livenessProbeAfterTimeouts {
						r.consecutiveTimeouts = 0
						if _, cerr := r.nmgr.js.ConsumerInfo(r.stream, r.durable); errors.Is(cerr, nats.ErrConsumerNotFound) {
							log.Warn().Str("durable", r.durable).Msg("Durable consumer missing on liveness probe; re-binding")
							if rebindErr := r.rebindWithBackoff(ctx); rebindErr != nil {
								return Message{}, io.EOF
							}
						}
					}
					continue
				}
				r.consecutiveTimeouts = 0
				if errors.Is(err, nats.ErrConnectionClosed) ||
					errors.Is(err, nats.ErrSubscriptionClosed) ||
					errors.Is(err, nats.ErrConnectionDraining) {
					return Message{}, io.EOF
				}
				// The durable consumer went away underneath us — e.g. an older pod's
				// Unsubscribe during a rolling update, or a broker restart. Re-bind to
				// it (recreating it if needed) with bounded backoff instead of
				// hot-spinning on a dead subscription. A cancelled context during the
				// backoff means shutdown, surfaced as EOF.
				//
				// Note the at-least-once cost: if the consumer was truly deleted, the
				// re-created consumer starts at DeliverAll and replays retained (up to
				// streamMaxAge) messages once. This happens on the FIRST rollout onto
				// this fix, because the terminating old-code pod still deletes the
				// durable it owned; from then on the durable survives and there is no
				// replay. A fresh bring-up (down+up) avoids the one-time transition
				// replay entirely.
				if isConsumerGone(err) {
					log.Warn().Err(err).Str("durable", r.durable).Msg("Durable consumer unavailable; re-binding")
					if rebindErr := r.rebindWithBackoff(ctx); rebindErr != nil {
						return Message{}, io.EOF
					}
					continue
				}
				return Message{}, err
			}
			if len(msgs) == 0 {
				continue
			}
			r.consecutiveTimeouts = 0
			r.pending = msgs
		}
		nm := r.pending[0]
		r.pending = r.pending[1:]
		seq, deliv, appended := msgMeta(nm)
		msg := NewConsumedMessage(nm.Subject, nm.Data, deliv, natsHeaders(nm), natsAck{nm: nm})
		msg.StreamSeq = seq
		msg.AppendTime = appended
		return msg, nil
	}
}

// SubscribeLive opens an ephemeral, tenant-scoped fan-out subscription over a
// suffix's live subject, for streaming events to a connected client (the
// GraphQL subscription bridge, ADR-037/ADR-039). Unlike NewReader's durable,
// load-balanced pull consumer, each SubscribeLive is its own core NATS
// subscription bound to a single tenant's subject ("{instance}.{tenant}.{suffix}"):
// every subscriber receives every message for its tenant (fan-out, not
// load-balanced), there are no acks, and there is no backlog replay — a client
// sees events from subscribe time onward. The subscription is torn down when ctx
// is cancelled (the client unsubscribed or the socket closed). A slow reader
// drops messages (bounded buffer) rather than stalling the pipeline.
func (nmgr *NatsManager) SubscribeLive(ctx context.Context, tenant string, suffix string) (<-chan Message, error) {
	// Validate the tenant before it becomes a subscription filter — the read-side
	// twin of the WriteMessages guard, and the higher-blast-radius one: a tenant of
	// "*"/">" here would subscribe across EVERY tenant's live feed, not just corrupt
	// one publish. Legitimate tenants (a verified JWT claim, grammar-checked at
	// creation) always pass; a malformed one is rejected rather than fanned in.
	if err := core.ValidateToken(tenant); err != nil {
		return nil, fmt.Errorf("messaging: refusing to subscribe to a subject for an invalid tenant: %w", err)
	}
	// A per-device suffix has no tenant-scoped subject to listen on: nothing
	// publishes there, so a live subscription would sit silent forever with no error
	// — the same silent class the per-device addressing exists to remove. Listen
	// across that tenant's devices instead.
	subject := ScopedSubject(nmgr.Microservice.InstanceId, tenant, suffix)
	if IsPerDeviceSuffix(suffix) {
		subject += ".*"
	}
	raw := make(chan *nats.Msg, liveBuffer)
	sub, err := nmgr.nc.ChanSubscribe(subject, raw)
	if err != nil {
		return nil, err
	}
	out := make(chan Message)
	go func() {
		defer close(out)
		defer func() { _ = sub.Unsubscribe() }()
		for {
			select {
			case <-ctx.Done():
				return
			case nm, ok := <-raw:
				if !ok {
					return
				}
				// A live message is never acked (no acknowledger): it is a
				// fire-and-forget fan-out to the connected client, not a
				// durable-processing handoff. Headers are dropped (nil): the live
				// feed's consumer reads only the payload, so building a per-message
				// header map would be wasted work on the hot path.
				msg := NewConsumedMessage(nm.Subject, nm.Data, 0, nil, nil)
				select {
				case out <- msg:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	log.Info().Str("subject", subject).Msg("Opened live NATS subscription")
	return out, nil
}

// natsHeaders flattens a delivered message's NATS headers into the transport-
// neutral map carried on the envelope (E15), or nil when there are none.
func natsHeaders(nm *nats.Msg) map[string]string {
	if len(nm.Header) == 0 {
		return nil
	}
	headers := make(map[string]string, len(nm.Header))
	for k := range nm.Header {
		headers[k] = nm.Header.Get(k)
	}
	return headers
}

// msgMeta returns the JetStream stream sequence, delivery-attempt count and
// append time of a consumed message in a single metadata parse (the reply subject
// is parsed once, not once per field). All three are zero when metadata is
// unavailable — a non-JetStream message — which callers treat as "no durable
// position / first delivery / no send time".
//
// The stream sequence is the durable, gapless position a checkpointing consumer
// records to dedup replays (ADR-051); a 0 never matches a real stream sequence, so
// a metadata-less message is treated by the DETECT consumer as unprocessable rather
// than as a valid checkpoint.
//
// The append time is the broker's own timestamp for when the message was written
// to the stream, which for the ingest capture stream is when the device published
// it — so it is the tenant's SEND time, and the clock a drain-admission gate must
// meter against rather than the time we got round to reading it (ADR-030 I4).
func msgMeta(nm *nats.Msg) (streamSeq uint64, numDelivered int, appendTime time.Time) {
	md, err := nm.Metadata()
	if err != nil {
		return 0, 0, time.Time{}
	}
	return md.Sequence.Stream, int(md.NumDelivered), md.Timestamp
}

// HandleResponse logs the result of a read operation.
func (r *natsReader) HandleResponse(err error) {
	if err != nil {
		log.Error().Err(err).Str("suffix", r.suffix).Msg("nats read operation failed")
	} else if log.Debug().Enabled() {
		log.Debug().Str("suffix", r.suffix).Msg("nats read operation successful")
	}
}

// Backlog reports this reader's durable consumer's UNDELIVERED count (pending) and
// DELIVERED-BUT-UNACKED count (ackPending) from the broker's ConsumerInfo — the authoritative
// "is anyone still working the tail?" signal. Both zero means the durable is fully drained:
// nothing waits on the stream AND nothing is in flight at any consumer instance. It is the
// positive "caught up to the live tail" evidence a wall-clock idle-advance requires before it
// may fire a silent-series timer (ADR-051 slice 4c) — inferring caught-up from a quiet read
// loop is unsound, because the loop is ALSO quiet during a broker outage or a consumer re-bind
// while a backlog piles up. ackPending additionally exposes messages a PEER consumer took
// during a rolling-update overlap (invisible to pending), narrowing the split-brain window
// before the Slice-6 singleton deploy closes it. An error (broker unreachable, consumer gone)
// is surfaced so the caller fails safe. It queries ConsumerInfo directly and touches none of
// the read-loop goroutine's mutable state (stream and durable are immutable after creation),
// so it is safe to call concurrently with the read loop's Fetch. (pending also backs the
// Slice-8 consumer-lag operations gauge.)
func (r *natsReader) Backlog(ctx context.Context) (pending, ackPending uint64, err error) {
	ci, err := r.nmgr.js.ConsumerInfo(r.stream, r.durable, nats.Context(ctx))
	if err != nil {
		return 0, 0, err
	}
	return ci.NumPending, uint64(ci.NumAckPending), nil
}

// ----------------
// Lifecycle
// ----------------

// Initialize component.
func (nmgr *NatsManager) Initialize(ctx context.Context) error {
	return nmgr.lifecycle.Initialize(ctx)
}

// connectionEventHandlers logs the client's connection lifecycle.
//
// Without these, a broker outage is COMPLETELY SILENT on the client side. The
// options above are MaxReconnects(-1) and RetryOnFailedConnect(true) — deliberate,
// and right, because a service that dies when the broker blinks is worse than one
// that waits — but their combined effect is that the library absorbs every
// disconnect, retries forever, and says nothing. A service can sit unable to
// publish for an hour while its logs show only the silence of a service with
// nothing to do, which are indistinguishable.
//
// That gap widens with ADR-020 A0 rather than closing: the point of a 3-node
// cluster is to survive losing a node, and surviving it means clients disconnect
// and reconnect somewhere else. Those are the events that say whether failover
// happened at all, how long it took, and — via ConnectedUrl — whether the client
// actually landed on a different server or is looping against the same one.
//
// Logs, not metrics, and deliberately: what makes these useful is the WHICH and
// the WHY (which server, which error), and a counter cannot carry either. The
// broker-side counterpart is the prometheus-nats-exporter (nats_prom_exporter),
// which reports the same events from the server's view; a disconnect visible in
// one and not the other localizes the fault to the network between them.
func (nmgr *NatsManager) connectionEventHandlers() []nats.Option {
	area := nmgr.Microservice.FunctionalArea
	return []nats.Option{
		// The error is NON-nil for everything that actually loses a broker — io.EOF,
		// connection reset, a stale connection — and nil only when the client itself
		// closed. Lame-duck is NOT the nil case: the server announces it, then closes
		// the socket, and the read loop surfaces io.EOF like any other drop. (An
		// earlier version of this comment had that backwards.) Logged at warn either
		// way: the client is not connected, which is the fact that matters.
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			log.Warn().Str("area", area).Err(err).Str("server", nmgr.lastConnectedServer()).
				Msg("Disconnected from NATS; retrying indefinitely (MaxReconnects is unlimited). " +
					"Publishes are buffered up to the client's pending limit and then fail; " +
					"consumers stop receiving until the connection is restored")
		}),
		// The recovery, and the one that carries the interesting datum: which server
		// the client came back on. In a clustered broker a reconnect to a DIFFERENT
		// URL is failover working; repeated reconnects to the SAME one are a flapping
		// server rather than a node loss, and the two want opposite responses.
		// The FIRST successful connect does not come through ReconnectHandler.
		//
		// Under RetryOnFailedConnect the client fires ConnectedCB for the initial
		// attach and ReconnectedCB only afterwards — so without this handler, a
		// service that starts while the broker is down logs nothing at all when the
		// broker finally arrives. That is precisely the silence these handlers exist
		// to end, in the one case (a cold start of the whole instance, where NATS and
		// the services race) that A0's own rollout makes routine.
		nats.ConnectHandler(func(nc *nats.Conn) {
			nmgr.connectedServer.Store(nc.ConnectedUrl())
			log.Info().Str("area", area).Str("server", nc.ConnectedUrl()).
				Str("cluster", nc.ConnectedClusterName()).
				Msg("Connected to NATS")
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			nmgr.connectedServer.Store(nc.ConnectedUrl())
			log.Info().Str("area", area).Str("server", nc.ConnectedUrl()).
				Str("cluster", nc.ConnectedClusterName()).
				Msg("Reconnected to NATS. Durable consumers resume from their last ack; " +
					"anything the client had buffered while disconnected has been flushed")
		}),
		// Terminal. Unlimited reconnects do not make this unreachable: besides an
		// explicit Close, the client ABORTS the retry loop on a repeated
		// authorization error (nats.go sets `ar` on the second identical auth
		// failure unless IgnoreAuthErrorAbort), and an unparsed server -ERR closes
		// outright. A rotated or revoked broker credential is therefore an ordinary
		// route here, not an exotic one — which is worth knowing, because it is the
		// case where "restart the process" is the wrong advice and "fix the
		// credential" is the right one.
		//
		// Logged at ERROR when it was not asked for: the connection will not come
		// back on its own and every publish and subscription on it is dead. It is the
		// one connection event that is not self-healing, which is exactly why it must
		// not share a level with the two above.
		nats.ClosedHandler(func(nc *nats.Conn) {
			if nmgr.shuttingDown.Load() {
				log.Info().Str("area", area).Msg("NATS connection closed during shutdown")
				return
			}
			log.Error().Str("area", area).Err(nc.LastError()).
				Msg("NATS connection CLOSED permanently and NOT as part of a shutdown; it will not " +
					"reconnect. This service is now mute: no publishes, no deliveries, and no " +
					"further retries. The process must be restarted")
		}),
	}
}

// lastConnectedServer returns the broker URL last known to be connected, or the
// configured URL if none has been observed yet.
func (nmgr *NatsManager) lastConnectedServer() string {
	if v, ok := nmgr.connectedServer.Load().(string); ok && v != "" {
		return v
	}
	return nmgr.NatsUrl()
}

// ExecuteInitialize connects to NATS and obtains a JetStream context.
func (nmgr *NatsManager) ExecuteInitialize(context.Context) error {
	url := nmgr.NatsUrl()
	natscfg := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	opts := []nats.Option{
		nats.Name(nmgr.Microservice.FunctionalArea),
		nats.MaxReconnects(-1),
		nats.RetryOnFailedConnect(true),
	}
	opts = append(opts, nmgr.connectionEventHandlers()...)
	// When the broker terminates TLS (ADR-025) every client must dial over TLS or
	// the handshake on the TLS-required port fails; verify the server against the
	// CA threaded into the instance config.
	tlsConfig, err := natscfg.TLSConfig(natscfg.Hostname)
	if err != nil {
		return err
	}
	if tlsConfig != nil {
		opts = append(opts, nats.Secure(tlsConfig))
	}
	// Present the shared service credential once broker auth is enabled (ADR-025);
	// internal services are exempt from the device callout via auth_users, so this
	// static login is what places them in the APP account with full permissions.
	if natscfg.Auth.User != "" {
		opts = append(opts, nats.UserInfo(natscfg.Auth.User, natscfg.Auth.Password))
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return err
	}
	// 🔴 nats.Connect HAS NOT NECESSARILY CONNECTED YET.
	//
	// RetryOnFailedConnect(true) — set above so a service starting before the
	// broker keeps trying instead of crashlooping — makes Connect return a non-nil
	// conn and a NIL ERROR while it is still dialling. The conn is usable in the
	// sense that publishes queue, so nothing here fails; what breaks is everything
	// that reads CONNECTION-DERIVED state, because nats.go returns the zero value
	// for all of it unless the status is CONNECTED.
	//
	// That is not a theoretical hazard. It has now bitten twice:
	//
	//   - brokerIsClustered() cached "not clustered" from a still-dialling
	//     connection, which clamped every replica factor to 1 on a healthy HA
	//     cluster (fixed by reading it live, but the root cause was here);
	//   - KeyValueStore fails outright, because js.KeyValue consults
	//     ConnectedServerVersion() — empty unless CONNECTED — and reports
	//     "nats: key-value requires at least server version 2.6.2" against a
	//     2.14 server. Observed on a live cluster: device-management crashlooped
	//     every restart with an error blaming the server's version.
	//
	// The second one is the reason this waits rather than each call site guarding
	// itself. ensureStream is wrapped in RetryInfraConnect and survives; the KV
	// path is not, so a service whose first act is to create a bucket dies. Making
	// initialization mean "connected" fixes both, and every future reader of
	// connection state, in one place — and it makes the log line below true, which
	// it was not.
	//
	// Bounded, and a timeout is NOT fatal: the retry loop inside nats.go keeps
	// running, the connection handlers log the arrival, and the caller's own
	// RetryInfraConnect wrappers still apply. Failing startup here would trade a
	// confusing error for an outage.
	if err := waitForConnected(nc, connectWaitTimeout); err != nil {
		log.Warn().Err(err).Str("url", url).Msg(
			"NATS connection is not established yet; continuing to retry in the background. " +
				"Components that read connection state during initialization may degrade until it lands")
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return err
	}
	nmgr.nc = nc
	nmgr.js = js
	nmgr.connectedServer.Store(nc.ConnectedUrl())
	log.Info().Msg(fmt.Sprintf("Verified connectivity to NATS at '%s'", url))
	return nil
}

// connectWaitTimeout bounds how long initialization waits for the connection to
// actually be established. Generous, because the thing being waited on is a
// broker that may still be starting, and every second here is cheaper than a
// crashloop; bounded, because a service that never comes up is worse than one
// that comes up degraded and says so.
const connectWaitTimeout = 30 * time.Second

// waitForConnected blocks until the connection reports CONNECTED, or the timeout
// elapses. It polls rather than using a handler because the connection may
// ALREADY be connected by the time we get here — the common case — and a
// handler-based wait would miss the event that already happened.
func waitForConnected(nc *nats.Conn, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if nc.IsConnected() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("still %s after %s", nc.Status(), timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Start component.
func (nmgr *NatsManager) Start(ctx context.Context) error {
	return nmgr.lifecycle.Start(ctx)
}

// ExecuteStart instantiates the service's readers/writers via oncreate, then starts
// the stream-utilization sampler over the streams they ensured.
func (nmgr *NatsManager) ExecuteStart(context.Context) error {
	if err := nmgr.oncreate(nmgr); err != nil {
		return err
	}
	// Guard against starting a second sampler if Start is ever retried without an
	// intervening Stop (a real Starter.Postprocess failure): a nil stopSampler means
	// none is running. ExecuteStop nils it back out.
	if nmgr.stopSampler == nil {
		nmgr.stopSampler = make(chan struct{})
		nmgr.samplerWg.Add(1)
		go nmgr.runStreamMetrics()
	}
	log.Info().Msg("NATS component creation completed successfully.")
	return nil
}

// Stop component.
func (nmgr *NatsManager) Stop(ctx context.Context) error {
	return nmgr.lifecycle.Stop(ctx)
}

// ExecuteStop stops the metrics sampler, unsubscribes readers, and drains the
// connection. The sampler is stopped first (before Drain) so it is not mid-
// StreamInfo when the connection closes.
func (nmgr *NatsManager) ExecuteStop(context.Context) error {
	// Before anything that can close the connection: Drain below fires the
	// ClosedHandler, and it must know this was asked for.
	nmgr.shuttingDown.Store(true)
	if nmgr.stopSampler != nil {
		close(nmgr.stopSampler)
		nmgr.samplerWg.Wait()
		nmgr.stopSampler = nil
	}
	log.Info().Msg("Shutting down NATS readers.")
	for _, r := range nmgr.readers {
		// A bound subscription's Unsubscribe does NOT delete the durable (that is the
		// whole point of the Bind attach), so this releases local interest without
		// disturbing the consumer other replicas share.
		if s := r.sub.Load(); s != nil {
			if err := s.Unsubscribe(); err != nil {
				log.Error().Err(err).Str("suffix", r.suffix).Msg("Error unsubscribing NATS reader.")
			}
		}
	}
	if nmgr.nc != nil {
		if err := nmgr.nc.Drain(); err != nil {
			log.Error().Err(err).Msg("Error draining NATS connection.")
		}
	}
	return nil
}

// Terminate component.
func (nmgr *NatsManager) Terminate(ctx context.Context) error {
	return nmgr.lifecycle.Terminate(ctx)
}

// ExecuteTerminate closes the NATS connection.
func (nmgr *NatsManager) ExecuteTerminate(context.Context) error {
	nmgr.shuttingDown.Store(true)
	if nmgr.nc != nil && !nmgr.nc.IsClosed() {
		nmgr.nc.Close()
	}
	return nil
}
