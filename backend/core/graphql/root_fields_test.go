// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lexSDL is the schema the tokenisation tests run against. Every ROOT resolver counts
// its call into the counter the context carries; Obj's field does not, so a nested
// selection can never be mistaken for a root one.
//
// The custom scalar Any accepts every parsed literal, so scalar validation cannot
// hide a lexical disagreement. Before v1.11.1 it even let graphql-go execute `-x`;
// the upstream parser now refuses that before scalar validation.
const lexSDL = `
	schema { query: Query mutation: Mutation }
	scalar Any
	type Query { ping: Int! q(s: [String], i: Int): Int! obj: Obj! }
	type Obj { x: Int! }
	type Mutation { bump(s: [String], a: Any, l: [Any]): Int! m(s: String, n: Int, a: Any): Int! }
`

// lexAny is the Any scalar: it accepts whatever literal it is handed.
type lexAny struct{}

func (lexAny) ImplementsGraphQLType(name string) bool { return name == "Any" }
func (*lexAny) UnmarshalGraphQL(any) error            { return nil }

// silentLogger drops graphql-go's recovered-panic reports during fuzzing. Before
// v1.11.1 a minus before a string or brace reached deserialisation and panicked.
type silentLogger struct{}

func (silentLogger) LogPanic(context.Context, any) {}

// lexInner is the graphql-go schema over lexSDL, without the work limit.
func lexInner() *graphql.Schema {
	return graphql.MustParseSchema(lexSDL, &lexRoot{}, graphql.Logger(silentLogger{}))
}

type lexCounter struct{}

type lexRoot struct{}

func lexCount(ctx context.Context) int32 {
	if c, ok := ctx.Value(lexCounter{}).(*atomic.Int32); ok {
		return c.Add(1)
	}
	return 0
}

func (lexRoot) Ping(ctx context.Context) int32 { return lexCount(ctx) }
func (lexRoot) Q(ctx context.Context, _ struct {
	S *[]*string
	I *int32
}) int32 {
	return lexCount(ctx)
}
func (lexRoot) Obj(ctx context.Context) *lexObj { lexCount(ctx); return &lexObj{} }
func (lexRoot) Bump(ctx context.Context, _ struct {
	S *[]*string
	A *lexAny
	L *[]*lexAny
}) int32 {
	return lexCount(ctx)
}
func (lexRoot) M(ctx context.Context, _ struct {
	S *string
	N *int32
	A *lexAny
}) int32 {
	return lexCount(ctx)
}

type lexObj struct{}

func (lexObj) X() int32 { return 1 }

// lexSchema is a Schema over lexSDL with both root-field ceilings at limit.
func lexSchema(limit int) *Schema {
	return &Schema{
		inner:            lexInner(),
		maxQueryLength:   DefaultGraphQLMaxQueryLength,
		maxQueryRoots:    limit,
		maxMutationRoots: limit,
		maxCost:          DefaultGraphQLMaxQueryCost,
		maxAliases:       DefaultGraphQLMaxFieldAliases,
	}
}

// execCounting runs exec and reports how many root resolvers it called.
func execCounting(exec func(context.Context) *graphql.Response) (*graphql.Response, int32) {
	var calls atomic.Int32
	resp := exec(context.WithValue(context.Background(), lexCounter{}, &calls))
	return resp, calls.Load()
}

// tokenisationBypasses are historical different-tokenisation attacks. v1.11.1
// closes the numeric and escaped block-quote gaps; our subset still refuses every
// document. rawRoots is the exact number of roots upstream now executes (zero means
// upstream also refuses). reason pins a syntax refusal before any field counting.
var tokenisationBypasses = []struct {
	name, doc, reason string
	rawRoots          int32
}{
	// v1.11.0 closed at the escaped quote and executed three fields. v1.11.1
	// honours the escape and executes one field; we retain the established refusal.
	{"escaped block-string quote", `mutation { a: bump(s: ["""X\""" ]) b: bump(s: ["y"]) c: bump(s: ["\""" """ ]) }`, "is not accepted", 1},
	{"escaped block-string quote, bare", `mutation { a: bump(s: """ \""" ) b: bump c: bump(s: """ """) }`, "is not accepted", 0},
	{"block comment", "mutation { a: bump /* } */ b: bump c: bump }", "is not accepted", 3},
	{"quote in block comment", `mutation { a: bump /* " */ b: bump /* " */ }`, "is not accepted", 2},
	{"line comment", "mutation { a: bump // }\n b: bump c: bump }", "is not accepted", 3},
	// Go raw strings are still refused locally; v1.11.1 also refuses the minus.
	{"raw string", "mutation { a: bump(s: -`}`) b: bump c: bump }", "is not accepted", 0},
	// v1.11.1 consumeNumber no longer swallows a structural token after '-'.
	{"minus swallows a brace", "mutation { a: bump(s: [-}]) b: bump }", "expected number", 0},
}

func TestTokenisationBypassesAreRefused(t *testing.T) {
	schema := lexSchema(1)
	for _, tc := range tokenisationBypasses {
		t.Run(tc.name, func(t *testing.T) {
			resp, calls := execCounting(func(ctx context.Context) *graphql.Response {
				return schema.Exec(ctx, tc.doc, "", nil)
			})
			require.Len(t, resp.Errors, 1, "%v", resp.Errors)
			assert.Nil(t, resp.Errors[0].Extensions, "%s", resp.Errors[0].Message)
			assert.Contains(t, resp.Errors[0].Message, tc.reason)
			assert.Nil(t, resp.Data)
			assert.Equal(t, int32(0), calls, "no resolver may run for a refused document")

			raw, rawCalls := execCounting(func(ctx context.Context) *graphql.Response {
				return schema.inner.Exec(ctx, tc.doc, "", nil)
			})
			if tc.rawRoots == 0 {
				require.NotEmpty(t, raw.Errors, "upstream also refuses this syntax")
				assert.Nil(t, raw.Data)
			} else {
				require.Empty(t, raw.Errors)
			}
			assert.Equal(t, tc.rawRoots, rawCalls)
		})
	}
}

