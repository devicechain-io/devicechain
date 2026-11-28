// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	graphql "github.com/graph-gophers/graphql-go"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// costSDL serves one page field of each shape the cost count recognizes: a page sized
// by an input object (items), a list sized by an optional argument (recent), a lookup
// by a list of tokens (byToken), a nested page (Item.children), and a field that takes
// an argument NAMED like a page size but returns no list (echo), which must not be
// multiplied.
const costSDL = `
schema { query: Query }
input PaginationInput { pageNumber: Int! pageSize: Int! }
input ItemCriteria { pageNumber: Int! pageSize: Int! }
input LooseCriteria { pageSize: Int = 25 }
type ItemPage { results: [Item!]! }
type Echo { token: String! }
type Item {
	token: String!
	children(pagination: PaginationInput!): ItemPage!
	recentChildren(limit: Int = 5): [Item!]!
}
type Query {
	items(criteria: ItemCriteria!): ItemPage!
	recent(limit: Int): [Item!]!
	byToken(tokens: [String!]!): [Item!]!
	echo(limit: Int!): Echo!
	loose(criteria: LooseCriteria!): ItemPage!
	ping: Boolean!
}`

// costRoot resolves costSDL, counting every children call: the resolver an aliased
// nested field multiplies.
type costRoot struct{ children *atomic.Int64 }

type costItem struct {
	token    string
	children *atomic.Int64
}

type costPage struct{ items []*costItem }

func (p *costPage) Results() []*costItem { return p.items }

func (i *costItem) Token() string { return i.token }

func (i *costItem) Children(args struct {
	Pagination struct{ PageNumber, PageSize int32 }
}) *costPage {
	i.children.Add(1)
	return &costPage{}
}

func (r *costRoot) page(n int32) *costPage {
	p := &costPage{}
	for k := int32(0); k < n; k++ {
		p.items = append(p.items, &costItem{token: fmt.Sprint(k), children: r.children})
	}
	return p
}

func (r *costRoot) Items(args struct {
	Criteria struct{ PageNumber, PageSize int32 }
}) *costPage {
	return r.page(args.Criteria.PageSize)
}
func (r *costRoot) Recent(args struct{ Limit *int32 }) []*costItem { return nil }
func (r *costRoot) ByToken(args struct{ Tokens []string }) []*costItem {
	return nil
}
func (r *costRoot) Echo(args struct{ Limit int32 }) *costItem {
	return &costItem{children: r.children}
}
func (r *costRoot) Loose(args struct{ Criteria struct{ PageSize int32 } }) *costPage {
	return &costPage{}
}
func (r *costRoot) Ping() bool { return true }

func (i *costItem) RecentChildren(args struct{ Limit int32 }) []*costItem { return nil }

func costSchema(t *testing.T) (*Schema, *atomic.Int64) {
	t.Helper()
	n := &atomic.Int64{}
	return MustParseSchema(costSDL, &costRoot{children: n}), n
}

// costOf is the cost of a document's only operation against costSDL.
func costOf(t *testing.T, s *Schema, doc string, vars map[string]any) int64 {
	t.Helper()
	ops, fragments, qerr := readDocument(doc, DefaultGraphQLMaxQueryLength)
	require.Nil(t, qerr)
	require.Len(t, ops, 1)
	return operationCost(ops[0], fragments, s.model, vars)
}

// decoded turns a JSON object into variables exactly as the HTTP handler decodes them.
func decoded(t *testing.T, js string) map[string]any {
	t.Helper()
	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(js), &v))
	return v
}

