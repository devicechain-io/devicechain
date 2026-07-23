// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// maxSeq is the largest valid Sparkplug payload sequence number; the counter is a
// single byte that wraps 255→0, so a wire value above it is malformed.
const maxSeq = 255

// datatypeBoolean is the Sparkplug B datatype code for a Boolean metric.
const datatypeBoolean = 11

// bdSeqMetric is the reserved Sparkplug metric name carrying the birth/death
// sequence. It appears in NBIRTH and its matching NDEATH will; the Host uses it
// ONLY to correlate a death with the birth that opened the session (B3) — never
// as an ordering magnitude (it rolls 255→0, so a raw compare inverts at the
// wrap), which is why SessionId/ordering is derived from a host-observed connect
// epoch elsewhere, not from this value.
const bdSeqMetric = "bdSeq"

// nodeControlRebirthMetric is the reserved Sparkplug metric a Host sets to true
// in an NCMD to command an edge node to re-emit its NBIRTH (and DBIRTHs).
const nodeControlRebirthMetric = "Node Control/Rebirth"

// defaultRebirthBackoff bounds how often the Host will command one edge node to
// rebirth: at most one request per window, so a burst of gapped/aliased messages
// collapses to a single command rather than a storm. It is short enough that a
// node that never recovers is re-asked on the next window (escalation, not
// silence).
const defaultRebirthBackoff = 5 * time.Second

// nodeKey identifies an edge node within a Sparkplug group.
type nodeKey struct {
	group string
	node  string
}

// nodeSession is the Host's in-memory model of one edge node's live Sparkplug
// session (ADR-069 SP2). Sparkplug is stateful: the alias table and the rolling
// per-node sequence are established by the NBIRTH and consumed by every following
// message, so the Host must hold them to validate the stream and detect the gaps
// that require a rebirth. It is NOT durable — a clean-session Host rebuilds it
// from births on reconnect (SP4 reconstructs via rebirth-all on failover).
type nodeSession struct {
	// online is true between an accepted NBIRTH and its matching NDEATH.
	online bool

	// bdSeq is the birth/death sequence declared by the NBIRTH that opened this
	// session; hasBdSeq records whether one was present. Used only to equality-
	// match the NDEATH will (B3), never magnitude-ordered.
	bdSeq    uint64
	hasBdSeq bool

	// lastSeq is the last accepted payload sequence number. The next message is
	// valid iff its seq == lastSeq+1 mod 256 — the uint8 makes the wrap modular,
	// so 255→0 is a normal advance, not a gap (B2).
	lastSeq uint8

	// aliases maps a metric alias to its name, rebuilt from scratch on every
	// NBIRTH and extended by each DBIRTH (aliases are unique across the whole edge
	// node, node + devices). A DATA message referencing an unknown alias means the
	// Host missed the birth that defined it → rebirth.
	aliases map[uint64]string

	// devices maps each child device that has sent a DBIRTH to its own per-device
	// session epoch (minted at that DBIRTH). Membership is the born-set (DDATA for a
	// device not present is data-before-birth → rebirth); the epoch is the device's
	// presence SessionId (ADR-067). It is PER-DEVICE, not the node's, so a device
	// re-homed under an older-born node is not ordered stale, and a device that flaps
	// within one node session re-emits a fresh SessionId (and a fresh dedup id).
	devices map[string]uint64

	// sessionEpoch is this node session's host-observed connect epoch, minted at the
	// accepted NBIRTH. It is the node's presence SessionId and is carried by the
	// node's matching NDEATH.
	sessionEpoch uint64

	// lastRebirth is when the Host last commanded this node to rebirth; the zero
	// value permits an immediate request. It drives the per-node backoff.
	lastRebirth time.Time

	// rebirthPending is set whenever the Host commands this node to rebirth (a gap the
	// backoff let through, or a failover reconcile), and cleared by the NBIRTH that
	// answers it. It disambiguates the two same-bdSeq births the Host can see: a
	// commanded rebirth (rebirthPending) re-announces with the SAME bdSeq and seq reset
	// to 0, and MUST re-adopt that seq baseline or the node's next message reads as a
	// gap and re-triggers a rebirth forever (the SP2 livelock); an UNsolicited same-bdSeq
	// birth is a QoS-1 redelivery that must be skipped so it can't reset a baseline the
	// live DATA stream has already advanced.
	rebirthPending bool
}

