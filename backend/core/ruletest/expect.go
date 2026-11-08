// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ruletest

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// TestFile is a *.test.yaml: a scenario to run and the expectations to judge its result by.
type TestFile struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	// Name labels the file in the report; it defaults to the file's name.
	Name string `yaml:"name"`
	// Scenario is the scenario to run, as a path relative to this file. It defaults to
	// scenario.yaml in the same directory.
	Scenario string   `yaml:"scenario"`
	Expect   []Expect `yaml:"expect"`
}

// Expect is one expectation. Exactly one of the five blocks is named.
type Expect struct {
	// Name labels the expectation in the report; it defaults to a description of the block.
	Name        string        `yaml:"name"`
	Raised      *EdgeExpect   `yaml:"raised"`
	Cleared     *EdgeExpect   `yaml:"cleared"`
	NeverRaised *EdgeExpect   `yaml:"neverRaised"`
	Intent      *IntentExpect `yaml:"intent"`
	NeverIntent *IntentExpect `yaml:"neverIntent"`
	// AllowDegraded lets a neverRaised, neverIntent or count expectation be judged on a partial
	// result. Without it such an expectation is an error on a partial result: a result that dropped
	// late readings or truncated intents cannot prove that something did NOT happen, or how often.
	AllowDegraded bool `yaml:"allowDegraded"`
}

// Window bounds when an expectation looks, as offsets from the scenario start: from After to
// After+Within. Within omitted means to the end of the scenario.
type Window struct {
	After  Duration  `yaml:"after"`
	Within *Duration `yaml:"within"`
}

// EdgeExpect matches the alarm edges a rule derives. Rule and Device omitted match any.
type EdgeExpect struct {
	Rule   string `yaml:"rule"`
	Device string `yaml:"device"`
	Window `yaml:",inline"`
	// Count, when set, requires exactly that many matching edges rather than at least one.
	Count *int `yaml:"count"`
}

// IntentExpect matches the side effects REACT would request. Fields omitted match any.
type IntentExpect struct {
	Rule   string `yaml:"rule"`
	Device string `yaml:"device"`
	// Kind is raiseAlarm, clearAlarm, sendCommand, httpCall or publish.
	Kind string `yaml:"kind"`
	// Edge restricts to the intents of raised or resolved edges.
	Edge string `yaml:"edge"`
	// Command and Payload match a sendCommand: the command key, and its argument object, compared
	// as JSON (key order and whitespace do not matter).
	Command string `yaml:"command"`
	Payload any    `yaml:"payload"`
	// AlarmKey matches a raise or clear.
	AlarmKey string `yaml:"alarmKey"`
	// Connector, URL, Body and BodyContains match a publish or httpCall.
	Connector    string `yaml:"connector"`
	URL          string `yaml:"url"`
	Body         string `yaml:"body"`
	BodyContains string `yaml:"bodyContains"`
	Window       `yaml:",inline"`
	Count        *int `yaml:"count"`
}

// ParseTestFile reads and validates a *.test.yaml.
func ParseTestFile(data []byte) (*TestFile, error) {
	var f TestFile
	if err := DecodeStrict(data, &f); err != nil {
		return nil, err
	}
	if err := checkHeader(f.APIVersion, f.Kind, KindTest); err != nil {
		return nil, err
	}
	if len(f.Expect) == 0 {
		return nil, errors.New("expect is required: a test with no expectation can never fail")
	}
	for i := range f.Expect {
		if err := f.Expect[i].validate(); err != nil {
			return nil, fmt.Errorf("expect[%d]: %w", i, err)
		}
	}
	return &f, nil
}

var intentKinds = map[string]bool{"raiseAlarm": true, "clearAlarm": true, "sendCommand": true, "httpCall": true, "publish": true}

func (e Expect) blocks() int {
	n := 0
	for _, b := range []bool{e.Raised != nil, e.Cleared != nil, e.NeverRaised != nil, e.Intent != nil, e.NeverIntent != nil} {
		if b {
			n++
		}
	}
	return n
}

