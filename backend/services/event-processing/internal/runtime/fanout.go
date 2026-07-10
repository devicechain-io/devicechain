// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

// PlanResult is the outcome of fanning one resolved event out across its applicable rules.
type PlanResult struct {
	// Events are the per-rule core events to feed the engine (across every sample the message
	// carries). Empty when the event matches no in-scope rule or carries no metric any rule
	// gates on.
	Events []core.Event
	// EvalErrors counts leaf-predicate evaluation failures (e.g. a raw-CEL leaf tripping the
	// runtime cost limit, or referencing a metric it did not presence-guard). The offending
	// rule contributes no event for that sample: the event is SKIPPED, not fed a false leaf —
	// which for a Duration rule is the safe outcome (a false would cancel the hold; a skip
	// preserves it). The count is surfaced for metrics rather than failing the whole message.
	EvalErrors int
}

// Plan selects the rules a resolved event feeds and builds one core Event per applicable rule
// per sample, honoring THE METRIC-SCOPED FEED CONTRACT (rules.CompiledRule): a rule that gates
// on a metric a sample does not carry is SKIPPED for that sample — never fed a false leaf. This
// is the load-bearing fix for the Duration hazard: a duration rule keyed on "temperature" must
// not see a "battery" sample (which would evaluate its leaf false and cancel the running hold).
//
//   - GateMetric set (structured threshold/duration/repeating): feed only samples carrying it.
//   - ValueMetric set (deltaRate, non-count aggregate): feed only samples carrying it.
//   - both empty (absence, match-every, count aggregate, correlation, raw-CEL): feed every
//     in-scope sample. Absence is deliberately device-scoped (every event a heartbeat); a
//     raw-CEL author owns totality.
//
// occurred is the message's clamped event time; every built event is stamped with it (all of a
// message's samples share the message time, matching event-management's persistence). A
// correlation rule (KeyedByAnchor) fans to one event per matching anchor, keyed by the anchor
// token with the source device as the distinct member, and the per-fan Input's anchor map is
// pinned to THAT anchor's token so an anchor-referencing member gate evaluates against the
// correct anchor.
//
// attr is the source device's flattened dynamic-threshold attribute map (DeviceAttributeView.For,
// SERVER-over-SHARED), bound onto every sample's Input so a dynamic comparison ("m[k] > attr[k]",
// slice 4c-3b-2) resolves the bound from the device's own attribute. It is the SAME per-device map
// for every sample (attributes are device-, not sample-scoped); nil when the device has none, which
// the presence-guarded comparison reads as a clean non-match. The predicate never mutates it.
func (reg *RuleRegistry) Plan(seq uint64, tenant string, ev *dmmodel.ResolvedEvent, occurred time.Time, attr map[string]float64) PlanResult {
	scoped := reg.RulesFor(tenant, ev.ProfileVersionToken)
	if len(scoped) == 0 {
		return PlanResult{}
	}
	res := PlanResult{}
	inputs := BuildInputs(ev, occurred)
	for i := range inputs {
		inputs[i].Attr = attr
	}
	for _, in := range inputs {
		for _, sr := range scoped {
			cr := sr.Compiled
			// Metric-scoped feed. GateMetric and ValueMetric are mutually exclusive per the
			// compiler, so at most one is set; either, when set, must be present to feed.
			if gate := cr.GateMetric; gate != "" {
				if _, ok := in.M[gate]; !ok {
					continue
				}
			}
			if vm := cr.ValueMetric; vm != "" {
				if _, ok := in.M[vm]; !ok {
					continue
				}
			}

			if cr.KeyedByAnchor() {
				res.fanCorrelation(seq, cr, ev, in)
				continue
			}

			e, err := cr.BuildEvent(seq, in.Device, "", in)
			if err != nil {
				res.EvalErrors++
				continue
			}
			res.Events = append(res.Events, e)
		}
	}
	return res
}

// fanCorrelation appends one event per matching anchor on the event, keyed by the anchor token
// with the source device as the member. Each fan gets an Input whose anchor-of-this-type is
// pinned to the fan's anchor token, so a member gate referencing anchors[type] sees the anchor
// the series is keyed on rather than whichever one BuildInputs collapsed to.
func (res *PlanResult) fanCorrelation(seq uint64, cr *rules.CompiledRule, ev *dmmodel.ResolvedEvent, in predicate.Input) {
	at := cr.AnchorType
	for _, a := range ev.Anchors {
		if a.AnchorType != at {
			continue
		}
		pinned := in
		pinned.Anchors = pinAnchor(in.Anchors, at, a.AnchorToken)
		e, err := cr.BuildEvent(seq, a.AnchorToken, in.Device, pinned)
		if err != nil {
			res.EvalErrors++
			continue
		}
		res.Events = append(res.Events, e)
	}
}

// pinAnchor returns a copy of the anchor map with anchorType set to token (the specific anchor
// this correlation fan is keyed on). It copies rather than mutating the shared sample map, so
// two anchors of the same type do not clobber each other across fans.
func pinAnchor(src map[string]string, anchorType, token string) map[string]string {
	out := make(map[string]string, len(src)+1)
	for k, v := range src {
		out[k] = v
	}
	out[anchorType] = token
	return out
}
