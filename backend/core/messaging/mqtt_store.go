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
// $MQTT_msgs uses INTEREST retention, so it normally skips the write when nobody
// is subscribed. That is not a reprieve here: event-sources holds a QoS-1
// subscription across the whole device-plane topic tree, so interest exists for
// every device subject at all times. Whether the second copy is written comes
// down to the QoS a device publishes at, which is third-party firmware.
//
// Bounding trades one failure for a better one. Unbounded, exhaustion crashloops
// every stream-creating service. Bounded, each stream fails in the way its own
// discard policy dictates: $MQTT_msgs and $MQTT_out are DiscardOld, so they drop
// the oldest undelivered messages — bad for those messages, survivable for the
// cluster, and visible in the stream metrics. $MQTT_qos2in is DiscardNew, so it
// REFUSES new QoS 2 publishes instead of evicting anything.
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
			// non-fatal, giving up here leaves the stream unbounded until the next
			// restart, which could be weeks away. Returning the error only after the
			// schedule is exhausted keeps the loud failure for a real problem.
			lastErr = err
			continue
		}
		if info.Config.MaxBytes == maxBytes {
			return nil
		}
		cfg := info.Config
		previous := cfg.MaxBytes
		cfg.MaxBytes = maxBytes
		if _, err := nmgr.js.UpdateStream(&cfg); err != nil {
			lastErr = err
			continue
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
	// startup after one connects will apply the bound.
	log.Info().Str("stream", name).
		Msg("MQTT gateway stream does not exist yet; nothing has connected over MQTT. Ceiling will be applied on a later startup.")
	return nil
}