func (w Window) validate() error {
	if w.After < 0 || (w.Within != nil && *w.Within <= 0) {
		return errors.New("after must not be negative and within must be positive")
	}
	return nil
}

func (e Expect) validate() error {
	if n := e.blocks(); n != 1 {
		return fmt.Errorf("name exactly one of raised, cleared, neverRaised, intent and neverIntent (found %d)", n)
	}
	for _, x := range []*EdgeExpect{e.Raised, e.Cleared, e.NeverRaised} {
		if x == nil {
			continue
		}
		if err := x.Window.validate(); err != nil {
			return err
		}
		if x.Count != nil && *x.Count < 0 {
			return errors.New("count must not be negative")
		}
	}
	if e.NeverRaised != nil && e.NeverRaised.Count != nil {
		return errors.New("neverRaised takes no count: it means zero")
	}
	for _, x := range []*IntentExpect{e.Intent, e.NeverIntent} {
		if x == nil {
			continue
		}
		if err := x.Window.validate(); err != nil {
			return err
		}
		if x.Kind != "" && !intentKinds[x.Kind] {
			return fmt.Errorf("kind %q is not one of raiseAlarm, clearAlarm, sendCommand, httpCall, publish", x.Kind)
		}
		if x.Edge != "" && x.Edge != "raised" && x.Edge != "resolved" {
			return fmt.Errorf("edge must be raised or resolved, not %q", x.Edge)
		}
		if x.Count != nil && *x.Count < 0 {
			return errors.New("count must not be negative")
		}
		if x.Payload != nil {
			if _, err := json.Marshal(normalizeYAML(x.Payload)); err != nil {
				return fmt.Errorf("payload is not JSON-representable: %w", err)
			}
		}
		if x == e.NeverIntent && x.Count != nil {
			return errors.New("neverIntent takes no count: it means zero")
		}
	}
	return nil
}

// Edge is a derived alarm transition, as a server reports it.
type Edge struct {
	At     time.Time
	Series string
	Rule   string
	Kind   string
	Raise  bool
}

// Intent is a REACT side effect an edge would have caused, as a server reports it.
type Intent struct {
	At        time.Time
	Series    string
	Rule      string
	Raise     bool
	Kind      string
	AlarmKey  string
	Command   string
	Payload   string
	Connector string
	URL       string
	Body      string
}

// Degradation is one reason the server gave for a partial result, with a count.
type Degradation struct {
	Reason string
	Count  int
}

// Result is what an evaluation returned.
type Result struct {
	Edges   []Edge
	Intents []Intent
	// Evaluated is the token of every rule the evaluation ran. An expectation that names a rule
	// not in it cannot be judged: it is an error, never a pass.
	Evaluated []string
	// Skipped maps a rule the server did not run to the reason it gave.
	Skipped map[string]string
	// Degradations are the reasons the result is partial. A partial result makes a neverRaised,
	// neverIntent or count expectation inconclusive unless it sets allowDegraded.
	Degradations []Degradation
	// Degraded is the server's sentence describing the degradations; it is shown with a failure.
	Degraded string
}

// partial reports whether the server said the result is incomplete.
func (r Result) partial() bool { return len(r.Degradations) > 0 || r.Degraded != "" }

// Outcome is the verdict on one expectation: passed, failed, or errored (it could not be judged).
type Outcome struct {
	Name    string
	Passed  bool
	Errored bool
	Message string
}

// Judge evaluates every expectation of f against res. start is the scenario's start and duration
// its length, which anchor each expectation's window.
func Judge(f *TestFile, res Result, start time.Time, duration time.Duration) []Outcome {
	out := make([]Outcome, 0, len(f.Expect))
	evaluated := map[string]bool{}
	for _, r := range res.Evaluated {
		evaluated[r] = true
	}
	for i, e := range f.Expect {
		o := e.judge(res, start, duration)
		if msg := e.unjudgeable(res, evaluated); msg != "" {
			o = Outcome{Name: o.Name, Errored: true, Message: msg}
		}
		if o.Name == "" {
			o.Name = fmt.Sprintf("expect[%d]", i)
		}
		if e.Name != "" {
			o.Name = e.Name
		}
		if !o.Passed && !o.Errored && res.Degraded != "" {
			o.Message += " (the evaluation reported: " + res.Degraded + ")"
		}
		out = append(out, o)
	}
	return out
}