func TestCostCountsEveryFieldTimesTheRowsAbove(t *testing.T) {
	s, _ := costSchema(t)
	const page10 = `query { items(criteria: {pageNumber: 1, pageSize: 10}) { results { token } } }`
	cases := []struct {
		name string
		doc  string
		vars map[string]any
		want int64
	}{
		// items 1 + 10 rows × (results 1 + token 1).
		{"a literal page size", page10, nil, 21},
		{"a page size from a variable", `query($n: Int!) { items(criteria: {pageNumber: 1, pageSize: $n}) { results { token } } }`,
			decoded(t, `{"n": 10}`), 21},
		{"a page size inside an input-object variable", `query($c: ItemCriteria!) { items(criteria: $c) { results { token } } }`,
			decoded(t, `{"c": {"pageNumber": 1, "pageSize": 10}}`), 21},
		{"an unsupplied variable counts the largest page", `query($c: ItemCriteria!) { items(criteria: $c) { results { token } } }`,
			nil, 1 + maxPageSize*2},
		{"a variable's declared default", `query($n: Int = 10) { items(criteria: {pageNumber: 1, pageSize: $n}) { results { token } } }`,
			nil, 21},
		{"a page size above the largest page counts the largest page", `query { items(criteria: {pageNumber: 1, pageSize: 50000}) { results { token } } }`,
			nil, 1 + maxPageSize*2},
		{"a page size below 1 counts the largest page", `query { items(criteria: {pageNumber: 1, pageSize: 0}) { results { token } } }`,
			nil, 1 + maxPageSize*2},
		{"an omitted optional size counts the largest page", `query { recent { token } }`, nil, 1 + maxPageSize},
		{"a written optional size", `query { recent(limit: 5) { token } }`, nil, 6},
		{"a lookup costs its list's length", `query { byToken(tokens: ["a", "b", "c"]) { token } }`, nil, 4},
		{"a lookup list from a variable", `query($t: [String!]!) { byToken(tokens: $t) { token } }`,
			decoded(t, `{"t": ["a", "b", "c", "d"]}`), 5},
		{"an unsupplied lookup list counts the most keys", `query($t: [String!]!) { byToken(tokens: $t) { token } }`, nil, 1 + maxPageSize},
		{"a size-named argument on a field that returns no list multiplies nothing", `query { echo(limit: 900) { token } }`, nil, 2},
		// items 1 + 10 × (results 1 + 2 aliases × (children 1 + 3 × (results 1 + token 1))).
		{"nested pages multiply", `query { items(criteria: {pageNumber: 1, pageSize: 10}) { results {
			a: children(pagination: {pageNumber: 1, pageSize: 3}) { results { token } }
			b: children(pagination: {pageNumber: 1, pageSize: 3}) { results { token } } } } }`, nil, 1 + 10*(1+2*(1+3*2))},
		{"a fragment costs as though written inline", `query { items(criteria: {pageNumber: 1, pageSize: 10}) { results { ...F } } }
			fragment F on Item { token }`, nil, 21},
		{"an inline fragment costs as though written inline", `query { items(criteria: {pageNumber: 1, pageSize: 10}) { results { ... on Item { token } } } }`, nil, 21},
		{"root fields alone cost one each", `query { ping a: ping b: ping }`, nil, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, costOf(t, s, tc.doc, tc.vars))
		})
	}
}

func TestCostCeilingAdmitsNAndRefusesNPlusOne(t *testing.T) {
	s, _ := costSchema(t)
	lim := s.limits()
	lim.maxCost = 21
	const atN = `query { items(criteria: {pageNumber: 1, pageSize: 10}) { results { token } } }`
	const overN = `query { ping items(criteria: {pageNumber: 1, pageSize: 10}) { results { token } } }`
	require.Equal(t, int64(21), costOf(t, s, atN, nil))
	require.Equal(t, int64(22), costOf(t, s, overN, nil))

	assert.Nil(t, checkWorkWith(atN, nil, s.model, lim), "a document costing exactly the ceiling is admitted")
	qerr := checkWorkWith(overN, nil, s.model, lim)
	require.NotNil(t, qerr, "one over the ceiling is refused")
	assert.Equal(t, costLimitCode, qerr.Extensions["code"])
	assert.Contains(t, qerr.Message, "has a cost of 22; the maximum is 21")
}

