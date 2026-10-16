// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package isolationsweep is the judge of scoped memberships (ADR-081) that is generated
// from the SERVED SCHEMA rather than from a list: it calls every query, mutation and
// subscription a schema declares as a member limited to one customer, against a fixture
// holding that customer's rows and another customer's, and fails when any answer carries
// the other customer's data. A new operation enters the sweep the moment it enters the
// schema; nobody has to remember to add it.
//
// It complements, and does not replace, the two gates that make a scoped caller safe by
// construction — auth.Authorize refusing every scoped caller, and core/rdb's scope callback
// refusing every model without a rule. Those cannot see a resolver that gates on nothing,
// a raw read, a cache, or a field resolver that reaches past its parent's rows; this can,
// because it judges the ANSWER.
//
// 🔴 ITS NEGATIVE CONTROL IS NOT OPTIONAL. The same queries run UNSCOPED must surface the
// hidden customer's data at least once, or the fixture holds nothing to leak and every
// scoped assertion passed vacuously. Run refuses to report clean in that case.
//
// How an operation is called: every required argument is filled with a value of its type
// — every String and ID-like scalar with the hidden customer's device token (Config.Probe),
// so a by-token read is aimed straight at a row the caller must not see — and the
// selection is every argument-free scalar field to a bounded depth. Each scoped call is
// then made a SECOND time with a token that names nothing, and the two answers must be
// identical once the probe is substituted: a row the caller may not see has to be
// indistinguishable from one that does not exist (the NOT_FOUND contract), and comparing
// the two answers is what lets the probe's echo in an answer pass while the hidden row's
// own data does not. A subscription is judged the same way, on everything it answers
// within a short wait — its subscribe-time error included. Every operation that has an
// optional argument is called a second time with EVERY argument and input field filled
// (its "(all arguments)" document), so a path that only an optional filter reaches — a
// stream's originator, a search's by-device filter — is aimed at the hidden row too.
// That reaches every operation, though not every path through one; the per-service
// tests of each opted-in read cover the arguments that matter to it.
package isolationsweep

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/introspection"
)

// Config is one service's sweep.
type Config struct {
	// Schema is the service's served schema, parsed with its real root resolver.
	Schema *gqlcore.Schema
	// Scoped is a request context for a member limited to the visible customer, built as
	// the transports build it (tenant + verified claims carrying the scope), with any
	// providers the resolvers need.
	Scoped context.Context
	// Unscoped is the same request context for an unscoped member with the same
	// authorities: the negative control.
	Unscoped context.Context
	// Probe is the value every required String argument is filled with: the token of a
	// row the scoped caller must not see.
	Probe string
	// Hidden are strings that may appear in an answer only if the hidden customer's data
	// reached it — its customer token, its devices' tokens, its alarms' keys.
	Hidden []string
	// SkipMutations leaves mutations out of the scoped run, for a fixture a scoped write
	// could damage. A service that opts any write in to scoped callers must not set it.
	SkipMutations bool
	// SubscriptionWait is how long a subscription is read for its first answer (default
	// 300ms). A stream that answers nothing in that time leaked nothing in it.
	SubscriptionWait time.Duration
	// Literals names an argument or input field, by name, whose value the sweep cannot
	// guess and the all-arguments document needs to get past a discriminator — an entity
	// type beside a token (originatorType: "device"). The value is a GraphQL literal.
	// With no entry the probe is used; the operation is reached either way, but the path
	// behind the discriminator is not.
	Literals map[string]string
}

// Reporter is the slice of testing.TB the sweep reports through (*testing.T satisfies it).
// Fatalf must not return.
type Reporter interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// Outcome is how one operation answered the scoped caller.
type Outcome struct {
	Operation string // "query devices", "mutation clearAlarm", ...
	Codes     []string
	Data      bool // answered with data and no error
	Leak      string
}