// The counterweight: the same tokens, where graphql-go reads them as ONE root field
// and the reader reads them at all, are not refused at a limit of 1. A counter that
// refused everything containing a comment or a block string would pass the test above
// and break real clients. (TestRefusedLexemesAreStillAcceptedInsideStringsAndComments
// is the same counterweight for the refused lexemes.)
func TestTokenisationLookalikesStillRun(t *testing.T) {
	schema := lexSchema(1)
	for _, doc := range []string{
		"mutation { a: bump # b: bump\n }",
		`mutation { a: bump(s: """ b: bump c: bump """) }`,
		`"""a description""" mutation { a: bump }`,
		"query { obj { x a: x b: x } }",
	} {
		resp, calls := execCounting(func(ctx context.Context) *graphql.Response {
			return schema.Exec(ctx, doc, "", nil)
		})
		require.Empty(t, resp.Errors, "%s", doc)
		assert.Equal(t, int32(1), calls, "%s", doc)
	}
}

// 🔴 THE DIFFERENTIAL PROPERTY, WITH graphql-go AS THE ARBITER: whenever the work limit
// lets a document through at limit L, graphql-go makes at most L root resolver calls
// executing it. The counter may refuse more than it needs to; it may never count fewer
// root fields than graphql-go runs.
//
// Run it with `go test -run=^$ -fuzz=FuzzRootFieldLimit -fuzztime=120s ./graphql/`.
// Plain `go test` runs the seed corpus below.
func FuzzRootFieldLimit(f *testing.F) {
	for _, tc := range tokenisationBypasses {
		f.Add(tc.doc, "", uint8(0))
	}
	for _, doc := range lexemeFuzzSeeds {
		f.Add(doc, "", uint8(0))
	}
	for _, seed := range []string{
		`mutation { a: bump b: bump }`,
		`mutation { bump bump bump }`,
		`mutation { ...F } fragment F on Mutation { a: bump b: bump }`,
		`mutation { ... on Mutation { a: bump b: bump } }`,
		`mutation { ... @include(if: true) { a: bump b: bump } }`,
		`mutation { ...F } fragment F on Mutation { ...G a: bump } fragment G on Mutation { b: bump ...F }`,
		`mutation A { bump } mutation B { a: bump b: bump }`,
		`query { a: ping b: q(s: ["x"], i: 1) c: obj { x } }`,
		`{ ping }`,
		`query ($v: [String] = ["a", """b"""]) { a: q(s: $v) b: ping }`,
		`mutation { a: m(s: "é", n: -1) b: m(s: "\"") }`,
		`mutation { a: m(s: "\u{1F600}") b: bump }`,
		`mutation { a: m(s: "😀") b: bump }`,
		"mutation { a: bump(s: [\"#\"]) # }\n b: bump }",
		`mutation { a: bump(s: ["""a""" """b"""]) b: bump }`,
		`mutation { a: m(s: """""") b: bump }`,
		`"""d""" mutation { a: bump b: bump }`,
		"mutation { a: bump ,,, b: bump }",
		"mutation { a: bump(s: [-{]) b: bump }",
		"mutation { a: m(s: 'x') b: bump }",
		"mutation {a:bump b:bump c:bump d:bump e:bump f:bump}",
	} {
		f.Add(seed, "", uint8(0))
		f.Add(seed, "A", uint8(1))
		f.Add(seed, "B", uint8(2))
	}

	inner := lexInner()
	f.Fuzz(func(t *testing.T, doc, operationName string, l uint8) {
		limit := int(l%4) + 1
		if checkWork(doc, 1<<16, limit, limit) != nil {
			return
		}
		var calls atomic.Int32
		ctx := context.WithValue(context.Background(), lexCounter{}, &calls)
		func() {
			// A panic inside graphql-go's parser is graphql-go's to fix, and runs
			// nothing; the resolver count below is still the verdict.
			defer func() { _ = recover() }()
			inner.Exec(ctx, doc, operationName, nil)
		}()
		if n := calls.Load(); int(n) > limit {
			t.Fatalf("the work limit passed a document at limit %d, and graphql-go made %d root "+
				"resolver calls executing it (operation %q):\n%s", limit, n, operationName, doc)
		}
	})
}

// Sanity for the property above: a document over the limit really does make the fuzz
// body's resolvers count past it when the limit is bypassed, so a quiet fuzz run is the
// counter holding and not a harness whose counter never moves.
func TestFuzzHarnessCountsRootCalls(t *testing.T) {
	inner := lexInner()
	doc := aliased("mutation", "bump", 4)
	_, calls := execCounting(func(ctx context.Context) *graphql.Response {
		return inner.Exec(ctx, doc, "", nil)
	})
	require.Equal(t, int32(4), calls)
	require.NotNil(t, checkWork(doc, 1<<16, 3, 3), "and the limit refuses it")
	require.True(t, strings.HasPrefix(fmt.Sprint(checkWork(doc, 1<<16, 3, 3)), "graphql: mutation (anonymous) selects 4"))
}