func TestCostCeilingCountsVariableSizedPages(t *testing.T) {
	s, _ := costSchema(t)
	lim := s.limits()
	lim.maxCost = 21
	const doc = `query($c: ItemCriteria!) { items(criteria: $c) { results { token } } }`
	assert.Nil(t, checkWorkWith(doc, decoded(t, `{"c": {"pageNumber": 1, "pageSize": 10}}`), s.model, lim))
	qerr := checkWorkWith(doc, decoded(t, `{"c": {"pageNumber": 1, "pageSize": 11}}`), s.model, lim)
	require.NotNil(t, qerr, "the page size the variables name is the one counted")
	assert.Equal(t, costLimitCode, qerr.Extensions["code"])
}

// The cost count must not change what a root-only operation is told: those are bounded
// by the root-field ceiling, and the count adds one per field.
func TestRootOnlyOperationsAreUnaffectedByTheCostCeiling(t *testing.T) {
	s, _ := costSchema(t)
	var b strings.Builder
	b.WriteString("query {")
	for i := 0; i < DefaultGraphQLMaxQueryRootFields; i++ {
		fmt.Fprintf(&b, " p%d: ping", i)
	}
	b.WriteString(" }")
	assert.Nil(t, checkWorkWith(b.String(), nil, s.model, s.limits()),
		"a query at the root-field ceiling is admitted by every other ceiling too")
	qerr := checkWorkWith(strings.Replace(b.String(), "query {", "query { extra: ping", 1), nil, s.model, s.limits())
	require.NotNil(t, qerr)
	assert.Equal(t, workLimitCode, qerr.Extensions["code"], "and one more root field is still the root ceiling's refusal")
}

func TestAnAliasedNestedFieldIsRefusedBeforeAnyResolverRuns(t *testing.T) {
	s, children := costSchema(t)
	var b strings.Builder
	b.WriteString("query { items(criteria: {pageNumber: 1, pageSize: 100}) { results {")
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&b, " c%d: children(pagination:{pageNumber:1,pageSize:1}){results{token}}", i)
	}
	require.Less(t, b.Len(), DefaultGraphQLMaxQueryLength, "inside every ceiling that existed before")
	b.WriteString(" } } }")
	before := testutil.ToFloat64(workRefusals.WithLabelValues("cost"))
	resp := s.Exec(context.Background(), b.String(), "", nil)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, costLimitCode, resp.Errors[0].Extensions["code"])
	assert.Nil(t, resp.Data)
	assert.Zero(t, children.Load(), "no nested resolver ran")
	assert.Equal(t, before+1, testutil.ToFloat64(workRefusals.WithLabelValues("cost")), "and the refusal is counted")
}

// The serving path reads the request's variables: the same document is admitted with
// a small page and refused with a large one.
func TestExecCountsThePageSizeTheVariablesName(t *testing.T) {
	t.Setenv(EnvGraphQLMaxQueryCost, "500")
	s, children := costSchema(t)
	const doc = `query($n: Int!) { items(criteria: {pageNumber: 1, pageSize: $n}) { results { token children(pagination: {pageNumber: 1, pageSize: 1}) { results { token } } } } }`
	resp := s.Exec(context.Background(), doc, "", map[string]any{"n": float64(10)})
	require.Empty(t, resp.Errors)
	assert.Equal(t, int64(10), children.Load())

	children.Store(0)
	resp = s.Exec(context.Background(), doc, "", map[string]any{"n": float64(200)})
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, costLimitCode, resp.Errors[0].Extensions["code"])
	assert.Zero(t, children.Load())

	sub, err := s.Subscribe(context.Background(), doc, "", map[string]any{"n": float64(200)})
	require.NoError(t, err)
	got, ok := (<-sub).(*graphql.Response)
	require.True(t, ok)
	require.Len(t, got.Errors, 1)
	assert.Equal(t, costLimitCode, got.Errors[0].Extensions["code"], "Subscribe applies the same count")
}

