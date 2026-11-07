// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package perturb is the experimental "fork the timeline" seam: a decorating replay opener that
// applies perturbation operators to a recorded resolved-event stream BEFORE the detector sees it.
//
// The detection engine, the compiled rules and the preview statistics are untouched. A
// counterfactual is the same preview.Run over the same history, read through an opener that
// answers "what if" by editing the events on the way past: drop a device, offset or scale a
// metric, freeze a value, black out a window. Running the real rules once over the untouched
// stream and once over the perturbed stream, then diffing the two firing timelines (see Diff),
// is a measured counterfactual rather than an opinion.
//
// Properties the operators keep, because a counterfactual is worth nothing if it is not
// reproducible:
//
//   - Deterministic. The same specs over the same history produce byte-identical output. The
//     only state is per-reader and advances with the stream order, never with the wall clock.
//   - Pure with respect to the recording. Nothing is written back; an event no operator touches
//     passes through with its original bytes.
//   - Tenant-contained. A message for another tenant is passed through untouched (the preview
//     discards it) and the operators never read it.
//
// Known limits of the prototype, stated so a result is not over-read: the baseline and the fork
// are two reads of the stream, not one snapshot, so events that land or age out between them
// show as divergence (the window should lie in the settled past); events an operator drops are
// consumed here and so do not count against the preview's scan limits (the request time budget
// still bounds the read); an operator edits the
// entries named by its metric, including a calculated entry that shares the name, but it does not
// re-derive OTHER calculated metrics from a perturbed input; location, alert and presence events
// are only ever dropped, never rewritten; and a freeze without an explicit value can hold only
// what the replay window itself contains.
//
// EXPERIMENTAL: reachable only behind the experimental flag, and not a served interface.
package perturb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// Operator names.
const (
	// OpDropDevice removes every event of one device inside [From, To) (an open end is
	// unbounded): "pump P-7 failed at 14:00" is a drop-device from 14:00 with no end.
	OpDropDevice = "drop-device"
	// OpMetricOffset replaces a metric's value v with v*Scale + Offset (Scale defaults to 1):
	// "every rooftop sensor reads 5 degrees hotter" is an offset of 5 with no device.
	OpMetricOffset = "metric-offset"
	// OpMetricFreeze holds a metric at one value from From on: the stuck sensor. Without an
	// explicit Value it holds the last value seen before From (or, with none seen, the first
	// value at or after it).
	OpMetricFreeze = "metric-freeze"
	// OpBlackout removes every event inside [From, To), for one device or for all of them:
	// "the gateway on Tower 2 went dark for 20 minutes". Both ends are required.
	OpBlackout = "blackout"
)

// Spec is one perturbation. Device scopes an operator to one device token ("" = every device,
// where the operator allows it); Metric names the measurement an offset or freeze acts on.
type Spec struct {
	Op     string
	Device string
	Metric string
	Offset float64
	// Scale multiplies the value before Offset is added. nil means 1.
	Scale *float64
	// Value is the explicit hold value of a freeze. nil holds the last observed value.
	Value *float64
	// From and To bound the perturbation in occurred time, [From, To). Zero means unbounded.
	From time.Time
	To   time.Time
}

// MaxSpecs bounds how many perturbations one fork carries.
const MaxSpecs = 16

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// Validate refuses a spec that would silently do nothing or something other than it says.
func (s Spec) Validate() error {
	if !s.To.IsZero() && !s.From.IsZero() && !s.To.After(s.From) {
		return fmt.Errorf("%s: the window must end after it starts", s.Op)
	}
	switch s.Op {
	case OpDropDevice:
		if s.Device == "" {
			return errors.New("drop-device needs a device")
		}
	case OpMetricOffset:
		if s.Metric == "" {
			return errors.New("metric-offset needs a metric")
		}
		if !finite(s.Offset) {
			return errors.New("metric-offset needs a finite offset")
		}
		if s.Scale != nil && !finite(*s.Scale) {
			return errors.New("metric-offset needs a finite scale")
		}
	case OpMetricFreeze:
		if s.Metric == "" {
			return errors.New("metric-freeze needs a metric")
		}
		if s.Value != nil && !finite(*s.Value) {
			return errors.New("metric-freeze needs a finite value")
		}
	case OpBlackout:
		if s.From.IsZero() || s.To.IsZero() {
			return errors.New("blackout needs both a start and an end")
		}
	default:
		return fmt.Errorf("unknown perturbation %q (want %s, %s, %s or %s)", s.Op,
			OpDropDevice, OpMetricOffset, OpMetricFreeze, OpBlackout)
	}
	return nil
}

