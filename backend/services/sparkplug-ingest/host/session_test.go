// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// --- synthetic Sparkplug producer -------------------------------------------
//
// These tests drive the SP2 session machine with hand-built messages — the same
// (topic, payload) pairs a real edge node puts on the wire — and assert the ONE
// externally observable effect the machine has: how many rebirth commands it
// emits. Rebirth-count is the check-that-cannot-fail here because every session
// error (missed birth, seq gap, unknown alias, data-before-birth) funnels into
// exactly that action, and the negative control (a clean stream) pins it to zero
// so a naive rebirth-on-everything machine cannot pass.

// recorder counts the rebirth callbacks the tracker fires.
type recorder struct {
	mu    sync.Mutex
	calls []nodeKey
}

func (r *recorder) fn(group, node string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, nodeKey{group, node})
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// sessionHarness wires a tracker to a recorder and a movable clock. The clock is
// read through a closure so advancing h.now takes effect on the next Observe —
// which is how the backoff-window tests move time.
type sessionHarness struct {
	tr  *SessionTracker
	rec *recorder
	now time.Time
}

func newHarness(backoff time.Duration) *sessionHarness {
	h := &sessionHarness{rec: &recorder{}, now: time.Unix(1_700_000_000, 0)}
	h.tr = NewSessionTracker(func() time.Time { return h.now }, backoff, h.rec.fn)
	return h
}

// sess returns the tracked session for the fixed test node "g/n".
func (h *sessionHarness) sess() *nodeSession { return h.tr.sessions[nodeKey{"g", "n"}] }

func nTop(mt MessageType) Topic { return Topic{GroupID: "g", EdgeNodeID: "n", MessageType: mt} }
func dTop(mt MessageType, device string) Topic {
	return Topic{GroupID: "g", EdgeNodeID: "n", MessageType: mt, DeviceID: device}
}

// pl builds a payload with the given seq (pass -1 to omit seq, as an NDEATH will
// does) and metrics.
func pl(seq int, metrics ...*sppb.Payload_Metric) *sppb.Payload {
	p := &sppb.Payload{Metrics: metrics}
	if seq >= 0 {
		p.Seq = proto.Uint64(uint64(seq))
	}
	return p
}

// birthMetric is a name+alias metric as it appears in a BIRTH, defining an alias.
func birthMetric(name string, alias uint64) *sppb.Payload_Metric {
	return &sppb.Payload_Metric{Name: proto.String(name), Alias: proto.Uint64(alias)}
}

// dataMetric is an alias-only metric as it appears in a DATA message.
func dataMetric(alias uint64) *sppb.Payload_Metric {
	return &sppb.Payload_Metric{Alias: proto.Uint64(alias)}
}

// bdSeqM is a bdSeq metric as it appears in an NBIRTH / NDEATH.
func bdSeqM(v uint64) *sppb.Payload_Metric {
	return &sppb.Payload_Metric{
		Name:  proto.String(bdSeqMetric),
		Value: &sppb.Payload_Metric_LongValue{LongValue: v},
	}
}

// Case 1: a dropped sequence number produces exactly one rebirth.
func TestAdversarialSeqGapTriggersOneRebirth(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.Observe(nTop(NBIRTH), pl(0, birthMetric("temperature", 1)))
	h.tr.Observe(nTop(NDATA), pl(1, dataMetric(1))) // in order — accepted
	h.tr.Observe(nTop(NDATA), pl(3, dataMetric(1))) // seq 2 skipped — gap
	assert.Equal(t, 1, h.rec.count(), "a single seq gap must produce exactly one rebirth")
}

// Case 2: an NDEATH arriving before any NBIRTH is guard-rejected — no session to
// kill, so it is ignored (not a rebirth, not a spurious offline).
func TestAdversarialDeathBeforeBirthIsRejected(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.Observe(nTop(NDEATH), pl(-1, bdSeqM(5)))
	assert.Equal(t, 0, h.rec.count(), "a death with no live session emits no rebirth")
	if s := h.sess(); assert.NotNil(t, s) {
		assert.False(t, s.online, "a pre-birth death must not mark a node online")
	}
}