func TestCostSaturatesRatherThanWrapping(t *testing.T) {
	s, _ := costSchema(t)
	doc := `query { items(criteria: {pageNumber: 1, pageSize: 1000}) { results { token } } }`
	for i := 0; i < 7; i++ {
		doc = strings.Replace(doc, "results { token }", "results { token children(pagination: {pageNumber: 1, pageSize: 1000}) { results { token } } }", 1)
	}
	lim := s.limits()
	lim.maxCost = 1_000_000_000_000_000_000
	assert.NotNil(t, checkWorkWith(doc, nil, s.model, lim), "1000^8 rows is refused, not wrapped round to a small number")
}

// checkQuickly runs checkWorkWith and fails the test if it does not answer promptly: the
// collection must be bounded by the document, not by the tree its fragments describe.
func checkQuickly(t *testing.T, doc string, s *Schema, lim workLimits) *gqlerrors.QueryError {
	t.Helper()
	done := make(chan *gqlerrors.QueryError, 1)
	start := time.Now()
	go func() { done <- checkWorkWith(doc, nil, s.model, lim) }()
	select {
	case qerr := <-done:
		assert.Less(t, time.Since(start), 2*time.Second)
		return qerr
	case <-time.After(10 * time.Second):
		t.Fatal("the collection did not finish")
		return nil
	}
}

func TestFragmentCollectionIsBounded(t *testing.T) {
	s, _ := costSchema(t)

	t.Run("a fragment spread twice at one level is expanded once", func(t *testing.T) {
		// Fragment k spreads fragment k+1 twice: walked path by path, 2^40 visits.
		var b strings.Builder
		b.WriteString("query { items(criteria: {pageNumber: 1, pageSize: 1}) { results { ...F0 } } }\n")
		for k := 0; k < 40; k++ {
			fmt.Fprintf(&b, "fragment F%d on Item { a%d: token ...F%d ...F%d }\n", k, k, k+1, k+1)
		}
		b.WriteString("fragment F40 on Item { token }\n")
		lim := s.limits()
		lim.maxAliases = 100 // the 41 names of token are not what this case is about
		assert.Nil(t, checkQuickly(t, b.String(), s, lim))
		// items 1 + 1 × (results 1 + 40 aliased tokens + token).
		assert.Equal(t, int64(1+1+41), costOf(t, s, b.String(), nil))
	})

	t.Run("a zero-cost fragment graph ending in an undefined fragment", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("query { ping ...F0 }\n")
		for k := 0; k < 40; k++ {
			fmt.Fprintf(&b, "fragment F%d on Query { ...F%d ...F%d }\n", k, k+1, k+1)
		}
		b.WriteString("fragment F40 on Query { ...Missing }\n")
		assert.Nil(t, checkQuickly(t, b.String(), s, s.limits()), "it costs 1; validation refuses the undefined fragment")
	})

	t.Run("a fragment graph that really expands is refused at the visit bound", func(t *testing.T) {
		// Each fragment selects the next under two names: 2^40 distinct fields.
		var b strings.Builder
		b.WriteString("query { items(criteria: {pageNumber: 1, pageSize: 1}) { results { ...F0 } } }\n")
		for k := 0; k < 40; k++ {
			fmt.Fprintf(&b, "fragment F%d on Item { a: recentChildren(limit: 1) { ...F%d } b: recentChildren(limit: 1) { ...F%d } }\n", k, k+1, k+1)
		}
		b.WriteString("fragment F40 on Item { token }\n")
		lim := s.limits()
		lim.maxCost = costSaturation - 1
		qerr := checkQuickly(t, b.String(), s, lim)
		require.NotNil(t, qerr, "refused with no cost ceiling to speak of")
		assert.Equal(t, costLimitCode, qerr.Extensions["code"])
		assert.Contains(t, qerr.Message, "expands to more than")
	})

	t.Run("a cycle ends", func(t *testing.T) {
		cyclic := `query { items(criteria: {pageNumber: 1, pageSize: 1}) { results { ...A } } }
			fragment A on Item { token ...B }
			fragment B on Item { token ...A }`
		assert.Equal(t, int64(1+1*(1+1)), costOf(t, s, cyclic, nil), "validation refuses the document")
	})
}