// SessionTracker validates the Sparkplug session for every edge node it observes
// and decides when a node must be commanded to rebirth. It is the SP2 core: a
// pure, in-memory state machine driven by decoded messages, with the actual NCMD
// publish delegated to an injected rebirth callback so the machine is testable
// without a broker and the callback runs OUTSIDE the tracker lock (an MQTT
// publish must never be held under the state mutex).
type SessionTracker struct {
	mu       sync.Mutex
	sessions map[nodeKey]*nodeSession

	now     func() time.Time
	backoff time.Duration
	rebirth func(group, node string)

	// epoch mints every SessionId this tracker issues (ADR-067). It was extracted into
	// the shared adapter at L1 (the I3 deferral) so LwM2M's registration registry mints
	// through the same floor+monotonicity rather than reimplementing it. nextEpoch calls
	// it under tr.mu, so its mutex is a LEAF under tr.mu; SetEpochFloor/MintEpoch forward
	// to it off the hot path.
	epoch *adapter.EpochSource
}

// Observation is everything a single decoded Sparkplug message contributes to the
// pipeline: the numeric samples to ingest and any presence transitions it asserts. A
// message yields at most one node-or-device transition, except an NDEATH, which
// cascades a DISCONNECTED to the node AND every known child device.
type Observation struct {
	Samples  []Sample
	Presence []PresenceEvent
}

// nodeExternalId / deviceExternalId build the ADR-049 external id the projection keys
// on, matching SparkplugExternalId's slash-joined form (identity.go).
func nodeExternalId(key nodeKey) string { return key.group + "/" + key.node }
func deviceExternalId(key nodeKey, device string) string {
	return key.group + "/" + key.node + "/" + device
}

// nextEpoch mints a strictly-monotone host-observed connect epoch (ADR-067). The
// caller holds tr.mu; the epoch source's own mutex is a leaf taken under it.
func (tr *SessionTracker) nextEpoch() uint64 {
	return tr.epoch.Next()
}

// SetEpochFloor raises the epoch generator's floor so every subsequently-minted
// SessionId exceeds any already persisted for this source's devices (ADR-067). A
// fresh leader calls it on lease acquisition with max(session_id)+1 from the
// device-state read model, so a handover (or a restart across a wall-clock step-back)
// can never mint an epoch the projection rejects as stale. Lowering is a no-op.
func (tr *SessionTracker) SetEpochFloor(floor uint64) {
	tr.epoch.SetFloor(floor)
}

// MintEpoch mints a fresh strictly-monotone epoch above the current floor, for the
// failover reconciliation to stamp its timeout-DISCONNECTED transitions (ADR-067
// SP4b). Called AFTER SetEpochFloor(max+1), so the minted epoch exceeds every stored
// session and the reconcile-DISCONNECT supersedes them — while a genuine re-birth
// during the probe window mints a LATER (higher) epoch via nextEpoch and thus
// supersedes the reconcile-DISCONNECT, so a slow-but-alive node self-heals regardless
// of the race between its birth and the probe timeout.
func (tr *SessionTracker) MintEpoch() uint64 {
	return tr.epoch.Mint()
}

// NewSessionTracker builds a tracker. now supplies the clock for backoff (pass
// time.Now in production); rebirth is invoked to command a node to re-emit its
// births (nil is allowed — the decision still updates backoff state). The rebirth
// callback fires OUTSIDE the tracker lock and must not block the caller (the
// production wiring hands it to an async publisher).
func NewSessionTracker(now func() time.Time, backoff time.Duration, rebirth func(group, node string)) *SessionTracker {
	if now == nil {
		now = time.Now
	}
	if backoff <= 0 {
		backoff = defaultRebirthBackoff
	}
	return &SessionTracker{
		sessions: map[nodeKey]*nodeSession{},
		now:      now,
		backoff:  backoff,
		rebirth:  rebirth,
		// The epoch source shares this tracker's clock, so a moved-clock test drives both
		// identically and the extraction is behaviour-preserving.
		epoch: adapter.NewEpochSource(now),
	}
}