// Describe is a one-line human reading of the spec, for result headers and agent narration.
func (s Spec) Describe() string {
	var b strings.Builder
	b.WriteString(s.Op)
	if s.Device != "" {
		b.WriteString(" device=" + s.Device)
	}
	if s.Metric != "" {
		b.WriteString(" metric=" + s.Metric)
	}
	switch s.Op {
	case OpMetricOffset:
		scale := 1.0
		if s.Scale != nil {
			scale = *s.Scale
		}
		fmt.Fprintf(&b, " value*%g%+g", scale, s.Offset)
	case OpMetricFreeze:
		if s.Value != nil {
			fmt.Fprintf(&b, " at=%g", *s.Value)
		}
	}
	if !s.From.IsZero() {
		b.WriteString(" from=" + s.From.UTC().Format(time.RFC3339))
	}
	if !s.To.IsZero() {
		b.WriteString(" to=" + s.To.UTC().Format(time.RFC3339))
	}
	return b.String()
}

func (s Spec) covers(t time.Time) bool {
	if !s.From.IsZero() && t.Before(s.From) {
		return false
	}
	if !s.To.IsZero() && !t.Before(s.To) {
		return false
	}
	return true
}

func (s Spec) device(token string) bool { return s.Device == "" || s.Device == token }

// Source is the replay-opener shape this package decorates (preview.ReplayOpener's method).
type Source interface {
	NewReplayReaderFromTime(suffix string, start time.Time) (messaging.ReplayReader, time.Time, error)
}

// Opener is the replay opener a fork reads through. It satisfies preview.ReplayOpener.
type Opener struct {
	inner  Source
	tenant string
	specs  []Spec
}

// Wrap decorates inner so that tenant's resolved events are read through specs. With no specs
// the opener is the IDENTITY perturbation: it still presents the tenant's events one per
// message under dense synthetic sequences, so a baseline read through Wrap(nil) and a fork read
// through Wrap(specs) differ by the perturbations and by nothing else.
func Wrap(inner Source, tenant string, specs []Spec) (*Opener, error) {
	if tenant == "" {
		return nil, errors.New("perturb: a fork needs a tenant")
	}
	if len(specs) > MaxSpecs {
		return nil, fmt.Errorf("perturb: %d perturbations, above the maximum of %d", len(specs), MaxSpecs)
	}
	for i, s := range specs {
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("perturbation %d: %w", i+1, err)
		}
	}
	return &Opener{inner: inner, tenant: tenant, specs: append([]Spec(nil), specs...)}, nil
}

// NewReplayReaderFromTime opens the inner reader and wraps it. Each open gets fresh operator
// state, so two reads of one opener are independent and identical.
func (o *Opener) NewReplayReaderFromTime(suffix string, start time.Time) (messaging.ReplayReader, time.Time, error) {
	r, first, err := o.inner.NewReplayReaderFromTime(suffix, start)
	if err != nil {
		return nil, time.Time{}, err
	}
	return &reader{inner: r, tenant: o.tenant, specs: o.specs,
		last: map[string]string{}, frozen: map[string]string{}}, first, nil
}

type reader struct {
	inner  messaging.ReplayReader
	tenant string
	specs  []Spec

	seq     uint64
	pending []messaging.Message
	// last is the newest value seen per "device\x00metric"; frozen the value a freeze latched.
	last   map[string]string
	frozen map[string]string
}

func (r *reader) Close() error { return r.inner.Close() }

func (r *reader) Read(ctx context.Context) (messaging.Message, error) {
	for {
		if len(r.pending) > 0 {
			m := r.pending[0]
			r.pending = r.pending[1:]
			return m, nil
		}
		msg, err := r.inner.Read(ctx)
		if err != nil {
			return messaging.Message{}, err
		}
		subjectTenant, ok := messaging.ParseTenantFromSubject(msg.Subject)
		if !ok || subjectTenant != r.tenant {
			return msg, nil // not this tenant's: the preview discards it, so it is never read here
		}
		values, units := [][]byte{msg.Value}, []messaging.Message{msg}
		if msg.IsEnvelope() {
			us, _, serr := messaging.SplitEnvelope(msg, subjectTenant, func(string) {}, nil)
			if serr != nil {
				continue // an unreadable envelope is skipped, as the preview skips it
			}
			values, units = make([][]byte, len(us)), make([]messaging.Message, len(us))
			for i, u := range us {
				values[i], units[i] = u.Msg.Value, u.Msg
			}
		}
		for i, v := range values {
			out, keep := r.apply(v)
			if !keep {
				continue
			}
			// Dense synthetic sequences: an envelope's members share one stream sequence and the
			// engine's idempotency guard would refuse all but the first of them once presented
			// as separate messages. The sequence only orders the replay; it is never reported.
			r.seq++
			m := units[i]
			m.Value = out
			m.StreamSeq = r.seq
			r.pending = append(r.pending, m)
		}
	}
}