func TestOmittedSizesTakeTheSchemaDefault(t *testing.T) {
	s, _ := costSchema(t)
	cases := []struct {
		name string
		doc  string
		vars map[string]any
		want int64
	}{
		// items 1 + 1 × (results 1 + recentChildren 1 + 5 × token 1).
		{"an omitted argument with a default", `query { items(criteria: {pageNumber: 1, pageSize: 1}) { results { recentChildren { token } } } }`, nil, 1 + 1*(1+1+5)},
		{"an unsupplied variable for it", `query($l: Int) { items(criteria: {pageNumber: 1, pageSize: 1}) { results { recentChildren(limit: $l) { token } } } }`, nil, 1 + 1*(1+1+5)},
		{"an explicit size still wins", `query { items(criteria: {pageNumber: 1, pageSize: 1}) { results { recentChildren(limit: 9) { token } } } }`, nil, 1 + 1*(1+1+9)},
		// loose 1 + 25 × (results 1 + token 1).
		{"an omitted input field with a default", `query { loose(criteria: {}) { results { token } } }`, nil, 1 + 25*2},
		{"an input-object variable without it", `query($c: LooseCriteria!) { loose(criteria: $c) { results { token } } }`, decoded(t, `{"c": {}}`), 1 + 25*2},
		{"an input-object variable with it null", `query($c: LooseCriteria!) { loose(criteria: $c) { results { token } } }`, decoded(t, `{"c": {"pageSize": null}}`), 1 + 25*2},
		{"no default is the largest page", `query { recent { token } }`, nil, 1 + maxPageSize},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, costOf(t, s, tc.doc, tc.vars))
		})
	}
}

func TestSkipAndIncludeAreHonoured(t *testing.T) {
	s, _ := costSchema(t)
	const fragment = "\nfragment F on Item { recentChildren(limit: 9) { token } }"
	// items 1 + 10 × (results 1 + token 1) with nothing else running; recentChildren
	// adds 10 × (1 + 9 × 1) when it does.
	const none, with = int64(1 + 10*2), int64(1 + 10*2 + 10*(1+9))
	cases := []struct {
		name, vars, sel string
		values          map[string]any
		want            int64
	}{
		{"a field skipped by a literal", "", "recentChildren(limit: 9) @skip(if: true) { token }", nil, none},
		{"a field included by a literal", "", "recentChildren(limit: 9) @include(if: true) { token }", nil, with},
		{"a field left out by a literal include", "", "recentChildren(limit: 9) @include(if: false) { token }", nil, none},
		{"a spread skipped by a variable", "($s: Boolean!)", "...F @skip(if: $s)", map[string]any{"s": true}, none},
		{"a spread run by a variable", "($s: Boolean!)", "...F @skip(if: $s)", map[string]any{"s": false}, with},
		{"a spread left out by a literal include", "", "...F @include(if: false)", nil, none},
		{"an inline fragment left out by a variable", "($i: Boolean!)", "... on Item @include(if: $i) { recentChildren(limit: 9) { token } }", map[string]any{"i": false}, none},
		{"an inline fragment skipped by a literal", "", "... @skip(if: true) { recentChildren(limit: 9) { token } }", nil, none},
		{"a variable's default", "($s: Boolean = true)", "recentChildren(limit: 9) @skip(if: $s) { token }", nil, none},
		{"a supplied value over the default", "($s: Boolean = true)", "recentChildren(limit: 9) @skip(if: $s) { token }", map[string]any{"s": false}, with},
		{"an unsettled condition counts as running", "($s: Boolean!)", "recentChildren(limit: 9) @skip(if: $s) { token }", nil, with},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := "query" + tc.vars + " { items(criteria: {pageNumber: 1, pageSize: 10}) { results { token " + tc.sel + " } } }"
			if strings.Contains(tc.sel, "...F") {
				doc += fragment
			}
			assert.Equal(t, tc.want, costOf(t, s, doc, tc.values))
		})
	}

	t.Run("a skipped alias is not counted by the alias ceiling", func(t *testing.T) {
		lim := s.limits()
		lim.maxAliases = 1
		doc := `query { items(criteria: {pageNumber: 1, pageSize: 1}) { results { a: token b: token @skip(if: true) } } }`
		assert.Nil(t, checkWorkWith(doc, nil, s.model, lim))
	})
}

