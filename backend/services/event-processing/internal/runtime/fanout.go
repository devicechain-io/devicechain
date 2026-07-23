// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
)

// PlanResult is the outcome of fanning one resolved event out across its applicable rules.
type PlanResult struct {
	// Events are the per-rule core events to feed the engine (across every sample the message
	// carries). Empty when the event matches no in-scope rule or carries no metric any rule
	// gates on.
	Events []core.Event
	// Descopes are the (rule, series) pairs whose GROUP-SCOPED rule the event is OUT of scope
	// for (ADR-062 S4 membership-flip): the event's resolved entity is not a member of the
	// rule's pinned group@version, so instead of feeding a sample the runtime feeds a descope
	// to the engine (Engine.Descope) — resolving any raised alarm and tearing down the series'
	// keyed state so nothing fires spuriously for a series the rule no longer covers. Computed
	// once per event (memberships are per-event, not per-sample).
	Descopes []DescopeOp
	// EvalErrors counts leaf-predicate evaluation failures (e.g. a raw-CEL leaf tripping the
	// runtime cost limit, or referencing a metric it did not presence-guard). The offending
	// rule contributes no event for that sample: the event is SKIPPED, not fed a false leaf —
	// which for a Duration rule is the safe outcome (a false would cancel the hold; a skip
	// preserves it). The count is surfaced for metrics rather than failing the whole message.
	EvalErrors int
}

// DescopeOp names a (rule, series) whose keyed state the engine must drop + resolve because the
// series left the rule's scoped group (ADR-062 S4). At is the event's clamped time — the moment
// the descope takes effect (and the stale-guarded falling-edge time of any resolve it emits).
type DescopeOp struct {
	RuleID string
	Series string
	At     time.Time
}

// scopeMemberKey is the set-membership key for a stamped group@version — the exact pair the
// resolver denormalized onto the event and a scoped rule pins.
type scopeMemberKey struct {
	token   string
	version int32
}

// scopeMembershipSet projects an event's stamped ScopeMemberships into a set for O(1) rule
// scope tests. Empty for the pay-nothing common case (a tenant not using rule scoping, or a
// device in no rule-scoped group).
func scopeMembershipSet(ev *dmmodel.ResolvedEvent) map[scopeMemberKey]struct{} {
	if len(ev.ScopeMemberships) == 0 {
		return nil
	}
	set := make(map[scopeMemberKey]struct{}, len(ev.ScopeMemberships))
	for _, m := range ev.ScopeMemberships {
		set[scopeMemberKey{token: m.GroupToken, version: m.Version}] = struct{}{}
	}
	return set
}

