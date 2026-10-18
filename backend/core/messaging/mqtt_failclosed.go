// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// THE FAIL-CLOSED MQTT ACKNOWLEDGEMENT: the platform owns the gateway's message store.
//
// The broker's MQTT gateway answers a QoS 1 PUBLISH only once it has stored a copy in its
// own message store, $MQTT_msgs, and when that store refuses the copy it sends no PUBACK and
// closes the connection, so a device that kept the packet sends it again. The store the
// gateway creates for itself does not make that a guarantee: it discards OLD (a full store
// evicts and still acknowledges), it is unbounded, and it has INTEREST retention with nobody
// interested in device publishes, so it skips them and acknowledges anyway.
//
// With infrastructure.nats.mqttFailClosed on, event-sources declares the store instead:
// discard NEW, a positive byte ceiling, replicas set explicitly, and one platform durable
// per kind of device publish (events, command responses, device reports), each forwarding
// only this instance's publishes of its kind. The gateway looks the store up before creating it and keeps a store it
// finds, so the declared config is the one it uses. Every behaviour named here is pinned
// against the real broker in mqtt_failclosed_guard_test.go.
//
// Three properties are what make this safe to depend on, and each is enforced here rather
// than assumed:
//
//   - THE RECONCILE IS FATAL. A store left discard-old, unbounded or without its durables
//     acknowledges what it then loses, which is the failure this exists to end. So when the
//     setting is on, a start that cannot declare the store stops event-sources from starting.
//   - IT RUNS BEFORE event-sources' OWN MQTT-FACING COMPONENTS. event-sources calls it from
//     its broker manager's create hook, ahead of every reader, source and the presence tap.
//     (It cannot run before the BROKER's MQTT listener, which belongs to another process: a
//     device that connects before event-sources has ever started meets the gateway's own
//     store. Refusing those connections is the job of the broker's auth callout and is not
//     part of this file.)
//   - IT IS WATCHED. A durable deleted at runtime turns the store back into silent loss with
//     a normal PUBACK, so MqttFailClosedWatch re-runs the pass every 30 s, recreates what is
//     missing, and counts it on metrics an alert reads.
//
// # The store is shared by every pod, and the pods can disagree
//
// During a rolling update some pods have the setting on and some off, or on with different
// bounds, and any of them can restart in the middle. The store has to end up as the pods
// that REMAIN want it, and must never be left fail-closed with durables nobody reads. So the
// store carries, in its own metadata, who it is held for:
//
//   - FAIL-CLOSED IS A LEASE. A pod with the setting on writes this instance's owner mark,
//     the signature of the bound it wants (its "holder"), and a heartbeat it renews on every
//     pass. Pods that want the same signature hold it together. A pod that wants something
//     else, on or off, leaves a store whose heartbeat is fresh alone and takes it over only
//     once the heartbeat has gone stale (mqttFailClosedLeaseTTL), which is when no pod that
//     wants it any more is alive. So the pods never undo each other every 30 s, and whichever
//     wanted state is still running is the one the store converges to.
//   - RELEASING DOES NOT REMOVE THE DURABLES, OR THE BOUND. A pod with the setting off that
//     takes over a stale store drops the owner mark (so no pod skips a direct copy any more)
//     and marks it released, and leaves the durables in place: they hold store copies whose
//     direct copies a pod with the setting on has already skipped (see
//     mqtt_failclosed_view.go), and every event-sources pod that sees this instance's mark on
//     the store reads them. The store stays discard NEW at its bound until they are drained:
//     discard old, or a lower bound, would EVICT the oldest of those copies, which are the only
//     ones (TestAReleaseKeepsEveryStoreCopyTheDurablesHold). Once the store has been released
//     for mqttFailClosedReleaseGrace (every pod has stopped skipping by then) the pass records
//     the store's last sequence as the drain cut; every copy of a skipped direct copy is at or
//     below it. The durables are removed once each has acked past the cut (or holds nothing),
//     which a reader keeping up with steady traffic reaches, and only then is the gateway's
//     discard policy put back and the marks cleared. A pod with the setting off on a store that
//     carries no mark of this instance never writes it.
//   - EVERY WRITE IS UNDER A LOCK, read again inside it. The JetStream API has no
//     compare-and-set on a stream's config, so two pods deciding from the same read could
//     each write: a repair computed before a release would undo it, or create durables on a
//     store just released. A pass takes the instance's lock (the locks KV bucket) before
//     writing and re-reads the store under it, so it acts on what is there, not on what was.
//     The lock is RENEWED, compare-and-set, before every write the pass makes, and a pass
//     whose renewal fails stops before writing: the bucket's entry lifetime is whatever the
//     service that created the bucket chose (user-management's is 5 s), shorter than a pass
//     that retries, so a lock that was only taken would expire under a slow pass and let a
//     second pod write beside it (TestTheFailClosedLockOutlivesTheLocksBucketTTL).
//   - DURABLES BEFORE DISCARD NEW, AND THE FROM MARKS A SETTLE AFTER THE DURABLES. Taking a
//     store over creates the durables, then writes the fail-closed config, and records each
//     direct stream's last sequence (the "from" mark a reader skips direct copies above) only
//     on a pass made once that kind's durable has existed for mqttFailClosedFromSettle. Until
//     then the kind has no mark and nothing of it is skipped, so no pod skips a direct copy
//     whose store copy nobody held: the cost of the wait is a duplicate, never a loss.
//
// 🔴 ONE INSTANCE PER BROKER ACCOUNT. The store is per account, and on a shared broker every
// instance's devices are in the same one, so they share one store and one bound: one
// instance's stalled reader refuses the other's devices, and each instance's reconcile
// rewrites the other's. The reconcile therefore refuses to enable the store on a broker it
// can see serving another instance (see foreignInstances for how it looks).

const (
	// mqttFailClosedKeyPrefix begins every metadata key this file writes on the store.
	mqttFailClosedKeyPrefix = "io.devicechain.mqtt-fail-closed."
	// mqttFailClosedOwnerKey names the instance that holds the store fail-closed. It is what
	// lets a second instance on the same broker see that the store is already somebody's,
	// and what tells a pod with the setting off that this instance turned it on.
	mqttFailClosedOwnerKey = mqttFailClosedKeyPrefix + "owner"
	// mqttFailClosedHolderKey is the signature (mqttFailClosedSignature) of the store the
	// holding pods want: the lease's subject.
	mqttFailClosedHolderKey = mqttFailClosedKeyPrefix + "holder"
	// mqttFailClosedHeartbeatKey is when a holding pod last renewed the lease, in Unix
	// nanoseconds.
	mqttFailClosedHeartbeatKey = mqttFailClosedKeyPrefix + "heartbeat"
	// mqttFailClosedFromKeyPrefix begins one key per direct stream: its last sequence when
	// the durables were known to exist. See mqtt_failclosed_view.go.
	mqttFailClosedFromKeyPrefix = mqttFailClosedKeyPrefix + "from."
	// mqttFailClosedReleasedKey names the instance that released the store (a pod with the
	// setting off took it over), while its durables are still being drained.
	mqttFailClosedReleasedKey = mqttFailClosedKeyPrefix + "released"
	// mqttFailClosedReleasedAtKey is when it was released, in Unix nanoseconds.
	mqttFailClosedReleasedAtKey = mqttFailClosedKeyPrefix + "released-at"
	// mqttFailClosedDrainToKey is a released store's drain cut: its last sequence once the
	// release grace had passed. See finishMqttFailClosed.
	mqttFailClosedDrainToKey = mqttFailClosedKeyPrefix + "drain-to"

	// mqttFailClosedArea is the functional area that owns the platform durables. Their names
	// are built from it, so it is fixed here rather than read from whichever service calls.
	mqttFailClosedArea = "event-sources"

	// mqttMessageStoreSourceTag marks a dedup id built from a $MQTT_msgs sequence; see
	// MqttMessageStoreDedupID.
	mqttMessageStoreSourceTag = "m"

	// mqttFailClosedLockName is the instance lock every write to the store is made under.
	mqttFailClosedLockName = "mqtt-fail-closed"

	// mqttFailClosedPendingSubject is the only subject of a store this file creates, until the
	// pass that created it has created its durables too. No device publishes to it (natsauth
	// grants a device only subjects that begin with its instance id), so the store takes
	// nothing in between. See assertMqttFailClosed.
	mqttFailClosedPendingSubject = mqttMsgsSubjectPrefix + "_devicechain.pending"

	// MqttFailClosedWatchInterval is how often MqttFailClosedWatch runs a pass.
	MqttFailClosedWatchInterval = 30 * time.Second
)

