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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/ruletest"
	"github.com/devicechain-io/dc-microservice/userclient"
)

// Client is the GraphQL seam. *userclient.TenantSession satisfies it, which is what carries the
// caller's own tenant access token: the server authorizes the evaluation as the person who ran it.
type Client interface {
	Query(ctx context.Context, baseURL, query string, variables map[string]any, out any) error
}

const evaluateQuery = `query($input:EvaluateRulesInput!){evaluateRules(input:$input){` +
	`ok refusal evaluatedRules degraded degradations{reason count} ` +
	`skippedRules{rule reason group message} diagnostics{severity message nodeId code} ` +
	`edges{occurredAt series rule kind signal} ` +
	`intents{occurredAt series rule signal kind alarmKey command payload connector url body}}}`

// busyBackoff is how long a run waits before each retry of an evaluation the server refused as
// BUSY. The server refuses rather than queues, so the client is where the waiting belongs; a run
// that is still refused after the last wait reports the refusal as an error.
var busyBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// Options configures a run.
type Options struct {
	Client   Client
	Endpoint string
	// Out receives the progress report. Nil discards it.
	Out io.Writer
	// Wait waits d or until ctx is done, returning ctx's error then. Nil waits on a timer; a test
	// substitutes one that records the waits without taking them.
	Wait func(ctx context.Context, d time.Duration) error
}