// Plan selects the rules a resolved event feeds and builds one core Event per applicable rule
// per sample, honoring THE METRIC-SCOPED FEED CONTRACT (rules.CompiledRule): a rule that gates
// on a metric a sample does not carry is SKIPPED for that sample — never fed a false leaf. This
// is the load-bearing fix for the Duration hazard: a duration rule keyed on "temperature" must
// not see a "battery" sample (which would evaluate its leaf false and cancel the running hold).
//
//   - GateMetric set (structured threshold/duration/repeating): feed only samples carrying it.
//   - ValueMetric set (deltaRate, non-count aggregate): feed only samples carrying it.
//   - FeedMetrics set (a raw-CEL threshold/duration leaf, review D4): feed only samples carrying
//     at least one referenced metric — so unrelated telemetry does not resolve/cancel it.
//   - all empty (absence, match-every, count aggregate, correlation, an unscopeable raw-CEL leaf):
//     feed every in-scope sample. Absence is deliberately device-scoped (every event a
//     heartbeat); a raw-CEL author whose leaf touches m opaquely owns totality.
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
	profileRules := reg.RulesFor(tenant, ev.ProfileVersionToken)
	if len(profileRules) == 0 {
		return PlanResult{}
	}
	res := PlanResult{}

	// ADR-062 S4 group scope, decided ONCE per event (memberships are per-event, not
	// per-sample): partition the profile's rules into those the event is IN scope for (fed
	// normally below) and those a GROUP-SCOPED rule is OUT of scope for (fed a descope, so the
	// series' state is dropped + any alarm resolved). An unscoped rule (empty GroupToken) is
	// always in scope — the profile-wide default. The membership set is empty for the
	// pay-nothing common case, so an unscoped-only profile skips this entirely.
	members := scopeMembershipSet(ev)
	scoped := profileRules
	if hasGroupScopedRule(profileRules) {
		scoped = make([]*ScopedRule, 0, len(profileRules))
		for _, sr := range profileRules {
			if sr.GroupToken == "" {
				scoped = append(scoped, sr)
				continue
			}
			if _, ok := members[scopeMemberKey{token: sr.GroupToken, version: sr.GroupVersion}]; ok {
				scoped = append(scoped, sr)
				continue
			}
			// Out of scope: drop this series' state for the rule + resolve if raised.
			res.Descopes = append(res.Descopes, DescopeOp{
				RuleID: sr.Compiled.ID, Series: ev.SourceDeviceToken, At: occurred,
			})
		}
	}
	// Only the descope path had work (every applicable rule was out of scope): skip building
	// inputs entirely — there is nothing to feed.
	if len(scoped) == 0 {
		return res
	}

	// A presence StateChange (ADR-067 S3b) is authoritative connectivity, NOT a data heartbeat:
	// it feeds ONLY Connectivity rules a typed connect/disconnect edge and never enters the
	// measurement/heartbeat path (BuildInputs returns nil for it, so a DISCONNECT can't reset an
	// absence timer). Translating here in Plan — not in the persist loop — means the ADR-053
	// preview runner, which also calls Plan, previews connectivity rules for free.
	if ev.EventType == esmodel.StateChange {
		res.planConnectivity(seq, ev, occurred, scoped)
		return res
	}

	inputs := BuildInputs(ev, occurred)
	for i := range inputs {
		inputs[i].Attr = attr
	}
	for _, in := range inputs {
		for _, sr := range scoped {
			cr := sr.Compiled
			// A Connectivity rule is fed ONLY by a StateChange (handled above), never a measurement
			// sample — skip it here so a data event doesn't build a no-op presence-less event.
			if cr.Core.Kind == core.Connectivity {
				continue
			}
			// Metric-scoped feed. GateMetric (structured), ValueMetric (value kinds), and
			// FeedMetrics (a raw-CEL threshold/duration leaf, review D4) are mutually exclusive per
			// the compiler, so at most one gate is active; whichever is set must be present to feed.
			// FeedMetrics is a SET — feed when the event carries at least one member. Scoping is
			// safe because the compiler sets FeedMetrics ONLY for a leaf it proved is false when
			// none of those metrics are present (predicate.ScopableMetrics), so a skipped event
			// could not have raised — no dropped alarm.
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
			if feed := cr.FeedMetrics; len(feed) > 0 && !anyMetricPresent(in.M, feed) {
				continue
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

// planConnectivity feeds a resolved presence StateChange to the in-scope Connectivity rules
// (ADR-067 S3b) as a typed connect/disconnect edge — SessionId rides a uint64 PresenceEdge, never
// Event.Value (a float64 loses a UnixNano-scale epoch). Non-Connectivity rules never see a
// StateChange (a measurement rule has nothing to do with presence). One edge per rule, keyed on the
// source device; the engine's per-series cursor orders it via presence.Decide.
func (res *PlanResult) planConnectivity(seq uint64, ev *dmmodel.ResolvedEvent, occurred time.Time, scoped []*ScopedRule) {
	p, ok := ev.Payload.(*dmmodel.ResolvedStateChangePayload)
	if !ok {
		return // malformed presence payload: nothing to feed (the persist/projection paths log it)
	}
	edge := &core.PresenceEdge{
		SessionId: p.SessionId,
		Connected: p.State == string(esmodel.PresenceConnected),
	}
	for _, sr := range scoped {
		if sr.Compiled.Core.Kind != core.Connectivity {
			continue
		}
		res.Events = append(res.Events, core.Event{
			Seq:      seq,
			Key:      core.SeriesKey{Rule: sr.Compiled.ID, Series: ev.SourceDeviceToken},
			Time:     occurred,
			Presence: edge,
		})
	}
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

// hasGroupScopedRule reports whether any rule in the set carries a group scope — the cheap gate
// that keeps the per-event scope partition (and its membership-set build) off the hot path for
// the overwhelmingly common all-unscoped profile.
func hasGroupScopedRule(rules []*ScopedRule) bool {
	for _, sr := range rules {
		if sr.GroupToken != "" {
			return true
		}
	}
	return false
}

// anyMetricPresent reports whether the sample carries at least one of the given metric keys.
// It backs the raw-CEL threshold/duration feed scope (CompiledRule.FeedMetrics): the compiler
// only populates that set for a leaf it proved is false when none of the metrics are present, so
// a sample carrying none of them cannot raise — it would only evaluate the leaf to the false that
// spuriously resolves the alarm / cancels the hold, and is skipped.
func anyMetricPresent(m map[string]float64, metrics []string) bool {
	for _, k := range metrics {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
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