func TestFieldsThatExecutionMergesAreCostedOnce(t *testing.T) {
	s, _ := costSchema(t)
	one := costOf(t, s, `query { items(criteria: {pageNumber: 1, pageSize: 10}) { results { recentChildren(limit: 9) { token } } } }`, nil)
	three := costOf(t, s, `query { items(criteria: {pageNumber: 1, pageSize: 10}) { results { ...A ...B ...C } } }
		fragment A on Item { recentChildren(limit: 9) { token } }
		fragment B on Item { recentChildren(limit: 9) { token } }
		fragment C on Item { recentChildren(limit: 9) { token } }`, nil)
	assert.Equal(t, one, three, "three fragments selecting one field run it once")
	twice := costOf(t, s, `query { items(criteria: {pageNumber: 1, pageSize: 10}) { results {
		recentChildren(limit: 9) { token } recentChildren(limit: 9) { token } } } }`, nil)
	assert.Equal(t, one, twice, "one field written twice runs once")
	named := costOf(t, s, `query { items(criteria: {pageNumber: 1, pageSize: 10}) { results {
		a: recentChildren(limit: 9) { token } b: recentChildren(limit: 9) { token } } } }`, nil)
	assert.Greater(t, named, one, "under two names it runs twice")
	argued := costOf(t, s, `query { items(criteria: {pageNumber: 1, pageSize: 10}) { results {
		recentChildren(limit: 9) { token } recentChildren(limit: 8) { token } } } }`, nil)
	assert.Greater(t, argued, one, "with different arguments it is not merged (and validation refuses it)")
}

// The served schema, exactly: the review's examples against device-management's SDL.
func TestServedSchemaDefaultsSkipsAndMerges(t *testing.T) {
	sdl, err := os.ReadFile("../../services/device-management/graphql/schema.graphql")
	require.NoError(t, err)
	s := MustParseSchema(string(sdl), nil)
	cost := func(doc string) int64 {
		t.Helper()
		ops, fragments, qerr := readDocument(doc, DefaultGraphQLMaxQueryLength)
		require.Nil(t, qerr)
		return operationCost(ops[0], fragments, s.model, nil)
	}
	const defaulted = `query { alarmSuppressionWindows(criteria: {pageNumber: 1, pageSize: 50}) { results { openings { start end } } } }`
	assert.Equal(t, int64(601), cost(defaulted), "openings(limit: Int = 5) counts 5")
	assert.NoError(t, s.CheckWork(defaulted, nil))

	const single = `query { alarmSuppressionWindows(criteria: {pageNumber: 1, pageSize: 1000}) { results { openings(limit: 20) { start end } } } }`
	assert.Equal(t, int64(42001), cost(single))
	assert.Equal(t, int64(42001), cost(`query { alarmSuppressionWindows(criteria: {pageNumber: 1, pageSize: 1000}) { results {
		a: openings(limit: 20) { start end } b: openings(limit: 20) @skip(if: true) { start end } c: openings(limit: 20) @skip(if: true) { start end } } } }`))
	assert.Equal(t, int64(42001), cost(`query { alarmSuppressionWindows(criteria: {pageNumber: 1, pageSize: 1000}) { results { ...A ...B ...C } } }
		fragment A on AlarmSuppressionWindow { openings(limit: 20) { start end } }
		fragment B on AlarmSuppressionWindow { openings(limit: 20) { start end } }
		fragment C on AlarmSuppressionWindow { openings(limit: 20) { start end } }`))
}