var (
	// mqttFailClosedLeaseTTL is how long a holder's heartbeat keeps pods that want something
	// else from taking the store over. Three passes, so one slow or failed pass of a live
	// holder does not hand the store away.
	mqttFailClosedLeaseTTL = 3 * MqttFailClosedWatchInterval
	// mqttFailClosedReleaseGrace is how long a released store keeps its durables at least. It
	// is well above the time a pod's look at the store may be trusted to SKIP a direct copy
	// (mqttStoreViewTTL), so by then every pod has stopped skipping and every store copy of a
	// direct copy that was skipped is already in a durable.
	mqttFailClosedReleaseGrace = 2 * MqttFailClosedWatchInterval
	// mqttFailClosedLockTTL is the lock bucket's entry lifetime when this file creates the
	// bucket: how long a pod that died holding the lock keeps the others out. Usually the
	// bucket already exists with another service's lifetime (user-management's 5 s), which is
	// why a pass renews the lock before every write (mqttFenced) rather than relying on it.
	mqttFailClosedLockTTL = 30 * time.Second
	// mqttFailClosedLockWait is how long a START waits for the lock; a watch pass does not
	// wait (another pod is making the pass for it).
	mqttFailClosedLockWait = mqttFailClosedLockTTL + 5*time.Second

	// 🔑 HOW MUCH CLOCK SKEW BETWEEN PODS THE LEASE TAKES, AND WHAT MORE COSTS. The heartbeat
	// and the release time are written by one pod's clock and read by another's, so a reader
	// whose clock runs s ahead sees them s older than they are. A holder renews the heartbeat
	// on every pass once it has aged half an interval, so a live holder's heartbeat is at most
	// about one interval (30 s) old, two after one failed pass. The lease (90 s) therefore
	// holds against up to 30 s of skew through a failed pass, and 60 s without one. The release
	// grace (60 s, judged against a release time another pod may have written) holds against up
	// to 45 s before it comes within mqttStoreViewTTL. The lock and the from-mark settle are
	// timed by the broker, not by a pod, and take no skew.
	//
	// Beyond those bounds the pods hand the store back and forth: one with another wanted
	// state reads a live holder's heartbeat as stale and takes the store over, and the holder
	// takes it back on its next pass. That costs DUPLICATES and never loss, because nothing a
	// take-over or a release does removes a durable or loosens the store: a release keeps the
	// durables and discard new, a take-over writes from marks only after its settle, and the
	// durables are removed only by a pass that finds the store released for the grace with
	// every durable drained past its cut, which a holder taking the store back every pass
	// prevents. Node clocks kept by NTP are within milliseconds, far inside these bounds.

	// mqttFailClosedInsufficientRetry is how a pass retries a durable create the cluster
	// refused for "insufficient resources": ordinary for a few seconds after the store's own
	// replicas were raised, while the new peers catch up.
	mqttFailClosedInsufficientRetry = []time.Duration{time.Second, 2 * time.Second, 3 * time.Second}
)

// mqttFailClosedFromSettle is how long a platform durable must have existed before a pass
// records its kind's from mark, the direct stream's last sequence that readers skip above.
//
// 🔴 A MARK READ AS SOON AS THE DURABLE EXISTS CAN LOSE A PUBLISH. The gateway sends a QoS 1
// publish's direct copy into the subject space BEFORE it stores the store copy, and the two go
// to different streams with their own leaders. So a publish can be in flight across the
// durable's creation: its store copy applied on a replica that did not yet know the durable
// (interest is decided per replica, when the write is applied) and skipped for no interest,
// while its direct copy, still queued at the direct stream's leader, has not been given a
// sequence. A mark read at that moment is BELOW the sequence the direct copy then gets, so
// every reader skips the only copy, after the device had its PUBACK.
//
// Waiting narrows that window to nothing in practice, and 🔴 IT IS A HEURISTIC, NOT A BOUND.
// It assumes that once the durable has existed for this long, every replica has applied it
// and every direct copy whose store copy predates it has landed: that no direct copy spends
// longer than the settle between the gateway and its stream's sequence (the route to the
// stream leader and its ingest queue). Nothing in the broker bounds that delay; a direct copy
// held up longer than the settle, after a store copy skipped for no interest, would be above
// the mark and skipped. Under the assumption, a sequence above the mark read now belongs to a
// publish whose store copy a durable holds. Clock skew between broker nodes (the durable's
// creation stamped by one, the answer's time stamp by another) shortens the real wait by the
// skew; up to about 10 s of it still leaves the gateway's 5 s store wait covered. Until the
// mark is recorded the kind has none, and a reader skips nothing of it (carriedByStore): the
// store copy and the direct copy are both forwarded, a duplicate the dedup id cannot fold,
// for at most this long plus a pass after every take-over or re-created durable.
//
// The age is the BROKER's: the durable's creation time against the time stamp of the broker's
// answer, both from its own clock, so a pod's clock does not decide it. Half a watch interval:
// three times the gateway's 5 s wait for a store acknowledgement, and short enough that the
// first watch pass after a take-over records the marks. A variable only so a test can shorten
// it.
var mqttFailClosedFromSettle = MqttFailClosedWatchInterval / 2

// mqttFailClosedRequestTimeout bounds each raw JetStream API request the reconcile makes
// itself (the stream listing). The nats.go JetStream calls bound theirs with the context's
// default wait; a raw request does not, and the startup reconcile and the watch both pass
// a context with no deadline, so an API request the broker never answers (a meta-leader
// election) would otherwise stall the start, or stop the watch for good, without a word.
// A variable only so a test can shorten it.
var mqttFailClosedRequestTimeout = 5 * time.Second

// mqttFailClosedRetry is the backoff the startup reconcile retries a broker failure on. A
// JetStream API timeout during a leader election is ordinary at startup; failing the start
// on the first one would turn a blip into a restart.
var mqttFailClosedRetry = []time.Duration{0, time.Second, 2 * time.Second, 5 * time.Second}

// ErrMqttFailClosedSharedBroker is the reconcile's refusal on a broker that serves another
// instance. errors.Is matches it through the wrapped message that names the instances.
var ErrMqttFailClosedSharedBroker = errors.New("the broker serves another DeviceChain instance")

// MqttFailClosedKind is the kind of device publish a platform durable holds.
type MqttFailClosedKind string

const (
	// MqttFailClosedEvents is device telemetry: {instance}.{tenant}.devices.{device}.events.
	MqttFailClosedEvents MqttFailClosedKind = "events"
	// MqttFailClosedResponses is command responses: {instance}.{tenant}.command-responses.{device}.
	MqttFailClosedResponses MqttFailClosedKind = "command-responses"
	// MqttFailClosedReports is device reports: {instance}.{tenant}.device-reports.{device}.
	MqttFailClosedReports MqttFailClosedKind = "device-reports"
)

// MqttFailClosedDurable is one platform durable on the message store.
type MqttFailClosedDurable struct {
	Kind MqttFailClosedKind
	// Name is the durable consumer's name.
	Name string
	// Forwards is what the durable's reader hands out: the gateway's store prefix in front of
	// the subject a device is granted to publish this kind of message on, with a wildcard for
	// the tenant and for the device. The reader acks and drops anything else it is delivered.
	Forwards string
	// Filter is the durable's filter subject on the broker. It is Forwards for every kind but
	// events, whose durable is UNFILTERED ("").
	//
	// 🔴 WHY EVENTS ARE UNFILTERED: THE BROKER'S COST OF A WILDCARD FILTER GROWS WITH THE
	// BACKLOG. For each message it delivers, the broker looks for the next one the filter
	// matches in the current file block. A filter that is not the stream's own subject
	// ("$MQTT.msgs.>") is looked up through the block's per-subject index, which visits every
	// device subject in the block, unless the rest of the block is shorter than four times its
	// subject count (nats-server 2.15 filestore, msgBlock.firstMatching). A reader that keeps
	// up reads near the end of a block and takes the cheap linear walk; one that has fallen a
	// block behind pays a visit to every device's subject PER MESSAGE, so it reads slower the
	// further behind it is and does not catch up. Measured on a three-node cluster with 3,000
	// device subjects (TestPerfRawDrain), a 300k backlog drained at ~11k/s filtered and ~117k/s
	// unfiltered. Events are nearly everything in the store, so an unfiltered durable whose
	// reader drops the rest costs one delivery and one ack per message of another kind.
	// Command responses and device reports, which are sparse, keep their filters: for a
	// sparse filter the index is the cheap path.
	//
	// 🔑 THE TENANT IS A WILDCARD (OR ABSENT), AND THE TENANT PURGE RELIES ON IT. The purge
	// deletes every consumer on this store whose filter begins with
	// "$MQTT.msgs.{instance}.{tenant}.", and no valid tenant is "*", so these filters can never
	// be matched by it (TestTheTenantPurgeNeverDeletesThePlatformDurable and
	// TestTheTenantPurgeNeverDeletesAFailClosedDurable); an empty filter begins with nothing.
	Filter string
}

// MqttFailClosedDurables is the exact set of platform durables for an instance: one per kind
// of device QoS 1 publish, each forwarding exactly what natsauth grants a device for that
// kind (see Filter for why the events durable's broker filter is wider than that).
func MqttFailClosedDurables(instanceId string) []MqttFailClosedDurable {
	durable := func(kind MqttFailClosedKind) string {
		return DurableName(instanceId, mqttFailClosedArea, "mqtt-msgs-"+string(kind))
	}
	responses := mqttMsgsSubjectPrefix + DeviceScopedSubject(instanceId, "*", SubjectCommandResponses, "*")
	reports := mqttMsgsSubjectPrefix + DeviceScopedSubject(instanceId, "*", SubjectDeviceReports, "*")
	return []MqttFailClosedDurable{
		{Kind: MqttFailClosedEvents, Name: durable(MqttFailClosedEvents),
			Forwards: mqttMsgsSubjectPrefix + DeviceEventsWildcard(instanceId), Filter: ""},
		{Kind: MqttFailClosedResponses, Name: durable(MqttFailClosedResponses), Forwards: responses, Filter: responses},
		{Kind: MqttFailClosedReports, Name: durable(MqttFailClosedReports), Forwards: reports, Filter: reports},
	}
}

// MqttFailClosedConsumerConfig is the config a platform durable is created with.
//
// It is the platform reader's consumer config (natsReader.consumerConfig) with DeliverNew
// and an explicit replica count. DeliverNew because the durable is created over a store that
// may already hold messages kept for the gateway's own session consumers, none of them the
// platform's. The reader over it (NewMqttMessageStoreReader) only ever BINDS to the durable
// and never creates it, so a released store cannot have a durable recreated on it by a
// reader's self-heal.
func MqttFailClosedConsumerConfig(d MqttFailClosedDurable, replicas int) *nats.ConsumerConfig {
	return &nats.ConsumerConfig{
		Durable:       d.Name,
		AckPolicy:     nats.AckExplicitPolicy,
		AckWait:       AckWait,
		MaxDeliver:    MaxDeliver,
		MaxAckPending: readerMaxAckPending,
		FilterSubject: d.Filter,
		DeliverPolicy: nats.DeliverNewPolicy,
		Replicas:      replicas,
	}
}

