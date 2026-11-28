// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	gqlerrors "github.com/graph-gophers/graphql-go/errors"
	"github.com/graph-gophers/graphql-go/introspection"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// The cost count: how much work one operation can ask for, read from the document
// before any resolver runs.
//
// WHY IT EXISTS. The root-field ceiling bounds how many resolvers an operation starts at
// its ROOT. It does not bound a nested field, and a nested field under a page of results
// runs once per row. So a document with a few hundred aliases of one nested list field,
// under one page of a hundred parents, ran tens of thousands of resolvers, each with its
// own database statements, inside a request every other ceiling admitted. The cost count
// is the bound on that shape.
//
// IT COSTS WHAT EXECUTION RUNS. The operation is first collected the way graphql-go
// collects it for execution (normalize): fragment spreads and inline fragments are
// merged into the level they sit in, a selection whose @skip or @include says it does
// not run is left out, and fields that share a response key, name and arguments are
// merged into one, their selections merged into one level below. A fragment spread
// twice at one level is expanded once, which is also what makes a cycle end. The
// collected tree is then costed:
//
//	cost(field) = 1 + rows(field) × cost(its selections)
//
// summed over the operation. rows is 1 for a field that returns one object, and for a
// list field whose length the request does not set (the count cannot know it; see
// "what it does not see").
//
// For a field the schema serves as a page (it returns a list, or an object holding one,
// and takes a page size — an argument named pageSize, limit or first, or an input
// object with a field of that name), rows is the page size the REQUEST names, read from
// a literal or from the request's variables, wherever in the arguments it sits. A size
// the request leaves out (or names by a variable it does not supply) is the schema's
// declared default for it, or maxPageSize — the most rows any served page returns — when
// the schema declares none. A size that is not a positive integer, or is above
// maxPageSize, counts as maxPageSize. For a list field that takes a list argument (a
// lookup by tokens), rows is that list's length.
//
// Above the ceiling (DC_GRAPHQL_MAX_QUERY_COST) the operation is refused before it runs,
// the same way and in the same place as the root-field ceiling (see checkWork).
//
// THE COLLECTION HAS ITS OWN BOUND (maxSelectionVisits), independent of the cost ceiling:
// a few fragments can describe a tree far larger than the document (each spreading the
// next under two names), and the collection would otherwise build all of it before the
// cost could say no. Past the bound the operation is refused the same way.
//
// ⚠️ WHAT IT DOES NOT SEE, so nobody reads more into it. A list the request cannot size —
// a device's relationships, a profile's metrics — counts as one row, because its length
// is the data's, not the request's. And a field costs 1 whatever its resolver does. The
// count bounds the multiplication a document can request; it is not a model of each
// resolver's price. The alias ceiling (maxFieldAliases) bounds the other half of the
// shape at every level, whatever the lists hold.

// maxPageSize is the most rows any page the platform serves can hold, and so the row
// count the cost count assumes when it cannot read a page size. It is rdb.MaxPageSize
// (a test holds the two equal); it is not imported because rdb's own tests import this
// package.
const maxPageSize = 1000

// maxSelectionVisits bounds how many selections collecting one operation may visit. It
// is not tunable: every first-party document visits a few thousand at most, and a
// 100KB document written out without fragments cannot reach it.
const maxSelectionVisits = 200_000

// sizeArgNames are the argument (and input-object field) names a served page takes its
// size from.
var sizeArgNames = map[string]bool{"pageSize": true, "limit": true, "first": true}

// costSaturation is where the count stops adding: far above any ceiling, and far below
// overflow, so a pathological document cannot wrap the count round to a small number.
const costSaturation = math.MaxInt64 / 4

func satAdd(a, b int64) int64 {
	if a >= costSaturation-b {
		return costSaturation
	}
	return a + b
}

func satMul(a, b int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > costSaturation/b {
		return costSaturation
	}
	return a * b
}

// fieldInfo is what the cost count needs to know about one served field.
type fieldInfo struct {
	// returns is the named type the field returns, under any list and non-null wrapping.
	returns string
	// list is whether the field returns a list.
	list bool
	// sized is whether the field serves a page: it returns a list, or an object holding
	// one, and takes a page size.
	sized bool
	// omitted is the page size of a sized field whose request names none: the schema's
	// declared default, or maxPageSize where it declares none.
	omitted int64
	// listArgs are the arguments declared as lists, for a list field that is not sized
	// (a lookup): its rows are the longest of them.
	listArgs []string
}

