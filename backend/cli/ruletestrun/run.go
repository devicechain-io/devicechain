// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package ruletestrun is the engine of `dcctl test`: it takes the scenarios and test files read by
// core/ruletest, has the platform evaluate each scenario's rules over the events it generates, and
// judges the result against the test files' expectations.
//
// The platform does the evaluating (the evaluateRules query on the event-processing service), not
// dcctl, for the one reason that matters: a test of a rule is only as good as its agreement with the
// engine that runs the rule. dcctl holds no copy of the detection logic, so it cannot drift from it.
// What dcctl does hold is the part that has no business on a server: generating events from a seed,
// and deciding whether a result is the one a test expected.
//
// A scenario is evaluated once however many test files use it, and the evaluation executes nothing:
// the alarms, commands and webhooks in the result are what the rules ASK for, never what happened.
package ruletestrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/ruletest"
)

// Client is the GraphQL seam. *userclient.TenantSession satisfies it, which is what carries the
// caller's own tenant access token: the server authorizes the evaluation as the person who ran it.
type Client interface {
	Query(ctx context.Context, baseURL, query string, variables map[string]any, out any) error
}

// defaultBodyLimit is the server's default request-body ceiling (DC_GRAPHQL_MAX_BODY_BYTES).
const defaultBodyLimit = 4 << 20

// bodyHeadroom is left under the limit for the query text and the JSON envelope around the variables.
const bodyHeadroom = 64 << 10

const evaluateQuery = `query($input:EvaluateRulesInput!){evaluateRules(input:$input){` +
	`ok degraded skippedRules diagnostics{severity message nodeId} ` +
	`edges{occurredAt series rule kind signal} ` +
	`intents{occurredAt series rule signal kind alarmKey command payload connector url body}}}`

// Options configures a run.
type Options struct {
	Client   Client
	Endpoint string
	// Out receives the progress report. Nil discards it.
	Out io.Writer
}

type wireResult struct {
	Ok           bool     `json:"ok"`
	Degraded     *string  `json:"degraded"`
	SkippedRules []string `json:"skippedRules"`
	Diagnostics  []struct {
		Severity string  `json:"severity"`
		Message  string  `json:"message"`
		NodeID   *string `json:"nodeId"`
	} `json:"diagnostics"`
	Edges []struct {
		OccurredAt string `json:"occurredAt"`
		Series     string `json:"series"`
		Rule       string `json:"rule"`
		Kind       string `json:"kind"`
		Signal     string `json:"signal"`
	} `json:"edges"`
	Intents []struct {
		OccurredAt string  `json:"occurredAt"`
		Series     string  `json:"series"`
		Rule       string  `json:"rule"`
		Signal     string  `json:"signal"`
		Kind       string  `json:"kind"`
		AlarmKey   *string `json:"alarmKey"`
		Command    *string `json:"command"`
		Payload    *string `json:"payload"`
		Connector  *string `json:"connector"`
		URL        *string `json:"url"`
		Body       *string `json:"body"`
	} `json:"intents"`
}

// evaluated is one scenario's outcome, shared by every suite that uses it.
type evaluated struct {
	result  ruletest.Result
	skipped []string
	err     error
	took    time.Duration
}

