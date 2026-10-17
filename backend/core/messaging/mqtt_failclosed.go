// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// per kind of device publish (events, command responses, device reports), each filtered to
// this instance. The gateway looks the store up before creating it and keeps a store it
// finds, so the declared config is the one it uses. Every behaviour named here is pinned
// against the real broker in mqtt_failclosed_guard_test.go.
//
// Three properties are what make this safe to depend on, and each is enforced here rather
// than assumed:
//
//   - THE RECONCILE IS FATAL. A store left discard-old, unbounded or without its durables
//     acknowledges what it then loses, which is the failure this exists to end. So when the
//     setting is on, a reconcile that cannot finish stops event-sources from starting.
//   - IT RUNS BEFORE event-sources' OWN MQTT-FACING COMPONENTS. event-sources calls it from
//     its broker manager's create hook, ahead of every reader, source and the presence tap.
//     (It cannot run before the BROKER's MQTT listener, which belongs to another process: a
//     device that connects before event-sources has ever started meets the gateway's own
//     store. Refusing those connections is the job of the broker's auth callout and is not
//     part of this file.)
//   - IT IS WATCHED. A durable deleted at runtime turns the store back into silent loss with
//     a normal PUBACK, so MqttFailClosedWatch re-runs the reconcile every 30 s, recreates
//     what is missing, and counts it on metrics an alert reads.
//
// 🔴 ONE INSTANCE PER BROKER ACCOUNT. The store is per account, and on a shared broker every
// instance's devices are in the same one, so they share one store and one bound: one
// instance's stalled reader refuses the other's devices, and each instance's reconcile
// rewrites the other's. The reconcile therefore refuses to enable the store on a broker it
// can see serving another instance (see foreignInstances for how it looks).

const (
	// mqttFailClosedOwnerKey is the stream metadata key naming the instance that owns the
	// fail-closed message store. It is what lets a second instance on the same broker see
	// that the store is already somebody's, and what tells a reconcile with the setting off
	// that this instance turned it on earlier and has to undo it.
	mqttFailClosedOwnerKey = "io.devicechain.mqtt-fail-closed.owner"

	// mqttFailClosedReleasedKey names the instance that turned the fail-closed store OFF
	// (releaseMqttFailClosed). The watch stands down on a store this instance released: in
	// a rolling update that turns the setting off, the pods still running with it on would
	// otherwise create the durables again behind the pods that removed them, and the last
	// of them to stop would leave unread durables on a discard-new store, which then
	// refuses every QoS 1 publish. A start with the setting on clears it.
	mqttFailClosedReleasedKey = "io.devicechain.mqtt-fail-closed.released"

	// mqttFailClosedArea is the functional area that owns the platform durables. Their names
	// are built from it, so it is fixed here rather than read from whichever service calls.
	mqttFailClosedArea = "event-sources"

	// mqttMessageStoreSourceTag marks a dedup id built from a $MQTT_msgs sequence; see
	// MqttMessageStoreDedupID.
	mqttMessageStoreSourceTag = "m"

	// MqttFailClosedWatchInterval is how often MqttFailClosedWatch re-runs the reconcile.
	MqttFailClosedWatchInterval = 30 * time.Second
)

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
	// Filter is the durable's filter subject: the gateway's store prefix in front of the
	// subject a device is granted to publish this kind of message on, with a wildcard for
	// the tenant and for the device.
	//
	// 🔑 THE TENANT IS A WILDCARD, AND THE TENANT PURGE RELIES ON IT. The purge deletes every
	// consumer on this store whose filter begins with "$MQTT.msgs.{instance}.{tenant}.", and
	// no valid tenant is "*", so these filters can never be matched by it
	// (TestTheTenantPurgeNeverDeletesThePlatformDurable and
	// TestTheTenantPurgeNeverDeletesAFailClosedDurable).
	Filter string
}