// costModel is the served schema, as far as the cost count reads it.
type costModel struct {
	fields map[string]map[string]fieldInfo // type name -> field name -> info
	roots  map[string]string               // operation kind -> root type name
}

// unwrap returns the named type under t's list and non-null wrapping, and whether any
// of the wrapping is a list.
func unwrap(t *introspection.Type) (name string, list bool) {
	for t != nil {
		switch t.Kind() {
		case "LIST":
			list = true
		case "NON_NULL":
		default:
			if n := t.Name(); n != nil {
				name = *n
			}
			return name, list
		}
		t = t.OfType()
	}
	return name, list
}

// declaredSize reads a schema default for a page size: a positive integer up to
// maxPageSize, or maxPageSize for anything else (no default, null, out of range).
func declaredSize(def *string) int64 {
	if def == nil {
		return maxPageSize
	}
	n, err := strconv.ParseInt(*def, 10, 64)
	if err != nil {
		return maxPageSize
	}
	return pageRows(n)
}

// buildCostModel reads the cost model out of a parsed schema.
func buildCostModel(s *introspection.Schema) *costModel {
	all := &struct{ IncludeDeprecated bool }{IncludeDeprecated: true}
	m := &costModel{fields: map[string]map[string]fieldInfo{}, roots: map[string]string{}}
	for kind, t := range map[string]*introspection.Type{
		opQuery: s.QueryType(), opMutation: s.MutationType(), opSubscription: s.SubscriptionType(),
	} {
		if t != nil && t.Name() != nil {
			m.roots[kind] = *t.Name()
		}
	}

	// First pass: which input objects carry a page size (and its default), and which
	// output types hold a list.
	inputSize := map[string]int64{}
	holdsList := map[string]bool{}
	for _, t := range s.Types() {
		if t.Name() == nil {
			continue
		}
		switch t.Kind() {
		case "INPUT_OBJECT":
			if fs := t.InputFields(all); fs != nil {
				for _, f := range *fs {
					if sizeArgNames[f.Name()] {
						inputSize[*t.Name()] = max(inputSize[*t.Name()], declaredSize(f.DefaultValue()))
					}
				}
			}
		case "OBJECT", "INTERFACE":
			if fs := t.Fields(all); fs != nil {
				for _, f := range *fs {
					if _, list := unwrap(f.Type()); list {
						holdsList[*t.Name()] = true
					}
				}
			}
		}
	}

	for _, t := range s.Types() {
		if t.Name() == nil || (t.Kind() != "OBJECT" && t.Kind() != "INTERFACE") {
			continue
		}
		fs := t.Fields(all)
		if fs == nil {
			continue
		}
		byName := map[string]fieldInfo{}
		for _, f := range *fs {
			info := fieldInfo{}
			info.returns, info.list = unwrap(f.Type())
			takesSize := false
			for _, a := range f.Args(all) {
				argType, argList := unwrap(a.Type())
				if sizeArgNames[a.Name()] {
					takesSize = true
					info.omitted = max(info.omitted, declaredSize(a.DefaultValue()))
				} else if n, ok := inputSize[argType]; ok {
					takesSize = true
					info.omitted = max(info.omitted, n)
				}
				if argList {
					info.listArgs = append(info.listArgs, a.Name())
				}
			}
			info.sized = takesSize && (info.list || holdsList[info.returns])
			byName[f.Name()] = info
		}
		m.fields[*t.Name()] = byName
	}
	return m
}

// field looks up one field of one type. A nil model (no schema) knows no field.
func (m *costModel) field(typ, name string) (fieldInfo, bool) {
	if m == nil {
		return fieldInfo{}, false
	}
	info, ok := m.fields[typ][name]
	return info, ok
}

func (m *costModel) root(kind string) string {
	if m == nil {
		return ""
	}
	return m.roots[kind]
}

// selNode is one field of a collected operation: the fields of the document that
// execution merges into one resolver call (see normalize).
type selNode struct {
	key, name string
	rows      int64
	children  []*selNode
}

// collector collects one operation into its execution tree.
type collector struct {
	model     *costModel
	fragments map[string][]fragmentDef
	vars      map[string]any
	defaults  map[string]any
	visits    int
	budget    int
}

// errTooManySelections is the collection exceeding its bound.
type errTooManySelections struct{}

