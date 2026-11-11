// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

func compare(v float64, op rules.CompareOp, t float64) bool {
	switch op {
	case rules.OpGt:
		return v > t
	case rules.OpGe:
		return v >= t
	case rules.OpLt:
		return v < t
	case rules.OpLe:
		return v <= t
	case rules.OpEq:
		return v == t
	case rules.OpNe:
		return v != t
	}
	return false
}
func metric(e evaluate.Event, name string) (float64, bool) {
	for _, m := range e.Metrics {
		if m.Name == name {
			return m.Value, true
		}
	}
	return 0, false
}
func startState(s side, r evaluate.Result, events []evaluate.Event, dev string, lo, start time.Time) State {
	if !s.enabled() {
		return Clear
	}
	state := Unknown
	edgeStart := lo
	if s.fidelity.ResetKind == rules.ResetAggregate {
		// Cold-start partial aggregates do not establish the live window's state.
		edgeStart = lo.Add(s.fidelity.Horizon)
	}
	for _, e := range r.Edges {
		if e.Series == dev && !e.At.Before(edgeStart) && e.At.Before(start) {
			state = Clear
			if e.Raise {
				state = Raised
			}
		}
	}
	if state != Unknown {
		return state
	}
	f := s.fidelity
	if !f.ResetStructured {
		return Unknown
	}
	var samples []evaluate.Event
	for _, e := range events {
		if e.Device != dev || e.At.Before(lo) || !e.At.Before(start) {
			continue
		}
		if f.GateMetric != "" {
			g, ok := metric(e, f.GateMetric)
			if !ok || !compare(g, f.GateOp, f.GateThreshold) {
				continue
			}
		}
		if f.Metric != "" {
			if _, ok := metric(e, f.Metric); !ok {
				continue
			}
		}
		samples = append(samples, e)
	}
	if f.ResetKind == rules.ResetReading {
		for _, e := range samples {
			v, _ := metric(e, f.Metric)
			if !compare(v, f.Op, f.Threshold) {
				return Clear
			}
		}
		return Unknown
	}
	// A trailing full-window test is deliberately also used for tumbling windows.
	// It requires a complete observed horizon, rather than inventing a partial pane.
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].At.Before(samples[j].At) })
	left := 0
	sum := 0.0
	var minQ, maxQ []int
	for right := 0; right < len(samples); {
		end := right + 1
		for end < len(samples) && samples[end].At.Equal(samples[right].At) {
			end++
		}
		for i := right; i < end; i++ {
			v, _ := metric(samples[i], f.Metric)
			sum += v
			for len(minQ) > 0 {
				last, _ := metric(samples[minQ[len(minQ)-1]], f.Metric)
				if last < v {
					break
				}
				minQ = minQ[:len(minQ)-1]
			}
			minQ = append(minQ, i)
			for len(maxQ) > 0 {
				last, _ := metric(samples[maxQ[len(maxQ)-1]], f.Metric)
				if last > v {
					break
				}
				maxQ = maxQ[:len(maxQ)-1]
			}
			maxQ = append(maxQ, i)
		}
		at := samples[right].At
		for left < end && !samples[left].At.After(at.Add(-f.Horizon)) {
			v, _ := metric(samples[left], f.Metric)
			sum -= v
			left++
		}
		for len(minQ) > 0 && minQ[0] < left {
			minQ = minQ[1:]
		}
		for len(maxQ) > 0 && maxQ[0] < left {
			maxQ = maxQ[1:]
		}
		if !at.Before(lo.Add(f.Horizon)) && end > left {
			v := sum
			switch f.Agg {
			case rules.AggCount:
				v = float64(end - left)
			case rules.AggAvg:
				v /= float64(end - left)
			case rules.AggMin:
				v, _ = metric(samples[minQ[0]], f.Metric)
			case rules.AggMax:
				v, _ = metric(samples[maxQ[0]], f.Metric)
			}
			if !compare(v, f.Op, f.Threshold) {
				return Clear
			}
		}
		right = end
	}

	return Unknown
}
func canonicalJSON(s string) string {
	var v any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
func commandActions(env *RuleEnvelope) (string, error) {
	if env == nil || !env.Enabled {
		return "[]", nil
	}
	def, err := rules.Decode(env.Definition)
	if err != nil {
		return "", err
	}
	var keys []string
	for _, a := range def.Actions {
		if a.Type == rules.ActionSendCommand && a.SendCommand != nil {
			// Copy the nested action before normalizing its JSON payload.
			command := *a.SendCommand
			command.Payload = canonicalJSON(command.Payload)
			a.SendCommand = &command
			b, err := json.Marshal(a)
			if err != nil {
				return "", err
			}
			keys = append(keys, string(b))
		}
	}
	sort.Strings(keys)
	if keys == nil {
		keys = []string{}
	}
	b, _ := json.Marshal(keys)
	return string(b), nil
}
func divergence(ctx context.Context, p Params, b, d side, br, dr evaluate.Result, c DeviceCoverage, lo time.Time) *time.Time {
	// Historical versions are checked even when neither compared rule sends a command:
	// an unavailable historical rule cannot establish identical physical history.
	base, err := commandActions(b.env)
	if err != nil {
		return &lo
	}
	versions := make([]string, 0, len(c.Versions))
	for v := range c.Versions {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	for _, v := range versions {
		if p.VersionRules == nil {
			return &lo
		}
		env, found, err := p.VersionRules(ctx, v)
		if err != nil || !found {
			return &lo
		}
		actions, err := commandActions(env)
		if err != nil || actions != base {
			return &lo
		}
	}
	group := func(r evaluate.Result) map[time.Time][]string {
		m := map[time.Time][]string{}
		for _, in := range r.Intents {
			if in.Series == c.Device && in.Kind == evaluate.KindSendCommand && !in.At.Before(lo) {
				parts := []string{in.Command, canonicalJSON(in.Payload)}
				if in.ExpiresAt != nil {
					parts = append(parts, in.ExpiresAt.Sub(in.At).String())
				}
				key, _ := json.Marshal(parts)
				at := in.At.UTC()
				m[at] = append(m[at], string(key))
			}
		}
		for _, keys := range m {
			sort.Strings(keys)
		}
		return m
	}
	bm, dm := group(br), group(dr)
	times := map[time.Time]bool{}
	for at := range bm {
		times[at] = true
	}
	for at := range dm {
		times[at] = true
	}
	var earliest *time.Time
	for at := range times {
		bs, _ := json.Marshal(bm[at])
		ds, _ := json.Marshal(dm[at])
		if string(bs) != string(ds) && (earliest == nil || at.Before(*earliest)) {
			v := at
			earliest = &v
		}
	}
	return earliest
}