// MqttFailClosedDurables is the exact set of platform durables for an instance: one per kind
// of device QoS 1 publish, each matching exactly what natsauth grants a device for that kind.
func MqttFailClosedDurables(instanceId string) []MqttFailClosedDurable {
	durable := func(kind MqttFailClosedKind) string {
		return DurableName(instanceId, mqttFailClosedArea, "mqtt-msgs-"+string(kind))
	}
	return []MqttFailClosedDurable{
		{MqttFailClosedEvents, durable(MqttFailClosedEvents),
			mqttMsgsSubjectPrefix + DeviceEventsWildcard(instanceId)},
		{MqttFailClosedResponses, durable(MqttFailClosedResponses),
			mqttMsgsSubjectPrefix + DeviceScopedSubject(instanceId, "*", SubjectCommandResponses, "*")},
		{MqttFailClosedReports, durable(MqttFailClosedReports),
			mqttMsgsSubjectPrefix + DeviceScopedSubject(instanceId, "*", SubjectDeviceReports, "*")},
	}
}

// MqttFailClosedConsumerConfig is the config a platform durable is created with.
//
// It is the platform reader's consumer config (natsReader.consumerConfig) with DeliverNew
// and an explicit replica count: the server freezes a durable's config at creation, so
// the reader that later binds to it has to find the config it would have created itself.
// DeliverNew because the durable is created over a store that may already hold messages
// kept for the gateway's own session consumers, none of them the platform's.
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
//     re-creation.
//   - THE TENANT. Dedup is per stream and the inbound stream serves every tenant; see
//     DedupID in event-sources for why the prefix stays even where the sequence is unique.
func MqttMessageStoreDedupID(tenant string, epoch time.Time, seq uint64) string {
	if tenant == "" || seq == 0 || epoch.IsZero() || epoch.UnixNano() <= 0 {
		return ""
	}
	return tenant + "." + mqttMessageStoreSourceTag + strconv.FormatInt(epoch.UnixNano(), 36) +
		"." + strconv.FormatUint(seq, 36)
}

// MqttMessageStoreState is what a fail-closed reconcile leaves the store as.
type MqttMessageStoreState struct {
	// Epoch is the store's creation time, the epoch of MqttMessageStoreDedupID.
	Epoch    time.Time
	MaxBytes int64
	Replicas int
}

// mqttFailClosedReport is what one reconcile pass did.
type mqttFailClosedReport struct {
	state MqttMessageStoreState
	// createdStore is true when the pass created the store.
	createdStore bool
	// repaired names each store setting the pass had to put back.
	repaired []string
	// createdDurables names each durable the pass had to create.
	createdDurables []string
	// presentDurables names each durable that exists after the pass.
	presentDurables []string
	// absentDurables names each durable the pass found missing and could not create.
	absentDurables []string
	// foreign names the other instances the pass saw on this broker.
	foreign []string
	// released is true when the watch stood down on a store this instance released.
	released bool
}