// Run evaluates every scenario the plan needs and judges every suite. It returns one Case per
// expectation, and one Case in error for each suite that could not be judged. It returns an error
// only for a cancelled context.
func Run(ctx context.Context, plan *ruletest.Plan, opt Options) ([]ruletest.Case, error) {
	out := opt.Out
	if out == nil {
		out = io.Discard
	}
	evals := map[string]*evaluated{}
	var paths []string
	for p, l := range plan.Scenarios {
		if l.Err == nil {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	// Only scenarios some loadable suite refers to are evaluated.
	needed := map[string]bool{}
	for _, s := range plan.Suites {
		if s.Err == nil {
			needed[s.ScenarioPath] = true
		}
	}
	for _, p := range paths {
		if !needed[p] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		l := plan.Scenarios[p]
		started := time.Now()
		ev := evaluate(ctx, opt, l)
		ev.took = time.Since(started)
		evals[p] = ev
		if ev.err == nil {
			fmt.Fprintf(out, "evaluated %s: %d events, %d edges, %d intents in %s\n", p,
				len(l.Events.Events), len(ev.result.Edges), len(ev.result.Intents), ev.took.Round(time.Millisecond))
			for _, s := range ev.skipped {
				fmt.Fprintf(out, "  warning: a published rule was not evaluated: %s\n", s)
			}
		}
	}

	var cases []ruletest.Case
	for _, s := range plan.Suites {
		switch {
		case s.Err != nil:
			cases = append(cases, ruletest.Case{Suite: s.Name, Name: "load", Error: s.Err.Error()})
			fmt.Fprintf(out, "ERROR %s: %v\n", s.Name, s.Err)
		case evals[s.ScenarioPath].err != nil:
			err := evals[s.ScenarioPath].err
			cases = append(cases, ruletest.Case{Suite: s.Name, Name: "evaluate", Error: err.Error()})
			fmt.Fprintf(out, "ERROR %s: %v\n", s.Name, err)
		default:
			ev := evals[s.ScenarioPath]
			l := plan.Scenarios[s.ScenarioPath]
			for _, o := range ruletest.Judge(s.Test, ev.result, l.Scenario.Start, l.Scenario.Duration.D()) {
				c := ruletest.Case{Suite: s.Name, Name: o.Name, Took: ev.took}
				if o.Passed {
					fmt.Fprintf(out, "PASS  %s: %s\n", s.Name, o.Name)
				} else {
					c.Failure = o.Message
					fmt.Fprintf(out, "FAIL  %s: %s\n        %s\n", s.Name, o.Name, o.Message)
				}
				cases = append(cases, c)
			}
		}
	}
	return cases, nil
}

// Summary counts the cases.
func Summary(cases []ruletest.Case) (passed, failed, errored int) {
	for _, c := range cases {
		switch {
		case c.Error != "":
			errored++
		case c.Failure != "":
			failed++
		default:
			passed++
		}
	}
	return
}

func evaluate(ctx context.Context, opt Options, l *ruletest.Loaded) *evaluated {
	sc := l.Scenario
	events := make([]map[string]any, len(l.Events.Events))
	for i, e := range l.Events.Events {
		ms := make([]map[string]any, len(e.Metrics))
		for j, m := range e.Metrics {
			ms[j] = map[string]any{"name": m.Name, "value": m.Value}
		}
		events[i] = map[string]any{
			"deviceToken": e.Device, "occurredAt": e.At.UTC().Format(time.RFC3339Nano), "measurements": ms,
		}
	}
	input := map[string]any{
		"profileToken": sc.Profile, "ruleSet": sc.RuleSet, "events": events,
		"advanceTo": l.Events.AdvanceTo.UTC().Format(time.RFC3339Nano),
	}
	if sc.Draft != nil {
		if sc.Draft.Graph != "" {
			input["graph"] = l.Draft
		} else {
			input["ruleDefinition"] = l.Draft
		}
	}
	vars := map[string]any{"input": input}
	body, err := json.Marshal(vars)
	if err != nil {
		return &evaluated{err: err}
	}
	if len(body) > defaultBodyLimit-bodyHeadroom {
		return &evaluated{err: fmt.Errorf("scenario %s generates %d events, a request of %d bytes, which is over the %d the server accepts "+
			"by default (DC_GRAPHQL_MAX_BODY_BYTES); lengthen `every` or shorten `duration`, or split the scenario",
			l.Path, len(events), len(body), defaultBodyLimit)}
	}

	var resp struct {
		Eval wireResult `json:"evaluateRules"`
	}
	if err := opt.Client.Query(ctx, opt.Endpoint, evaluateQuery, vars, &resp); err != nil {
		return &evaluated{err: fmt.Errorf("evaluating %s: %w", l.Path, err)}
	}
	w := resp.Eval
	if !w.Ok {
		var msgs []string
		for _, d := range w.Diagnostics {
			m := d.Message
			if d.NodeID != nil {
				m = "node " + *d.NodeID + ": " + m
			}
			msgs = append(msgs, m)
		}
		return &evaluated{err: fmt.Errorf("the platform rejected scenario %s: %s", l.Path, strings.Join(msgs, "; "))}
	}
	res, err := toResult(w)
	if err != nil {
		return &evaluated{err: fmt.Errorf("evaluating %s: %w", l.Path, err)}
	}
	return &evaluated{result: res, skipped: w.SkippedRules}
}

func toResult(w wireResult) (ruletest.Result, error) {
	var res ruletest.Result
	if w.Degraded != nil {
		res.Degraded = *w.Degraded
	}
	parse := func(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }
	for _, e := range w.Edges {
		at, err := parse(e.OccurredAt)
		if err != nil {
			return res, fmt.Errorf("the platform returned an edge time %q that is not RFC 3339", e.OccurredAt)
		}
		res.Edges = append(res.Edges, ruletest.Edge{At: at, Series: e.Series, Rule: e.Rule, Kind: e.Kind, Raise: e.Signal == "raised"})
	}
	for _, i := range w.Intents {
		at, err := parse(i.OccurredAt)
		if err != nil {
			return res, fmt.Errorf("the platform returned an intent time %q that is not RFC 3339", i.OccurredAt)
		}
		in := ruletest.Intent{At: at, Series: i.Series, Rule: i.Rule, Raise: i.Signal == "raised", Kind: i.Kind}
		if i.AlarmKey != nil {
			in.AlarmKey = *i.AlarmKey
		}
		if i.Command != nil {
			in.Command = *i.Command
		}
		if i.Payload != nil {
			in.Payload = *i.Payload
		}
		if i.Connector != nil {
			in.Connector = *i.Connector
		}
		if i.URL != nil {
			in.URL = *i.URL
		}
		if i.Body != nil {
			in.Body = *i.Body
		}
		res.Intents = append(res.Intents, in)
	}
	return res, nil
}

// WriteJUnit writes cases to path, creating its directory is the caller's business.
func WriteJUnit(path string, cases []ruletest.Case) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := ruletest.WriteJUnit(f, cases); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ErrFailed is returned by the command when any expectation failed or any suite errored.
var ErrFailed = errors.New("tests failed")

// Describe renders the one-line summary.
func Describe(passed, failed, errored int) string {
	return strconv.Itoa(passed) + " passed, " + strconv.Itoa(failed) + " failed, " + strconv.Itoa(errored) + " errored"
}