// Run sweeps every operation of cfg.Schema and fails t on any leak, or when the negative
// control saw nothing to leak. It returns the scoped outcomes for a caller that wants to
// assert more (an operation that must be FORBIDDEN, one that must answer data).
func Run(t Reporter, cfg Config) []Outcome {
	t.Helper()
	if cfg.Probe == "" || len(cfg.Hidden) == 0 {
		t.Fatalf("isolationsweep: a sweep with no probe or no hidden markers asserts nothing")
	}
	if cfg.SubscriptionWait == 0 {
		cfg.SubscriptionWait = 300 * time.Millisecond
	}
	schema := cfg.Schema.Inspect()
	ops := operations(schema, placeholder, cfg.Literals)
	if len(ops) == 0 {
		t.Fatalf("isolationsweep: the schema declares no operations, so the sweep reached nothing")
	}

	// The negative control: unscoped, the queries must surface the hidden customer.
	controlSaw := ""
	for _, op := range ops {
		if op.kind != "query" {
			continue
		}
		resp := cfg.Schema.Exec(cfg.Unscoped, op.with(cfg.Probe), "", nil)
		if m := markerIn(answerText(resp), cfg.Hidden); m != "" {
			controlSaw = op.name + " (" + m + ")"
			break
		}
	}
	if controlSaw == "" {
		t.Fatalf("isolationsweep: NEGATIVE CONTROL FAILED — no query answered an unscoped caller with "+
			"any of %v, so the fixture holds nothing a scoped caller could leak and the sweep would pass "+
			"vacuously", cfg.Hidden)
	}

	outcomes := make([]Outcome, 0, len(ops))
	run := func(kinds ...string) {
		for _, op := range ops {
			if !contains(kinds, op.kind) {
				continue
			}
			var o Outcome
			if op.kind == "subscription" {
				o = subscribeOnce(cfg, op)
			} else {
				o = judge(op, cfg)
			}
			if o.Leak != "" {
				t.Errorf("isolationsweep: %s answered a scoped caller with %q (codes %v)", o.Operation, o.Leak, o.Codes)
			}
			outcomes = append(outcomes, o)
		}
	}
	// Reads first, then streams, then writes, so a scoped write that did land cannot
	// change what the reads were judged against.
	run("query")
	run("subscription")
	if !cfg.SkipMutations {
		run("mutation")
	}
	return outcomes
}

// Find returns the outcome of the named operation ("query devices").
func Find(outcomes []Outcome, operation string) (Outcome, bool) {
	for _, o := range outcomes {
		if o.Operation == operation {
			return o, true
		}
	}
	return Outcome{}, false
}

type operation struct {
	kind, name, document string
}

// placeholder stands for the probe in a built document; with substitutes a token for it.
const placeholder = "isolationsweep-probe"

// missing is the token that names nothing, the probe's twin.
const missing = "isolationsweep-missing"