// MqttMessageStoreDedupID builds the Nats-Msg-Id for an event forwarded from the message
// store: "<tenant>.m<epoch>.<sequence>", the epoch being the store's creation time in
// nanoseconds and both numbers in base 36. It is "" when any part is missing, which
// publishes no dedup header at all; that is safer than a weak id.
//
// Each part is there because the id would silently suppress a real event without it:
//
//   - THE SOURCE TAG. The capture forward publishes "<tenant>.<capture sequence>" into the
//     same inbound stream. Without a tag, store sequence N and capture sequence N would be
//     one id, and while both paths run the second would be acknowledged as a duplicate and
//     dropped. A capture id has one '.', this has two, and no tenant contains one, so the
//     two shapes cannot meet.
//   - THE EPOCH. A store that is deleted and created again starts its sequence at 1, and
//     an id reused inside the duplicate window is suppressed without trace. The creation
//     time is the store's own, survives a restart and a leader move, and changes on every
//     re-creation. The reader names it per message (Message.DedupID on what it hands out).
//   - THE TENANT. Dedup is per stream and the inbound stream serves every tenant; see
//     DedupID in event-sources for why the prefix stays even where the sequence is unique.
func MqttMessageStoreDedupID(tenant string, epoch time.Time, seq uint64) string {
	if tenant == "" || seq == 0 || epoch.IsZero() || epoch.UnixNano() <= 0 {
		return ""
	}
	return tenant + "." + mqttMessageStoreSourceTag + strconv.FormatInt(epoch.UnixNano(), 36) +
		"." + strconv.FormatUint(seq, 36)
}

// MqttMessageStoreState is the store as a start left it.
type MqttMessageStoreState struct {
	// Epoch is the store's creation time, the epoch of MqttMessageStoreDedupID.
	Epoch    time.Time
	MaxBytes int64
	Replicas int
	// Standing is what this pod found: see the mqttStanding constants.
	Standing string
}

// The standing of a pass: what this pod found the store to be, from where it stands.
const (
	// mqttStandingPristine: no mark of this instance on the store, or no store. A pod with the
	// setting off does nothing here.
	mqttStandingPristine = "pristine"
	// mqttStandingHolder: fail-closed in force, held by this pod's signature.
	mqttStandingHolder = "holder"
	// mqttStandingStandby: fail-closed in force, held by live pods with another signature; this
	// pod (setting on) takes it over when their heartbeat goes stale.
	mqttStandingStandby = "standby"
	// mqttStandingHeldByOn: this pod has the setting off, and live pods with it on hold the
	// store; it releases the store when their heartbeat goes stale.
	mqttStandingHeldByOn = "held-by-pods-with-it-on"
	// mqttStandingReleased: released by a pod with the setting off, durables not yet removed.
	mqttStandingReleased = "released"
	// mqttStandingSkipped: another pod held the lock; this watch pass wrote nothing.
	mqttStandingSkipped = "skipped"
)

// mqttFailClosedReport is what one pass found and did.
type mqttFailClosedReport struct {
	state    MqttMessageStoreState
	standing string
	// took is true when the pass took the store over (from no store, a store with no mark of
	// this instance, a released one, or a stale holder): what it changed was a decision, not
	// a repair, and is not counted as one.
	took bool
	// createdStore is true when the pass created the store.
	createdStore bool
	// repaired names each store setting a HOLDING pass had to put back.
	repaired []string
	// createdDurables names each durable the pass had to create.
	createdDurables []string
	// counted is true when what the pass created was missing from a store this instance held
	// when the watch last looked: loss, counted on the metrics. A start, or the first take-over
	// of a store, creates durables that were never there.
	counted bool
	// presentDurables names each durable that exists after the pass.
	presentDurables []string
	// absentDurables names each durable the pass found missing and could not create.
	absentDurables []string
	// foreign names the other instances the pass saw on this broker.
	foreign []string
	// foreignKnown is true when the pass completed the look for other instances, so that
	// an empty foreign means "none" rather than "could not ask".
	foreignKnown bool
	// pendingReplicas is true when a replica raise was refused for want of resources and is
	// left for the next pass.
	pendingReplicas bool
	// pendingFrom names each durable whose kind the pass left without a from mark because the
	// durable is younger than mqttFailClosedFromSettle: nothing of that kind is skipped yet.
	pendingFrom []string
	// released is true when the pass released the store; finished when it removed the
	// durables of a released store and cleared its marks.
	released, finished bool
	// marked is true when the store carries a mark of this instance after the pass: the
	// signal for a pod with the setting off that it has durables to read.
	marked bool
}

// mqttStoreMarks is this instance's marks on the store, as read from its metadata.
type mqttStoreMarks struct {
	owned, released bool
	// foreignOwner is another instance's owner mark.
	foreignOwner bool
	holder       string
	heartbeat    time.Time
	releasedAt   time.Time
	from         map[string]uint64
	// drainTo is the released store's drain cut, hasDrainTo whether it has been recorded.
	drainTo    uint64
	hasDrainTo bool
}

func (nmgr *NatsManager) marksOf(info *nats.StreamInfo) mqttStoreMarks {
	var m mqttStoreMarks
	if info == nil {
		return m
	}
	me := nmgr.Microservice.InstanceId
	md := info.Config.Metadata
	m.owned = me != "" && md[mqttFailClosedOwnerKey] == me
	m.foreignOwner = md[mqttFailClosedOwnerKey] != "" && !m.owned
	m.released = me != "" && md[mqttFailClosedReleasedKey] == me
	m.holder = md[mqttFailClosedHolderKey]
	m.heartbeat = unixNanoTime(md[mqttFailClosedHeartbeatKey])
	m.releasedAt = unixNanoTime(md[mqttFailClosedReleasedAtKey])
	m.from = mqttFailClosedFromMarks(md)
	if n, err := strconv.ParseUint(md[mqttFailClosedDrainToKey], 10, 64); err == nil {
		m.drainTo, m.hasDrainTo = n, true
	}
	return m
}

func unixNanoTime(s string) time.Time {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// mqttFailClosedSignature is what the holder key records: the bound and the replica count a
// pod declares the store with. Pods that agree on it hold the store together.
func mqttFailClosedSignature(maxBytes int64, replicas int) string {
	return strconv.FormatInt(maxBytes, 10) + "/" + strconv.Itoa(replicas)
}

// mqttNow is the clock the lease is read and written by; a test seam.
func (nmgr *NatsManager) mqttNow() time.Time {
	if nmgr.mqttClock != nil {
		return nmgr.mqttClock()
	}
	return time.Now()
}

// SetMqttFailClosedClockForTesting replaces the clock the fail-closed lease is read and
// written by, so a test outside this package can move a pod past a heartbeat or a grace
// without waiting it out. Production never calls it. It takes a test's T for its Helper
// method alone, as SetAckWaitForTesting does.
func (nmgr *NatsManager) SetMqttFailClosedClockForTesting(tb interface{ Helper() }, now func() time.Time) {
	tb.Helper()
	nmgr.mqttClock = now
}

// SetMqttFailClosedFromSettleForTesting replaces mqttFailClosedFromSettle for the rest of a
// test, so a test outside this package can have a pass record the from marks without waiting
// the settle out. It is process-wide; the test's Cleanup restores it. Production never calls it.
func SetMqttFailClosedFromSettleForTesting(tb interface {
	Helper()
	Cleanup(func())
}, d time.Duration) {
	tb.Helper()
	saved := mqttFailClosedFromSettle
	mqttFailClosedFromSettle = d
	tb.Cleanup(func() { mqttFailClosedFromSettle = saved })
}

// ReconcileMqttFailClosed is a start's pass with infrastructure.nats.mqttFailClosed on: it
// declares the gateway's message store as the platform's fail-closed store and creates its
// platform durables, or finds it held by live pods with other settings (standby, which is
// not an error), refusing a broker that serves another instance. Call it before anything in
// the service connects over MQTT or reads the store, and treat its error as fatal.
func (nmgr *NatsManager) ReconcileMqttFailClosed(ctx context.Context) (MqttMessageStoreState, error) {
	var lastErr error
	for _, wait := range mqttFailClosedRetry {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return MqttMessageStoreState{}, ctx.Err()
			case <-time.After(wait):
			}
		}
		rep, err := nmgr.mqttFailClosedPass(ctx, true)
		if err == nil {
			rep.state.Standing = rep.standing
			if rep.standing == mqttStandingStandby {
				log.Warn().Str("stream", MqttMessageStore).Int64("maxBytes", rep.state.MaxBytes).
					Msg("The MQTT message store is held fail-closed by event-sources pods with a different bound or " +
						"replica count. This pod reads it and takes it over once they have stopped")
			} else {
				log.Info().Str("stream", MqttMessageStore).Int64("maxBytes", rep.state.MaxBytes).
					Int("replicas", rep.state.Replicas).Time("epoch", rep.state.Epoch).
					Bool("created", rep.createdStore).Strs("createdDurables", rep.createdDurables).
					Msg("The MQTT gateway's message store is the platform's fail-closed store: discard new, " +
						"bounded, with a platform durable for device events, command responses and device reports")
			}
			return rep.state, nil
		}
		// A shared broker is a configuration fact, not a blip: retrying cannot change it.
		if errors.Is(err, ErrMqttFailClosedSharedBroker) || errors.Is(err, errMqttFailClosedUnfixable) {
			return MqttMessageStoreState{}, err
		}
		lastErr = err
	}
	return MqttMessageStoreState{}, fmt.Errorf("could not declare the MQTT gateway's message store as "+
		"the fail-closed store (infrastructure.nats.mqttFailClosed is on, so MQTT ingest does not start "+
		"without it): %w", lastErr)
}

// errMqttFailClosedUnfixable marks a store whose config the broker will not let an update
// change, which no retry can fix.
var errMqttFailClosedUnfixable = errors.New("the message store cannot be updated into the fail-closed config")

// errMqttFailClosedLockBusy is a start's failure to take the lock within mqttFailClosedLockWait.
var errMqttFailClosedLockBusy = errors.New("another event-sources pod held the fail-closed store's lock")