type wireResult struct {
	Ok             bool     `json:"ok"`
	Refusal        *string  `json:"refusal"`
	EvaluatedRules []string `json:"evaluatedRules"`
	Degraded       *string  `json:"degraded"`
	Degradations   []struct {
		Reason string `json:"reason"`
		Count  int    `json:"count"`
	} `json:"degradations"`
	SkippedRules []struct {
		Rule    string `json:"rule"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"skippedRules"`
	Diagnostics []struct {
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
		ev := evaluateWithRetry(ctx, opt, l, out)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ev.took = time.Since(started)
		evals[p] = ev
		if ev.err == nil {
			fmt.Fprintf(out, "evaluated %s: %d events, %d edges, %d intents in %s\n", p,
				len(l.Events.Events), len(ev.result.Edges), len(ev.result.Intents), ev.took.Round(time.Millisecond))
			for _, s := range ev.skipped {
				fmt.Fprintf(out, "  warning: a published rule was not evaluated: %s\n", s)
			}
			if ev.result.Degraded != "" {
				fmt.Fprintf(out, "  warning: the result is partial: %s\n", ev.result.Degraded)
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
				switch {
				case o.Errored:
					c.Error = o.Message
					fmt.Fprintf(out, "ERROR %s: %s\n        %s\n", s.Name, o.Name, o.Message)
				case o.Passed:
					fmt.Fprintf(out, "PASS  %s: %s\n", s.Name, o.Name)
				default:
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

// errBusy marks an evaluation the server refused as BUSY, the one refusal worth retrying.
var errBusy = errors.New("busy")

// evaluateWithRetry evaluates a scenario, waiting and retrying while the server is BUSY.
func evaluateWithRetry(ctx context.Context, opt Options, l *ruletest.Loaded, out io.Writer) *evaluated {
	wait := opt.Wait
	if wait == nil {
		wait = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	ev := evaluate(ctx, opt, l)
	for _, d := range busyBackoff {
		if !errors.Is(ev.err, errBusy) {
			return ev
		}
		fmt.Fprintf(out, "the platform is busy evaluating; retrying %s in %s\n", l.Path, d)
		if err := wait(ctx, d); err != nil {
			return &evaluated{err: err}
		}
		ev = evaluate(ctx, opt, l)
	}
	return ev
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

	// The server's request-body limit decides what is too large, not a copy of its default here:
	// an operator who raised DC_GRAPHQL_MAX_BODY_BYTES gets the larger scenarios they allowed.
	var resp struct {
		Eval *wireResult `json:"evaluateRules"`
	}
	if err := opt.Client.Query(ctx, opt.Endpoint, evaluateQuery, vars, &resp); err != nil {
		var se *userclient.StatusError
		if errors.As(err, &se) && se.StatusCode == http.StatusRequestEntityTooLarge {
			return &evaluated{err: fmt.Errorf("scenario %s generates %d events, a request larger than the server accepts "+
				"(DC_GRAPHQL_MAX_BODY_BYTES); lengthen `every` or shorten `duration`, or split the scenario", l.Path, len(events))}
		}
		return &evaluated{err: fmt.Errorf("evaluating %s: %w", l.Path, err)}
	}
	if resp.Eval == nil {
		return &evaluated{err: fmt.Errorf("evaluating %s: the platform returned no result", l.Path)}
	}
	w := *resp.Eval
	if !w.Ok {
		var msgs []string
		for _, d := range w.Diagnostics {
			m := d.Message
			if d.NodeID != nil {
				m = "node " + *d.NodeID + ": " + m
			}
			msgs = append(msgs, m)
		}
		err := fmt.Errorf("the platform rejected scenario %s: %s", l.Path, strings.Join(msgs, "; "))
		if w.Refusal != nil && *w.Refusal == "BUSY" {
			err = fmt.Errorf("%w: %w", errBusy, err)
		}
		return &evaluated{err: err}
	}
	res, err := toResult(w)
	if err != nil {
		return &evaluated{err: fmt.Errorf("evaluating %s: %w", l.Path, err)}
	}
	var skipped []string
	for _, s := range w.SkippedRules {
		skipped = append(skipped, s.Message)
	}
	return &evaluated{result: res, skipped: skipped}
}

// intentKinds maps the served EvaluatedIntentKind to the test file's vocabulary.
var intentKinds = map[string]string{
	"RAISE_ALARM": "raiseAlarm", "CLEAR_ALARM": "clearAlarm", "SEND_COMMAND": "sendCommand",
	"HTTP_CALL": "httpCall", "PUBLISH": "publish",
}

// raised maps the served EvaluatedSignal to an edge direction, refusing a value it does not know
// rather than reading it as "resolved".
func raised(signal string) (bool, error) {
	switch signal {
	case "RAISED":
		return true, nil
	case "RESOLVED":
		return false, nil
	}
	return false, fmt.Errorf("the platform returned a signal %q this dcctl does not know", signal)
}

func toResult(w wireResult) (ruletest.Result, error) {
	var res ruletest.Result
	if w.Degraded != nil {
		res.Degraded = *w.Degraded
	}
	res.Evaluated = append(res.Evaluated, w.EvaluatedRules...)
	for _, d := range w.Degradations {
		res.Degradations = append(res.Degradations, ruletest.Degradation{Reason: d.Reason, Count: d.Count})
	}
	if len(w.SkippedRules) > 0 {
		res.Skipped = map[string]string{}
		for _, s := range w.SkippedRules {
			res.Skipped[s.Rule] = s.Message
		}
	}
	parse := func(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }
	for _, e := range w.Edges {
		at, err := parse(e.OccurredAt)
		if err != nil {
			return res, fmt.Errorf("the platform returned an edge time %q that is not RFC 3339", e.OccurredAt)
		}
		up, err := raised(e.Signal)
		if err != nil {
			return res, err
		}
		res.Edges = append(res.Edges, ruletest.Edge{At: at, Series: e.Series, Rule: e.Rule, Kind: e.Kind, Raise: up})
	}
	for _, i := range w.Intents {
		at, err := parse(i.OccurredAt)
		if err != nil {
			return res, fmt.Errorf("the platform returned an intent time %q that is not RFC 3339", i.OccurredAt)
		}
		up, err := raised(i.Signal)
		if err != nil {
			return res, err
		}
		kind, ok := intentKinds[i.Kind]
		if !ok {
			return res, fmt.Errorf("the platform returned an intent kind %q this dcctl does not know", i.Kind)
		}
		in := ruletest.Intent{At: at, Series: i.Series, Rule: i.Rule, Raise: up, Kind: kind}
		for _, f := range []struct {
			src *string
			dst *string
		}{{i.AlarmKey, &in.AlarmKey}, {i.Command, &in.Command}, {i.Payload, &in.Payload},
			{i.Connector, &in.Connector}, {i.URL, &in.URL}, {i.Body, &in.Body}} {
			if f.src != nil {
				*f.dst = *f.src
			}
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

// Exit statuses of `dcctl test`.
const (
	// ExitFailed: an expectation was judged and did not hold.
	ExitFailed = 1
	// ExitErrored: something could not be judged (a file did not load, the platform refused the
	// evaluation, sign-in or the network failed, an expectation was inconclusive).
	ExitErrored = 2
)

// ExitError is an error that carries the process exit status `dcctl` exits with.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode is the status the process exits with.
func (e *ExitError) ExitCode() int { return e.Code }

// ErrFailed is the error behind ExitFailed.
var ErrFailed = errors.New("tests failed")

// ErrErrored is the error behind ExitErrored.
var ErrErrored = errors.New("tests could not be run")

// Outcome is the error `dcctl test` returns for a summary: nil when everything held, exit status 2
// when anything errored (an error outranks a failure, because the run did not finish its
// question), and exit status 1 when an expectation failed.
func Outcome(failed, errored int) error {
	switch {
	case errored > 0:
		return &ExitError{Code: ExitErrored, Err: ErrErrored}
	case failed > 0:
		return &ExitError{Code: ExitFailed, Err: ErrFailed}
	}
	return nil
}

// Errored wraps err as an exit status 2 error: the tests could not be run.
func Errored(err error) error { return &ExitError{Code: ExitErrored, Err: err} }

// Describe renders the one-line summary.
func Describe(passed, failed, errored int) string {
	return strconv.Itoa(passed) + " passed, " + strconv.Itoa(failed) + " failed, " + strconv.Itoa(errored) + " errored"
}
