// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// The MQTT gateway's own JetStream streams. nats-server creates these lazily,
// per account, the first time MQTT is used; the names are fixed in its source
// (server/mqtt.go) rather than configurable.
const (
	// MqttMessageStore holds a SECOND copy of every QoS>=1 MQTT message, in
	// addition to the copy that lands in the platform stream whose subject
	// matches it. nats-server creates it with interest retention and NO size
	// limit whatsoever — no MaxBytes, no MaxMsgs, no MaxAge — and exposes no
	// server option to bound it.
	MqttMessageStore = "$MQTT_msgs"
	// MqttPubRelStore holds outgoing QoS 2 PUBREL records. Also unbounded as
	// created. Low volume, but bounded here for the same reason: an unbounded
	// stream in the shared account is a hole in the disk budget regardless of how
	// fast anyone expects it to fill.
	MqttPubRelStore = "$MQTT_out"
	// MqttQoS2InStore holds inbound QoS 2 messages awaiting their PUBREL, one per
	// (session, packet id). It is the one gateway stream a DEVICE can fill on
	// purpose: nats-server deletes a record only when the sender's PUBREL arrives,
	// so firmware that opens QoS 2 publishes and never completes the handshake
	// accumulates up to 65,535 messages per session with nothing reclaiming them.
	//
	// It is created with DiscardNew, so bounding it means a flood is REFUSED at
	// publish rather than evicting anything — the best failure of the three, and
	// the reason bounding it is strictly an improvement.
	MqttQoS2InStore = "$MQTT_qos2in"
	// MqttSessionStore holds one record per persistent MQTT session, keyed by a HASH of
	// the client id. Deliberately unbounded (see below); named here because the ADR-077
	// tenant purge has to talk about it — its subject carries no tenant, but its record
	// value does.
	MqttSessionStore = "$MQTT_sess"
	// MqttRetainedStore holds one record per retained topic, under the original subject.
	// Deliberately unbounded (see below). Its subjects DO carry the tenant, so a
	// tenant-scoped purge reaches it.
	MqttRetainedStore = "$MQTT_rmsgs"
)

// The gateway's other two streams — $MQTT_sess and $MQTT_rmsgs — are deliberately
// NOT bounded here. Both are LimitsPolicy with MaxMsgsPer=1 (one record per
// session, one per retained topic), so they grow with session and topic count
// rather than with message volume. More importantly they are created with the
// default DiscardOld: giving them a byte ceiling would make a full stream silently
// EVICT live sessions and retained messages, breaking session resumption rather
// than protecting anything. Their growth is tracked as part of the unaccounted
// headroom instead.

// mqttStoreLookupBackoff bounds how long ReconcileMqttStores waits for the
// gateway to create its streams. They appear on the first MQTT client
// connection, so a service that reconciles immediately after connecting can
// legitimately arrive first.
var mqttStoreLookupBackoff = []time.Duration{0, time.Second, 2 * time.Second, 5 * time.Second}