// normalize collects an operation as graphql-go collects it for execution: fragments
// merged into their level, @skip/@include honoured, and fields sharing a response key,
// name, arguments and parent type merged into one, their selections merged below.
// It reports false when the collection visits more than budget selections.
func normalize(op rootOperation, fragments map[string][]fragmentDef, model *costModel, vars map[string]any, budget int) (roots []*selNode, ok bool) {
	c := &collector{model: model, fragments: fragments, vars: vars, defaults: op.varDefaults, budget: budget}
	defer func() {
		if p := recover(); p != nil {
			if _, over := p.(errTooManySelections); !over {
				panic(p)
			}
			roots, ok = nil, false
		}
	}()
	return c.level([]typedSelections{{typ: model.root(op.kind), sels: op.roots}}), true
}

// typedSelections are selections whose fields belong to typ ("" when unknown).
type typedSelections struct {
	typ  string
	sels []rootSelection
}

// group is the fields of one level that execution merges into one.
type group struct {
	key, name string
	typ       string
	args      []argValue
	subs      []typedSelections
}

// level collects one selection level.
func (c *collector) level(in []typedSelections) []*selNode {
	var order []*group
	groups := map[string]*group{}
	expanded := map[string]bool{}
	var flatten func(sels []rootSelection, typ string)
	flatten = func(sels []rootSelection, typ string) {
		for _, s := range sels {
			c.visits++
			if c.visits > c.budget {
				panic(errTooManySelections{})
			}
			if !c.included(s.conds) {
				continue
			}
			switch {
			case s.spread != "":
				// Spread twice at one level, a fragment adds the same fields twice,
				// which merge: expanding it once is the same collection, and it is what
				// keeps a fragment graph from expanding once per path through it.
				if expanded[s.spread] {
					continue
				}
				expanded[s.spread] = true
				for _, def := range c.fragments[s.spread] {
					flatten(def.sels, def.on)
				}
			case s.inline != nil:
				t := typ
				if s.on != "" {
					t = s.on
				}
				flatten(s.inline, t)
			default:
				gk := typ + "\x00" + s.key + "\x00" + s.name + "\x00" + canonicalArgs(s.args)
				g := groups[gk]
				if g == nil {
					g = &group{key: s.key, name: s.name, typ: typ, args: s.args}
					groups[gk] = g
					order = append(order, g)
				}
				info, known := c.model.field(typ, s.name)
				child := ""
				if known {
					child = info.returns
				}
				if s.sub != nil {
					g.subs = append(g.subs, typedSelections{typ: child, sels: s.sub})
				}
			}
		}
	}
	for _, ts := range in {
		flatten(ts.sels, ts.typ)
	}
	nodes := make([]*selNode, 0, len(order))
	for _, g := range order {
		info, known := c.model.field(g.typ, g.name)
		n := &selNode{key: g.key, name: g.name, rows: c.rows(g.args, info, known)}
		if len(g.subs) > 0 {
			n.children = c.level(g.subs)
		}
		nodes = append(nodes, n)
	}
	return nodes
}

// included reports whether a selection's @skip and @include let it run. A condition
// the request does not settle (a variable it does not supply, with no default) counts
// as running, which can only over-count; graphql-go refuses such a request anyway.
func (c *collector) included(conds []condition) bool {
	for _, cond := range conds {
		v, known := c.boolValue(cond.value)
		if !known {
			continue
		}
		if cond.skip == v {
			return false
		}
	}
	return true
}

