// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	nats "github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-microservice/streams"
)

// THE MESSAGE STORE AS THIS MANAGER LAST SAW IT, and the two per-message decisions read from
// that look.
//
// A device QoS 1 publish exists twice: the gateway delivers it into the subject space (the
// DIRECT copy, which lands in whichever platform stream captures its subject: the device-events
// capture stream, command-responses or device-reports) and stores it in $MQTT_msgs (the STORE
// copy, the one the PUBACK waits for). With the fail-closed store in force the store copy is
// the one the platform forwards, so a reader of a direct stream skips the direct copy; without
// that, every QoS 1 publish would reach the platform twice.
//
//   - WHICH DIRECT COPIES ARE SKIPPED. Only one that carries the gateway's header with QoS 1
//     (QoS 0 is never stored; QoS 2 is delivered after its PUBREL down a path that does not
//     wait on the store, so the direct copy stays the one that counts), whose sequence in its
//     stream is above the store's "from" mark for that stream, and only while this manager's
//     look at the store is fresh (mqttStoreViewTTL) and shows it this instance's. The mark is
//     the stream's last sequence read AFTER the platform durables existed, so every direct copy
//     above it has a store copy a durable holds. A look that is stale, failed, or shows the store
//     released skips nothing: the cost of being unsure is a duplicate, never a loss.
//   - WHICH INCARNATION A STORE MESSAGE CAME FROM. The store can be deleted and created again,
//     which restarts its sequences, and the dedup id has to say which incarnation a sequence
//     belongs to (MqttMessageStoreDedupID). A message belongs to the incarnation created at C
//     with last stored message at L when C <= its stored time <= L in any look: that
//     incarnation existed throughout [C, L], and an
//     earlier one was deleted before C, so whatever was stored in that span was stored in it.
//     A message outside the window of the latest look is looked at once more, and if it is
//     still outside it gets no epoch and so no dedup id. Nothing here depends on when the
//     fetch ran against the look, which is what makes it hold across a recreation the watch
//     makes while the reader is mid-batch.

var (
	// mqttStoreViewRefresh is how old a look may be before a per-message decision takes a new
	// one. A variable so a test can shorten it.
	mqttStoreViewRefresh = 5 * time.Second
	// mqttStoreViewTTL is how old a look may be and still decide to SKIP a direct copy. The
	// release grace (mqttFailClosedReleaseGrace) is sized well above it, so a pod whose looks
	// keep failing has stopped skipping long before the durables that held the store copies of
	// what it skipped can be removed.
	mqttStoreViewTTL = 15 * time.Second
	// mqttStoreViewTimeout bounds one look.
	mqttStoreViewTimeout = 2 * time.Second
)

// mqttGatewayHeader is the header the MQTT gateway puts on every message it delivers from an
// MQTT client, holding the publish's QoS as its first byte (nats-server mqtt.go,
// mqttNatsHeader). A message without it did not come from the gateway.
const mqttGatewayHeader = "Nmqtt-Pub"

// mqttFailClosedDirectSuffix is the platform stream the direct copy of each kind lands in.
var mqttFailClosedDirectSuffix = map[MqttFailClosedKind]string{
	MqttFailClosedEvents:    streams.DeviceEventsCapture,
	MqttFailClosedResponses: SubjectCommandResponses,
	MqttFailClosedReports:   SubjectDeviceReports,
}

// mqttStoreSnapshot is one look at the store.
type mqttStoreSnapshot struct {
	// at is the local time the look completed; zero when there has been none.
	at      time.Time
	present bool
	// created is the incarnation's creation time, lastTime the stored time of its last message.
	created, lastTime time.Time
	// owned is this instance's owner mark: the fail-closed store in force. marked is either of
	// this instance's marks (owner or released): the store has platform durables to read.
	owned, marked bool
	// from is each direct stream's mark (suffix -> sequence), present only while owned.
	from map[string]uint64
}

// mqttStoreView is a manager's latest look at the store; see the file comment.
type mqttStoreView struct {
	nmgr *NatsManager
	// refreshMu serializes looks; mu guards snap.
	refreshMu sync.Mutex
	mu        sync.Mutex
	snap      mqttStoreSnapshot
	// everMarked is set by the first look that saw a mark of this instance on the store. With
	// the setting off, a reader skips nothing (and takes no look) until it is set: a pod with
	// the setting off that has never seen the store marked behaves exactly as before the
	// setting existed.
	everMarked atomic.Bool
}

// mqttView is this manager's view, built on first use.
func (nmgr *NatsManager) mqttView() *mqttStoreView {
	nmgr.mqttViewOnce.Do(func() { nmgr.mqttViewState = &mqttStoreView{nmgr: nmgr} })
	return nmgr.mqttViewState
}

// observe records a look: info is the store, nil when it does not exist.
func (v *mqttStoreView) observe(info *nats.StreamInfo, at time.Time) {
	snap := mqttStoreSnapshot{at: at}
	if info != nil {
		snap.present = true
		snap.created = info.Created
		snap.lastTime = info.State.LastTime
		me := v.nmgr.Microservice.InstanceId
		snap.owned = me != "" && info.Config.Metadata[mqttFailClosedOwnerKey] == me
		snap.marked = snap.owned || (me != "" && info.Config.Metadata[mqttFailClosedReleasedKey] == me)
		if snap.owned {
			snap.from = mqttFailClosedFromMarks(info.Config.Metadata)
		}
	}
	if snap.marked {
		v.everMarked.Store(true)
	}
	v.mu.Lock()
	if !snap.at.Before(v.snap.at) {
		v.snap = snap
	}
	v.mu.Unlock()
}

