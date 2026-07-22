// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

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

	// devices is the set of child devices that have sent a DBIRTH. DDATA for a
	// device not in this set is a protocol violation (data before birth), not
	// device liveness → rebirth.
	devices map[string]bool

	// lastRebirth is when the Host last commanded this node to rebirth; the zero
	// value permits an immediate request. It drives the per-node backoff.
	lastRebirth time.Time
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
func (tr *SessionTracker) Observe(top Topic, p *sppb.Payload) []Sample {
	if top.IsState || !isTrackedType(top.MessageType) {
		return nil
	}
	key := nodeKey{top.GroupID, top.EdgeNodeID}
	samples, rebirth := tr.step(key, top, p)
	if rebirth && tr.rebirth != nil {
		tr.rebirth(key.group, key.node)
	}
	return samples
}

// step applies one message under the lock and returns the samples to ingest and
// whether a rebirth command should be emitted (already backoff-gated, so at most
// one per window per node). Samples are non-nil only for an accepted BIRTH or DATA
// message; a rejected message yields nil samples and (subject to backoff) a rebirth.
func (tr *SessionTracker) step(key nodeKey, top Topic, p *sppb.Payload) ([]Sample, bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	// NBIRTH is the authoritative reset: it rebuilds the session from scratch, so
	// it never needs a prior session and never triggers a rebirth (a replayed
	// retained BIRTH just re-initialises cleanly — no double-count).
	if top.MessageType == NBIRTH {
		return tr.onNBirth(key, p), false
	}

	// Every other tracked message needs a session to validate against; create an
	// offline placeholder so backoff state has a home even for a node we have not
	// yet seen a birth from (else a storm of pre-birth data cannot be throttled).
	s := tr.sessions[key]
	if s == nil {
		s = &nodeSession{aliases: map[uint64]string{}, devices: map[string]bool{}}
		tr.sessions[key] = s
	}

	switch top.MessageType {
	case NDEATH:
		return nil, tr.onNDeath(s, p)
	case DBIRTH:
		return tr.onDBirth(key, s, top.DeviceID, p)
	case DDEATH:
		return nil, tr.onDDeath(key, s, top.DeviceID, p)
	case NDATA:
		return tr.onNData(key, s, p)
	case DDATA:
		return tr.onDData(key, s, top.DeviceID, p)
	}
	return nil, false
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
func (tr *SessionTracker) onNBirth(key nodeKey, p *sppb.Payload) []Sample {
	bd, hasBd := bdSeqOf(p)
	if prev := tr.sessions[key]; prev != nil && prev.online && prev.hasBdSeq && hasBd && prev.bdSeq == bd {
		// duplicate/redelivered birth — keep the live session intact and emit NOTHING
		// (its metrics were already ingested at the first birth; re-ingesting would
		// double-count).
		return nil
	}
	s := &nodeSession{
		online:   true,
		bdSeq:    bd,
		hasBdSeq: hasBd,
		aliases:  map[uint64]string{},
		devices:  map[string]bool{},
	}
	// A conformant NBIRTH declares seq 0; adopt whatever it declares as the
	// baseline so the following seq lines up rather than reading as a gap.
	s.lastSeq = uint8(p.GetSeq())
	addAliases(s.aliases, p)
	tr.sessions[key] = s
	// A birth carries the node's current metric values (Sparkplug is report-by-
	// exception, so the birth is the ONLY place a rarely-changing metric appears) —
	// ingest them.
	return samplesFrom(p, s.aliases, tr.now)
}

// onNDeath applies an edge-node death, guarded by bdSeq (B3): the will is
// accepted only when it carries the SAME bdSeq as the birth that opened the live
// session. A death for a node with no live session, or one carrying a stale
// bdSeq from a prior connection, is ignored so it cannot tear down a session that
// a newer birth already re-established. NDEATH is exempt from the seq counter.
func (tr *SessionTracker) onNDeath(s *nodeSession, p *sppb.Payload) bool {
	if !s.online {
		return false
	}
	// Fail closed: when the live session carries a bdSeq, accept the death only if
	// it carries a MATCHING one. A death that is absent a bdSeq, or carries a
	// different one, is uncorrelatable (a stale/duplicate will from a prior
	// session) and must not tear down the live session. A bdSeq-less session (a
	// non-conformant node) cannot be correlated at all, so its death is accepted.
	if s.hasBdSeq {
		if bd, ok := bdSeqOf(p); !ok || bd != s.bdSeq {
			return false
		}
	}
	s.online = false
	s.aliases = map[uint64]string{}
	s.devices = map[string]bool{}
	return false
}

// onDBirth records a device birth. It requires a live node session (a device
// birth with no node birth means the Host missed the NBIRTH → rebirth), advances
// the seq, and extends the node's alias table with the device's metrics.
func (tr *SessionTracker) onDBirth(key nodeKey, s *nodeSession, device string, p *sppb.Payload) ([]Sample, bool) {
	if !s.online {
		return nil, tr.wantRebirth(s)
	}
	if !tr.advanceSeq(s, p) {
		return nil, tr.wantRebirth(s)
	}
	addAliases(s.aliases, p)
	s.devices[device] = true
	return samplesFrom(p, s.aliases, tr.now), false
}

// onDDeath applies a device death: requires a live node, advances the seq, and
// removes the device from the born set. A death for an unknown device is a no-op
// after the seq check (liveness, not a protocol violation).
func (tr *SessionTracker) onDDeath(key nodeKey, s *nodeSession, device string, p *sppb.Payload) bool {
	if !s.online {
		return tr.wantRebirth(s)
	}
	if !tr.advanceSeq(s, p) {
		return tr.wantRebirth(s)
	}
	delete(s.devices, device)
	return false
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
	if !s.devices[device] {
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
	return true
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
