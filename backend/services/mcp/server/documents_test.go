// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
)

// Every document MCP sends is a STRING the compiler does not read, validated by a schema
// in another module. So when a field was dropped from a schema, the tool that selected it
// kept compiling and every unit test here kept passing (they fake the downstream), and
// the tool failed only at runtime with `Cannot query field ...`. This file asks the
// schema, with the SERVER'S OWN VALIDATOR: each area's served SDL is parsed by the same
// library the service parses it with, and each document is validated the way the service
// would validate it.
//
// 🔴 RUN WITH -count=1 AFTER TOUCHING A SCHEMA: the SDL files live outside this module and
// Go's test cache does not track them, so an edited schema is served a stale PASS.

// servedTenantSchema parses the schema an area serves at its tenant mount — the one MCP's
// GraphQLClient posts to (/graphql).
func servedTenantSchema(t *testing.T, area string) *graphql.Schema {
	t.Helper()
	dir := filepath.Join("..", "..", area, "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil {
		t.Fatalf("classify %s: %v", dir, err)
	}
	if !served {
		t.Fatalf("area %q serves no schema at %s; a test that parsed nothing would validate everything",
			area, schemaplane.MountTenant)
	}
	// A nil resolver is enough: validation reads the schema, never a resolver.
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatalf("parse %s: %v", area, err)
	}
	return schema
}