// mqttFailClosedAction is what a pass decided to do.
type mqttFailClosedAction int

const (
	mqttActNone mqttFailClosedAction = iota
	mqttActHold
	mqttActTakeOn
	mqttActStandby
	mqttActHeldByOn
	mqttActRelease
	mqttActFinish
)

// decide is the lease, as a function of the store and this pod's wanted state.
func (nmgr *NatsManager) decide(info *nats.StreamInfo, marks mqttStoreMarks, on bool, sig string, now time.Time) mqttFailClosedAction {
	fresh := !marks.heartbeat.IsZero() && now.Sub(marks.heartbeat) < mqttFailClosedLeaseTTL
	if on {
		switch {
		case marks.foreignOwner:
			// Another instance holds it: a shared broker, refused at a start and reported by
			// the watch. Taking it over would only have the two instances' passes rewrite it
			// in turn.
			return mqttActStandby
		case !marks.owned:
			return mqttActTakeOn
		case marks.holder == sig:
			return mqttActHold
		case fresh:
			return mqttActStandby
		default:
			return mqttActTakeOn
		}
	}
	switch {
	case marks.owned && fresh:
		return mqttActHeldByOn
	case marks.owned:
		return mqttActRelease
	case marks.released:
		if now.Sub(marks.releasedAt) >= mqttFailClosedReleaseGrace || marks.releasedAt.IsZero() {
			return mqttActFinish
		}
		return mqttActNone
	}
	return mqttActNone
}