// isTrackedType reports whether a message type participates in the node session
// machine. STATE is the Host's own presence (handled in the receive path, not
// here); NCMD/DCMD are host→node commands the Host emits, never receives, so they
// are not tracked inbound.
func isTrackedType(mt MessageType) bool {
	switch mt {
	case NBIRTH, NDEATH, NDATA, DBIRTH, DDEATH, DDATA:
		return true
	default:
		return false
	}
}

// Observe advances the session machine for one decoded Sparkplug message and
// returns the numeric samples that message contributes for ingest (nil when it was
// rejected, a duplicate birth, or a death, or carried no numeric metrics). If the
// message reveals a gap the Host cannot reconcile (missed birth, seq gap, unknown
// alias, data-before-birth), it also commands the node to rebirth. Extraction is
// co-located with validation because "was this accepted?" and the alias table that
// resolves a DATA metric's name are both known only here, under the one lock — the
// samples and the rebirth decision come from the same step. The rebirth callback
// fires OUTSIDE the lock.
func (tr *SessionTracker) Observe(top Topic, p *sppb.Payload) Observation {
	if top.IsState || !isTrackedType(top.MessageType) {
		return Observation{}
	}
	key := nodeKey{top.GroupID, top.EdgeNodeID}
	samples, presence, rebirth := tr.step(key, top, p)
	if rebirth && tr.rebirth != nil {
		tr.rebirth(key.group, key.node)
	}
	return Observation{Samples: samples, Presence: presence}
}

// step applies one message under the lock and returns the samples to ingest, the
// presence transitions it asserts, and whether a rebirth command should be emitted
// (already backoff-gated, so at most one per window per node). Samples are non-nil
// only for an accepted BIRTH or DATA message; presence is non-nil for an accepted
// BIRTH (CONNECTED) or DEATH (DISCONNECTED); a rejected message yields neither and
// (subject to backoff) a rebirth.
func (tr *SessionTracker) step(key nodeKey, top Topic, p *sppb.Payload) ([]Sample, []PresenceEvent, bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	// NBIRTH is the authoritative reset: it rebuilds the session from scratch, so
	// it never needs a prior session and never triggers a rebirth (a replayed
	// retained BIRTH just re-initialises cleanly — no double-count).
	if top.MessageType == NBIRTH {
		samples, presence := tr.onNBirth(key, p)
		return samples, presence, false
	}

	// Every other tracked message needs a session to validate against; create an
	// offline placeholder so backoff state has a home even for a node we have not
	// yet seen a birth from (else a storm of pre-birth data cannot be throttled).
	s := tr.sessions[key]
	if s == nil {
		s = &nodeSession{aliases: map[uint64]string{}, devices: map[string]uint64{}}
		tr.sessions[key] = s
	}

	switch top.MessageType {
	case NDEATH:
		presence, rebirth := tr.onNDeath(key, s, p)
		return nil, presence, rebirth
	case DBIRTH:
		return tr.onDBirth(key, s, top.DeviceID, p)
	case DDEATH:
		presence, rebirth := tr.onDDeath(key, s, top.DeviceID, p)
		return nil, presence, rebirth
	case NDATA:
		samples, rebirth := tr.onNData(key, s, p)
		return samples, nil, rebirth
	case DDATA:
		samples, rebirth := tr.onDData(key, s, top.DeviceID, p)
		return samples, nil, rebirth
	}
	return nil, nil, false
}

