// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import "time"

// emitOrd is where, inside ONE operation (a message or an advance), a detection was made. The
// single engine emits an operation's detections in a fixed sequence: first every timer the new
// frontier made due, in (deadline, rule, series) order; then every pane it closed, in (end, rule,
// series) order; then every held sample it released, run by run in (first sample time, rule,
// series) order; and last the message's events, in the order the message carried them. Those are
// total orders over keys, so the K shards, each producing its own subset in that same relative
// order, can be merged back into exactly the sequence one engine would have produced by sorting on
// this. It is only recorded by an engine a ConcurrentSharded owns (trackOrd).
type emitOrd struct {
	phase  uint8 // ordTimer, ordPane, ordPending, ordEvent, ordOther
	t      int64 // the heap key's time, unix nanoseconds (timer deadline, pane end, first held sample)
	rule   string
	series string
	idx    int32 // ordEvent: the event's position in the message
	sub    int32 // the detection's position among those the same step made
}

const (
	ordTimer uint8 = iota
	ordPane
	ordPending
	ordEvent
	ordOther
)

func (a emitOrd) less(b emitOrd) bool {
	if a.phase != b.phase {
		return a.phase < b.phase
	}
	if a.t != b.t {
		return a.t < b.t
	}
	if a.rule != b.rule {
		return a.rule < b.rule
	}
	if a.series != b.series {
		return a.series < b.series
	}
	if a.idx != b.idx {
		return a.idx < b.idx
	}
	return a.sub < b.sub
}

// step marks the start of one step of an operation: everything emitted until the next step is
// ordered under it.
func (e *Engine) step(phase uint8, t time.Time, rule, series string, idx int32) {
	if e.trackOrd {
		e.cur = emitOrd{phase: phase, t: t.UnixNano(), rule: rule, series: series, idx: idx}
	}
}

// stamp gives a detection its place in the current step.
func (e *Engine) stamp(d Detection) Detection {
	if e.trackOrd {
		d.ord = e.cur
		e.cur.sub++
	}
	return d
}

// drainOrdered is Drain keeping each detection's place, for the sharded engine's shards; it also
// returns the engine to "no step", so the next operation's control-plane emissions order as ordOther.
func (e *Engine) drainOrdered() []Detection {
	out := e.out
	e.out = nil
	e.cur = emitOrd{phase: ordOther}
	return out
}
