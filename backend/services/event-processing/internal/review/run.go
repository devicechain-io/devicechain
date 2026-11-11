// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-event-processing/internal/replayinput"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

type side struct {
	env      *RuleEnvelope
	def      rules.Rule
	compiled *rules.CompiledRule
	fidelity rules.Fidelity
}

func (s side) enabled() bool { return s.env != nil && s.env.Enabled }
func (s side) metrics() []string {
	if s.compiled == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, m := range append([]string{s.compiled.ValueMetric, s.compiled.GateMetric, s.fidelity.GateMetric}, s.compiled.FeedMetrics...) {
		if m != "" {
			seen[m] = true
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
func validate(p Params) error {
	bad := func(s string) error { return &InputError{Msg: s} }
	if p.Tenant == "" || p.ProfileToken == "" || p.Source == nil || p.Now.IsZero() {
		return bad("tenant, profile, source and now are required")
	}
	if len(p.Devices) < 1 || len(p.Devices) > MaxDevices {
		return bad("review requires 1..50 devices")
	}
	seen := map[string]bool{}
	for _, d := range p.Devices {
		if strings.TrimSpace(d) == "" || seen[d] {
			return bad("devices must be distinct and non-empty")
		}
		seen[d] = true
	}
	if len(p.Windows) != 2 {
		return bad("review requires exactly one INCIDENT and one NORMAL window")
	}
	roles := map[Role]bool{}
	for _, w := range p.Windows {
		if (w.Role != Incident && w.Role != Normal) || roles[w.Role] {
			return bad("review requires one INCIDENT and one NORMAL window")
		}
		roles[w.Role] = true
		if w.Start.IsZero() || !w.End.After(w.Start) || w.End.Sub(w.Start) > MaxWindow {
			return bad("each window must have positive duration at most 24h")
		}
		if w.End.After(p.Now.Add(-2 * time.Minute)) {
			return bad("each window must end at least two minutes before now")
		}
	}
	if p.Lateness < 0 || p.ShiftWindow < 0 || p.WindowBudget < 0 || p.MaxScan < 0 || p.MaxRead < 0 {
		return bad("durations and scan limits must not be negative")
	}
	if len(p.Expectations) > MaxExpectations {
		return bad("review allows at most 10 expectations")
	}
	for _, e := range p.Expectations {
		if !roles[e.Window] {
			return bad("expectation names an unknown window")
		}
		if err := e.Expect.Validate(); err != nil {
			return bad("invalid expectation: " + err.Error())
		}
		if e.Expect.AllowDegraded {
			return bad("allowDegraded is not allowed in a review")
		}
		for _, x := range edgeBlocks(e.Expect) {
			if x != nil {
				if x.Count != nil {
					return bad("count is not allowed in a review")
				}
				if x.Device != "" && !seen[x.Device] {
					return bad("expectation device is outside the reviewed devices")
				}
			}
		}
		for _, x := range intentBlocks(e.Expect) {
			if x != nil {
				if x.Count != nil {
					return bad("count is not allowed in a review")
				}
				if x.Device != "" && !seen[x.Device] {
					return bad("expectation device is outside the reviewed devices")
				}
			}
		}
	}
	bounds := map[Role]bool{}
	for _, b := range p.StreamBounds {
		if !roles[b.Role] || bounds[b.Role] || b.Stream != p.Suffix || b.LastSeq < b.FirstSeq || (b.FirstSeq == 0) != (b.LastSeq == 0) {
			return bad("recorded stream bounds are invalid")
		}
		bounds[b.Role] = true
	}
	return nil
}
func prepare(p Params, env *RuleEnvelope, name string) (side, error) {
	s := side{env: env, fidelity: rules.Fidelity{Supported: true}}
	if env == nil {
		return s, nil
	}
	if env.Token == "" {
		return s, &InputError{Msg: name + " rule has no token"}
	}
	def, err := rules.Decode(env.Definition)
	if err != nil {
		return s, &InputError{Msg: name + ": " + err.Error()}
	}
	def.ID = runtime.PublishedRuleID(p.Tenant, p.ProfileToken+"@review", env.Token)
	c, err := rules.Compile(def, rules.DefaultLimits())
	if err != nil {
		return s, &InputError{Msg: name + ": " + err.Error()}
	}
	s.def = def
	s.compiled = c
	s.fidelity = rules.ReplayFidelity(def, c)
	if env.EntityGroupToken != "" {
		s.fidelity.Supported = false
		s.fidelity.Reasons = append(s.fidelity.Reasons, "the rule is group-scoped, which a replay of telemetry cannot reproduce")
	}
	for _, m := range s.metrics() {
		for _, constant := range p.ConstantMetrics {
			if m == constant {
				s.fidelity.Supported = false
				s.fidelity.Reasons = append(s.fidelity.Reasons, "the rule reads calculated metric "+m+", whose formula reads device constants")
			}
		}
	}
	return s, nil
}

// Run validates and establishes replay eligibility before opening either window.
func Run(ctx context.Context, p Params) (Result, error) {
	if err := validate(p); err != nil {
		return Result{}, err
	}
	b, err := prepare(p, p.Base, "base")
	if err != nil {
		return Result{}, err
	}
	d, err := prepare(p, p.Draft, "draft")
	if err != nil {
		return Result{}, err
	}
	out := Result{Fidelity: Fidelity{Status: "SUPPORTED", Label: "replay of recorded inputs"}, Outcome: "INCONCLUSIVE"}
	for i, s := range []side{b, d} {
		if !s.fidelity.Supported {
			out.Fidelity.Status = "UNSUPPORTED"
			name := []string{"base", "draft"}[i]
			for _, r := range s.fidelity.Reasons {
				out.Fidelity.Reasons = append(out.Fidelity.Reasons, name+": "+r)
			}
		}
	}
	if out.Fidelity.Status == "UNSUPPORTED" {
		return out, nil
	}
	horizon := b.fidelity.Horizon
	if d.fidelity.Horizon > horizon {
		horizon = d.fidelity.Horizon
	}
	lead := horizon + LeadInMargin
	if lead > MaxLeadIn {
		lead = MaxLeadIn
	}
	windows := append([]Window(nil), p.Windows...)
	sort.Slice(windows, func(i, j int) bool { return windows[i].Role < windows[j].Role })
	devices := append([]string(nil), p.Devices...)
	sort.Strings(devices)
	results := map[Role][2]evaluate.Result{}
	for _, w := range windows {
		wr, evs, err := readWindow(ctx, p, w, lead, devices)
		if err != nil {
			return Result{}, err
		}
		br, err := evaluateSide(ctx, p, b, evs, w.End)
		if err != nil {
			return Result{}, err
		}
		dr, err := evaluateSide(ctx, p, d, evs, w.End)
		if err != nil {
			return Result{}, err
		}
		for _, r := range []evaluate.Result{br, dr} {
			for _, g := range r.Degradations {
				if g.Reason != evaluate.DegradedLateReadings {
					addReason(&wr.Degraded, "evaluation is incomplete: "+g.Reason)
				}
			}
		}
		partition(&wr, br, dr)
		for i := range wr.Devices {
			c := &wr.Devices[i]
			c.LateBase = br.LateByDevice[c.Device]
			c.LateDraft = dr.LateByDevice[c.Device]
			c.StateBase = startState(b, br, evs, c.Device, wr.LeadInStart, w.Start)
			c.StateDraft = startState(d, dr, evs, c.Device, wr.LeadInStart, w.Start)
			c.Divergence = divergence(ctx, p, b, d, br, dr, *c, wr.LeadInStart)
		}
		diff(&wr, b, d, p.ShiftWindow)
		out.Windows = append(out.Windows, wr)
		results[w.Role] = [2]evaluate.Result{br, dr}
	}
	for i, e := range p.Expectations {
		var w *WindowResult
		for j := range out.Windows {
			if out.Windows[j].Role == e.Window {
				w = &out.Windows[j]
			}
		}
		rs := results[e.Window]
		v, reason, evidence, name := judge(e.Expect, *w, b, d, rs[1], true)
		bv, breason, _, _ := judge(e.Expect, *w, b, d, rs[0], false)
		out.Expectations = append(out.Expectations, ExpectationResult{Index: i, Name: name, Window: e.Window, Verdict: v, Baseline: bv, Reason: reason, BaselineReason: breason, Evidence: evidence})
	}
	fail, inc := false, false
	per := map[Role]int{}
	for _, e := range out.Expectations {
		per[e.Window]++
		fail = fail || e.Verdict == Fail
		inc = inc || e.Verdict == Inconclusive
	}
	degraded := false
	for _, w := range out.Windows {
		degraded = degraded || len(w.Degraded) > 0
	}
	if fail && !inc {
		out.Outcome = "FAILED"
	} else if !fail && !inc && !degraded && per[Incident] > 0 && per[Normal] > 0 {
		out.Outcome = "PASSED"
	}
	var digests strings.Builder
	for _, w := range out.Windows {
		digests.WriteString(w.InputDigest)
	}
	out.InputDigest = digest(digests.String())
	return out, nil
}
func evaluateSide(ctx context.Context, p Params, s side, evs []evaluate.Event, end time.Time) (evaluate.Result, error) {
	if !s.enabled() || len(evs) == 0 {
		return evaluate.Result{}, nil
	}
	return evaluate.Run(ctx, evaluate.Params{Tenant: p.Tenant, ProfileVersion: p.ProfileToken + "@review", Rules: []evaluate.Rule{{Token: s.env.Token, Def: s.def}}, Events: evs, AdvanceTo: end, Lateness: p.Lateness, AppliedGuard: p.AppliedGuard, AttributeLate: true, MaxEvents: evaluate.HardMaxEvents, MaxEdges: evaluate.HardMaxEdges, MaxIntents: evaluate.HardMaxIntents})
}
func readWindow(ctx context.Context, p Params, w Window, lead time.Duration, devices []string) (WindowResult, []evaluate.Event, error) {
	wr := WindowResult{Role: w.Role, Start: w.Start, End: w.End, LeadInStart: w.Start.Add(-lead)}
	set := map[string]bool{}
	coverage := map[string]*DeviceCoverage{}
	for _, dev := range devices {
		set[dev] = true
		coverage[dev] = &DeviceCoverage{Device: dev, Readings: map[string]int{}, LeadInReadings: map[string]int{}, Versions: map[string]int{}}
	}
	budget := p.WindowBudget
	if budget == 0 {
		budget = 45 * time.Second
	}
	wc, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	reader, first, err := p.Source.NewReplayReaderFromTime(p.Suffix, wr.LeadInStart)
	if err != nil {
		return wr, nil, err
	}
	defer reader.Close()
	if !first.IsZero() && first.After(wr.LeadInStart) {
		addReason(&wr.Degraded, "history before the earliest retained event has aged out; lead-in is incomplete")
	}
	var bound *Bounds
	for i := range p.StreamBounds {
		if p.StreamBounds[i].Role == w.Role {
			bound = &p.StreamBounds[i]
		}
	}
	upper := uint64(0)
	if bound != nil {
		upper = bound.LastSeq
	}
	var events []evaluate.Event
	counts, stop, err := replayinput.Scan(wc, reader, replayinput.Admission{Tenant: p.Tenant, AdmitVersion: func(v string) bool {
		tail, ok := strings.CutPrefix(v, p.ProfileToken+"@")
		if !ok {
			return false
		}
		for _, ch := range tail {
			if ch < '0' || ch > '9' {
				return false
			}
		}
		n, e := strconv.ParseUint(tail, 10, 64)
		return e == nil && n > 0
	}, Devices: set, Span: replayinput.TimeRange{Start: wr.LeadInStart, End: w.End}, UpperSeq: upper, MaxScan: p.MaxScan, MaxRead: p.MaxRead, AppliedGuard: p.AppliedGuard}, func(_ messaging.Message, ev *resolvedevent.ResolvedEvent, _ bool) {
		payload, ok := ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
		if !ok {
			return
		}
		for _, entry := range payload.Entries {
			at := entry.OccurredTime
			if at.Before(wr.LeadInStart) || at.After(w.End) {
				continue
			}
			e := evaluate.Event{Device: ev.SourceDeviceToken, At: at}
			for _, m := range entry.Entries {
				v, err := strconv.ParseFloat(m.Value, 64)
				if err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
					e.Metrics = append(e.Metrics, evaluate.Metric{Name: m.Name, Value: v})
				}
			}
			if len(e.Metrics) == 0 {
				continue
			}
			sort.SliceStable(e.Metrics, func(i, j int) bool { return e.Metrics[i].Name < e.Metrics[j].Name })
			events = append(events, e)
			c := coverage[e.Device]
			c.Versions[ev.ProfileVersionToken]++
			if c.First.IsZero() || at.Before(c.First) {
				c.First = at
			}
			if c.Last.IsZero() || at.After(c.Last) {
				c.Last = at
			}
			for _, m := range e.Metrics {
				if at.Before(w.Start) {
					c.LeadInReadings[m.Name]++
				} else {
					c.Readings[m.Name]++
				}
			}
		}
	})
	if err != nil {
		return wr, nil, err
	}
	if bound != nil && counts.FirstSeq > bound.FirstSeq {
		addReason(&wr.Degraded, "recorded history has aged out before the first recorded sequence")
	}
	switch stop {
	case replayinput.StopReadCap:
		addReason(&wr.Degraded, "the replay read cap was reached; history is truncated")
	case replayinput.StopScanCap:
		addReason(&wr.Degraded, "the replay scan cap was reached; admitted history is truncated")
	case replayinput.StopDeadline:
		addReason(&wr.Degraded, "the window time budget expired before replay completed")
	}
	if wc.Err() == context.DeadlineExceeded && stop != replayinput.StopDeadline {
		addReason(&wr.Degraded, "the window time budget expired before replay completed")
	}

	if counts.Corrupt > 0 {
		addReason(&wr.Degraded, fmt.Sprintf("%d corrupt records in this tenant's history could not be replayed", counts.Corrupt))
	}
	wr.Bounds = Bounds{Role: w.Role, Stream: p.Suffix, FirstSeq: counts.FirstSeq, LastSeq: counts.LastSeq}
	// The message past an upper bound was delivered by the reader, but must not
	// move the reusable bound forward on successive reruns.
	if bound != nil && wr.Bounds.LastSeq > bound.LastSeq {
		wr.Bounds.LastSeq = bound.LastSeq
	}
	for _, dev := range devices {
		wr.Devices = append(wr.Devices, *coverage[dev])
	}
	var canonical strings.Builder
	for _, e := range events {
		fmt.Fprintf(&canonical, "%s\t%s", e.Device, e.At.UTC().Format(time.RFC3339Nano))
		for _, m := range e.Metrics {
			fmt.Fprintf(&canonical, "\t%s=%s", m.Name, strconv.FormatFloat(m.Value, 'g', -1, 64))
		}
		canonical.WriteByte('\n')
	}
	wr.InputDigest = digest(canonical.String())
	return wr, events, nil
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func addReason(dst *[]string, s string) {
	for _, r := range *dst {
		if r == s {
			return
		}
	}
	*dst = append(*dst, s)
}
func partition(w *WindowResult, b, d evaluate.Result) {
	for side, r := range []evaluate.Result{b, d} {
		for _, e := range r.Edges {
			if e.At.Before(w.Start) {
				if side == 0 {
					w.LeadInBaseEdges = append(w.LeadInBaseEdges, e)
				} else {
					w.LeadInDraftEdges = append(w.LeadInDraftEdges, e)
				}
			} else if !e.At.After(w.End) {
				if side == 0 {
					w.BaseEdges = append(w.BaseEdges, e)
				} else {
					w.DraftEdges = append(w.DraftEdges, e)
				}
			}
		}
		for _, in := range r.Intents {
			if in.At.Before(w.Start) {
				if side == 0 {
					w.LeadInBaseIntents = append(w.LeadInBaseIntents, in)
				} else {
					w.LeadInDraftIntents = append(w.LeadInDraftIntents, in)
				}
			} else if !in.At.After(w.End) {
				if side == 0 {
					w.BaseIntents = append(w.BaseIntents, in)
				} else {
					w.DraftIntents = append(w.DraftIntents, in)
				}
			}
		}
	}
}
