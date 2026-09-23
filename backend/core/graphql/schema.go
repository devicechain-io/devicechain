// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

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
// ⚠️ WHAT IT DOES NOT BOUND, stated so nobody reads more into it. It counts ROOT fields
// only. Aliases of a NESTED field — a thousand aliases of an expensive child under one
// root query — are untouched, and still run under graphql-go's parallelism limit. This
// bounds serial mutation work and root fan-out per request, not per-request work in
// general.
type Schema struct {
	inner *graphql.Schema

	maxQueryLength   int
	maxQueryRoots    int
	maxMutationRoots int
}

// Exec checks the document against the work limit and, when it passes, executes it
// exactly as graphql-go's Schema.Exec would. A document over the limit gets a
// request-level error and no data; no resolver runs.
func (s *Schema) Exec(ctx context.Context, query, operationName string, variables map[string]any) *graphql.Response {
	if err := s.checkWork(query); err != nil {
		return &graphql.Response{Errors: []*gqlerrors.QueryError{err}}
	}
	return s.inner.Exec(ctx, query, operationName, variables)
}

// Subscribe checks the document against the work limit before handing it to
// graphql-go's Subscribe. graphql-go runs a query or mutation handed to Subscribe to
// completion inside the call, so the limit is decided by the OPERATION TYPE in the
// document, never by which entry point received it. The WebSocket transport refuses
// anything but a subscription before it gets here; this is the second line behind that,
// for any caller that does not. A refused document arrives as one response carrying the
// error on an already-closed channel, the same shape graphql-go uses for its own request
// errors.
func (s *Schema) Subscribe(ctx context.Context, query, operationName string, variables map[string]any) (<-chan any, error) {
	if err := s.checkWork(query); err != nil {
		ch := make(chan any, 1)
		ch <- &graphql.Response{Errors: []*gqlerrors.QueryError{err}}
		close(ch)
		return ch, nil
	}
	return s.inner.Subscribe(ctx, query, operationName, variables)
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

// checkWork applies the root-field ceilings with this schema's resolved limits.
func (s *Schema) checkWork(query string) *gqlerrors.QueryError {
	return checkWork(query, s.maxQueryLength, s.maxQueryRoots, s.maxMutationRoots)
}

// CheckWork reports whether a document is within the root-field ceilings a service
// resolves from its environment. It is exported for the client-document sweeps, which
// assert that every document a first-party client sends passes it: that turns "no
// legitimate client exceeds the cap" into a standing test rather than a number in prose.
func CheckWork(query string) error {
	if err := checkWork(query, maxQueryLength(), maxQueryRootFields(), maxMutationRootFields()); err != nil {
		return err
	}
	return nil
}

// workLimitCode is the extensions code a refused document carries, so a client can
// recognize the refusal without matching the message text.
const workLimitCode = "TOO_MANY_ROOT_FIELDS"

// checkWork is the limit itself.
//
// 🔴 THE LENGTH CEILING IS CHECKED FIRST, BEFORE ANYTHING IS READ. graphql-go applies
// its own MaxQueryLength inside Exec — which runs AFTER this. Reading first would let an
// unauthenticated caller make this function walk a body-sized (4 MiB) document that the
// old code refused by length alone: a new amplifier added by the fix for an old one. The
// message matches graphql-go's own so a client sees one wording either way.
//
// 🔴 THE FIELDS ARE COUNTED BY A READER THAT TOKENISES EXACTLY AS graphql-go DOES
// (readRootFields), not by a general GraphQL parser. The count is only a limit if it
// counts what graphql-go then executes, and a conformant parser reads some documents
// differently from graphql-go's text/scanner-based lexer — comments, raw strings and
// block-string escapes are all read differently — which is enough to hide any number
// of extra root fields from the count. readRootFields says how and why.
//
// Every operation in the document is counted, not only the one operationName selects.
// That is the simpler rule and the fail-closed one: a document cannot carry an
// oversized operation past the check by naming a different one, and a legitimate client
// sends one operation per document anyway.
//
// A document the reader cannot read is refused. graphql-go would refuse nearly all of
// them too, and where it would not, refusing is the direction that cannot run an
// uncounted document.
func checkWork(query string, maxLen, maxQueryRoots, maxMutationRoots int) *gqlerrors.QueryError {
	if len(query) > maxLen {
		return gqlerrors.Errorf("query length %d exceeds the maximum allowed query length of %d bytes", len(query), maxLen)
	}
	ops, fragments, err := readRootFields(query)
	if err != nil {
		return gqlerrors.Errorf("the document could not be parsed: %s", err.Error())
	}
	for _, op := range ops {
		var limit int
		switch op.kind {
		case opMutation:
			limit = maxMutationRoots
		case opQuery:
			limit = maxQueryRoots
		default:
			// A subscription is limited to ONE root field by graphql-go's own
			// validation, which is stricter than anything set here.
			continue
		}
		n := countRootKeys(op.roots, fragments)
		if n > limit {
			name := op.name
			if name == "" {
				name = "(anonymous)"
			}
			return &gqlerrors.QueryError{
				Message: fmt.Sprintf("%s %s selects %d root fields; the maximum is %d",
					op.kind, name, n, limit),
				Extensions: map[string]any{"code": workLimitCode},
			}
		}
	}
	return nil
}