// apply runs every spec over one encoded resolved event. keep=false removes it.
func (r *reader) apply(value []byte) ([]byte, bool) {
	ev, err := resolvedevent.UnmarshalResolvedEvent(value)
	if err != nil {
		return value, true // a corrupt payload is the preview's to skip, not this decorator's
	}
	changed := false
	// originals are the recorded values, snapshotted before any operator edits them: every
	// operator reads the recording, never another operator's output, and the "last seen" a freeze
	// holds is recorded from them once the whole event is done.
	originals := originalValues(ev)
	for si, s := range r.specs {
		switch s.Op {
		case OpDropDevice, OpBlackout:
			if !s.device(ev.SourceDeviceToken) {
				continue
			}
			payload, batched := ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
			if !batched {
				if s.covers(ev.OccurredTime) {
					return nil, false
				}
				continue
			}
			// A measurement event batches samples across time, so it is cut per sample.
			kept := make([]resolvedevent.ResolvedMeasurementsEntry, 0, len(payload.Entries))
			for _, e := range payload.Entries {
				if !s.covers(e.OccurredTime) {
					kept = append(kept, e)
				}
			}
			if len(kept) == 0 && len(payload.Entries) > 0 {
				return nil, false
			}
			if len(kept) != len(payload.Entries) {
				payload.Entries = kept
				changed = true
			}
		case OpMetricOffset, OpMetricFreeze:
			payload, ok := ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
			if !ok || !s.device(ev.SourceDeviceToken) {
				continue
			}
			for i := range payload.Entries {
				sample := &payload.Entries[i]
				for j := range sample.Entries {
					if sample.Entries[j].Name != s.Metric {
						continue
					}
					if r.perturbValue(si, s, ev.SourceDeviceToken, sample.OccurredTime, &sample.Entries[j], originals[[2]int{i, j}]) {
						changed = true
					}
				}
			}
		}
	}
	r.recordLast(ev, originals)
	if !changed {
		return value, true
	}
	out, merr := resolvedevent.MarshalResolvedEvent(ev)
	if merr != nil {
		return value, true
	}
	return out, true
}

// originalValues snapshots each measurement entry's recorded value, keyed by (sample, entry).
func originalValues(ev *resolvedevent.ResolvedEvent) map[[2]int]string {
	payload, ok := ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
	if !ok {
		return nil
	}
	out := map[[2]int]string{}
	for i := range payload.Entries {
		for j := range payload.Entries[i].Entries {
			out[[2]int{i, j}] = payload.Entries[i].Entries[j].Value
		}
	}
	return out
}

// recordLast remembers the newest RECORDED value per device and metric, for freezes that hold
// "the last value seen before From". It runs after every operator has read the event.
func (r *reader) recordLast(ev *resolvedevent.ResolvedEvent, originals map[[2]int]string) {
	payload, ok := ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
	if !ok {
		return
	}
	for i := range payload.Entries {
		for j := range payload.Entries[i].Entries {
			orig := originals[[2]int{i, j}]
			if v, err := strconv.ParseFloat(orig, 64); err == nil && finite(v) {
				r.last[ev.SourceDeviceToken+"\x00"+payload.Entries[i].Entries[j].Name] = orig
			}
		}
	}
}

func (r *reader) perturbValue(si int, s Spec, device string, at time.Time, e *resolvedevent.ResolvedMeasurementEntry, original string) bool {
	key := device + "\x00" + s.Metric
	cur, err := strconv.ParseFloat(original, 64)
	if err != nil || !finite(cur) {
		return false // a non-numeric reading has nothing to offset or hold
	}
	if !s.covers(at) {
		return false
	}
	switch s.Op {
	case OpMetricOffset:
		scale := 1.0
		if s.Scale != nil {
			scale = *s.Scale
		}
		e.Value = strconv.FormatFloat(cur*scale+s.Offset, 'g', -1, 64)
		return true
	case OpMetricFreeze:
		fk := strconv.Itoa(si) + "|" + key
		held, latched := r.frozen[fk]
		if !latched {
			switch {
			case s.Value != nil:
				held = strconv.FormatFloat(*s.Value, 'g', -1, 64)
			case r.last[key] != "":
				held = r.last[key]
			default:
				held = original
			}
			r.frozen[fk] = held
		}
		if e.Value == held {
			return false
		}
		e.Value = held
		return true
	}
	return false
}