// rule returns the rule the expectation names ("" for any rule).
func (e Expect) rule() string {
	switch {
	case e.Raised != nil:
		return e.Raised.Rule
	case e.Cleared != nil:
		return e.Cleared.Rule
	case e.NeverRaised != nil:
		return e.NeverRaised.Rule
	case e.Intent != nil:
		return e.Intent.Rule
	case e.NeverIntent != nil:
		return e.NeverIntent.Rule
	}
	return ""
}

// negativeOrCounted reports whether the expectation claims an absence or an exact number, which a
// partial result cannot establish.
func (e Expect) negativeOrCounted() bool {
	switch {
	case e.NeverRaised != nil, e.NeverIntent != nil:
		return true
	case e.Raised != nil:
		return e.Raised.Count != nil
	case e.Cleared != nil:
		return e.Cleared.Count != nil
	case e.Intent != nil:
		return e.Intent.Count != nil
	}
	return false
}

// unjudgeable returns why the expectation cannot be judged against res, or "" when it can. It is an
// ERROR, not a failure, because the rule under test was not shown to be wrong: the run could not
// answer the question.
func (e Expect) unjudgeable(res Result, evaluated map[string]bool) string {
	if r := e.rule(); r != "" && !evaluated[r] {
		if why, ok := res.Skipped[r]; ok {
			return fmt.Sprintf("rule %s was skipped by the evaluation, so this expectation cannot be judged: %s", r, why)
		}
		ran := append([]string(nil), res.Evaluated...)
		if len(ran) == 0 {
			return fmt.Sprintf("rule %s was not evaluated: the evaluation ran no rules", r)
		}
		return fmt.Sprintf("rule %s was not evaluated, so this expectation cannot be judged; the rules evaluated were: %s",
			r, strings.Join(ran, ", "))
	}
	if res.partial() && e.negativeOrCounted() && !e.AllowDegraded {
		why := res.Degraded
		if why == "" {
			var parts []string
			for _, d := range res.Degradations {
				parts = append(parts, fmt.Sprintf("%s (%d)", d.Reason, d.Count))
			}
			why = strings.Join(parts, ", ")
		}
		return "inconclusive: the evaluation's result is partial, so it cannot show that something did not happen or how often (" +
			why + "); fix the scenario, or set allowDegraded: true on this expectation to judge it anyway"
	}
	return ""
}

func (w Window) bounds(start time.Time, duration time.Duration) (time.Time, time.Time) {
	lo := start.Add(w.After.D())
	hi := start.Add(duration)
	if w.Within != nil {
		hi = lo.Add(w.Within.D())
	}
	return lo, hi
}

func describeWindow(w Window, lo, hi time.Time) string {
	if w.After == 0 && w.Within == nil {
		return "anywhere in the scenario"
	}
	return fmt.Sprintf("between %s and %s", lo.UTC().Format(time.RFC3339), hi.UTC().Format(time.RFC3339))
}

func who(rule, device string) string {
	var parts []string
	if rule != "" {
		parts = append(parts, "rule "+rule)
	}
	if device != "" {
		parts = append(parts, "device "+device)
	}
	if len(parts) == 0 {
		return "any rule on any device"
	}
	return strings.Join(parts, " on ")
}