// Case 3: replaying a retained NBIRTH re-initialises the session cleanly — no
// rebirth, and the following in-order data is still accepted (not double-counted
// as a gap against a stale seq).
func TestAdversarialRetainedBirthReplayIsCleanReInit(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	birth := func() { h.tr.Observe(nTop(NBIRTH), pl(0, birthMetric("temperature", 1), birthMetric("humidity", 2))) }
	birth()
	h.tr.Observe(nTop(NDATA), pl(1, dataMetric(1)))
	birth() // retained BIRTH re-delivered
	h.tr.Observe(nTop(NDATA), pl(1, dataMetric(1)))

	assert.Equal(t, 0, h.rec.count(), "a retained-birth replay must not trigger a rebirth")
	if s := h.sess(); assert.NotNil(t, s) {
		assert.True(t, s.online)
		assert.Len(t, s.aliases, 2, "alias table is rebuilt, not accumulated/duplicated")
		assert.Equal(t, uint8(1), s.lastSeq)
	}
}

// Case 4: DDATA before its device's DBIRTH is a protocol violation → rebirth, and
// crucially NOT treated as device liveness (the device is not marked born).
func TestAdversarialDataBeforeDeviceBirthRebirthsNotLiveness(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.Observe(nTop(NBIRTH), pl(0, birthMetric("temperature", 1)))
	// A well-formed DDATA (valid next seq, no unresolved alias) whose ONLY defect
	// is that no DBIRTH preceded it — so the device-born guard is the sole thing
	// that can trigger the rebirth. (An aliased metric here would let the alias
	// guard mask a removed device-born check — the test would pass for the wrong
	// reason. Verified: without the guard, this stays green unless it is isolated.)
	h.tr.Observe(dTop(DDATA, "sensor-7"), pl(1))

	assert.Equal(t, 1, h.rec.count(), "data before device birth must rebirth")
	if s := h.sess(); assert.NotNil(t, s) {
		assert.False(t, s.devices["sensor-7"], "a violating DDATA must not register the device as alive")
	}
}

// Case 5 (negative control): a fully well-formed session — node birth, device
// birth, node/device data, device death, node death (bdSeq-matched) — emits ZERO
// rebirths. This is the control that catches a rebirth-on-everything machine that
// would still pass cases 1–4.
func TestAdversarialCleanStreamEmitsZeroRebirths(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(7), birthMetric("temperature", 1)))
	h.tr.Observe(dTop(DBIRTH, "sensor-7"), pl(1, birthMetric("flow", 10)))
	h.tr.Observe(dTop(DDATA, "sensor-7"), pl(2, dataMetric(10)))
	h.tr.Observe(nTop(NDATA), pl(3, dataMetric(1)))
	h.tr.Observe(dTop(DDEATH, "sensor-7"), pl(4))
	h.tr.Observe(nTop(NDEATH), pl(-1, bdSeqM(7)))

	assert.Equal(t, 0, h.rec.count(), "a clean stream must never rebirth")
	if s := h.sess(); assert.NotNil(t, s) {
		assert.False(t, s.online, "the matched NDEATH must close the session")
	}
}

// M15 accept-side control: a current-session NDEATH (matching bdSeq) is accepted
// and closes the session; a stale NDEATH (mismatched bdSeq) is ignored and leaves
// the live session online. Both without any rebirth.
func TestNDeathBdSeqGuard(t *testing.T) {
	t.Run("matching bdSeq is accepted", func(t *testing.T) {
		h := newHarness(defaultRebirthBackoff)
		h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5)))
		h.tr.Observe(nTop(NDEATH), pl(-1, bdSeqM(5)))
		assert.Equal(t, 0, h.rec.count())
		assert.False(t, h.sess().online, "a matched death closes the session")
	})
	t.Run("stale bdSeq is ignored", func(t *testing.T) {
		h := newHarness(defaultRebirthBackoff)
		h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5)))
		h.tr.Observe(nTop(NDEATH), pl(-1, bdSeqM(4)))
		assert.Equal(t, 0, h.rec.count())
		assert.True(t, h.sess().online, "a stale death must not tear down the live session")
	})
	t.Run("bdSeq-less death is rejected when the session has one (fail closed)", func(t *testing.T) {
		// A correlatable session must not be closed by an uncorrelatable death —
		// the fail-open version of this guard accepted any death missing a bdSeq.
		h := newHarness(defaultRebirthBackoff)
		h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(5)))
		h.tr.Observe(nTop(NDEATH), pl(-1)) // no bdSeq metric
		assert.Equal(t, 0, h.rec.count())
		assert.True(t, h.sess().online, "a death with no bdSeq must not close a bdSeq-correlated session")
	})
}

