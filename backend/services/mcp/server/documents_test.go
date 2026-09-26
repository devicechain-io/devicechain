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

// A document built as a bare `document{...}` literal is sent but never recorded, so the
// test above would never see it. Only newDocument may build one in non-test code.
func TestDocumentsAreBuiltOnlyThroughNewDocument(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	parsed := 0
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
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "newDocument" {
				continue
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if lit, ok := n.(*ast.CompositeLit); ok {
					if id, ok := lit.Type.(*ast.Ident); ok && id.Name == "document" {
						t.Errorf("%s: document literal outside newDocument; build it with newDocument so it is validated",
							fset.Position(lit.Pos()))
					}
				}
				return true
			})
		}
	}
	if parsed == 0 {
		t.Fatal("parsed no source files; a scan of nothing finds nothing")
	}
}