func (e Expect) judge(res Result, start time.Time, duration time.Duration) Outcome {
	switch {
	case e.Raised != nil:
		return judgeEdges("raised", *e.Raised, true, res, start, duration)
	case e.Cleared != nil:
		return judgeEdges("cleared", *e.Cleared, false, res, start, duration)
	case e.NeverRaised != nil:
		x := *e.NeverRaised
		lo, hi := x.Window.bounds(start, duration)
		var hits []Edge
		for _, ed := range res.Edges {
			if ed.Raise && matchEdge(x, ed, lo, hi) {
				hits = append(hits, ed)
			}
		}
		name := fmt.Sprintf("%s never raised", who(x.Rule, x.Device))
		if len(hits) > 0 {
			return Outcome{Name: name, Message: fmt.Sprintf("expected %s never to raise %s, but it raised %d time(s), first at %s on %s",
				who(x.Rule, x.Device), describeWindow(x.Window, lo, hi), len(hits), hits[0].At.UTC().Format(time.RFC3339), hits[0].Series)}
		}
		return Outcome{Name: name, Passed: true}
	case e.Intent != nil:
		return judgeIntent(*e.Intent, false, res, start, duration)
	default:
		return judgeIntent(*e.NeverIntent, true, res, start, duration)
	}
}

func matchEdge(x EdgeExpect, ed Edge, lo, hi time.Time) bool {
	return (x.Rule == "" || x.Rule == ed.Rule) && (x.Device == "" || x.Device == ed.Series) &&
		!ed.At.Before(lo) && !ed.At.After(hi)
}

func judgeEdges(verb string, x EdgeExpect, raise bool, res Result, start time.Time, duration time.Duration) Outcome {
	lo, hi := x.Window.bounds(start, duration)
	var in, anywhere []Edge
	for _, ed := range res.Edges {
		if ed.Raise != raise || (x.Rule != "" && x.Rule != ed.Rule) || (x.Device != "" && x.Device != ed.Series) {
			continue
		}
		anywhere = append(anywhere, ed)
		if matchEdge(x, ed, lo, hi) {
			in = append(in, ed)
		}
	}
	name := fmt.Sprintf("%s %s", who(x.Rule, x.Device), verb)
	if x.Count != nil {
		name += fmt.Sprintf(" %d time(s)", *x.Count)
	}
	pass := len(in) > 0
	if x.Count != nil {
		pass = len(in) == *x.Count
	}
	if pass {
		return Outcome{Name: name, Passed: true}
	}
	want := "at least once"
	if x.Count != nil {
		want = fmt.Sprintf("exactly %d time(s)", *x.Count)
	}
	msg := fmt.Sprintf("expected %s to be %s %s, %s; it was %s %d time(s) in that window",
		who(x.Rule, x.Device), verb, want, describeWindow(x.Window, lo, hi), verb, len(in))
	if len(in) == 0 && len(anywhere) > 0 {
		msg += fmt.Sprintf(" (it was %s outside it, first at %s)", verb, anywhere[0].At.UTC().Format(time.RFC3339))
	} else if len(in) == 0 {
		msg += fmt.Sprintf("; it was never %s in the whole scenario (the evaluation derived %d edge(s) in all)", verb, len(res.Edges))
	}
	return Outcome{Name: name, Message: msg}
}

func matchIntent(x IntentExpect, in Intent, lo, hi time.Time) bool {
	if in.At.Before(lo) || in.At.After(hi) {
		return false
	}
	eq := func(want, got string) bool { return want == "" || want == got }
	if !eq(x.Rule, in.Rule) || !eq(x.Device, in.Series) || !eq(x.Kind, in.Kind) || !eq(x.Command, in.Command) ||
		!eq(x.AlarmKey, in.AlarmKey) || !eq(x.Connector, in.Connector) || !eq(x.URL, in.URL) || !eq(x.Body, in.Body) {
		return false
	}
	if x.Edge == "raised" && !in.Raise || x.Edge == "resolved" && in.Raise {
		return false
	}
	if x.BodyContains != "" && !strings.Contains(in.Body, x.BodyContains) {
		return false
	}
	if x.Payload != nil && !jsonEqual(x.Payload, in.Payload) {
		return false
	}
	return true
}

// jsonEqual reports whether want (decoded from the test file) and got (a JSON document) are the
// same value. An empty got equals an empty object, because that is what a no-argument command is.
func jsonEqual(want any, got string) bool {
	var g any
	if strings.TrimSpace(got) == "" {
		g = map[string]any{}
	} else if err := json.Unmarshal([]byte(got), &g); err != nil {
		return false
	}
	b, err := json.Marshal(normalizeYAML(want))
	if err != nil {
		return false
	}
	var w any
	if err := json.Unmarshal(b, &w); err != nil {
		return false
	}
	return reflect.DeepEqual(w, g)
}