// onNBirth rebuilds the node session from the birth certificate: it resets the
// seq baseline, records the bdSeq for death correlation, and rebuilds the alias
// table from scratch. The prior session (if any) is discarded, which also clears
// any rebirth backoff — the node has recovered.
//
// A redelivered birth is idempotent: because bdSeq increments once per genuine
// session, an NBIRTH whose bdSeq equals the live session's is a duplicate (QoS 1
// is at-least-once). Rebuilding on it would reset the seq baseline and clear the
// device table, spuriously gapping the next real message — so a same-bdSeq birth
// for a live session is left untouched.
func (tr *SessionTracker) onNBirth(key nodeKey, p *sppb.Payload) ([]Sample, []PresenceEvent) {
	bd, hasBd := bdSeqOf(p)
	if prev := tr.sessions[key]; prev != nil && prev.online && prev.hasBdSeq && hasBd && prev.bdSeq == bd {
		if !prev.rebirthPending {
			// Unsolicited same-bdSeq birth = a QoS-1 redelivery: keep the live session
			// intact and emit NOTHING (its metrics were already ingested and its CONNECTED
			// already emitted at the first birth; repeating either would double-count /
			// re-emit a stale epoch, and re-adopting its seq would reset a baseline the
			// live DATA stream has already advanced).
			return nil, nil
		}
		// Commanded rebirth (we asked for it): the node re-announced with the SAME bdSeq
		// and seq reset. Re-adopt the seq baseline and refresh the node's alias table so
		// its following DATA validates again — this is what breaks the rebirth livelock —
		// but keep the SAME session (epoch + born devices) and do NOT re-emit CONNECTED:
		// the node never disconnected. Re-ingest the birth's current values (a redelivery
		// dedups by content downstream; a genuine rebirth carries fresh values).
		prev.rebirthPending = false
		prev.lastSeq = uint8(p.GetSeq())
		addAliases(prev.aliases, p)
		return samplesFrom(p, prev.aliases, tr.now), nil
	}
	epoch := tr.nextEpoch()
	s := &nodeSession{
		online:       true,
		bdSeq:        bd,
		hasBdSeq:     hasBd,
		aliases:      map[uint64]string{},
		devices:      map[string]uint64{},
		sessionEpoch: epoch,
	}
	// A conformant NBIRTH declares seq 0; adopt whatever it declares as the
	// baseline so the following seq lines up rather than reading as a gap.
	s.lastSeq = uint8(p.GetSeq())
	addAliases(s.aliases, p)
	tr.sessions[key] = s
	presence := []PresenceEvent{{
		ExternalId: nodeExternalId(key),
		Connected:  true,
		Reason:     "nbirth",
		SessionId:  epoch,
		OccurredAt: tr.now(),
	}}
	// A birth carries the node's current metric values (Sparkplug is report-by-
	// exception, so the birth is the ONLY place a rarely-changing metric appears) —
	// ingest them.
	return samplesFrom(p, s.aliases, tr.now), presence
}

// onNDeath applies an edge-node death, guarded by bdSeq (B3): the will is
// accepted only when it carries the SAME bdSeq as the birth that opened the live
// session. A death for a node with no live session, or one carrying a stale
// bdSeq from a prior connection, is ignored so it cannot tear down a session that
// a newer birth already re-established. NDEATH is exempt from the seq counter. On
// accept it emits a DISCONNECTED for the node AND cascades one to every known child
// device (a node death implies all its devices are offline in Sparkplug), each
// carrying its own per-device epoch.
func (tr *SessionTracker) onNDeath(key nodeKey, s *nodeSession, p *sppb.Payload) ([]PresenceEvent, bool) {
	if !s.online {
		return nil, false
	}
	// Fail closed: when the live session carries a bdSeq, accept the death only if
	// it carries a MATCHING one. A death that is absent a bdSeq, or carries a
	// different one, is uncorrelatable (a stale/duplicate will from a prior
	// session) and must not tear down the live session. A bdSeq-less session (a
	// non-conformant node) cannot be correlated at all, so its death is accepted.
	if s.hasBdSeq {
		if bd, ok := bdSeqOf(p); !ok || bd != s.bdSeq {
			return nil, false
		}
	}
	now := tr.now()
	presence := make([]PresenceEvent, 0, len(s.devices)+1)
	presence = append(presence, PresenceEvent{
		ExternalId: nodeExternalId(key), Connected: false, Reason: "ndeath-will",
		SessionId: s.sessionEpoch, OccurredAt: now,
	})
	for device, epoch := range s.devices {
		presence = append(presence, PresenceEvent{
			ExternalId: deviceExternalId(key, device), Connected: false, Reason: "ndeath-will",
			SessionId: epoch, OccurredAt: now,
		})
	}
	s.online = false
	s.aliases = map[uint64]string{}
	s.devices = map[string]uint64{}
	return presence, false
}