// mqttFailClosedPass is one pass: a start's (startup) or the watch's. It reads the store,
// decides from the lease what this pod should do, and if that writes anything takes the
// instance lock and decides again from a fresh read before it does.
//
// A start with the setting on stops at a broker that serves another instance, and waits for
// the lock; a watch pass reports the other instances and goes on keeping its own durables
// (removing them would only turn the store's refusals into silent loss), and leaves the pass
// to the pod holding the lock when it is held.
func (nmgr *NatsManager) mqttFailClosedPass(ctx context.Context, startup bool) (mqttFailClosedReport, error) {
	var rep mqttFailClosedReport
	instanceId := nmgr.Microservice.InstanceId
	if err := core.ValidateToken(instanceId); err != nil {
		return rep, fmt.Errorf("%w: invalid instance id: %v", errMqttFailClosedUnfixable, err)
	}
	natscfg := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	on := natscfg.MqttFailClosed
	maxBytes := natscfg.MqttMessageStoreMaxBytes()
	if maxBytes <= 0 {
		// 0 is UNLIMITED to JetStream; a fail-closed store without a bound is not one.
		return rep, fmt.Errorf("%w: its ceiling must be positive, got %d", errMqttFailClosedUnfixable, maxBytes)
	}
	sig := mqttFailClosedSignature(maxBytes, nmgr.effectiveStreamReplicas())

	// Whether this instance held the store when the watch last looked: a store found missing
	// now that was held then was deleted out from under it, which is a repair, not a take-over.
	prev := nmgr.mqttView().current()
	info, err := nmgr.readMqttStore(ctx)
	if err != nil {
		return rep, err
	}
	if on {
		// Look for other instances BEFORE writing anything: on a shared broker the right
		// outcome is to change nothing.
		foreign, err := nmgr.foreignInstances(ctx, instanceId, info)
		if err != nil {
			return rep, err
		}
		rep.foreign, rep.foreignKnown = foreign, true
		if len(foreign) > 0 && startup {
			return rep, fmt.Errorf("%w (%s): the MQTT gateway keeps ONE message store per broker account, "+
				"so with infrastructure.nats.mqttFailClosed on, one instance's backlog would refuse the other's "+
				"devices and each would rewrite the other's bound. Refusing to start. Turn mqttFailClosed off "+
				"for this instance: MQTT ingest then stays on the device-events capture stream",
				ErrMqttFailClosedSharedBroker, strings.Join(foreign, ", "))
		}
	}
	wasOwned := nmgr.marksOf(info).owned || (info == nil && prev.present && prev.owned)

	act := nmgr.decide(info, nmgr.marksOf(info), on, sig, nmgr.mqttNow())
	if act == mqttActNone || act == mqttActStandby || act == mqttActHeldByOn {
		return nmgr.reportReadOnly(ctx, info, act, rep)
	}

	if nmgr.mqttPassHook != nil {
		nmgr.mqttPassHook("decided")
	}
	wait := time.Duration(0)
	if startup {
		wait = mqttFailClosedLockWait
	}
	var passErr error
	held, err := nmgr.withMqttFailClosedLock(ctx, wait, func(ctx context.Context) error {
		// Everything is read and decided again under the lock: another pod may have
		// written between the read above and now.
		info, err := nmgr.readMqttStore(ctx)
		if err != nil {
			return err
		}
		if nmgr.mqttPassHook != nil {
			nmgr.mqttPassHook("locked")
		}
		marks := nmgr.marksOf(info)
		switch act := nmgr.decide(info, marks, on, sig, nmgr.mqttNow()); act {
		case mqttActHold, mqttActTakeOn:
			rep, passErr = nmgr.assertMqttFailClosed(ctx, info, marks, act == mqttActTakeOn, wasOwned && !startup, sig, rep)
		case mqttActRelease:
			rep, passErr = nmgr.releaseMqttFailClosed(ctx, info, rep)
		case mqttActFinish:
			rep, passErr = nmgr.finishMqttFailClosed(ctx, info, marks, rep)
		default:
			rep, passErr = nmgr.reportReadOnly(ctx, info, act, rep)
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	if !held {
		if startup {
			return rep, errMqttFailClosedLockBusy
		}
		rep.standing = mqttStandingSkipped
		return rep, nil
	}
	return rep, passErr
}

// readMqttStore reads the store, nil when it does not exist, and records the look.
func (nmgr *NatsManager) readMqttStore(ctx context.Context) (*nats.StreamInfo, error) {
	info, err := nmgr.js.StreamInfo(MqttMessageStore, nats.Context(ctx))
	if errors.Is(err, nats.ErrStreamNotFound) {
		info, err = nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", MqttMessageStore, err)
	}
	nmgr.mqttView().observe(info, time.Now())
	return info, nil
}

// reportReadOnly fills the report of a pass that writes nothing: which of this instance's
// durables exist, read and never created.
func (nmgr *NatsManager) reportReadOnly(ctx context.Context, info *nats.StreamInfo, act mqttFailClosedAction, rep mqttFailClosedReport) (mqttFailClosedReport, error) {
	marks := nmgr.marksOf(info)
	rep.marked = marks.owned || marks.released
	switch act {
	case mqttActStandby:
		rep.standing = mqttStandingStandby
	case mqttActHeldByOn:
		rep.standing = mqttStandingHeldByOn
	case mqttActNone:
		if marks.released {
			rep.standing = mqttStandingReleased
		} else {
			rep.standing = mqttStandingPristine
		}
	}
	if info == nil {
		return rep, nil
	}
	rep.state = mqttStateOf(info, rep.standing)
	if !rep.marked {
		return rep, nil
	}
	for _, d := range MqttFailClosedDurables(nmgr.Microservice.InstanceId) {
		_, err := nmgr.js.ConsumerInfo(MqttMessageStore, d.Name, nats.Context(ctx))
		switch {
		case err == nil:
			rep.presentDurables = append(rep.presentDurables, d.Name)
		case errors.Is(err, nats.ErrConsumerNotFound):
			rep.absentDurables = append(rep.absentDurables, d.Name)
		default:
			return rep, fmt.Errorf("reading the platform durable %s on %s: %w", d.Name, MqttMessageStore, err)
		}
	}
	return rep, nil
}

func mqttStateOf(info *nats.StreamInfo, standing string) MqttMessageStoreState {
	replicas := info.Config.Replicas
	if replicas < 1 {
		replicas = 1
	}
	return MqttMessageStoreState{Epoch: info.Created, MaxBytes: info.Config.MaxBytes, Replicas: replicas, Standing: standing}
}

// assertMqttFailClosed holds or takes over the store for this pod's signature, under the lock.
//
// The order is the point (see the file comment): the store if it is missing, as the gateway
// would make it; then the durables; then each direct stream's from mark, read after the
// durables exist; and only then the fail-closed config with this instance's marks. A replica
// raise goes last and on its own, so a cluster that cannot yet place the new replicas leaves
// the store fail-closed at the old count and the next pass tries again.
func (nmgr *NatsManager) assertMqttFailClosed(ctx context.Context, info *nats.StreamInfo, marks mqttStoreMarks,
	takeOver, countRepairs bool, sig string, rep mqttFailClosedReport) (mqttFailClosedReport, error) {
	opt := nats.Context(ctx)
	instanceId := nmgr.Microservice.InstanceId
	natscfg := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	maxBytes := natscfg.MqttMessageStoreMaxBytes()
	desiredReplicas := nmgr.effectiveStreamReplicas()
	rep.took = takeOver
	rep.counted = countRepairs
	rep.standing = mqttStandingHolder

	if info == nil {
		if err := mqttFenced(ctx); err != nil {
			return rep, err
		}
		// 🔴 CREATED TAKING NOTHING, and given the gateway's subjects only once the durables
		// exist, by the same update that writes the fail-closed config below. A store created
		// with $MQTT.msgs.> would take the gateway's copies at once, with no durable interested
		// in them, so it would skip each one and the gateway would PUBACK it; and a reader whose
		// look predates the deletion (up to mqttStoreViewTTL old) still holds the old from
		// marks and skips the direct copy too. That publish would be acknowledged and kept
		// nowhere. With no stream on the gateway's subjects, its store write is not answered,
		// it sends no PUBACK and closes the connection, and the device sends the message again
		// (TestARecreatedStoreTakesNothingUntilItsDurablesExist).
		created, err := nmgr.js.AddStream(&nats.StreamConfig{
			Name:      MqttMessageStore,
			Subjects:  []string{mqttFailClosedPendingSubject},
			Storage:   nats.FileStorage,
			Retention: nats.InterestPolicy,
			// Discard OLD, as the gateway makes it: the store becomes discard-new only once its
			// durables exist and the marks are written, below.
			Discard:  nats.DiscardOld,
			MaxBytes: maxBytes,
			Replicas: desiredReplicas,
		}, opt)
		switch {
		case err == nil:
			info = created
			rep.createdStore = true
			if countRepairs {
				rep.repaired = append(rep.repaired, "store")
			}
			if nmgr.mqttPassHook != nil {
				nmgr.mqttPassHook("store-created")
			}
		case errors.Is(err, nats.ErrStreamNameAlreadyInUse):
			// The gateway created it between the read and the create. Reconcile what is there.
			if info, err = nmgr.js.StreamInfo(MqttMessageStore, opt); err != nil {
				return rep, fmt.Errorf("reading %s after a concurrent create: %w", MqttMessageStore, err)
			}
		default:
			return rep, fmt.Errorf("creating %s: %w", MqttMessageStore, err)
		}
	}
	cfg := info.Config
	repaired, err := conformMqttFailClosedStore(&cfg, maxBytes)
	if err != nil {
		return rep, err
	}
	// A store this pass created is counted once, as "store": the subjects it was created with
	// are this pass's own, not a change made out of band.
	if countRepairs && !takeOver && !rep.createdStore {
		rep.repaired = append(rep.repaired, repaired...)
	}

	replicas := info.Config.Replicas
	if replicas < 1 {
		replicas = 1
	}
	createdKind := map[MqttFailClosedKind]bool{}
	for _, d := range MqttFailClosedDurables(instanceId) {
		created, absent, err := nmgr.ensureMqttFailClosedDurable(ctx, d, replicas)
		if absent {
			rep.absentDurables = append(rep.absentDurables, d.Name)
		}
		if err != nil {
			return rep, err
		}
		if created {
			createdKind[d.Kind] = true
			rep.createdDurables = append(rep.createdDurables, d.Name)
		}
		rep.presentDurables = append(rep.presentDurables, d.Name)
	}

	// The from marks: kept where the durable has existed throughout (this instance held the
	// store, and the pass did not have to create it). Otherwise one is recorded only once the
	// durable has existed for mqttFailClosedFromSettle, and until then its kind has NO mark,
	// so no reader skips a direct copy of it: see mqttFailClosedFromSettle for why a mark read
	// as soon as the durable exists can lose a publish.
	from := map[string]uint64{}
	for _, d := range MqttFailClosedDurables(instanceId) {
		suffix := mqttFailClosedDirectSuffix[d.Kind]
		if createdKind[d.Kind] || rep.createdStore {
			continue
		}
		if have, ok := marks.from[suffix]; ok && marks.owned {
			from[suffix] = have
			continue
		}
		age, err := nmgr.mqttDurableAge(ctx, d.Name)
		if err != nil {
			return rep, err
		}
		if age < mqttFailClosedFromSettle {
			rep.pendingFrom = append(rep.pendingFrom, d.Name)
			continue
		}
		seq, err := nmgr.directStreamLastSeq(ctx, suffix)
		if err != nil {
			return rep, err
		}
		from[suffix] = seq
	}
	for _, d := range MqttFailClosedDurables(instanceId) {
		if createdKind[d.Kind] || rep.createdStore {
			rep.pendingFrom = append(rep.pendingFrom, d.Name)
		}
	}

	now := nmgr.mqttNow()
	md := make(map[string]string, len(cfg.Metadata)+8)
	for k, v := range cfg.Metadata {
		if !strings.HasPrefix(k, mqttFailClosedKeyPrefix) {
			md[k] = v
		}
	}
	md[mqttFailClosedOwnerKey] = instanceId
	md[mqttFailClosedHolderKey] = sig
	for suffix, seq := range from {
		md[mqttFailClosedFromKeyPrefix+suffix] = strconv.FormatUint(seq, 10)
	}
	// The heartbeat is renewed when it has aged half a pass or anything else changes, so the
	// pods holding the store write it about once a pass between them.
	heartbeat := strconv.FormatInt(now.UnixNano(), 10)
	if !marks.heartbeat.IsZero() && now.Sub(marks.heartbeat) < MqttFailClosedWatchInterval/2 {
		heartbeat = cfg.Metadata[mqttFailClosedHeartbeatKey]
	}
	md[mqttFailClosedHeartbeatKey] = heartbeat
	if len(repaired) > 0 || !metadataEqual(md, cfg.Metadata) {
		cfg.Metadata = md
		if err := mqttFenced(ctx); err != nil {
			return rep, err
		}
		updated, err := nmgr.js.UpdateStream(&cfg, opt)
		if err != nil {
			return rep, fmt.Errorf("updating %s into the fail-closed config (%s): %w",
				MqttMessageStore, strings.Join(append(repaired, "marks"), ", "), err)
		}
		info = updated
	}

	if changed, refusedDownward := applyStreamReplicas(&cfg, desiredReplicas); changed {
		if err := mqttFenced(ctx); err != nil {
			return rep, err
		}
		raised, err := nmgr.js.UpdateStream(&cfg, opt)
		switch {
		case err == nil:
			// Not counted as a repair: a raise is the configured count arriving (an operator
			// raised it, or the cluster grew), and nothing lowers it, so there is no
			// out-of-band change of replicas for the alert to report.
			info = raised
		case isInsufficientResources(err):
			// The cluster cannot place the new replicas yet. Not a failure of the store,
			// which is fail-closed at its current count: the next pass tries again.
			rep.pendingReplicas = true
			log.Warn().Err(err).Str("stream", MqttMessageStore).Int("current", replicas).Int("desired", desiredReplicas).
				Msg("The cluster has not got the resources to raise the MQTT message store's replicas yet; " +
					"it stays fail-closed at its current count and the next check tries again")
		default:
			return rep, fmt.Errorf("raising the replicas of %s to %d: %w", MqttMessageStore, desiredReplicas, err)
		}
	} else if refusedDownward {
		log.Warn().Str("stream", MqttMessageStore).Int("current", cfg.Replicas).Int("desired", desiredReplicas).
			Msg("The MQTT message store has more replicas than configured; it is left as it is, " +
				"because a starting pod does not scale replication down")
	}
	// The durables follow the store's replicas: a durable on fewer peers than its store is
	// lost with its node, and the store then skips (and acknowledges) every publish of its kind.
	if err := nmgr.raiseMqttFailClosedDurables(ctx, info.Config.Replicas, &rep); err != nil {
		return rep, err
	}

	rep.marked = true
	rep.state = mqttStateOf(info, rep.standing)
	if rep.state.Epoch.IsZero() {
		return rep, fmt.Errorf("%s reports no creation time, so no dedup id could be built from its sequences",
			MqttMessageStore)
	}
	nmgr.mqttView().observe(info, time.Now())
	return rep, nil
}

// raiseMqttFailClosedDurables raises each platform durable to the store's replica count. A
// raise the cluster cannot place yet for want of resources is retried a few times, then left
// for the next pass (rep.pendingReplicas), never failed: the durable is there, on fewer peers.
func (nmgr *NatsManager) raiseMqttFailClosedDurables(ctx context.Context, replicas int, rep *mqttFailClosedReport) error {
	if replicas <= 1 {
		return nil
	}
	opt := nats.Context(ctx)
	for _, d := range MqttFailClosedDurables(nmgr.Microservice.InstanceId) {
		ci, err := nmgr.js.ConsumerInfo(MqttMessageStore, d.Name, opt)
		if err != nil {
			return fmt.Errorf("reading the platform durable %s on %s: %w", d.Name, MqttMessageStore, err)
		}
		current := ci.Config.Replicas
		if current < 1 {
			current = 1
		}
		if current >= replicas {
			continue
		}
		cfg := ci.Config
		cfg.Replicas = replicas
		raise := func() error {
			if err := mqttFenced(ctx); err != nil {
				return err
			}
			_, err := nmgr.js.UpdateConsumer(MqttMessageStore, &cfg, opt)
			return err
		}
		err = raise()
		for _, wait := range mqttFailClosedInsufficientRetry {
			if err == nil || !isTransientRaise(err) {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			err = raise()
		}
		switch {
		case err == nil:
		case isTransientRaise(err):
			rep.pendingReplicas = true
			log.Warn().Err(err).Str("durable", d.Name).Int("current", current).Int("desired", replicas).
				Msg("The cluster has not got the resources to raise a platform durable's replicas yet; the next check tries again")
		default:
			return fmt.Errorf("raising the replicas of the platform durable %s to %d: %w", d.Name, replicas, err)
		}
	}
	return nil
}

func metadataEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// isInsufficientResources reports the cluster refusing a placement for want of peers or
// storage: transient while a replica raise is being caught up to.
func isInsufficientResources(err error) bool {
	var apiErr *nats.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode == nats.JSErrCodeInsufficientResourcesErr
}

// jsErrConsumerReplicasExceedsStream is the server's JSConsumerReplicasExceedsStream: a durable
// raised before the cluster has applied its store's own raise.
const jsErrConsumerReplicasExceedsStream nats.ErrorCode = 10126

// isTransientRaise reports a refusal of a durable's replica raise that the next attempt can
// get past: the cluster has not the resources yet, or has not yet applied the store's raise.
func isTransientRaise(err error) bool {
	var apiErr *nats.APIError
	return isInsufficientResources(err) ||
		(errors.As(err, &apiErr) && apiErr.ErrorCode == jsErrConsumerReplicasExceedsStream)
}

// mqttDurableAge is how long the platform durable name on the store has existed, by the
// broker's clock: its creation time against the time stamp of the broker's answer. nats.go's
// ConsumerInfo does not decode that time stamp, so this is a raw API request. On a broker that
// does not report one, the pod's clock stands in for it.
func (nmgr *NatsManager) mqttDurableAge(ctx context.Context, name string) (time.Duration, error) {
	rctx, cancel := context.WithTimeout(ctx, mqttFailClosedRequestTimeout)
	defer cancel()
	msg, err := nmgr.nc.RequestWithContext(rctx, "$JS.API.CONSUMER.INFO."+MqttMessageStore+"."+name, nil)
	if err != nil {
		return 0, fmt.Errorf("reading the age of the platform durable %s on %s: %w", name, MqttMessageStore, err)
	}
	var resp struct {
		Error   *jsAPIError `json:"error"`
		Created time.Time   `json:"created"`
		Now     time.Time   `json:"ts"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return 0, fmt.Errorf("reading the age of the platform durable %s on %s: %w", name, MqttMessageStore, err)
	}
	if resp.Error != nil {
		return 0, fmt.Errorf("reading the age of the platform durable %s on %s: %s", name, MqttMessageStore,
			resp.Error.Description)
	}
	if resp.Created.IsZero() {
		// No creation time: the age cannot be shown, so it is not old enough.
		return 0, nil
	}
	now := resp.Now
	if now.IsZero() {
		now = nmgr.mqttNow()
	}
	return now.Sub(resp.Created), nil
}

// directStreamLastSeq is the last sequence of the platform stream suffix, 0 when it does not
// exist yet (every message it ever holds is then above the mark).
func (nmgr *NatsManager) directStreamLastSeq(ctx context.Context, suffix string) (uint64, error) {
	info, err := nmgr.js.StreamInfo(StreamName(nmgr.Microservice.InstanceId, suffix), nats.Context(ctx))
	if errors.Is(err, nats.ErrStreamNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading the %s stream's last sequence: %w", suffix, err)
	}
	return info.State.LastSeq, nil
}

// conformMqttFailClosedStore puts a store's config into the fail-closed shape and names each
// setting it changed. It refuses what an update cannot change. Replicas and the marks are
// the caller's.
func conformMqttFailClosedStore(cfg *nats.StreamConfig, maxBytes int64) ([]string, error) {
	if cfg.Storage != nats.FileStorage {
		return nil, fmt.Errorf("%w: %s is %v storage and an update cannot change storage",
			errMqttFailClosedUnfixable, MqttMessageStore, cfg.Storage)
	}
	if cfg.Retention == nats.WorkQueuePolicy {
		// Every QoS 1 SUBSCRIBE adds a session consumer on this store, and a work queue
		// refuses overlapping consumers; the server also refuses an update to or from it.
		return nil, fmt.Errorf("%w: %s is a work queue, which an update cannot change",
			errMqttFailClosedUnfixable, MqttMessageStore)
	}
	var repaired []string
	if cfg.Retention != nats.InterestPolicy {
		cfg.Retention = nats.InterestPolicy
		repaired = append(repaired, "retention")
	}
	if want := []string{mqttMsgsSubjectPrefix + ">"}; len(cfg.Subjects) != 1 || cfg.Subjects[0] != want[0] {
		cfg.Subjects = want
		repaired = append(repaired, "subjects")
	}
	if cfg.Discard != nats.DiscardNew || cfg.DiscardNewPerSubject {
		cfg.Discard = nats.DiscardNew
		cfg.DiscardNewPerSubject = false
		repaired = append(repaired, "discard")
	}
	if cfg.MaxBytes != maxBytes {
		cfg.MaxBytes = maxBytes
		repaired = append(repaired, "maxBytes")
	}
	return repaired, nil
}

// ensureMqttFailClosedDurable creates a platform durable if it is missing and puts its filter
// back if it has changed, reporting whether it had to create it and whether it is missing
// still (it was not there and could not be created). A create the cluster refuses for want of
// resources is retried a few times first (mqttFailClosedInsufficientRetry).
//
// The filter is checked by reading it back, because AddConsumer on an existing durable with
// a changed filter returns success and keeps the old one
// (TestAddConsumerSilentlyIgnoresAChangedFilterSubject).
func (nmgr *NatsManager) ensureMqttFailClosedDurable(ctx context.Context, d MqttFailClosedDurable, replicas int) (created, absent bool, err error) {
	opt := nats.Context(ctx)
	ci, err := nmgr.js.ConsumerInfo(MqttMessageStore, d.Name, opt)
	if errors.Is(err, nats.ErrConsumerNotFound) {
		cfg := MqttFailClosedConsumerConfig(d, replicas)
		cfg.AckWait = nmgr.ackWait()
		add := func() error {
			if err := mqttFenced(ctx); err != nil {
				return err
			}
			_, err := nmgr.js.AddConsumer(MqttMessageStore, cfg, opt)
			return err
		}
		err := add()
		for _, wait := range mqttFailClosedInsufficientRetry {
			if err == nil || !isInsufficientResources(err) {
				break
			}
			select {
			case <-ctx.Done():
				return false, true, ctx.Err()
			case <-time.After(wait):
			}
			err = add()
		}
		if err != nil {
			return false, true, fmt.Errorf("creating the platform durable %s on %s: %w", d.Name, MqttMessageStore, err)
		}
		return true, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("reading the platform durable %s on %s: %w", d.Name, MqttMessageStore, err)
	}
	if ci.Config.FilterSubject != d.Filter || len(ci.Config.FilterSubjects) > 0 {
		cfg := ci.Config
		cfg.FilterSubject = d.Filter
		cfg.FilterSubjects = nil
		if err := mqttFenced(ctx); err != nil {
			return false, false, err
		}
		if _, err := nmgr.js.UpdateConsumer(MqttMessageStore, &cfg, opt); err != nil {
			return false, false, fmt.Errorf("restoring the filter of the platform durable %s on %s: %w",
				d.Name, MqttMessageStore, err)
		}
		log.Warn().Str("durable", d.Name).Str("filter", d.Filter).Str("was", ci.Config.FilterSubject).
			Msg("Restored the filter subject of a platform durable on the MQTT message store")
	}
	return false, false, nil
}

// releaseMqttFailClosed releases a store whose holders are gone, for a pod with the setting
// off, under the lock: no owner, holder, heartbeat or from marks, and the released mark with
// its time. The durables stay, and so do discard new and the bound: the durables hold the
// only copies of direct copies that were skipped, and discard old, or the lower setting-off
// bound, would EVICT the oldest of them at once. finishMqttFailClosed puts the gateway's
// policy back once they are drained; see the file comment.
func (nmgr *NatsManager) releaseMqttFailClosed(ctx context.Context, info *nats.StreamInfo, rep mqttFailClosedReport) (mqttFailClosedReport, error) {
	cfg := info.Config
	md := make(map[string]string, len(cfg.Metadata)+2)
	for k, v := range cfg.Metadata {
		if !strings.HasPrefix(k, mqttFailClosedKeyPrefix) {
			md[k] = v
		}
	}
	md[mqttFailClosedReleasedKey] = nmgr.Microservice.InstanceId
	md[mqttFailClosedReleasedAtKey] = strconv.FormatInt(nmgr.mqttNow().UnixNano(), 10)
	cfg.Metadata = md
	if err := mqttFenced(ctx); err != nil {
		return rep, err
	}
	updated, err := nmgr.js.UpdateStream(&cfg, nats.Context(ctx))
	if err != nil {
		return rep, fmt.Errorf("releasing %s: %w", MqttMessageStore, err)
	}
	rep.released, rep.marked = true, true
	rep.standing = mqttStandingReleased
	rep.state = mqttStateOf(updated, rep.standing)
	nmgr.mqttView().observe(updated, time.Now())
	log.Warn().Str("stream", MqttMessageStore).
		Msg("infrastructure.nats.mqttFailClosed is off, and no pod with it on is holding the MQTT message store any " +
			"more: released it. Its platform durables are drained; then they are removed and the store gets the " +
			"gateway's discard policy and the setting-off bound back, and a QoS 1 PUBACK no longer means the platform " +
			"holds the message")
	return rep, nil
}

// finishMqttFailClosed ends the release of a store released at least the grace ago, under the
// lock, in three steps a pass cut short can resume from (each leaves the marks that make the
// next pass go on):
//
//  1. THE DRAIN CUT. The first pass after the grace records the store's last sequence. A
//     direct copy can have been skipped only by a look taken before the release (at most
//     mqttStoreViewTTL old when it skipped), and its store copy was stored before the device
//     had its PUBACK, so every store copy that is the only copy of a skipped direct copy is at
//     or below the cut. Above it, every store copy's direct copy was forwarded too.
//  2. THE DURABLES, once each has acked everything up to the cut, or holds nothing at all.
//     "Holds nothing at all" alone would never be reached by a durable that devices keep
//     publishing into, however well its reader keeps up.
//  3. THE STORE: the gateway's discard policy, the setting-off bound, and no mark of this
//     instance. Only now, with the durables gone, can discard old or a lower bound evict
//     nothing the platform still needs.
func (nmgr *NatsManager) finishMqttFailClosed(ctx context.Context, info *nats.StreamInfo, marks mqttStoreMarks, rep mqttFailClosedReport) (mqttFailClosedReport, error) {
	opt := nats.Context(ctx)
	rep.marked = true
	rep.standing = mqttStandingReleased
	rep.state = mqttStateOf(info, rep.standing)
	durables := MqttFailClosedDurables(nmgr.Microservice.InstanceId)
	drainTo := marks.drainTo
	if !marks.hasDrainTo {
		drainTo = info.State.LastSeq
		cfg := info.Config
		md := make(map[string]string, len(cfg.Metadata)+1)
		for k, v := range cfg.Metadata {
			md[k] = v
		}
		md[mqttFailClosedDrainToKey] = strconv.FormatUint(drainTo, 10)
		cfg.Metadata = md
		if err := mqttFenced(ctx); err != nil {
			return rep, err
		}
		updated, err := nmgr.js.UpdateStream(&cfg, opt)
		if err != nil {
			return rep, fmt.Errorf("recording the drain cut of %s: %w", MqttMessageStore, err)
		}
		info = updated
		nmgr.mqttView().observe(updated, time.Now())
	}
	for _, d := range durables {
		ci, err := nmgr.js.ConsumerInfo(MqttMessageStore, d.Name, opt)
		if errors.Is(err, nats.ErrConsumerNotFound) {
			continue
		}
		if err != nil {
			return rep, fmt.Errorf("reading the platform durable %s on %s: %w", d.Name, MqttMessageStore, err)
		}
		rep.presentDurables = append(rep.presentDurables, d.Name)
		empty := ci.NumPending == 0 && ci.NumAckPending == 0
		if !empty && ci.AckFloor.Stream < drainTo {
			// Not drained: below the cut, the store copies in it may be the only copies of
			// what a pod with the setting on skipped. The next pass looks again.
			return rep, nil
		}
	}
	for _, d := range durables {
		if err := mqttFenced(ctx); err != nil {
			return rep, err
		}
		if err := nmgr.js.DeleteConsumer(MqttMessageStore, d.Name, opt); err != nil && !errors.Is(err, nats.ErrConsumerNotFound) {
			return rep, fmt.Errorf("deleting the platform durable %s from %s: %w", d.Name, MqttMessageStore, err)
		}
	}
	rep.presentDurables = nil
	cfg := info.Config
	applyStreamDiscard(&cfg)
	cfg.MaxBytes = nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.MqttStoreMaxBytes
	md := make(map[string]string, len(cfg.Metadata))
	for k, v := range cfg.Metadata {
		if !strings.HasPrefix(k, mqttFailClosedKeyPrefix) {
			md[k] = v
		}
	}
	cfg.Metadata = md
	if err := mqttFenced(ctx); err != nil {
		return rep, err
	}
	updated, err := nmgr.js.UpdateStream(&cfg, opt)
	if err != nil {
		return rep, fmt.Errorf("handing %s back to the gateway's discard policy: %w", MqttMessageStore, err)
	}
	rep.finished, rep.marked = true, false
	rep.standing = mqttStandingPristine
	rep.state = mqttStateOf(updated, rep.standing)
	nmgr.mqttView().observe(updated, time.Now())
	log.Info().Str("stream", MqttMessageStore).
		Msg("Removed the drained platform durables from the released MQTT message store; it is the gateway's store again " +
			"(its discard policy and the setting-off bound)")
	return rep, nil
}

// errMqttFailClosedLockLost is a pass's refusal to write once it can no longer show it holds
// the lock (its renewal failed: the entry expired and another pod may have taken it).
var errMqttFailClosedLockLost = errors.New("the fail-closed store's lock was lost during the pass")

// mqttFenceKey carries a pass's lock fence in its context; see mqttFenced.
type mqttFenceKey struct{}

// mqttFenced renews the lock the pass holds, compare-and-set, and fails when it cannot: call
// it immediately before every write a pass makes. Outside a pass holding the lock it is a
// no-op (nothing in production writes the store outside one).
func mqttFenced(ctx context.Context) error {
	if f, ok := ctx.Value(mqttFenceKey{}).(func() error); ok {
		return f()
	}
	return nil
}

// withMqttFailClosedLock runs fn holding the instance's fail-closed lock, waiting up to wait
// for it. held is false when the lock was not taken in time, and fn did not run.
//
// It is the locks bucket every service's DistributedLock uses, with a key of its own, but not
// WithLock: a watch pass must not wait out WithLock's retries for a lock another pod is
// already making the pass under, and it must not log at info every 30 seconds.
//
// fn gets a context carrying the lock's FENCE (mqttFenced): a compare-and-set renewal of the
// entry, against the revision this pass last wrote. The bucket's entry lifetime is set by
// whichever service created the bucket first, which in a real install is 5 s, so the lock is
// held only while every write is preceded by a renewal that succeeds.
func (nmgr *NatsManager) withMqttFailClosedLock(ctx context.Context, wait time.Duration, fn func(ctx context.Context) error) (held bool, err error) {
	kv, err := nmgr.mqttFailClosedLockBucket()
	if err != nil {
		return false, fmt.Errorf("opening the fail-closed store's lock: %w", err)
	}
	key := kvKey(mqttFailClosedLockName)
	value := []byte(nmgr.Microservice.InstanceId)
	deadline := time.Now().Add(wait)
	var rev uint64
	for {
		r, err := kv.Create(key, value)
		if err == nil {
			rev = r
			break
		}
		if !errors.Is(err, nats.ErrKeyExists) {
			return false, fmt.Errorf("taking the fail-closed store's lock: %w", err)
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	lost := false
	fence := func() error {
		if lost {
			return errMqttFailClosedLockLost
		}
		r, err := kv.Update(key, value, rev)
		if err != nil {
			lost = true
			return fmt.Errorf("%w: %v", errMqttFailClosedLockLost, err)
		}
		rev = r
		return nil
	}
	defer func() {
		// Only our own entry: a revision-checked delete is a no-op if it expired and another
		// pod has taken the lock since.
		_ = kv.Delete(key, nats.LastRevision(rev))
	}()
	return true, fn(context.WithValue(ctx, mqttFenceKey{}, fence))
}

// mqttFailClosedLockBucket opens the instance's locks bucket once.
func (nmgr *NatsManager) mqttFailClosedLockBucket() (nats.KeyValue, error) {
	nmgr.mqttLockMu.Lock()
	defer nmgr.mqttLockMu.Unlock()
	if nmgr.mqttLockKV != nil {
		return nmgr.mqttLockKV, nil
	}
	kv, err := nmgr.KeyValueStore(lockBucket, LockBucketName(nmgr.Microservice.InstanceId), mqttFailClosedLockTTL)
	if err != nil {
		return nil, err
	}
	nmgr.mqttLockKV = kv
	return kv, nil
}

// foreignInstances names the other DeviceChain instances this broker account serves, as far
// as the broker can show them.
//
// HOW IT LOOKS, and what it can and cannot see. Two kinds of evidence, both read from the
// broker rather than from configuration, because no document says whether a broker is shared:
//
//   - the message store's owner metadata naming another instance: that instance enabled the
//     fail-closed store first;
//   - a platform stream named for another instance. Every instance creates one stream per
//     declared suffix (core/streams), named "<instance>_<suffix>" (StreamName), in the
//     account its services and its devices share. A stream of that shape under another
//     instance id is that instance.
//
// It cannot see an instance that has not created any stream yet. An instance installed onto
// the broker LATER is seen on the next pass, which is why MqttFailClosedWatch re-runs this
// and reports what it finds; the later instance, if it enables the setting, is refused at its
// own start because it sees this one.
func (nmgr *NatsManager) foreignInstances(ctx context.Context, instanceId string, store *nats.StreamInfo) ([]string, error) {
	found := map[string]bool{}
	if store != nil {
		if owner := store.Config.Metadata[mqttFailClosedOwnerKey]; owner != "" && owner != instanceId {
			found[owner] = true
		}
	}
	tails := make([]string, 0, len(streams.All))
	for _, s := range streams.All {
		tails = append(tails, "_"+sanitizeName(s.Suffix))
	}
	names, err := streamNamesStrict(ctx, nmgr.nc)
	if err != nil {
		return nil, fmt.Errorf("listing the broker's streams to look for other instances: %w", err)
	}
	for _, name := range names {
		if other, ok := otherInstanceStream(name, instanceId, tails); ok {
			found[other] = true
		}
	}
	out := make([]string, 0, len(found))
	for id := range found {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// streamNamesStrict pages through every stream name in the connection's account.
//
// It does not use nats.go's StreamNames, which returns a channel that simply closes when a
// page request fails: a listing cut short by one timeout would read as "no other instance",
// and the shared-broker refusal would pass for the wrong reason.
func streamNamesStrict(ctx context.Context, nc *nats.Conn) ([]string, error) {
	var names []string
	for offset := 0; ; {
		payload, err := json.Marshal(struct {
			Offset int `json:"offset"`
		}{Offset: offset})
		if err != nil {
			return nil, err
		}
		rctx, cancel := context.WithTimeout(ctx, mqttFailClosedRequestTimeout)
		msg, err := nc.RequestWithContext(rctx, "$JS.API.STREAM.NAMES", payload)
		cancel()
		if err != nil {
			return nil, err
		}
		var resp struct {
			Error   *jsAPIError `json:"error"`
			Total   int         `json:"total"`
			Streams []string    `json:"streams"`
		}
		if err := json.Unmarshal(msg.Data, &resp); err != nil {
			return nil, fmt.Errorf("reading the stream listing: %w", err)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("the broker refused the stream listing: %s", resp.Error.Description)
		}
		names = append(names, resp.Streams...)
		offset += len(resp.Streams)
		// An empty page ends it as well as reaching the total, so a total the server cannot
		// page to (a concurrent delete) does not spin here.
		if len(resp.Streams) == 0 || offset >= resp.Total {
			return names, nil
		}
	}
}

// otherInstanceStream reports whether a stream name is a platform stream of an instance
// other than instanceId, and which. A name is THIS instance's if any declared suffix reads it
// as one, since a suffix that ends in another suffix could otherwise read part of our own
// suffix as an instance id.
func otherInstanceStream(name, instanceId string, tails []string) (string, bool) {
	var other string
	for _, tail := range tails {
		prefix, ok := strings.CutSuffix(name, tail)
		if !ok || prefix == "" {
			continue
		}
		if prefix == instanceId {
			return "", false
		}
		if other == "" && core.ValidateToken(prefix) == nil {
			other = prefix
		}
	}
	return other, other != ""
}

// MqttFailClosedMetrics are the watch's series. Build them once, in the service's initialize
// phase, and only with the setting on: with it off nothing here is registered, so the service
// exports exactly what it did before the setting existed.
type MqttFailClosedMetrics struct {
	// durablePresent is 1 per platform durable that exists after the last pass, 0 for one
	// the pass could not create.
	durablePresent *prometheus.GaugeVec
	// durableMissing counts each pass that found a platform durable missing (and created it
	// again). Every message the store took for it while it was missing was acknowledged and
	// skipped, so each count is loss already.
	durableMissing *prometheus.CounterVec
	// storeRepaired counts each store setting a pass had to put back.
	storeRepaired *prometheus.CounterVec
	// foreignInstances is how many other instances the last pass saw on this broker.
	foreignInstances prometheus.Gauge
	// durablePending and durableAckPending are each platform durable's backlog: messages of
	// its kind in the store it has not been handed yet, and messages handed to it and not yet
	// acknowledged. See sampleMqttFailClosedDurables for why they are not the stream sampler's.
	durablePending, durableAckPending *prometheus.GaugeVec
}

// mqttFailClosedSettings are the store settings a pass can repair, the label values of
// storeRepaired. "store" is the whole store having been deleted.
var mqttFailClosedSettings = []string{"store", "retention", "subjects", "discard", "maxBytes"}

// NewMqttFailClosedMetrics registers the watch's series on ms, each exported at zero from the
// start so that the first event after a restart is an increase.
func NewMqttFailClosedMetrics(ms *core.Microservice, instanceId string) *MqttFailClosedMetrics {
	m := &MqttFailClosedMetrics{
		durablePresent: ms.NewGaugeVec("mqtt_failclosed_durable_present",
			"1 when a platform durable on the MQTT gateway's message store exists, 0 when the last check "+
				"could not create it. Without it, QoS 1 publishes of its kind are acknowledged and not kept.",
			[]string{"durable"}),
		durableMissing: ms.NewCounterVec("mqtt_failclosed_durable_missing_total",
			"Checks that found a platform durable on the MQTT gateway's message store missing. Each one "+
				"means QoS 1 publishes of its kind were acknowledged and not kept until it was created again.",
			[]string{"durable"}),
		storeRepaired: ms.NewCounterVec("mqtt_failclosed_store_repaired_total",
			"Checks that found a setting of the MQTT gateway's message store changed from the fail-closed "+
				"config and put it back, by setting.",
			[]string{"setting"}),
		foreignInstances: ms.NewGauge("mqtt_failclosed_foreign_instances",
			"Other DeviceChain instances the last check saw on this broker. Above 0, the instances share one "+
				"MQTT message store and one bound."),
		durablePending: ms.NewGaugeVec("mqtt_failclosed_durable_pending",
			"Messages in the MQTT gateway's message store that this platform durable has not been handed yet, "+
				"as of the last 30 s check: of its own kind for command responses and device reports, of every kind "+
				"for the unfiltered events durable. Absent while the durable cannot be read.",
			[]string{"durable"}),
		durableAckPending: ms.NewGaugeVec("mqtt_failclosed_durable_ack_pending",
			"Messages handed to this platform durable on the MQTT gateway's message store and not yet "+
				"acknowledged, as of the last 30 s check. Absent while the durable cannot be read.",
			[]string{"durable"}),
	}
	m.init(instanceId)
	return m
}

func (m *MqttFailClosedMetrics) init(instanceId string) {
	for _, d := range MqttFailClosedDurables(instanceId) {
		m.durableMissing.WithLabelValues(d.Name)
	}
	for _, s := range mqttFailClosedSettings {
		m.storeRepaired.WithLabelValues(s)
	}
}

// MqttFailClosedWatch runs a fail-closed pass on an interval and reports it.
type MqttFailClosedWatch struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// StartMqttFailClosedWatch starts the watch. Stop it before the broker connection closes.
//
// Every event-sources pod runs one, whatever its setting: a pod with it on holds the store (or
// stands by for pods with another bound), and a pod with it off is what releases a store whose
// holders are gone and later removes its drained durables. m is nil with the setting off, so
// such a pod registers and exports nothing; on a store that carries no mark of this instance
// its pass is one read and writes nothing. onPass, when not nil, is told after each pass that
// completed whether the store carries a mark of this instance: the cue for a pod with the
// setting off that there are platform durables for it to read.
func (nmgr *NatsManager) StartMqttFailClosedWatch(ctx context.Context, m *MqttFailClosedMetrics, interval time.Duration,
	onPass func(marked bool)) *MqttFailClosedWatch {
	ctx, cancel := context.WithCancel(ctx)
	w := &MqttFailClosedWatch{cancel: cancel}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				rep, err := nmgr.checkMqttFailClosed(ctx, m)
				if err == nil && rep.standing != mqttStandingSkipped && onPass != nil && ctx.Err() == nil {
					onPass(rep.marked)
				}
			}
		}
	}()
	return w
}

// Stop ends the watch and waits for a pass in progress to return.
func (w *MqttFailClosedWatch) Stop() {
	if w == nil {
		return
	}
	w.cancel()
	w.wg.Wait()
}

// checkMqttFailClosed is one pass of the watch, reported on m (nil: report nothing).
func (nmgr *NatsManager) checkMqttFailClosed(ctx context.Context, m *MqttFailClosedMetrics) (mqttFailClosedReport, error) {
	rep, err := nmgr.mqttFailClosedPass(ctx, false)
	if ctx.Err() != nil {
		return rep, ctx.Err()
	}
	if err != nil {
		log.Warn().Err(err).Str("stream", MqttMessageStore).Msg("The fail-closed MQTT store check did not complete")
	}
	if m == nil || rep.standing == mqttStandingSkipped {
		return rep, err
	}
	// As with the durable gauge below, only a look that completed moves this one: a pass
	// that could not list the streams has not seen the other instance go away.
	if rep.foreignKnown {
		m.foreignInstances.Set(float64(len(rep.foreign)))
	}
	if len(rep.foreign) > 0 {
		log.Error().Strs("instances", rep.foreign).Str("stream", MqttMessageStore).
			Msg("The broker also serves other DeviceChain instances. They share this instance's fail-closed MQTT " +
				"message store and its bound: one instance's backlog refuses the other's devices. Turn " +
				"infrastructure.nats.mqttFailClosed off, or give the instances separate brokers")
	}
	if rep.took && !rep.createdStore {
		log.Warn().Str("stream", MqttMessageStore).
			Msg("Took the MQTT message store over as the fail-closed store: the pods that held it before (with the " +
				"setting off, or with another bound) are gone")
	}
	for _, s := range rep.repaired {
		m.storeRepaired.WithLabelValues(s).Inc()
	}
	if slices.Contains(rep.repaired, "store") {
		log.Error().Str("stream", MqttMessageStore).
			Msg("The MQTT message store was missing and has been created again. Until then the gateway " +
				"could not store, and refused, QoS 1 publishes")
	}
	if len(rep.repaired) > 0 {
		log.Error().Strs("settings", rep.repaired).Str("stream", MqttMessageStore).
			Msg("Put the MQTT message store back into its fail-closed config. While the change lasted, a full " +
				"store may have discarded messages it had acknowledged")
	}
	for _, name := range rep.createdDurables {
		if !rep.counted {
			continue
		}
		m.durableMissing.WithLabelValues(name).Inc()
		log.Error().Str("durable", name).Str("stream", MqttMessageStore).
			Msg("A platform durable on the MQTT message store was missing and has been created again. While it " +
				"was missing, QoS 1 publishes of its kind were acknowledged to devices and not kept")
	}
	// Only what the pass KNOWS moves the gauge. A durable it never reached (the broker did
	// not answer) keeps its last value: a broker that does not answer has alerts of its own,
	// and reading "could not ask" as "missing" would page for the wrong failure.
	for _, name := range rep.presentDurables {
		m.durablePresent.WithLabelValues(name).Set(1)
	}
	for _, name := range rep.absentDurables {
		m.durableMissing.WithLabelValues(name).Inc()
		m.durablePresent.WithLabelValues(name).Set(0)
		log.Error().Str("durable", name).Str("stream", MqttMessageStore).
			Msg("A platform durable on the MQTT message store is missing and could not be created. QoS 1 " +
				"publishes of its kind are being acknowledged to devices and not kept")
	}
	nmgr.sampleMqttFailClosedDurables(ctx, m)
	return rep, err
}

// sampleMqttFailClosedDurables exports each platform durable's backlog, while the store carries
// a mark of this instance (it has platform durables to have a backlog).
//
// 🔴 NOT THE STREAM SAMPLER'S PER-DURABLE SERIES. Those (trackedDurables) are built for a
// durable that reads its stream alone, and on this store they would report the wrong thing:
// the store holds every QoS 1 publish of every kind and the gateway's own session messages, so
// a durable's cursor moves past sequences that were simply not its kind, which the unread-loss
// counter reads as messages removed before it was handed them, and its unread ratio divides
// one kind's backlog by the whole store's capacity. The store is discard NEW, so nothing is
// removed unread while it is declared as it should be; a changed discard policy is what
// mqtt_failclosed_store_repaired_total reports. What is left to see is where the store's fill
// is: which kind's reader is behind, when the store fills and refuses.
//
// A durable that cannot be read has its series withdrawn, as the stream sampler does, so a
// stale backlog is never read as a current one; so do all of them while the store carries no
// mark (it is missing, or was not created again): there are no platform durables to have one.
func (nmgr *NatsManager) sampleMqttFailClosedDurables(ctx context.Context, m *MqttFailClosedMetrics) {
	if m == nil {
		return
	}
	if !nmgr.MqttMessageStoreMarked() {
		m.durablePending.Reset()
		m.durableAckPending.Reset()
		return
	}
	for _, d := range MqttFailClosedDurables(nmgr.Microservice.InstanceId) {
		rctx, cancel := context.WithTimeout(ctx, mqttFailClosedRequestTimeout)
		ci, err := nmgr.js.ConsumerInfo(MqttMessageStore, d.Name, nats.Context(rctx))
		cancel()
		if err != nil {
			m.durablePending.DeleteLabelValues(d.Name)
			m.durableAckPending.DeleteLabelValues(d.Name)
			continue
		}
		m.durablePending.WithLabelValues(d.Name).Set(float64(ci.NumPending))
		m.durableAckPending.WithLabelValues(d.Name).Set(float64(ci.NumAckPending))
	}
}
