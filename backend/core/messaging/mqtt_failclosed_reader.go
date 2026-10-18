// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// THE READER OVER THE MESSAGE STORE: how the platform takes delivery of what the gateway
// PUBACKed, with infrastructure.nats.mqttFailClosed on.
//
// It is a durable reader like any other (batches, AckWait-paced retry, backpressure parking,
// the one-goroutine contract), bound to one of the platform durables
// (MqttFailClosedDurables), with five differences, each of which is here because the store is
// not a platform stream:
//
//   - IT NEVER CREATES ITS DURABLE. The fail-closed pass owns them: it creates them in the
//     order that makes skipping a direct copy safe, and removes them when the store is
//     released. A reader that recreated a durable on its own (the self-heal every other reader
//     has) would put one back on a released store. A missing durable is waited for, quietly,
//     and bound to once the pass has created it.
//   - IT HANDS EACH MESSAGE OUT AS THE DEVICE PUBLISHED IT: the subject without the store's
//     "$MQTT.msgs." prefix, and without the gateway's header, so the message reads exactly
//     like the direct copy a platform reader would have taken.
//   - IT FORWARDS ITS OWN KIND ONLY. The events durable is unfiltered, because a wildcard filter
//     over this store costs the broker a visit to every device's subject per delivered message
//     once the reader is behind (MqttFailClosedDurable.Filter), so it is delivered every kind,
//     another instance's publishes and the gateway's own; whatever does not match the kind's
//     subject (MqttFailClosedDurable.Forwards) is acked and dropped here.
//   - IT FORWARDS QoS 1 ONLY. A QoS 2 publish is delivered after its PUBREL down a path that
//     does not wait on the store, so its direct copy stays the one the platform forwards (the
//     readers of the direct streams skip only QoS 1); its store copy is acked and dropped here.
//   - EACH MESSAGE CARRIES THE DEDUP ID ITS FORWARD IS PUBLISHED UNDER (Message.DedupID):
//     MqttMessageStoreDedupID from its tenant, the epoch of the store incarnation it came from
//     (mqttStoreView.epochOf) and its sequence, or none when the epoch cannot be shown.

// mqttStoreBindRetry is how often a store reader whose durable is missing looks for it again.
var mqttStoreBindRetry = 2 * time.Second

// NewMqttMessageStoreReader builds the reader over this instance's platform durable of kind.
// The durable need not exist yet. Close it (MqttStoreReader.Close) when done; the manager's
// stop releases it too.
func (nmgr *NatsManager) NewMqttMessageStoreReader(kind MqttFailClosedKind) (*MqttStoreReader, error) {
	var d *MqttFailClosedDurable
	for _, x := range MqttFailClosedDurables(nmgr.Microservice.InstanceId) {
		if x.Kind == kind {
			x := x
			d = &x
		}
	}
	if d == nil {
		return nil, fmt.Errorf("messaging: no platform durable of kind %q on the MQTT message store", kind)
	}
	direct := mqttFailClosedDirectSuffix[kind]
	downstream := ""
	if kind == MqttFailClosedEvents {
		// The same gate the capture reader parks on: both forward into inbound-events.
		downstream = streams.ForwardsInto(streams.DeviceEventsCapture, mqttFailClosedArea)
	} else if streams.AppliesBackpressure(direct) {
		downstream = direct
	}
	r := &natsReader{
		nmgr:       nmgr,
		suffix:     direct,
		stream:     MqttMessageStore,
		subject:    d.Filter,
		durable:    d.Name,
		forwards:   d.Forwards,
		gate:       nmgr.Microservice.Readiness,
		downstream: downstream,
		store:      true,
	}
	if r.downstream != "" {
		nmgr.registerBackpressure(r.downstream)
	}
	r.configureFetch()
	if nmgr.nc != nil {
		r.reconnectsSeen = nmgr.nc.Stats().Reconnects
	}
	if err := r.bind(); err != nil && !isConsumerGone(err) {
		return nil, err
	}
	nmgr.streamMu.Lock()
	nmgr.storeReaders = append(nmgr.storeReaders, r)
	nmgr.streamMu.Unlock()
	log.Info().Str("durable", r.durable).Str("filter", r.subject).Str("forwards", r.forwards).
		Msg("Added reader over the MQTT message store")
	return &MqttStoreReader{r}, nil
}

