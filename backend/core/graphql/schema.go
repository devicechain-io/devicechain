// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"math"

	"github.com/devicechain-io/dc-microservice/credential"
	graphql "github.com/graph-gophers/graphql-go"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
	"github.com/graph-gophers/graphql-go/introspection"
)

// Schema is a parsed GraphQL schema that REFUSES an operation selecting more root fields
// than the platform allows, before any resolver runs.
//
// 🔑 IT IS A TYPE, NOT A CHECK SOMEONE HAS TO REMEMBER. The only way to obtain one is
// MustParseSchema, every handler constructor in this package takes one, and the
// graphql-go schema it wraps is unexported — so no service can build an HTTP or
// WebSocket handler over a schema whose Exec skips the limit. A guard that looked for
// the raw call would be a list to keep in step with the code; the type makes the
// distinction instead.
//
// WHY ROOT FIELDS. graphql-go executes the root fields of a MUTATION one after another,
// and an alias makes one field into as many as the document names. Before this limit,
// one unauthenticated request carrying ~1,500 aliased `login` mutations ran 1,500
// database lookups, bcrypt compares and audit writes, serially, bounded only by the raw
// query length. The byte ceiling cannot bound that: 100KB holds the whole attack.
//
// NESTED WORK IS BOUNDED BY THE COST COUNT, applied in the same place. Root fields alone
// left aliases of a NESTED field untouched — a few hundred aliases of an expensive child
// under one page of results ran once per alias per row. Every operation is also refused
// above a cost ceiling (every field costs 1, multiplied by the page size of each page it
// sits under; cost.go) and above a ceiling on how many names one field is selected under
// at one level (maxFieldAliases). cost.go says what the count does not see.
//
// 🔴 EXEC IS ALSO WHERE AN INTEGRITY REFUSAL (a taken unique value, a refused reference,
// a refused value) GETS ITS CODE, for the same reason the limit lives here: it is the
// only execution path, so every service's resolver errors pass through it (see
// answerIntegrity). Subscribe does not do it itself; the one production caller of
// Subscribe, the WebSocket pump, answers integrity refusals on each response it
// forwards, and the pump is the only place those responses are typed.
type Schema struct {
	inner *graphql.Schema
	// capabilitySchema is the exact served SDL, available only to authenticated
	// capability discovery. Developer introspection stays disabled by default.
	capabilitySchema string

	maxQueryLength   int
	maxQueryRoots    int
	maxMutationRoots int
	// maxCost and maxAliases are the cost and alias ceilings, and model the served
	// schema as the cost count reads it (which fields serve pages).
	maxCost    int64
	maxAliases int
	model      *costModel
	// maxCredChecks is the credential-check budget every execution carries (see
	// execContext).
	maxCredChecks int
}

// execContext is the context every execution runs under: the caller's, carrying a
// credential-check budget of maxCredChecks.
//
// 🔴 THIS IS WHERE THE LOGIN BACKSTOP IS INSTALLED, AND WHY HERE. Exec and Subscribe are
// the only ways to execute a document against a Schema, and a Schema is the only thing
// the handler constructors accept, so no resolver can reach credential.Checker from a
// GraphQL request without this budget in its context. The root-field limit reads the
// document; this does not, so it still holds for any document the root-field count
// misreads. If some construction path other than MustParseSchema leaves maxCredChecks
// at zero, every check is refused rather than unlimited.
func (s *Schema) execContext(ctx context.Context) context.Context {
	return credential.WithRequestBudget(ctx, s.maxCredChecks)
}

// Exec checks the document against the work limit and, when it passes, executes it
// exactly as graphql-go's Schema.Exec would. A document over the limit gets a
// request-level error and no data; no resolver runs.
func (s *Schema) Exec(ctx context.Context, query, operationName string, variables map[string]any) *graphql.Response {
	if err := s.checkWork(query, variables); err != nil {
		return &graphql.Response{Errors: []*gqlerrors.QueryError{err}}
	}
	resp := s.inner.Exec(s.execContext(ctx), query, operationName, variables)
	answerIntegrity(resp.Errors)
	return resp
}