func (c *collector) boolValue(v any) (bool, bool) {
	switch v := v.(type) {
	case litName:
		switch v {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	case varRef:
		if val, ok := c.vars[string(v)]; ok {
			b, isBool := val.(bool)
			return b, isBool
		}
		if def, ok := c.defaults[string(v)]; ok {
			return c.boolValue(def)
		}
	}
	return false, false
}

// canonicalArgs writes arguments in one form whatever order they were written in, so
// fields with the same arguments merge.
func canonicalArgs(args []argValue) string {
	var b strings.Builder
	writeArgs(&b, args)
	return b.String()
}

func writeArgs(b *strings.Builder, args []argValue) {
	sorted := append([]argValue(nil), args...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	b.WriteByte('(')
	for _, a := range sorted {
		b.WriteString(a.name)
		b.WriteByte(':')
		writeValue(b, a.value)
		b.WriteByte(',')
	}
	b.WriteByte(')')
}

func writeValue(b *strings.Builder, v any) {
	switch v := v.(type) {
	case []argValue:
		writeArgs(b, v)
	case []any:
		b.WriteByte('[')
		for _, e := range v {
			writeValue(b, e)
			b.WriteByte(',')
		}
		b.WriteByte(']')
	case varRef:
		b.WriteString("$" + string(v))
	default:
		fmt.Fprintf(b, "%T:%v", v, v)
	}
}

// treeCost costs a collected selection level.
func treeCost(nodes []*selNode) int64 {
	var total int64
	for _, n := range nodes {
		total = satAdd(total, satAdd(1, satMul(n.rows, treeCost(n.children))))
	}
	return total
}

// treeAliases is the most distinct response keys one field is selected under at any
// level of a collected tree: `a: children b: children` is 2.
func treeAliases(nodes []*selNode) int {
	keys := map[string]map[string]struct{}{}
	worst := 0
	for _, n := range nodes {
		if keys[n.name] == nil {
			keys[n.name] = map[string]struct{}{}
		}
		keys[n.name][n.key] = struct{}{}
		worst = max(worst, len(keys[n.name]), treeAliases(n.children))
	}
	return worst
}

// operationCost is one operation's cost; an operation whose collection exceeds its
// bound costs costSaturation.
func operationCost(op rootOperation, fragments map[string][]fragmentDef, model *costModel, vars map[string]any) int64 {
	nodes, ok := normalize(op, fragments, model, vars, maxSelectionVisits)
	if !ok {
		return costSaturation
	}
	return treeCost(nodes)
}

// maxFieldAliases is treeAliases of one operation's collected tree.
func maxFieldAliases(op rootOperation, fragments map[string][]fragmentDef, model *costModel, vars map[string]any) int {
	nodes, _ := normalize(op, fragments, model, vars, maxSelectionVisits)
	return treeAliases(nodes)
}

// rows is how many times over a field's selections run.
func (c *collector) rows(args []argValue, info fieldInfo, known bool) int64 {
	switch {
	case !known:
		// No schema says what this field is: a page size written in its arguments is
		// still read, so a document checked without a schema is not counted as free.
		if n, found := c.sizeIn(args); found {
			return n
		}
		return 1
	case info.sized:
		if n, found := c.sizeIn(args); found {
			return n
		}
		return info.omitted
	case info.list && len(info.listArgs) > 0:
		var n int64 = 1
		for _, a := range args {
			for _, name := range info.listArgs {
				if a.name == name {
					n = max(n, c.listLength(a.value))
				}
			}
		}
		return n
	default:
		return 1
	}
}

// sizeIn finds the largest page size named anywhere in a field's arguments. A size the
// request leaves to a variable it does not supply is not found, so the schema's
// default applies, as it does in execution.
func (c *collector) sizeIn(args []argValue) (int64, bool) {
	var n int64
	found := false
	for _, a := range args {
		if sizeArgNames[a.name] {
			if m, ok := c.sizeOf(a.value); ok {
				n, found = max(n, m), true
			}
			continue
		}
		if m, ok := c.sizeInValue(a.value); ok {
			n, found = max(n, m), true
		}
	}
	return n, found
}

// sizeInValue looks for a page size inside one argument value: an input object, a
// list, or a variable holding either.
func (c *collector) sizeInValue(v any) (int64, bool) {
	switch v := v.(type) {
	case []argValue:
		return c.sizeIn(v)
	case []any:
		var n int64
		found := false
		for _, e := range v {
			if m, ok := c.sizeInValue(e); ok {
				n, found = max(n, m), true
			}
		}
		return n, found
	case varRef:
		if val, ok := c.vars[string(v)]; ok {
			return sizeInJSON(val)
		}
		if def, ok := c.defaults[string(v)]; ok {
			return c.sizeInValue(def)
		}
	}
	return 0, false
}

// sizeOf reads one page-size value: a positive integer as written (capped at
// maxPageSize, since no page holds more), anything else written as maxPageSize. A
// variable the request does not supply, with no default, is not a size at all.
func (c *collector) sizeOf(v any) (int64, bool) {
	switch v := v.(type) {
	case int64:
		return pageRows(v), true
	case varRef:
		if val, ok := c.vars[string(v)]; ok {
			return jsonPageRows(val), true
		}
		if def, ok := c.defaults[string(v)]; ok {
			return c.sizeOf(def)
		}
		return 0, false
	}
	return maxPageSize, true
}

// listLength reads the length of one list argument: a literal list as written, a
// variable as supplied, and anything unreadable as maxPageSize — the most keys a
// lookup takes.
func (c *collector) listLength(v any) int64 {
	switch v := v.(type) {
	case []any:
		return max(1, int64(len(v)))
	case varRef:
		if val, ok := c.vars[string(v)]; ok {
			if l, ok := val.([]any); ok {
				return max(1, int64(len(l)))
			}
			if val == nil {
				return 1
			}
			return maxPageSize
		}
		if def, ok := c.defaults[string(v)]; ok {
			return c.listLength(def)
		}
		return maxPageSize
	}
	// A single value where a list is declared is coerced to a list of one.
	return 1
}

func pageRows(n int64) int64 {
	if n < 1 || n > maxPageSize {
		return maxPageSize
	}
	return n
}

// jsonPageRows reads a page size out of a decoded variable value.
func jsonPageRows(v any) int64 {
	if n, ok := jsonInt(v); ok {
		return pageRows(n)
	}
	return maxPageSize
}

// jsonInt reads an integer out of a decoded variable value, whichever way it was
// decoded: encoding/json gives float64 (or json.Number), a Go caller may pass an int.
func jsonInt(v any) (int64, bool) {
	switch v := v.(type) {
	case float64:
		if v != math.Trunc(v) || v < math.MinInt64 || v > math.MaxInt64 {
			return 0, false
		}
		return int64(v), true
	case json.Number:
		n, err := strconv.ParseInt(string(v), 10, 64)
		return n, err == nil
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	}
	return 0, false
}

// sizeInJSON looks for a page size inside a decoded variable value.
func sizeInJSON(v any) (int64, bool) {
	var n int64
	found := false
	switch v := v.(type) {
	case map[string]any:
		for k, e := range v {
			if sizeArgNames[k] {
				if e == nil {
					// An explicit null is the schema's default, as an absent key is.
					continue
				}
				n, found = max(n, jsonPageRows(e)), true
				continue
			}
			if m, ok := sizeInJSON(e); ok {
				n, found = max(n, m), true
			}
		}
	case []any:
		for _, e := range v {
			if m, ok := sizeInJSON(e); ok {
				n, found = max(n, m), true
			}
		}
	}
	return n, found
}

// Extension codes a refused document carries, so a client can recognize the refusal
// without matching the message text.
const (
	costLimitCode  = "QUERY_TOO_COSTLY"
	aliasLimitCode = "TOO_MANY_ALIASES"
)

func costRefusal(kind, name string, cost, limit int64) *gqlerrors.QueryError {
	return &gqlerrors.QueryError{
		Message: fmt.Sprintf("%s %s has a cost of %d; the maximum is %d (each field costs 1, multiplied by the page size of every page it is selected under)",
			kind, displayName(name), cost, limit),
		Extensions: map[string]any{"code": costLimitCode},
	}
}

func selectionsRefusal(kind, name string) *gqlerrors.QueryError {
	return &gqlerrors.QueryError{
		Message: fmt.Sprintf("%s %s expands to more than %d selections through its fragments",
			kind, displayName(name), maxSelectionVisits),
		Extensions: map[string]any{"code": costLimitCode},
	}
}

func aliasRefusal(kind, name string, n, limit int) *gqlerrors.QueryError {
	return &gqlerrors.QueryError{
		Message: fmt.Sprintf("%s %s selects one field under %d different names at one level; the maximum is %d",
			kind, displayName(name), n, limit),
		Extensions: map[string]any{"code": aliasLimitCode},
	}
}

func displayName(name string) string {
	if name == "" {
		return "(anonymous)"
	}
	return name
}

// workRefusals counts the operations a work ceiling refused before they ran, by the
// ceiling that refused them. Labels are fixed; no tenant, caller or document detail is
// exposed.
var workRefusals = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "devicechain", Subsystem: "graphql", Name: "work_refused_total",
	Help: "GraphQL operations refused before execution by a work ceiling, by ceiling (root_fields, cost or aliases).",
}, []string{"limit"})

// refusalLabels maps a refusal's extension code to its metric label.
var refusalLabels = map[string]string{
	workLimitCode:  "root_fields",
	costLimitCode:  "cost",
	aliasLimitCode: "aliases",
}

func init() {
	for _, l := range refusalLabels {
		workRefusals.WithLabelValues(l)
	}
}

// observeRefusal counts a refusal made by one of the work ceilings. A document refused
// for its length or because it could not be read is not counted here.
func observeRefusal(qerr *gqlerrors.QueryError) {
	code, _ := qerr.Extensions["code"].(string)
	if l, ok := refusalLabels[code]; ok {
		workRefusals.WithLabelValues(l).Inc()
	}
}