// MqttStoreReader is a reader over the MQTT gateway's message store; see the file comment. It
// is a MessageReader (the durable reader's own ReadMessage and HandleResponse).
type MqttStoreReader struct{ *natsReader }

// Close releases the reader's subscription. The durable is left alone: it is the pass's.
func (s *MqttStoreReader) Close() {
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	s.unbound = true
	if old := s.sub.Swap(nil); old != nil {
		_ = old.Unsubscribe()
	}
}

// Durable is the name of the durable the reader is bound to.
func (s *MqttStoreReader) Durable() string { return s.durable }

// storeHandOut turns a delivered store message into what the reader hands out, or reports
// that it is one to ack and drop (keep false): a QoS 2 copy, or a message that is not of this
// reader's kind (the events durable is unfiltered, see MqttFailClosedDurable.Filter).
func (r *natsReader) storeHandOut(nm *nats.Msg, msg *Message, seq uint64, appended time.Time) (keep bool) {
	if r.forwards != r.subject && !storeSubjectMatches(r.forwards, nm.Subject) {
		return false
	}
	if qos := nm.Header.Get(mqttGatewayHeader); qos != "" && qos[0] != '1' {
		return false
	}
	msg.Subject = strings.TrimPrefix(nm.Subject, mqttMsgsSubjectPrefix)
	if msg.Headers != nil {
		delete(msg.Headers, mqttGatewayHeader)
		if len(msg.Headers) == 0 {
			msg.Headers = nil
		}
	}
	if tenant, ok := ParseTenantFromSubject(msg.Subject); ok {
		msg.DedupID = MqttMessageStoreDedupID(tenant, r.nmgr.mqttView().epochOf(appended), seq)
	}
	return true
}

// storeLost drops a store reader's subscription when its durable has gone (the pass removed
// it from a released store, or the store itself was deleted), so the read loop waits for it
// quietly instead of re-binding in a loop that would log at error.
func (r *natsReader) storeLost(err error) {
	r.bindMu.Lock()
	if old := r.sub.Swap(nil); old != nil {
		_ = old.Unsubscribe()
	}
	r.bindMu.Unlock()
	r.dropPending()
	if !r.storeMissingLogged {
		r.storeMissingLogged = true
		log.Warn().Err(err).Str("durable", r.durable).
			Msg("The platform durable on the MQTT message store is gone; waiting for the fail-closed check to create it")
	}
}

// storeRebind tries once to bind a store reader whose subscription was dropped.
func (r *natsReader) storeRebind() bool {
	if err := r.bind(); err != nil {
		if !isConsumerGone(err) && !errors.Is(err, errReaderUnbound) {
			log.Warn().Err(err).Str("durable", r.durable).Msg("Could not bind to the platform durable on the MQTT message store")
		}
		return false
	}
	if r.storeMissingLogged {
		r.storeMissingLogged = false
		log.Info().Str("durable", r.durable).Msg("Bound to the platform durable on the MQTT message store again")
	}
	return true
}

// storeSubjectMatches reports whether subject matches pattern, a subject whose only wildcards
// are single-token ones ("*"). It allocates nothing: it runs on every message an unfiltered
// store reader is delivered.
func storeSubjectMatches(pattern, subject string) bool {
	for {
		pt, prest, pmore := strings.Cut(pattern, ".")
		st, srest, smore := strings.Cut(subject, ".")
		if st == "" || (pt != "*" && pt != st) {
			return false
		}
		if !pmore || !smore {
			return pmore == smore
		}
		pattern, subject = prest, srest
	}
}