// onDBirth records a device birth. It requires a live node session (a device
// birth with no node birth means the Host missed the NBIRTH → rebirth), advances
// the seq, extends the node's alias table, and mints a fresh per-device epoch — so
// each (re)birth is a new presence session (a new SessionId + a new dedup id).
func (tr *SessionTracker) onDBirth(key nodeKey, s *nodeSession, device string, p *sppb.Payload) ([]Sample, []PresenceEvent, bool) {
	if !s.online {
		return nil, nil, tr.wantRebirth(s)
	}
	if !tr.advanceSeq(s, p) {
		return nil, nil, tr.wantRebirth(s)
	}
	addAliases(s.aliases, p)
	epoch := tr.nextEpoch()
	s.devices[device] = epoch
	presence := []PresenceEvent{{
		ExternalId: deviceExternalId(key, device), Connected: true, Reason: "dbirth",
		SessionId: epoch, OccurredAt: tr.now(),
	}}
	return samplesFrom(p, s.aliases, tr.now), presence, false
}

// onDDeath applies a device death: requires a live node, advances the seq, and
// removes the device from the born set, emitting a DISCONNECTED carrying that
// device's own epoch. A death for an unknown device is a no-op after the seq check
// (it was never CONNECTED, and has no epoch to order it).
func (tr *SessionTracker) onDDeath(key nodeKey, s *nodeSession, device string, p *sppb.Payload) ([]PresenceEvent, bool) {
	if !s.online {
		return nil, tr.wantRebirth(s)
	}
	if !tr.advanceSeq(s, p) {
		return nil, tr.wantRebirth(s)
	}
	epoch, known := s.devices[device]
	delete(s.devices, device)
	if !known {
		return nil, false
	}
	presence := []PresenceEvent{{
		ExternalId: deviceExternalId(key, device), Connected: false, Reason: "ddeath",
		SessionId: epoch, OccurredAt: tr.now(),
	}}
	return presence, false
}

// onNData validates node data against the live session: it requires a birth,
// checks the seq, and checks every aliased metric resolves. Any failure asks the
// node to rebirth.
func (tr *SessionTracker) onNData(key nodeKey, s *nodeSession, p *sppb.Payload) ([]Sample, bool) {
	if !s.online {
		return nil, tr.wantRebirth(s)
	}
	if !tr.advanceSeq(s, p) || !tr.aliasesResolve(s, p) {
		return nil, tr.wantRebirth(s)
	}
	return samplesFrom(p, s.aliases, tr.now), false
}

// onDData validates device data. Beyond the node-birth and seq/alias checks, it
// enforces that the device has sent a DBIRTH: DDATA before DBIRTH is a protocol
// violation (the device's aliases were never defined), NOT device liveness, so it
// asks for a rebirth rather than treating the device as alive.
func (tr *SessionTracker) onDData(key nodeKey, s *nodeSession, device string, p *sppb.Payload) ([]Sample, bool) {
	if !s.online {
		return nil, tr.wantRebirth(s)
	}
	if _, born := s.devices[device]; !born {
		return nil, tr.wantRebirth(s) // data before birth — protocol violation, not liveness
	}
	if !tr.advanceSeq(s, p) || !tr.aliasesResolve(s, p) {
		return nil, tr.wantRebirth(s)
	}
	return samplesFrom(p, s.aliases, tr.now), false
}