// ReconcileMqttFailClosed declares the gateway's message store as the platform's
// fail-closed store and creates its platform durables, refusing a broker that serves another
// instance. Call it only with infrastructure.nats.mqttFailClosed on, before anything in the
// service connects over MQTT or reads the store, and treat its error as fatal.
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
		rep, err := nmgr.reconcileMqttFailClosed(ctx, true)
		if err == nil {
			log.Info().Str("stream", MqttMessageStore).Int64("maxBytes", rep.state.MaxBytes).
				Int("replicas", rep.state.Replicas).Time("epoch", rep.state.Epoch).
				Bool("created", rep.createdStore).Strs("repaired", rep.repaired).
				Strs("createdDurables", rep.createdDurables).
				Msg("The MQTT gateway's message store is the platform's fail-closed store: discard new, " +
					"bounded, with a platform durable for device events, command responses and device reports")
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

// reconcileMqttFailClosed is one pass of the reconcile. startup is the pass a start makes:
// it stops at a broker that serves another instance, and it enables the store even if this
// instance released it earlier. The watch's passes (startup false) instead report the other
// instances and go on keeping their own durables, because removing them would only turn the
// store's refusals into silent loss; and they stand down on a store this instance released
// (see mqttFailClosedReleasedKey).
func (nmgr *NatsManager) reconcileMqttFailClosed(ctx context.Context, startup bool) (mqttFailClosedReport, error) {
	var rep mqttFailClosedReport
	instanceId := nmgr.Microservice.InstanceId
	if err := core.ValidateToken(instanceId); err != nil {
		return rep, fmt.Errorf("%w: invalid instance id: %v", errMqttFailClosedUnfixable, err)
	}
	natscfg := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	maxBytes := natscfg.MqttMessageStoreMaxBytes()
	if maxBytes <= 0 {
		// 0 is UNLIMITED to JetStream; a fail-closed store without a bound is not one.
		return rep, fmt.Errorf("%w: its ceiling must be positive, got %d", errMqttFailClosedUnfixable, maxBytes)
	}
	opt := nats.Context(ctx)

	info, err := nmgr.js.StreamInfo(MqttMessageStore, opt)
	if errors.Is(err, nats.ErrStreamNotFound) {
		info = nil
	} else if err != nil {
		return rep, fmt.Errorf("reading %s: %w", MqttMessageStore, err)
	}

	// Look for other instances BEFORE writing anything: on a shared broker the right outcome
	// is to change nothing.
	foreign, err := nmgr.foreignInstances(ctx, instanceId, info)
	if err != nil {
		return rep, err
	}
	rep.foreign = foreign
	if len(foreign) > 0 && startup {
		return rep, fmt.Errorf("%w (%s): the MQTT gateway keeps ONE message store per broker account, "+
			"so with infrastructure.nats.mqttFailClosed on, one instance's backlog would refuse the other's "+
			"devices and each would rewrite the other's bound. Refusing to start. Turn mqttFailClosed off "+
			"for this instance: MQTT ingest then stays on the device-events capture stream",
			ErrMqttFailClosedSharedBroker, strings.Join(foreign, ", "))
	}

	if !startup && info != nil && info.Config.Metadata[mqttFailClosedReleasedKey] == instanceId {
		rep.released = true
		return rep, nil
	}

	desiredReplicas := nmgr.effectiveStreamReplicas()
	if info == nil {
		cfg := &nats.StreamConfig{
			Name:      MqttMessageStore,
			Subjects:  []string{mqttMsgsSubjectPrefix + ">"},
			Storage:   nats.FileStorage,
			Retention: nats.InterestPolicy,
			Discard:   nats.DiscardNew,
			MaxBytes:  maxBytes,
			Replicas:  desiredReplicas,
			Metadata:  map[string]string{mqttFailClosedOwnerKey: instanceId},
		}
		created, err := nmgr.js.AddStream(cfg, opt)
		switch {
		case err == nil:
			info = created
			rep.createdStore = true
		case errors.Is(err, nats.ErrStreamNameAlreadyInUse):
			// The gateway (or another replica of this service) created it between the lookup
			// and the create. Reconcile what is there instead.
			if info, err = nmgr.js.StreamInfo(MqttMessageStore, opt); err != nil {
				return rep, fmt.Errorf("reading %s after a concurrent create: %w", MqttMessageStore, err)
			}
		default:
			return rep, fmt.Errorf("creating %s: %w", MqttMessageStore, err)
		}
	}

	if !rep.createdStore {
		cfg := info.Config
		repaired, err := conformMqttFailClosedStore(&cfg, instanceId, maxBytes, desiredReplicas)
		if err != nil {
			return rep, err
		}
		if len(repaired) > 0 {
			updated, err := nmgr.js.UpdateStream(&cfg, opt)
			if err != nil {
				return rep, fmt.Errorf("updating %s into the fail-closed config (%s): %w",
					MqttMessageStore, strings.Join(repaired, ", "), err)
			}
			info = updated
			rep.repaired = repaired
		}
	}

	replicas := info.Config.Replicas
	if replicas < 1 {
		replicas = 1
	}
	rep.state = MqttMessageStoreState{Epoch: info.Created, MaxBytes: info.Config.MaxBytes, Replicas: replicas}
	if rep.state.Epoch.IsZero() {
		return rep, fmt.Errorf("%s reports no creation time, so no dedup id could be built from its sequences",
			MqttMessageStore)
	}

	for _, d := range MqttFailClosedDurables(instanceId) {
		created, absent, err := nmgr.ensureMqttFailClosedDurable(ctx, d, replicas)
		if absent {
			rep.absentDurables = append(rep.absentDurables, d.Name)
		}
		if err != nil {
			return rep, err
		}
		if created {
			rep.createdDurables = append(rep.createdDurables, d.Name)
		}
		rep.presentDurables = append(rep.presentDurables, d.Name)
	}
	return rep, nil
}

// conformMqttFailClosedStore puts a store's config into the fail-closed shape and names each
// setting it changed. It refuses what an update cannot change.
func conformMqttFailClosedStore(cfg *nats.StreamConfig, instanceId string, maxBytes int64, desiredReplicas int) ([]string, error) {
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
	if changed, refusedDownward := applyStreamReplicas(cfg, desiredReplicas); changed {
		repaired = append(repaired, "replicas")
	} else if refusedDownward {
		log.Warn().Str("stream", MqttMessageStore).Int("current", cfg.Replicas).Int("desired", desiredReplicas).
			Msg("The MQTT message store has more replicas than configured; it is left as it is, " +
				"because a starting pod does not scale replication down")
	}
	_, released := cfg.Metadata[mqttFailClosedReleasedKey]
	if cfg.Metadata[mqttFailClosedOwnerKey] != instanceId || released {
		md := make(map[string]string, len(cfg.Metadata)+1)
		for k, v := range cfg.Metadata {
			if k != mqttFailClosedReleasedKey {
				md[k] = v
			}
		}
		md[mqttFailClosedOwnerKey] = instanceId
		cfg.Metadata = md
		repaired = append(repaired, "owner")
	}
	return repaired, nil
}

// ensureMqttFailClosedDurable creates a platform durable if it is missing and puts its filter
// back if it has changed, reporting whether it had to create it and whether it is missing
// still (it was not there and could not be created).
//
// The filter is checked by reading it back, because AddConsumer on an existing durable with
// a changed filter returns success and keeps the old one
// (TestAddConsumerSilentlyIgnoresAChangedFilterSubject).
func (nmgr *NatsManager) ensureMqttFailClosedDurable(ctx context.Context, d MqttFailClosedDurable, replicas int) (created, absent bool, err error) {
	opt := nats.Context(ctx)
	ci, err := nmgr.js.ConsumerInfo(MqttMessageStore, d.Name, opt)
	if errors.Is(err, nats.ErrConsumerNotFound) {
		if _, err := nmgr.js.AddConsumer(MqttMessageStore, MqttFailClosedConsumerConfig(d, replicas), opt); err != nil {
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
		if _, err := nmgr.js.UpdateConsumer(MqttMessageStore, &cfg, opt); err != nil {
			return false, false, fmt.Errorf("restoring the filter of the platform durable %s on %s: %w",
				d.Name, MqttMessageStore, err)
		}
		log.Warn().Str("durable", d.Name).Str("filter", d.Filter).Str("was", ci.Config.FilterSubject).
			Msg("Restored the filter subject of a platform durable on the MQTT message store")
	}
	return false, false, nil
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
		msg, err := nc.RequestWithContext(ctx, "$JS.API.STREAM.NAMES", payload)
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

// releaseMqttFailClosedConfig turns a fail-closed store THIS instance owns back into the
// gateway's store, for a start with the setting off: the gateway's default discard policy,
// no owner mark, and the released mark (mqttFailClosedReleasedKey). It changes cfg only;
// the caller updates the stream and then removes the durables (removeMqttFailClosedDurables),
// in that order, so that a pod still running with the setting on sees the released mark and
// stops creating them before they are removed.
//
// Without it, turning the setting off would leave durables nobody reads on a discard-new
// store: QoS 1 device publishes would fill it, and the gateway would then refuse every one.
func (nmgr *NatsManager) releaseMqttFailClosedConfig(cfg *nats.StreamConfig) {
	applyStreamDiscard(cfg)
	md := make(map[string]string, len(cfg.Metadata)+1)
	for k, v := range cfg.Metadata {
		if k != mqttFailClosedOwnerKey {
			md[k] = v
		}
	}
	md[mqttFailClosedReleasedKey] = nmgr.Microservice.InstanceId
	cfg.Metadata = md
}

// removeMqttFailClosedDurables deletes this instance's platform durables, if there are any.
func (nmgr *NatsManager) removeMqttFailClosedDurables(ctx context.Context) error {
	opt := nats.Context(ctx)
	for _, d := range MqttFailClosedDurables(nmgr.Microservice.InstanceId) {
		if err := nmgr.js.DeleteConsumer(MqttMessageStore, d.Name, opt); err != nil && !errors.Is(err, nats.ErrConsumerNotFound) {
			return fmt.Errorf("deleting the platform durable %s from %s: %w", d.Name, MqttMessageStore, err)
		}
	}
	return nil
}

// mqttFailClosedMarks reports whether a store's config carries this instance's owner mark,
// and whether it carries this instance's released mark.
func (nmgr *NatsManager) mqttFailClosedMarks(cfg nats.StreamConfig) (owned, released bool) {
	me := nmgr.Microservice.InstanceId
	if me == "" {
		return false, false
	}
	return cfg.Metadata[mqttFailClosedOwnerKey] == me, cfg.Metadata[mqttFailClosedReleasedKey] == me
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
}

// mqttFailClosedSettings are the store settings a pass can repair, the label values of
// storeRepaired. "store" is the whole store having been deleted.
var mqttFailClosedSettings = []string{"store", "retention", "subjects", "discard", "maxBytes", "replicas", "owner"}

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

// MqttFailClosedWatch re-runs the fail-closed reconcile on an interval and reports it.
type MqttFailClosedWatch struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// StartMqttFailClosedWatch starts the watch. Stop it before the broker connection closes.
func (nmgr *NatsManager) StartMqttFailClosedWatch(ctx context.Context, m *MqttFailClosedMetrics, interval time.Duration) *MqttFailClosedWatch {
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
				nmgr.checkMqttFailClosed(ctx, m)
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

// checkMqttFailClosed is one pass of the watch.
func (nmgr *NatsManager) checkMqttFailClosed(ctx context.Context, m *MqttFailClosedMetrics) {
	rep, err := nmgr.reconcileMqttFailClosed(ctx, false)
	if ctx.Err() != nil {
		return
	}
	if rep.released {
		// This pod still has the setting on, and a pod with it off has released the store:
		// a rolling update turning it off, or a pod with an older config that restarted.
		// Either way the guarantee is not in force, so the durables read as missing here.
		for _, d := range MqttFailClosedDurables(nmgr.Microservice.InstanceId) {
			m.durablePresent.WithLabelValues(d.Name).Set(0)
		}
		log.Error().Str("stream", MqttMessageStore).
			Msg("infrastructure.nats.mqttFailClosed is on in this pod, but an event-sources pod with it off has " +
				"released the MQTT message store, so a QoS 1 PUBACK does not mean the platform holds the message. " +
				"Expected while a rolling update turns the setting off; otherwise restart this pod to declare the " +
				"store again")
		return
	}
	m.foreignInstances.Set(float64(len(rep.foreign)))
	if len(rep.foreign) > 0 {
		log.Error().Strs("instances", rep.foreign).Str("stream", MqttMessageStore).
			Msg("The broker also serves other DeviceChain instances. They share this instance's fail-closed MQTT " +
				"message store and its bound: one instance's backlog refuses the other's devices. Turn " +
				"infrastructure.nats.mqttFailClosed off, or give the instances separate brokers")
	}
	if rep.createdStore {
		m.storeRepaired.WithLabelValues("store").Inc()
		log.Error().Str("stream", MqttMessageStore).
			Msg("The MQTT message store was missing and has been created again. Until then the gateway " +
				"could not store, and refused, QoS 1 publishes")
	}
	for _, s := range rep.repaired {
		m.storeRepaired.WithLabelValues(s).Inc()
	}
	if len(rep.repaired) > 0 {
		log.Error().Strs("settings", rep.repaired).Str("stream", MqttMessageStore).
			Msg("Put the MQTT message store back into its fail-closed config. While the change lasted, a full " +
				"store may have discarded messages it had acknowledged")
	}
	for _, name := range rep.createdDurables {
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
	if err != nil {
		log.Warn().Err(err).Str("stream", MqttMessageStore).Msg("The fail-closed MQTT store check did not complete")
	}
}