// Subscribe checks the document against the work limit before handing it to
// graphql-go's Subscribe. graphql-go runs a query or mutation handed to Subscribe to
// completion inside the call, so the limit is decided by the OPERATION TYPE in the
// document, never by which entry point received it. The WebSocket transport refuses
// anything but a subscription before it gets here; this is the second line behind that,
// for any caller that does not. A refused document arrives as one response carrying the
// error on an already-closed channel, the same shape graphql-go uses for its own request
// errors. Unlike Exec it does not answer integrity refusals on what it returns: its caller reads
// the channel and does (see the Schema doc).
func (s *Schema) Subscribe(ctx context.Context, query, operationName string, variables map[string]any) (<-chan any, error) {
	if err := s.checkWork(query, variables); err != nil {
		ch := make(chan any, 1)
		ch <- &graphql.Response{Errors: []*gqlerrors.QueryError{err}}
		close(ch)
		return ch, nil
	}
	return s.inner.Subscribe(s.execContext(ctx), query, operationName, variables)
}

// ValidateWithVariables validates a document against the schema without executing it.
// It does not apply the work limit: nothing runs, so there is no work to bound.
func (s *Schema) ValidateWithVariables(query string, variables map[string]any) []*gqlerrors.QueryError {
	return s.inner.ValidateWithVariables(query, variables)
}

// Inspect exposes the schema's introspection view (used by tests that walk the served
// mutation set). It executes nothing.
func (s *Schema) Inspect() *introspection.Schema {
	return s.inner.Inspect()
}

// checkWork applies the work ceilings with this schema's resolved limits and cost
// model, and counts a refusal by the ceiling that made it.
func (s *Schema) checkWork(query string, variables map[string]any) *gqlerrors.QueryError {
	qerr := checkWorkWith(query, variables, s.model, s.limits())
	if qerr != nil {
		observeRefusal(qerr)
	}
	return qerr
}

func (s *Schema) limits() workLimits {
	return workLimits{
		maxLen: s.maxQueryLength, maxQueryRoots: s.maxQueryRoots, maxMutationRoots: s.maxMutationRoots,
		maxCost: s.maxCost, maxAliases: s.maxAliases,
	}
}

// CheckWork reports whether a document, with these variables, is within this schema's
// work ceilings, exactly as Exec would decide it. It is for the client-document sweeps
// that hold the served SDL: parse it with MustParseSchema (a nil resolver is enough) and
// every document a first-party client sends is checked against the same cost model the
// server uses.
func (s *Schema) CheckWork(query string, variables map[string]any) error {
	if err := checkWorkWith(query, variables, s.model, s.limits()); err != nil {
		return err
	}
	return nil
}

// CheckWork reports whether a document is within the work ceilings a service resolves
// from its environment. It is exported for the client-document sweeps, which assert
// that every document a first-party client sends passes it: that turns "no legitimate
// client exceeds the cap" into a standing test rather than a number in prose.
//
// It holds no schema, so its cost count reads only the page sizes the document writes:
// a page whose size comes from a variable, or is left out, is counted as one row. A
// sweep that holds the served SDL should use Schema.CheckWork, which counts what the
// server counts.
func CheckWork(query string) error {
	if err := checkWorkWith(query, nil, nil, envLimits()); err != nil {
		return err
	}
	return nil
}

// envLimits are the work ceilings a service resolves from its environment.
func envLimits() workLimits {
	return workLimits{
		maxLen: maxQueryLength(), maxQueryRoots: maxQueryRootFields(), maxMutationRoots: maxMutationRootFields(),
		maxCost: int64(maxQueryCost()), maxAliases: maxFieldAliasesLimit(),
	}
}