// advanceSeq checks the message sequence against the session and advances it on
// a match. The next valid seq is lastSeq+1 mod 256 — uint8 arithmetic makes the
// wrap modular, so 255→0 is a normal advance rather than a gap (B2). A missing
// seq on a seq-bearing message is treated as a gap (fail closed).
func (tr *SessionTracker) advanceSeq(s *nodeSession, p *sppb.Payload) bool {
	// Fail closed on a missing or out-of-range seq: a byte-sized counter never
	// exceeds 255, so a larger wire value is malformed, not a valid successor
	// (uint8 truncation would otherwise accept e.g. 257 as 1).
	if p.Seq == nil || p.GetSeq() > maxSeq {
		return false
	}
	got := uint8(p.GetSeq())
	if got != s.lastSeq+1 {
		return false
	}
	s.lastSeq = got
	return true
}

// aliasesResolve reports whether every alias-referenced metric in the payload is
// known to the session. A metric sent by name (no alias) is allowed; an alias not
// in the table means the Host missed the birth that defined it.
func (tr *SessionTracker) aliasesResolve(s *nodeSession, p *sppb.Payload) bool {
	for _, m := range p.GetMetrics() {
		if m.Alias == nil {
			continue
		}
		if _, ok := s.aliases[m.GetAlias()]; !ok {
			return false
		}
	}
	return true
}

// wantRebirth applies the per-node backoff and reports whether a rebirth command
// should be emitted now. It records the request time under the lock; the caller
// performs the publish outside it.
func (tr *SessionTracker) wantRebirth(s *nodeSession) bool {
	now := tr.now()
	if !s.lastRebirth.IsZero() && now.Sub(s.lastRebirth) < tr.backoff {
		return false // within the backoff window — collapse the storm
	}
	s.lastRebirth = now
	// We are about to command a rebirth: expect a same-bdSeq NBIRTH (seq reset to 0)
	// answering it, and mark it so onNBirth re-adopts the seq baseline rather than
	// skipping it as a redelivery (the livelock fix).
	s.rebirthPending = true
	return true
}

// MarkRebirthPending records that the Host has commanded a node to rebirth OUTSIDE the
// session machine's own gap detection — the failover reconcile publishes rebirths
// directly (ADR-067 SP4b) — so the NBIRTH answering it is treated as a commanded
// rebirth (seq-baseline re-adopted), not skipped as a redelivery. A node with no live
// session yet is a no-op: its next NBIRTH takes the normal full-rebuild path.
func (tr *SessionTracker) MarkRebirthPending(group, node string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if s := tr.sessions[nodeKey{group, node}]; s != nil {
		s.rebirthPending = true
	}
}

// bdSeqOf returns the bdSeq metric value from a payload, if present.
func bdSeqOf(p *sppb.Payload) (uint64, bool) {
	for _, m := range p.GetMetrics() {
		if m.GetName() == bdSeqMetric {
			return m.GetLongValue(), true
		}
	}
	return 0, false
}

// addAliases records every metric that declares an alias into the table (alias →
// name), the mapping later DATA messages are resolved against.
func addAliases(table map[uint64]string, p *sppb.Payload) {
	for _, m := range p.GetMetrics() {
		if m.Alias != nil {
			table[m.GetAlias()] = m.GetName()
		}
	}
}

// rebirthCommand encodes an NCMD payload carrying the reserved "Node Control/
// Rebirth"=true boolean metric — the Sparkplug command that asks an edge node to
// re-emit its NBIRTH (and its devices' DBIRTHs). ts is the command timestamp in
// milliseconds since the Unix epoch.
func rebirthCommand(ts int64) ([]byte, error) {
	return codec.Encode(&sppb.Payload{
		Timestamp: proto.Uint64(uint64(ts)),
		Metrics: []*sppb.Payload_Metric{{
			Name:     proto.String(nodeControlRebirthMetric),
			Datatype: proto.Uint32(datatypeBoolean),
			Value:    &sppb.Payload_Metric_BooleanValue{BooleanValue: true},
		}},
	})
}