// validationErrors validates doc against its area's schema with no variable VALUES.
//
// The one error dropped is VariablesOfCorrectType: graphql-go raises it for a required
// variable given no value, which is always the case here because no values are supplied.
// It is the ONLY rule filtered — widen this and a real defect can hide behind it (the
// "survives the filter" sub-test below is the check on that).
func validationErrors(schema *graphql.Schema, doc document) []*gqlerrors.QueryError {
	var kept []*gqlerrors.QueryError
	for _, e := range schema.Validate(doc.text) {
		if e.Rule == "VariablesOfCorrectType" {
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

func operationName(doc document) string {
	f := strings.Fields(doc.text)
	if len(f) < 2 {
		return ""
	}
	name, _, _ := strings.Cut(f[1], "(")
	return name
}

func TestEveryMCPDocumentValidatesAgainstTheSchemaItIsSentTo(t *testing.T) {
	// Floor: a registry that recorded nothing would validate everything. The two alarm
	// documents are named because they are the ones whose schema just lost a field.
	names := map[string]string{}
	for _, d := range documents {
		names[operationName(d)] = d.area
	}
	for _, want := range []string{"ListAlarms", "GetAlarm"} {
		if names[want] != "device-management" {
			t.Fatalf("registry has no %s document for device-management (have %v)", want, names)
		}
	}

	schemas := map[string]*graphql.Schema{}
	for _, d := range documents {
		s, ok := schemas[d.area]
		if !ok {
			s = servedTenantSchema(t, d.area)
			schemas[d.area] = s
		}
		for _, e := range validationErrors(s, d) {
			t.Errorf("%s (sent to %s): %s [%s]", operationName(d), d.area, e.Message, e.Rule)
		}
	}

	// The filter must not swallow a real defect: a document with an unknown field AND a
	// required variable given no value keeps the field error.
	t.Run("an unknown field survives the variable filter", func(t *testing.T) {
		bad := document{area: "device-management",
			text: `query Bad($tokens: [String!]!) { alarmsByToken(tokens: $tokens) { noSuchField } }`}
		errs := validationErrors(servedTenantSchema(t, bad.area), bad)
		if len(errs) != 1 || errs[0].Rule != "FieldsOnCorrectTypeRule" {
			t.Fatalf("want exactly one FieldsOnCorrectTypeRule error, got %v", errs)
		}
	})
}

// A document built as a literal is sent but never recorded, so the test above would never
// see it. Only newDocument may build one in non-test code.
func TestDocumentsAreBuiltOnlyThroughNewDocument(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	parsed := 0
	var callAreas []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed++
		for _, pos := range documentLiterals(f) {
			t.Errorf("%s: document literal outside newDocument; build it with newDocument so it is validated",
				fset.Position(pos))
		}
		areas, bad := newDocumentCallAreas(f)
		for _, pos := range bad {
			t.Errorf("%s: newDocument's area must be a string literal, so this test can check it was recorded",
				fset.Position(pos))
		}
		callAreas = append(callAreas, areas...)
	}
	if parsed == 0 {
		t.Fatal("parsed no source files; a scan of nothing finds nothing")
	}

	// Every newDocument call site is recorded, under the area it names. The comparison is by
	// area, so a registry that stopped recording one area's documents is caught even when
	// the others still validate.
	count := func(areas []string) map[string]int {
		m := map[string]int{}
		for _, a := range areas {
			m[a]++
		}
		return m
	}
	var recorded []string
	for _, d := range documents {
		recorded = append(recorded, d.area)
	}
	want, got := count(callAreas), count(recorded)
	if len(callAreas) == 0 || len(want) != len(got) {
		t.Fatalf("newDocument call sites by area %v, recorded documents by area %v", want, got)
	}
	for a, n := range want {
		if got[a] != n {
			t.Fatalf("newDocument call sites by area %v, recorded documents by area %v", want, got)
		}
	}

	// The scanner must be able to fail: both literal forms are reported, and newDocument's
	// own literal is not.
	t.Run("the scanner reports bare and elided-type literals", func(t *testing.T) {
		const src = `package server
func newDocument(area, text string) document { return document{area: area, text: text} }
var a = document{area: "x", text: "q"}
var b = &document{area: "x", text: "q"}
var c = []document{{area: "x", text: "q"}}
var d = map[string]document{"k": {area: "x", text: "q"}}
var e = [][]*document{{{area: "x", text: "q"}}}
var f = map[document]int{{area: "x", text: "q"}: 1}
var ok = []string{"not a document"}
`
		f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(documentLiterals(f)); got != 6 {
			t.Fatalf("scanner found %d document literals in the fixture, want 6", got)
		}
	})
}

// documentLiterals returns the position of every composite literal of type document (or
// *document) in f outside newDocument — including one whose type is elided because it is
// an element, key or value of a slice, array or map literal of documents. Types reached
// only through a named type (type docs []document) are not resolved; nothing in this
// package declares one.
func documentLiterals(f *ast.File) []token.Pos {
	seen := map[token.Pos]bool{}
	var hits []token.Pos
	isDoc := func(t ast.Expr) bool {
		if s, ok := t.(*ast.StarExpr); ok {
			t = s.X
		}
		id, ok := t.(*ast.Ident)
		return ok && id.Name == "document"
	}
	// visit walks a composite literal whose type is typ: its own when written, else the
	// one its enclosing literal implies.
	var visit func(lit *ast.CompositeLit, typ ast.Expr)
	visit = func(lit *ast.CompositeLit, typ ast.Expr) {
		if lit.Type != nil {
			typ = lit.Type
		}
		if isDoc(typ) && !seen[lit.Pos()] {
			seen[lit.Pos()] = true
			hits = append(hits, lit.Pos())
		}
		var key, elt ast.Expr
		switch tt := typ.(type) {
		case *ast.ArrayType:
			elt = tt.Elt
		case *ast.MapType:
			key, elt = tt.Key, tt.Value
		}
		if s, ok := elt.(*ast.StarExpr); ok {
			elt = s.X
		}
		if s, ok := key.(*ast.StarExpr); ok {
			key = s.X
		}
		for _, e := range lit.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.CompositeLit); ok && key != nil {
					visit(k, key)
				}
				e = kv.Value
			}
			if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
				e = u.X
			}
			if c, ok := e.(*ast.CompositeLit); ok {
				visit(c, elt)
			}
		}
	}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "newDocument" {
			continue
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			// Keep descending after visit: a literal can also sit inside a call or a func
			// literal within an element, which visit does not follow. seen stops a literal
			// reached both ways from being reported twice.
			if lit, ok := n.(*ast.CompositeLit); ok {
				visit(lit, nil)
			}
			return true
		})
	}
	return hits
}

// newDocumentCallAreas returns the area literal of every newDocument call in f, and the
// position of any call whose area is not a string literal.
func newDocumentCallAreas(f *ast.File) (areas []string, bad []token.Pos) {
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "newDocument" || len(call.Args) != 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			bad = append(bad, call.Pos())
			return true
		}
		areas = append(areas, strings.Trim(lit.Value, "`\""))
		return true
	})
	return areas, bad
}
