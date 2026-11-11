// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-microservice/ruletest"
)

func edgeBlocks(e ruletest.Expect) []*ruletest.EdgeExpect {
	return []*ruletest.EdgeExpect{e.Raised, e.Cleared, e.NeverRaised}
}
func intentBlocks(e ruletest.Expect) []*ruletest.IntentExpect {
	return []*ruletest.IntentExpect{e.Intent, e.NeverIntent}
}
func expectationDevice(e ruletest.Expect) string {
	for _, x := range edgeBlocks(e) {
		if x != nil {
			return x.Device
		}
	}
	for _, x := range intentBlocks(e) {
		if x != nil {
			return x.Device
		}
	}
	return ""
}
func judgedResult(r evaluate.Result, w WindowResult, until *time.Time) ruletest.Result {
	out := ruletest.Result{Evaluated: r.Evaluated}
	inside := func(at time.Time) bool {
		return !at.Before(w.Start) && !at.After(w.End) && (until == nil || !at.After(*until))
	}
	for _, e := range r.Edges {
		if inside(e.At) {
			out.Edges = append(out.Edges, ruletest.Edge{At: e.At, Series: e.Series, Rule: e.RuleToken, Kind: e.Kind, Raise: e.Raise})
		}
	}
	for _, in := range r.Intents {
		if inside(in.At) {
			out.Intents = append(out.Intents, ruletest.Intent{At: in.At, Series: in.Series, Rule: in.RuleToken, Raise: in.Raise, Kind: in.Kind, AlarmKey: in.AlarmKey, Command: in.Command, Payload: in.Payload, Connector: in.Connector, URL: in.URL, Body: in.Body})
		}
	}
	return out
}
func judge(e ruletest.Expect, w WindowResult, b, d side, r evaluate.Result, draft bool) (Verdict, string, []ReadingRef, string) {
	judgedSide := b
	if draft {
		judgedSide = d
	}
	full := judgedResult(r, w, nil)
	f := &ruletest.TestFile{Expect: []ruletest.Expect{e}}
	outcome := ruletest.Judge(f, full, w.Start, w.End.Sub(w.Start))[0]
	finish := func(v Verdict, reason string) (Verdict, string, []ReadingRef, string) {
		return v, reason, nil, outcome.Name
	}
	if !judgedSide.fidelity.Supported {
		return finish(Inconclusive, "the rule is unsupported for replay of recorded inputs")
	}
	dev := expectationDevice(e)
	var touched []DeviceCoverage
	for _, c := range w.Devices {
		if dev == "" || dev == c.Device {
			touched = append(touched, c)
		}
	}
	if len(w.Degraded) > 0 {
		return finish(Inconclusive, strings.Join(w.Degraded, "; "))
	}
	for _, c := range touched {
		if c.LateBase > 0 || c.LateDraft > 0 {
			return finish(Inconclusive, fmt.Sprintf("%s has readings declined as late in the %s window; either side may be incomplete", c.Device, w.Role))
		}
	}
	for _, c := range touched {
		if len(c.Readings) == 0 {
			return finish(Inconclusive, fmt.Sprintf("%s has no reading in the %s window", c.Device, w.Role))
		}
		for _, s := range []side{b, d} {
			for _, m := range s.metrics() {
				if c.Readings[m] == 0 {
					return finish(Inconclusive, fmt.Sprintf("%s has no reading of %s in the %s window", c.Device, m, w.Role))
				}
			}
		}
	}
	negative := e.NeverRaised != nil || e.NeverIntent != nil
	for _, c := range touched {
		if c.StateBase == Unknown || c.StateDraft == Unknown {
			return finish(Inconclusive, fmt.Sprintf("%s has UNKNOWN state when the %s window began; lead-in supplies no reset evidence", c.Device, w.Role))
		}
		state := c.StateBase
		if draft {
			state = c.StateDraft
		}
		if negative && state == Raised {
			return finish(Inconclusive, fmt.Sprintf("%s was already raised when the window began (%s)", c.Device, w.Role))
		}
	}
	if outcome.Errored {
		return finish(Inconclusive, outcome.Message)
	}
	var divergence *time.Time
	for _, c := range touched {
		if c.Divergence != nil && !c.Divergence.After(w.End) && (divergence == nil || c.Divergence.Before(*divergence)) {
			divergence = c.Divergence
		}
	}
	verdict := Fail
	reason := outcome.Message
	if divergence == nil {
		if outcome.Passed {
			verdict = Pass
			reason = "the expectation holds on identical recorded history"
		}
	} else {
		prefix := ruletest.Judge(f, judgedResult(r, w, divergence), w.Start, w.End.Sub(w.Start))[0]
		if negative {
			if !prefix.Passed && !prefix.Errored {
				verdict = Fail
				reason = prefix.Message
			} else {
				verdict = Inconclusive
				reason = "the two sides' commands diverged before absence could be established over the whole window"
			}
		} else {
			if prefix.Passed {
				verdict = Pass
				reason = "the expectation matched before the two sides' commands diverged"
			} else if outcome.Passed {
				verdict = Inconclusive
				reason = "first match after the two sides' commands diverged"
			} else {
				verdict = Inconclusive
				reason = "the expectation did not match before the two sides' commands diverged"
			}
		}
	}
	if verdict == Fail {
		reason, evidence := failureReason(e, w, b, d, r, draft, reason)
		return verdict, reason, evidence, outcome.Name
	}
	return finish(verdict, reason)
}
func failureReason(e ruletest.Expect, w WindowResult, b, d side, r evaluate.Result, draft bool, why string) (string, []ReadingRef) {
	label := "active rule"
	other := "draft"
	s := b
	otherIntents := w.DraftIntents
	if draft {
		label = "draft"
		other = "active rule"
		s = d
		otherIntents = w.BaseIntents
	}
	dev := expectationDevice(e)
	if dev == "" {
		dev = "the reviewed devices"
	}
	reason := fmt.Sprintf("%s in the %s window on %s. ", why, w.Role, dev)
	var evidence []ReadingRef
	// Reuse Judge to identify the actual matching witness, including payload, rule,
	// device and the expectation's own after/within limits.
	positive := e
	if e.NeverIntent != nil {
		positive.NeverIntent = nil
		positive.Intent = e.NeverIntent
	}
	if e.NeverRaised != nil {
		positive.NeverRaised = nil
		positive.Raised = e.NeverRaised
	}
	// Construct single-element judge inputs in linear time and use the same
	// expectation matcher for the witness as for the verdict.
	refs := map[string]map[time.Time]ReadingRef{}
	for _, ed := range r.Edges {
		if refs[ed.Series] == nil {
			refs[ed.Series] = map[time.Time]ReadingRef{}
		}
		ref := ReadingRef{Device: ed.Series, At: ed.At, Value: ed.Value, HasValue: ed.HasValue}
		if s.def.Type == "aggregate" {
			ref.Span = s.def.Window.D()
		}
		refs[ed.Series][ed.At.UTC()] = ref
	}
	report := func(device string, at time.Time, discriminator, payload string) {
		ref, ok := refs[device][at.UTC()]
		if !ok {
			ref = ReadingRef{Device: device, At: at}
		}
		value := "unavailable"
		if ref.HasValue {
			value = fmt.Sprintf("%g", ref.Value)
		}
		reason += fmt.Sprintf("The %s requests %s %s on %s at %s (reading value %s). ", label, discriminator, payload, device, at.UTC().Format(time.RFC3339Nano), value)
		matched := false
		for _, in := range otherIntents {
			if in.Series == device && in.At.Equal(at) {
				key := in.Command
				if key == "" {
					key = in.AlarmKey
				}
				reason += fmt.Sprintf("The %s requested %s %s. ", other, key, canonicalJSON(in.Payload))
				matched = true
				break
			}
		}
		if !matched {
			reason += fmt.Sprintf("The %s requested none at that instant. ", other)
		}
		evidence = append(evidence, ref)
	}
	if positive.Intent != nil {
		for _, in := range r.Intents {
			one := evaluate.Result{Evaluated: r.Evaluated, Intents: []evaluate.Intent{in}}
			hit := ruletest.Judge(&ruletest.TestFile{Expect: []ruletest.Expect{positive}}, judgedResult(one, w, nil), w.Start, w.End.Sub(w.Start))[0]
			if !hit.Passed {
				continue
			}
			key := in.Command
			if key == "" {
				key = in.AlarmKey
			}
			if key == "" {
				key = in.Kind
			}
			report(in.Series, in.At, key, canonicalJSON(in.Payload))
			break
		}
	} else {
		for _, ed := range r.Edges {
			one := evaluate.Result{Evaluated: r.Evaluated, Edges: []evaluate.Edge{ed}}
			hit := ruletest.Judge(&ruletest.TestFile{Expect: []ruletest.Expect{positive}}, judgedResult(one, w, nil), w.Start, w.End.Sub(w.Start))[0]
			if !hit.Passed {
				continue
			}
			key, payload := ed.RuleToken, ""
			for _, in := range r.Intents {
				if in.Series == ed.Series && in.At.Equal(ed.At) {
					if in.Command != "" {
						key = in.Command
						payload = canonicalJSON(in.Payload)
						break
					}
					if in.AlarmKey != "" {
						key = in.AlarmKey
					}
				}
			}
			report(ed.Series, ed.At, key, payload)
			break
		}
	}

	reason += "Both rules saw identical recorded readings up to the decisive instant."
	return reason, evidence
}