// normalizeYAML turns the map[any]any shapes YAML can produce into JSON-encodable ones.
func normalizeYAML(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = normalizeYAML(x)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[fmt.Sprint(k)] = normalizeYAML(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalizeYAML(x)
		}
		return out
	default:
		return v
	}
}

func describeIntent(x IntentExpect) string {
	var parts []string
	if x.Kind != "" {
		parts = append(parts, x.Kind)
	} else {
		parts = append(parts, "an intent")
	}
	if x.Command != "" {
		parts = append(parts, fmt.Sprintf("command %q", x.Command))
	}
	if x.Payload != nil {
		b, _ := json.Marshal(normalizeYAML(x.Payload))
		parts = append(parts, "payload "+string(b))
	}
	if x.AlarmKey != "" {
		parts = append(parts, fmt.Sprintf("alarm key %q", x.AlarmKey))
	}
	if x.Connector != "" {
		parts = append(parts, fmt.Sprintf("connector %q", x.Connector))
	}
	if x.URL != "" {
		parts = append(parts, "url "+x.URL)
	}
	if x.Body != "" {
		parts = append(parts, fmt.Sprintf("body %q", x.Body))
	}
	if x.BodyContains != "" {
		parts = append(parts, fmt.Sprintf("body containing %q", x.BodyContains))
	}
	if x.Edge != "" {
		parts = append(parts, "on a "+x.Edge+" edge")
	}
	return strings.Join(parts, ", ") + " from " + who(x.Rule, x.Device)
}

func judgeIntent(x IntentExpect, never bool, res Result, start time.Time, duration time.Duration) Outcome {
	lo, hi := x.Window.bounds(start, duration)
	var in []Intent
	for _, it := range res.Intents {
		if matchIntent(x, it, lo, hi) {
			in = append(in, it)
		}
	}
	desc := describeIntent(x)
	if never {
		name := "never " + desc
		if len(in) > 0 {
			return Outcome{Name: name, Message: fmt.Sprintf("expected no %s %s, but %d matched, first at %s on %s",
				desc, describeWindow(x.Window, lo, hi), len(in), in[0].At.UTC().Format(time.RFC3339), in[0].Series)}
		}
		return Outcome{Name: name, Passed: true}
	}
	pass := len(in) > 0
	name := "emits " + desc
	want := "at least once"
	if x.Count != nil {
		pass = len(in) == *x.Count
		name += fmt.Sprintf(" %d time(s)", *x.Count)
		want = fmt.Sprintf("exactly %d time(s)", *x.Count)
	}
	if pass {
		return Outcome{Name: name, Passed: true}
	}
	msg := fmt.Sprintf("expected %s to be emitted %s, %s; %d matched", desc, want, describeWindow(x.Window, lo, hi), len(in))
	if len(res.Intents) == 0 {
		msg += "; the evaluation emitted no intent at all"
	} else {
		msg += "; the intents emitted were: " + summarizeIntents(res.Intents, 5)
	}
	return Outcome{Name: name, Message: msg}
}

func summarizeIntents(intents []Intent, max int) string {
	var parts []string
	for i, it := range intents {
		if i == max {
			parts = append(parts, fmt.Sprintf("... and %d more", len(intents)-max))
			break
		}
		s := it.Kind
		switch it.Kind {
		case "sendCommand":
			s += fmt.Sprintf(" %q", it.Command)
		case "raiseAlarm", "clearAlarm":
			s += fmt.Sprintf(" %q", it.AlarmKey)
		case "publish":
			s += fmt.Sprintf(" %q", it.Connector)
		case "httpCall":
			s += " " + it.URL
		}
		parts = append(parts, fmt.Sprintf("%s (rule %s, device %s)", s, it.Rule, it.Series))
	}
	return strings.Join(parts, "; ")
}