// workLimitCode is the extensions code a document refused by the root-field ceiling
// carries, so a client can recognize the refusal without matching the message text.
const workLimitCode = "TOO_MANY_ROOT_FIELDS"

// workLimits are the ceilings checkWorkWith applies.
type workLimits struct {
	maxLen, maxQueryRoots, maxMutationRoots int
	maxCost                                 int64
	maxAliases                              int
}

// checkWork applies the length and root-field ceilings only, with no cost or alias
// ceiling. It is what the root-field tests (and the fuzzers that hold the reader to
// graphql-go) drive, so their verdicts stay about the root count alone; the serving
// path is checkWorkWith.
func checkWork(query string, maxLen, maxQueryRoots, maxMutationRoots int) *gqlerrors.QueryError {
	return checkWorkWith(query, nil, nil, workLimits{
		maxLen: maxLen, maxQueryRoots: maxQueryRoots, maxMutationRoots: maxMutationRoots,
		maxCost: costSaturation, maxAliases: math.MaxInt,
	})
}

// checkWorkWith is the limit itself.
//
// 🔴 THE LENGTH CEILING IS CHECKED FIRST, BEFORE ANYTHING IS READ (readDocument does
// both, in that order). graphql-go applies its own MaxQueryLength inside Exec — which
// runs AFTER this. Reading first would let an unauthenticated caller make this function
// walk a body-sized (4 MiB) document that the old code refused by length alone: a new
// amplifier added by the fix for an old one. The message matches graphql-go's own so a
// client sees one wording either way.
//
// 🔴 THE FIELDS ARE COUNTED BY A READER THAT TOKENISES EXACTLY AS graphql-go DOES
// (readRootFields), not by a general GraphQL parser. The count is only a limit if it
// counts what graphql-go then executes, and a conformant parser reads some documents
// differently from graphql-go's text/scanner-based lexer — comments, raw strings and
// block-string escapes are read differently — which is enough to hide any number of
// extra root fields from the count. Those lexemes are refused by the reader outright;
// readRootFields says how and why.
//
// Every operation in the document is counted, not only the one operationName selects.
// That is the simpler rule and the fail-closed one: a document cannot carry an
// oversized operation past the check by naming a different one, and a legitimate client
// sends one operation per document anyway.
//
// A document the reader cannot read is refused. graphql-go would refuse nearly all of
// them too, and where it would not, refusing is the direction that cannot run an
// uncounted document.
//
// After the root fields, each operation is collected as execution collects it
// (normalize, under its own bound on selections visited), and the collected tree is
// costed (treeCost) and checked for the most names one field is selected under at one
// level (treeAliases).
func checkWorkWith(query string, vars map[string]any, model *costModel, lim workLimits) *gqlerrors.QueryError {
	ops, fragments, qerr := readDocument(query, lim.maxLen)
	if qerr != nil {
		return qerr
	}
	for _, op := range ops {
		limit := -1
		switch op.kind {
		case opMutation:
			limit = lim.maxMutationRoots
		case opQuery:
			limit = lim.maxQueryRoots
		}
		// A subscription has no root-field ceiling here: graphql-go's own validation
		// limits it to ONE root field, which is stricter than anything set here.
		if limit >= 0 {
			if n := countRootKeys(op.roots, fragments); n > limit {
				return &gqlerrors.QueryError{
					Message: fmt.Sprintf("%s %s selects %d root fields; the maximum is %d",
						op.kind, displayName(op.name), n, limit),
					Extensions: map[string]any{"code": workLimitCode},
				}
			}
		}
		nodes, ok := normalize(op, fragments, model, vars, maxSelectionVisits)
		if !ok {
			return selectionsRefusal(op.kind, op.name)
		}
		if cost := treeCost(nodes); cost > lim.maxCost {
			return costRefusal(op.kind, op.name, cost, lim.maxCost)
		}
		if n := treeAliases(nodes); n > lim.maxAliases {
			return aliasRefusal(op.kind, op.name, n, lim.maxAliases)
		}
	}
	return nil
}