// ReconcileMqttStores applies a byte ceiling to the MQTT gateway's own streams.
//
// Why this is necessary: a QoS>=1 MQTT publish is stored TWICE — once in the
// platform stream matching its subject, and once in $MQTT_msgs. That second copy
// lives in the same account and counts against the same server-level
// max_file_store as everything the disk budget does account for, but it reserves
// nothing up front and has no ceiling of its own. Left alone it can consume all
// the headroom the budget leaves, producing exactly the "insufficient storage
// resources available" crashloop the budget exists to prevent — just from the
// unaccounted side.
//
// $MQTT_msgs uses INTEREST retention, so it skips the write for a subject no
// consumer on it is interested in, and still acknowledges the publish. Device
// telemetry normally has no such consumer: event-sources does not subscribe over
// MQTT (it reads the device-events capture stream), so what the store holds is the
// subjects some MQTT session has a QoS 1 subscription on. Which subjects those are
// is up to the clients, device firmware included, so the store's size is not
// something the platform controls either way, and it is bounded.
//
// Bounding trades one failure for a better one. Unbounded, exhaustion crashloops
// every stream-creating service. Bounded, each stream fails in the way its own
// discard policy dictates: $MQTT_msgs and $MQTT_out are DiscardOld, so they drop
// the oldest undelivered messages — bad for those messages, survivable for the
// cluster, and visible in the stream metrics. $MQTT_qos2in is DiscardNew, so it
// REFUSES new QoS 2 publishes instead of evicting anything.
//
// With infrastructure.nats.mqttFailClosed on, $MQTT_msgs is not this function's:
// ReconcileMqttFailClosed declares it (discard NEW, the hot ceiling, platform
// durables) and a failure there is fatal, so the caller bounds only the QoS 2
// stores, through ReconcileMqttQoS2Stores. With it off, a store that carries a mark
// of THIS instance is handed to the fail-closed pass, which releases it once no pod
// with the setting on holds it; a store that carries no mark of it is treated exactly
// as it always was.
//
// This runs on EVERY startup rather than once at install. nats-server looks the
// streams up before creating them and never reconciles config back onto an
// existing one, so a bound set here should persist — but reapplying each boot
// means correctness does not depend on that being true, which is not a property
// worth betting a cluster on.
//
// A missing stream is not an error. The gateway creates these lazily and an
// instance where nothing has ever connected over MQTT genuinely has none.
func (nmgr *NatsManager) ReconcileMqttStores(ctx context.Context, maxBytes, qos2MaxBytes int64) error {
	if maxBytes <= 0 || qos2MaxBytes <= 0 {
		// Fail safe rather than write an unlimited ceiling: 0 means UNLIMITED to
		// JetStream, so coercing here keeps a misconfiguration from silently
		// undoing the bound (ADR-023 never-unlimited).
		return errors.New("mqtt store ceilings must be positive")
	}
	for _, s := range MqttStoreBounds(maxBytes, qos2MaxBytes) {
		if err := nmgr.boundMqttStore(ctx, s.Name, s.MaxBytes); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileMqttQoS2Stores bounds only the gateway's two QoS 2 stores. It is the half of
// ReconcileMqttStores that still applies with infrastructure.nats.mqttFailClosed on, when
// ReconcileMqttFailClosed owns $MQTT_msgs.
func (nmgr *NatsManager) ReconcileMqttQoS2Stores(ctx context.Context, qos2MaxBytes int64) error {
	if qos2MaxBytes <= 0 {
		return errors.New("mqtt store ceilings must be positive")
	}
	for _, s := range MqttStoreBounds(1, qos2MaxBytes) {
		if s.Name == MqttMessageStore {
			continue
		}
		if err := nmgr.boundMqttStore(ctx, s.Name, s.MaxBytes); err != nil {
			return err
		}
	}
	return nil
}

// MqttStoreBound is one gateway stream and the ceiling applied to it.
type MqttStoreBound struct {
	Name     string
	MaxBytes int64
}

// MqttStoreBounds is the exact set of ceilings ReconcileMqttStores applies.
//
// It is exported and shared rather than inlined because a bounded stream reserves
// its ceiling up front, so this set IS the MQTT half of the disk budget. Deriving
// the budget from the same list the reconciler walks means bounding a new stream
// cannot silently understate the reservation — the failure that made the
// platform's own budget wrong before core/streams existed.
//
// The QoS 2 stores take a smaller ceiling than the message store. QoS 2 is not
// the recommended publish mode and buys nothing the platform's own opt-in event
// de-duplication does not, so sizing its buffers like the primary telemetry path
// would reserve disk the recommended configuration never touches — and every byte
// reserved here is a byte the platform's own streams cannot use.
func MqttStoreBounds(maxBytes, qos2MaxBytes int64) []MqttStoreBound {
	return []MqttStoreBound{
		{MqttMessageStore, maxBytes},
		{MqttPubRelStore, qos2MaxBytes},
		{MqttQoS2InStore, qos2MaxBytes},
	}
}

// boundMqttStore sets MaxBytes on one gateway stream, waiting briefly for it to
// be created if it is not there yet.
func (nmgr *NatsManager) boundMqttStore(ctx context.Context, name string, maxBytes int64) error {
	var lastErr error
	for _, wait := range mqttStoreLookupBackoff {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		info, err := nmgr.js.StreamInfo(name)
		if errors.Is(err, nats.ErrStreamNotFound) {
			continue
		}
		if err != nil {
			// Retry rather than give up on the first transient failure. This runs
			// during startup, when a JetStream API timeout during a leader election
			// is entirely ordinary — and because the caller treats failure as
			// non-fatal (with mqttFailClosed off; with it on, $MQTT_msgs is reconciled
			// by ReconcileMqttFailClosed, which is fatal), giving up here leaves the
			// stream unbounded until the next restart, which could be weeks away. Returning the error only after the
			// schedule is exhausted keeps the loud failure for a real problem.
			lastErr = err
			continue
		}
		cfg := info.Config
		// A store that carries a mark of this instance (it made it fail-closed, or released
		// it and has not finished removing its durables) is the fail-closed pass's: with the
		// setting off, that pass releases it once no pod with the setting on holds it any
		// more, and removes its durables once they are drained (mqttFailClosedPass). A store
		// with no mark of this instance's is handled exactly as it was before the setting
		// existed.
		if name == MqttMessageStore {
			if marks := nmgr.marksOf(info); marks.owned || marks.released {
				_, err := nmgr.mqttFailClosedPass(ctx, true)
				return err
			}
		}
		// A store a fail-closed pass created and did not finish (it stopped between creating
		// the store and creating its durables) still has only the subject it was created with,
		// so it takes none of the gateway's copies and the gateway refuses every QoS 1 publish.
		// With no mark on it, it is nobody's: it gets the gateway's subjects back.
		pending := false
		if name == MqttMessageStore {
			var perr error
			if pending, perr = nmgr.isAbandonedPendingMqttStore(ctx, info); perr != nil {
				lastErr = perr
				continue
			}
		}
		if pending {
			cfg.Subjects = []string{mqttMsgsSubjectPrefix + ">"}
		}
		if cfg.MaxBytes == maxBytes && !pending {
			return nil
		}
		previous := cfg.MaxBytes
		cfg.MaxBytes = maxBytes
		if _, err := nmgr.js.UpdateStream(&cfg); err != nil {
			lastErr = err
			continue
		}
		if pending {
			log.Warn().Str("stream", name).
				Msg("Gave the MQTT message store the gateway's subjects back: a fail-closed check created it and " +
					"stopped before finishing, so until now it refused every QoS 1 device publish")
		}
		log.Info().Str("stream", name).Int64("maxBytes", maxBytes).Int64("previous", previous).
			Msg("Bounded MQTT gateway stream so it cannot consume the JetStream disk budget's headroom")
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	// Nothing has used MQTT on this instance yet. Say so at info rather than warn:
	// it is the normal state of an instance with no MQTT devices, and the next
	// periodic check (MqttStoreBoundWatch) applies the bound as soon as one connects.
	log.Info().Str("stream", name).
		Msg("MQTT gateway stream does not exist yet; nothing has connected over MQTT. The ceiling is applied by the periodic check once it appears.")
	return nil
}

// mqttFailClosedPendingAbandon is how long a store may keep only the subject a fail-closed pass
// created it with before a pod with the setting off gives it the gateway's subjects back. A
// pass finishes the store within seconds of creating it, under the fail-closed lock; this is
// long enough that a pass still running is never cut into. Its age is the BROKER's: the store's
// creation time against the time stamp of the broker's own answer (mqttStoreAge), so a pod
// whose clock runs ahead cannot hand the store its subjects while a pass is still finishing it
// (that would be loss: the gateway would take its copies with no durable interested). A
// variable so a test can shorten it.
var mqttFailClosedPendingAbandon = 2 * time.Minute

// isAbandonedPendingMqttStore reports a message store a fail-closed pass created and never
// finished: its one subject is the one it is created with (mqttFailClosedPendingSubject), and
// the broker says it was created longer ago than mqttFailClosedPendingAbandon. The age is only
// asked for (one more API request) of a store that has that one subject.
func (nmgr *NatsManager) isAbandonedPendingMqttStore(ctx context.Context, info *nats.StreamInfo) (bool, error) {
	if info == nil || len(info.Config.Subjects) != 1 || info.Config.Subjects[0] != mqttFailClosedPendingSubject {
		return false, nil
	}
	age, err := nmgr.mqttStoreAge(ctx)
	if err != nil {
		return false, err
	}
	return age >= mqttFailClosedPendingAbandon, nil
}

// mqttStoreAge is how long the message store has existed by the broker's clock: its creation
// time against the time stamp of the answer to the stream-info request. nats.go's StreamInfo
// does not decode that time stamp, so this is a raw API request. On a broker that does not
// report one, the pod's clock stands in for it.
func (nmgr *NatsManager) mqttStoreAge(ctx context.Context) (time.Duration, error) {
	return nmgr.brokerAge(ctx, "$JS.API.STREAM.INFO."+MqttMessageStore, MqttMessageStore)
}