func (v *mqttStoreView) current() mqttStoreSnapshot {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.snap
}

// refresh takes a look, unless one completed after since already, and returns the latest.
func (v *mqttStoreView) refresh(since time.Time) (mqttStoreSnapshot, error) {
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()
	if s := v.current(); s.at.After(since) {
		return s, nil
	}
	return v.look()
}

// tryRefresh is refresh that does not wait for a look another goroutine is taking.
func (v *mqttStoreView) tryRefresh() {
	if !v.refreshMu.TryLock() {
		return
	}
	defer v.refreshMu.Unlock()
	_, _ = v.look()
}

// look reads the store; refreshMu is held.
func (v *mqttStoreView) look() (mqttStoreSnapshot, error) {
	if v.nmgr.js == nil {
		return v.current(), errors.New("messaging: JetStream context not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), mqttStoreViewTimeout)
	defer cancel()
	info, err := v.nmgr.js.StreamInfo(MqttMessageStore, nats.Context(ctx))
	if errors.Is(err, nats.ErrStreamNotFound) {
		info, err = nil, nil
	}
	if err != nil {
		return v.current(), err
	}
	v.observe(info, time.Now())
	return v.current(), nil
}

// MqttMessageStoreMarked reports whether this manager's latest look at the MQTT message store
// (a fail-closed pass, or a reader's) saw a mark of this instance on it: the store is held
// fail-closed for it, or was and still has platform durables. It takes no new look.
func (nmgr *NatsManager) MqttMessageStoreMarked() bool {
	s := nmgr.mqttView().current()
	return s.present && s.marked
}

// carriedByStore reports whether a message a reader took from the direct stream suffix, with
// the given stream sequence and gateway header, is a QoS 1 direct copy the store holds (see
// the file comment). The look it decides on is refreshed when it is older than
// mqttStoreViewRefresh, without waiting on a look already being taken.
//
// It decides only in a pod with the setting on, or one that has seen this instance's mark on
// the store (an event-sources pod with the setting off whose watch found the store held by
// pods with it on: it reads the store's durables too, so it skips what they hold, and a
// rollout of mixed pods forwards each publish once). Anywhere else it returns false at once.
func (v *mqttStoreView) carriedByStore(suffix string, gatewayHeader string, seq uint64) bool {
	if len(gatewayHeader) == 0 || gatewayHeader[0] != '1' || seq == 0 {
		return false
	}
	if !v.nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.MqttFailClosed && !v.everMarked.Load() {
		return false
	}
	if time.Since(v.current().at) >= mqttStoreViewRefresh {
		v.tryRefresh()
	}
	snap := v.current()
	if snap.at.IsZero() || time.Since(snap.at) >= mqttStoreViewTTL || !snap.present || !snap.owned {
		return false
	}
	from, ok := snap.from[suffix]
	return ok && seq > from
}

// epochOf is the epoch of the incarnation a store message stored at stored was stored in, or
// the zero time when that cannot be shown (see the file comment).
//
// Any look can vouch for a message whose stored time falls in its window, whenever the look
// was taken: an incarnation created at C whose last message was stored at L existed over all
// of [C, L], so whatever was stored in that span was stored in it. A message outside the
// current look's window (the newest messages, stored after the look) takes one new look.
func (v *mqttStoreView) epochOf(stored time.Time) time.Time {
	if stored.IsZero() {
		return time.Time{}
	}
	in := func(s mqttStoreSnapshot) bool {
		return s.present && !s.created.IsZero() && !stored.Before(s.created) && !stored.After(s.lastTime)
	}
	if s := v.current(); in(s) {
		return s.created
	}
	s, err := v.refresh(time.Now())
	if err != nil || !in(s) {
		return time.Time{}
	}
	return s.created
}

// mqttFailClosedFromMarks reads the direct streams' marks out of the store's metadata.
func mqttFailClosedFromMarks(md map[string]string) map[string]uint64 {
	out := map[string]uint64{}
	for k, val := range md {
		suffix, ok := strings.CutPrefix(k, mqttFailClosedFromKeyPrefix)
		if !ok {
			continue
		}
		if n, err := strconv.ParseUint(val, 10, 64); err == nil {
			out[suffix] = n
		}
	}
	return out
}

// carriesDirectCopies reports whether suffix is a stream a device QoS 1 publish lands in
// directly, whose reader skips the copies the store holds.
func carriesDirectCopies(suffix string) bool {
	for _, s := range mqttFailClosedDirectSuffix {
		if s == suffix {
			return true
		}
	}
	return false
}

// MqttStoreSkipsDirectCopiesForTesting takes a new look at the store and reports whether a
// reader of this manager would now skip the direct copy of a QoS 1 publish for every one of
// the platform's direct streams: the store is there, held fail-closed by this instance, and
// carries a from mark for each stream, so each direct copy above its mark has a store copy a
// durable holds. Until it reports true a publish is forwarded twice (the store copy, and the
// direct copy nothing skips), which is the cost of being unsure and not a fault, so a test
// that asserts a publish arrives once waits for this instead of a delay. Production never
// calls it.
func (nmgr *NatsManager) MqttStoreSkipsDirectCopiesForTesting(tb interface{ Helper() }) bool {
	tb.Helper()
	v := nmgr.mqttView()
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()
	s, err := v.look()
	if err != nil || !s.present || !s.owned {
		return false
	}
	for _, suffix := range mqttFailClosedDirectSuffix {
		if _, ok := s.from[suffix]; !ok {
			return false
		}
	}
	return true
}