func (op operation) with(token string) string {
	return strings.ReplaceAll(op.document, placeholder, token)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// operations builds one document per root field of every operation type with its required
// arguments, and a second, "(all arguments)", for every field whose arguments or input
// fields include an optional one.
func operations(s *introspection.Schema, probe string, lits map[string]string) []operation {
	var out []operation
	for _, root := range []struct {
		kind string
		typ  *introspection.Type
	}{{"query", s.QueryType()}, {"mutation", s.MutationType()}, {"subscription", s.SubscriptionType()}} {
		if root.typ == nil {
			continue
		}
		for _, f := range fieldsOf(root.typ) {
			if f.Name() == "__typename" {
				continue
			}
			document := func(all bool) string {
				var args []string
				for _, a := range f.Args(&struct{ IncludeDeprecated bool }{true}) {
					if a.Type().Kind() != "NON_NULL" && !all {
						continue
					}
					args = append(args, a.Name()+": "+literal(a.Name(), a.Type(), probe, 0, all, lits))
				}
				doc := root.kind + " { " + f.Name()
				if len(args) > 0 {
					doc += "(" + strings.Join(args, ", ") + ")"
				}
				return doc + selection(f.Type(), 0) + " }"
			}
			required, all := document(false), document(true)
			out = append(out, operation{kind: root.kind, name: root.kind + " " + f.Name(), document: required})
			if all != required {
				out = append(out, operation{kind: root.kind, name: root.kind + " " + f.Name() + " (all arguments)", document: all})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func fieldsOf(t *introspection.Type) []*introspection.Field {
	fs := t.Fields(&struct{ IncludeDeprecated bool }{true})
	if fs == nil {
		return nil
	}
	return *fs
}

func named(t *introspection.Type) *introspection.Type {
	for t != nil && (t.Kind() == "NON_NULL" || t.Kind() == "LIST") {
		t = t.OfType()
	}
	return t
}

// literal renders a value of t, for the argument or input field called name, as a GraphQL
// literal. With all set, an input object's optional fields are filled as well as its
// required ones; a name in lits takes the literal given for it.
func literal(name string, t *introspection.Type, probe string, depth int, all bool, lits map[string]string) string {
	switch t.Kind() {
	case "NON_NULL":
		return literal(name, t.OfType(), probe, depth, all, lits)
	case "LIST":
		return "[" + literal(name, t.OfType(), probe, depth, all, lits) + "]"
	case "ENUM":
		vals := t.EnumValues(&struct{ IncludeDeprecated bool }{true})
		if vals == nil || len(*vals) == 0 {
			return "null"
		}
		return (*vals)[0].Name()
	case "INPUT_OBJECT":
		if depth > 4 {
			return "{}"
		}
		var parts []string
		if fs := t.InputFields(&struct{ IncludeDeprecated bool }{true}); fs != nil {
			for _, f := range *fs {
				if f.Type().Kind() == "NON_NULL" || all {
					parts = append(parts, f.Name()+": "+literal(f.Name(), f.Type(), probe, depth+1, all, lits))
				}
			}
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	if lit, ok := lits[name]; ok {
		return lit
	}
	switch *t.Name() {
	case "Int":
		// The first page; any other Int large enough that a required page size returns
		// every fixture row, and small enough to stay under every page and lookup bound.
		if name == "pageNumber" {
			return "1"
		}
		return "50"
	case "Float":
		return "1.0"
	case "Boolean":
		return "true"
	case "ID":
		return `"1"`
	}
	b, _ := json.Marshal(probe)
	return string(b)
}

// selection selects every argument-free leaf of t, and argument-free object fields to a
// bounded depth.
func selection(t *introspection.Type, depth int) string {
	n := named(t)
	switch n.Kind() {
	case "SCALAR", "ENUM":
		return ""
	case "UNION":
		return " { __typename }"
	}
	var parts []string
	for _, f := range fieldsOf(n) {
		if len(requiredArgs(f)) > 0 {
			continue
		}
		fn := named(f.Type())
		switch fn.Kind() {
		case "SCALAR", "ENUM":
			parts = append(parts, f.Name())
		default:
			if depth < 2 {
				parts = append(parts, f.Name()+selection(f.Type(), depth+1))
			}
		}
	}
	parts = append(parts, "__typename")
	return " { " + strings.Join(parts, " ") + " }"
}

func requiredArgs(f *introspection.Field) []*introspection.InputValue {
	var out []*introspection.InputValue
	for _, a := range f.Args(&struct{ IncludeDeprecated bool }{true}) {
		if a.Type().Kind() == "NON_NULL" {
			out = append(out, a)
		}
	}
	return out
}

// answerText is the whole of what a caller learns from a response: its data, and each
// error's message and extensions.
func answerText(resp *graphql.Response) string {
	if resp == nil {
		return ""
	}
	type wireError struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions,omitempty"`
	}
	errs := make([]wireError, 0, len(resp.Errors))
	for _, e := range resp.Errors {
		errs = append(errs, wireError{e.Message, e.Extensions})
	}
	b, _ := json.Marshal(struct {
		Data   json.RawMessage `json:"data"`
		Errors []wireError     `json:"errors"`
	}{resp.Data, errs})
	return string(b)
}

func markerIn(text string, markers []string) string {
	for _, m := range markers {
		if m != "" && strings.Contains(text, m) {
			return m
		}
	}
	return ""
}

func codesOf(resp *graphql.Response) []string {
	var codes []string
	for _, e := range resp.Errors {
		code, _ := e.Extensions["code"].(string)
		if code == "" {
			code = "-"
		}
		codes = append(codes, code)
	}
	return codes
}

// judge calls op as the scoped caller with the probe and with the missing token, and
// reports a leak when the answers differ or the answer names a hidden marker.
func judge(op operation, cfg Config) Outcome {
	hidden := cfg.Schema.Exec(cfg.Scoped, op.with(cfg.Probe), "", nil)
	absent := cfg.Schema.Exec(cfg.Scoped, op.with(missing), "", nil)
	o := Outcome{Operation: op.name, Codes: codesOf(hidden)}
	o.Data = len(hidden.Errors) == 0 && len(hidden.Data) > 0 && string(hidden.Data) != "null"
	seen := strings.ReplaceAll(answerText(hidden), cfg.Probe, missing)
	if seen != answerText(absent) {
		o.Leak = "an answer for the hidden row that differs from the answer for a missing one: " + seen
		return o
	}
	o.Leak = markerIn(seen, cfg.Hidden)
	return o
}

// subscribeOnce judges a subscription exactly as judge judges a query: it subscribes as
// the scoped caller with the probe and again with the missing token, and the two
// transcripts — a subscribe-time error, or whatever each stream answered within the
// wait — must be identical once the probe is substituted, and must name no hidden
// marker. A stream that refuses a hidden device differently from a missing one is an
// existence oracle just as a query is, and its subscribe-time error is as much of its
// answer as a response: a subscription judged on the probe alone, with its error text
// never read, would pass a resolver that answers FORBIDDEN for the hidden device and
// NOT_FOUND for a missing one.
func subscribeOnce(cfg Config, op operation) Outcome {
	hidden := subscribeTranscript(cfg, op.with(cfg.Probe))
	absent := subscribeTranscript(cfg, op.with(missing))
	o := Outcome{Operation: op.name, Codes: hidden.codes, Data: hidden.data}
	seen := strings.ReplaceAll(hidden.text, cfg.Probe, missing)
	if seen != absent.text {
		o.Leak = "a stream answer for the hidden row that differs from the answer for a missing one: " + seen
		return o
	}
	o.Leak = markerIn(seen, cfg.Hidden)
	return o
}

// transcript is what one subscription answered within the wait.
type transcript struct {
	codes []string
	data  bool
	text  string
}

// subscribeTranscript subscribes with doc as the scoped caller and records everything
// the stream answers within the wait, the subscribe-time error included.
func subscribeTranscript(cfg Config, doc string) transcript {
	ctx, cancel := context.WithCancel(cfg.Scoped)
	defer cancel()
	ch, err := cfg.Schema.Subscribe(ctx, doc, "", nil)
	if err != nil {
		msg := fmt.Sprintf("subscribe: %v", err)
		return transcript{codes: []string{msg}, text: msg}
	}
	var tr transcript
	var text strings.Builder
	timer := time.NewTimer(cfg.SubscriptionWait)
	defer timer.Stop()
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				tr.text = text.String()
				return tr
			}
			if r, isResp := v.(*graphql.Response); isResp {
				tr.codes = append(tr.codes, codesOf(r)...)
				tr.data = tr.data || (len(r.Errors) == 0 && len(r.Data) > 0)
				text.WriteString(answerText(r))
				text.WriteByte('\n')
			}
		case <-timer.C:
			tr.text = text.String()
			return tr
		}
	}
}