// A redelivered NBIRTH (same bdSeq — QoS 1 is at-least-once) mid-session must be
// idempotent: it must NOT reset the seq baseline or clear the device table, which
// would spuriously gap the next real message and rebirth a healthy node. This is
// the M1 regression — distinct from the retained-replay case, which carries no
// bdSeq and legitimately rebuilds.
func TestDuplicateBirthMidSessionIsIdempotent(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	birth := pl(0, bdSeqM(9), birthMetric("temperature", 1))
	h.tr.Observe(nTop(NBIRTH), birth)
	h.tr.Observe(dTop(DBIRTH, "sensor-7"), pl(1, birthMetric("flow", 10)))
	h.tr.Observe(dTop(DDATA, "sensor-7"), pl(2, dataMetric(10)))
	h.tr.Observe(nTop(NBIRTH), birth) // duplicate delivery of the SAME birth

	// The live session is intact: device still born, seq baseline still at 2.
	if s := h.sess(); assert.NotNil(t, s) {
		assert.True(t, s.devices["sensor-7"], "a duplicate birth must not clear the device table")
		assert.Equal(t, uint8(2), s.lastSeq, "a duplicate birth must not reset the seq baseline")
	}
	// The next in-order message continues cleanly — no gap, no rebirth.
	h.tr.Observe(dTop(DDATA, "sensor-7"), pl(3, dataMetric(10)))
	assert.Equal(t, 0, h.rec.count(), "a duplicate birth must not cause a spurious rebirth")
}

// M16 rebirth-storm backoff oracle: a burst of gapped messages inside the backoff
// window collapses to a single rebirth; once the window elapses and the node has
// still not recovered, the machine re-requests (escalates) rather than falling
// silent.
func TestRebirthStormBackoff(t *testing.T) {
	backoff := 5 * time.Second
	h := newHarness(backoff)
	h.tr.Observe(nTop(NBIRTH), pl(0, birthMetric("temperature", 1)))

	// First gap → rebirth #1; further gaps in the same window are suppressed.
	h.tr.Observe(nTop(NDATA), pl(2, dataMetric(1)))
	h.tr.Observe(nTop(NDATA), pl(3, dataMetric(1)))
	h.tr.Observe(nTop(NDATA), pl(4, dataMetric(1)))
	assert.Equal(t, 1, h.rec.count(), "a rebirth storm must collapse to one request per window")

	// Window elapses, node still broken → escalate with a second request.
	h.now = h.now.Add(backoff + time.Second)
	h.tr.Observe(nTop(NDATA), pl(5, dataMetric(1)))
	assert.Equal(t, 2, h.rec.count(), "an unrecovered node must be re-asked after the backoff window")
}

// Wrap (B2): the per-node seq counter is modular — 255→0 is a normal advance, not
// a gap — so a stream crossing the wrap emits zero rebirths.
func TestSeqWrapIsNotAGap(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.Observe(nTop(NBIRTH), pl(254, birthMetric("temperature", 1))) // seed baseline at 254
	h.tr.Observe(nTop(NDATA), pl(255, dataMetric(1)))
	h.tr.Observe(nTop(NDATA), pl(0, dataMetric(1))) // wrap
	h.tr.Observe(nTop(NDATA), pl(1, dataMetric(1)))
	assert.Equal(t, 0, h.rec.count(), "a modular seq wrap must not read as a gap")
}

// An NDATA referencing an alias no birth defined means the Host missed the birth
// → rebirth. (Locks the alias-resolution guard independently of the seq check.)
func TestNDataUnknownAliasTriggersRebirth(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.Observe(nTop(NBIRTH), pl(0, birthMetric("temperature", 1)))
	h.tr.Observe(nTop(NDATA), pl(1, dataMetric(99))) // alias 99 never defined
	assert.Equal(t, 1, h.rec.count(), "an unresolved alias must rebirth")
}

// A wire seq above 255 is malformed (the counter is a single byte); it must be
// treated as a gap, not truncated into a valid successor (uint8(257)==1, which a
// truncating check would accept after seq 0).
func TestOutOfRangeSeqIsRejected(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.Observe(nTop(NBIRTH), pl(0, birthMetric("temperature", 1)))
	h.tr.Observe(nTop(NDATA), pl(257, dataMetric(1)))
	assert.Equal(t, 1, h.rec.count(), "an out-of-range seq must be rejected, not truncated")
}

// A rebirth command encodes to a decodable NCMD payload carrying Node
// Control/Rebirth=true — the actual message the machine puts on the wire.
func TestRebirthCommandEncodesNodeControlRebirth(t *testing.T) {
	b, err := rebirthCommand(1_700_000_000_000)
	assert.NoError(t, err)
	p, err := codec.Decode(b)
	assert.NoError(t, err)
	if assert.Len(t, p.GetMetrics(), 1) {
		assert.Equal(t, nodeControlRebirthMetric, p.GetMetrics()[0].GetName())
		assert.True(t, p.GetMetrics()[0].GetBooleanValue())
	}
}