func TestAliasCeiling(t *testing.T) {
	s, _ := costSchema(t)
	lim := s.limits()
	lim.maxAliases = 2
	cases := []struct {
		name    string
		doc     string
		refused bool
	}{
		{"two names for one field", `query { items(criteria: {pageNumber: 1, pageSize: 1}) { results { a: token b: token } } }`, false},
		{"three names for one field", `query { items(criteria: {pageNumber: 1, pageSize: 1}) { results { a: token b: token c: token } } }`, true},
		{"one name repeated is one", `query { items(criteria: {pageNumber: 1, pageSize: 1}) { results { a: token a: token a: token } } }`, false},
		{"a fragment's names join the level it is spread in", `query { items(criteria: {pageNumber: 1, pageSize: 1}) { results { a: token b: token ...F } } }
			fragment F on Item { c: token }`, true},
		{"an inline fragment's names join its level", `query { items(criteria: {pageNumber: 1, pageSize: 1}) { results { a: token b: token ... on Item { c: token } } } }`, true},
		{"fields sharing a key merge their selections", `query {
			x: items(criteria: {pageNumber: 1, pageSize: 1}) { results { a: token b: token } }
			x: items(criteria: {pageNumber: 1, pageSize: 1}) { results { c: token } } }`, true},
		{"different fields are counted apart", `query { a: ping b: ping items(criteria: {pageNumber: 1, pageSize: 1}) { results { a: token b: token } } }`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qerr := checkWorkWith(tc.doc, nil, s.model, lim)
			if !tc.refused {
				assert.Nil(t, qerr)
				return
			}
			require.NotNil(t, qerr)
			assert.Equal(t, aliasLimitCode, qerr.Extensions["code"])
		})
	}
}

func TestCostAndAliasCeilingsAreOperatorTunableButNeverOff(t *testing.T) {
	t.Setenv(EnvGraphQLMaxQueryCost, "1234")
	t.Setenv(EnvGraphQLMaxFieldAliases, "7")
	assert.Equal(t, 1234, maxQueryCost())
	assert.Equal(t, 7, maxFieldAliasesLimit())
	s, _ := costSchema(t)
	assert.Equal(t, int64(1234), s.maxCost)
	assert.Equal(t, 7, s.maxAliases)
	for _, v := range []string{"0", "-1", "lots"} {
		t.Setenv(EnvGraphQLMaxQueryCost, v)
		t.Setenv(EnvGraphQLMaxFieldAliases, v)
		assert.Equal(t, DefaultGraphQLMaxQueryCost, maxQueryCost(), v)
		assert.Equal(t, DefaultGraphQLMaxFieldAliases, maxFieldAliasesLimit(), v)
	}
}

// A Schema built without MustParseSchema has no ceilings set, and refuses rather than
// running unbounded.
func TestAZeroCostCeilingRefuses(t *testing.T) {
	s, _ := costSchema(t)
	bare := &Schema{inner: s.inner, maxQueryLength: DefaultGraphQLMaxQueryLength, maxQueryRoots: 5, maxMutationRoots: 5}
	resp := bare.Exec(context.Background(), `query { ping }`, "", nil)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, costLimitCode, resp.Errors[0].Extensions["code"])
}

func TestMaxPageSizeIsTheStoragePageCeiling(t *testing.T) {
	assert.Equal(t, rdb.MaxPageSize, maxPageSize)
}

// The schema-less CheckWork reads the sizes a document writes, so a sweep without the
// SDL still sees a literal nested page.
func TestSchemalessCheckWorkReadsWrittenPageSizes(t *testing.T) {
	var b strings.Builder
	b.WriteString("query { items(criteria: {pageNumber: 1, pageSize: 1000}) { results {")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, " c%d: f%d(pageSize: 1000) { x }", i, i)
	}
	b.WriteString(" } } }")
	err := CheckWork(b.String())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has a cost of")
}
