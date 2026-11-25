// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bulkchange

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
)

// Before BC-13 the declarations deliberately live in an unserved Go fragment.
// Validate against that exact fragment without importing the DM runtime.
func servedBulkChangeTestSDL(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "services", "device-management", "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil || !served {
		t.Fatalf("tenant schema: served=%v err=%v", served, err)
	}
	return sdl
}

func bulkChangeTestSDL(t *testing.T) string {
	t.Helper()
	sdl := servedBulkChangeTestSDL(t)
	dir := filepath.Join("..", "..", "services", "device-management", "graphql")
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, "fleet_operations_schema.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fragment := ""
	ast.Inspect(f, func(n ast.Node) bool {
		v, ok := n.(*ast.ValueSpec)
		if ok && len(v.Names) == 1 && v.Names[0].Name == "fleetOperationSchema" {
			literal, ok := v.Values[0].(*ast.BasicLit)
			if !ok {
				t.Fatal("schema fragment must remain literal until BC-13")
			}
			fragment, err = strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
		}
		return true
	})
	if fragment == "" {
		t.Fatal("missing unserved bulk-change fragment")
	}
	return sdl + "\n" + fragment
}

func TestBulkChangeDocumentsMatchUnservedFragment(t *testing.T) {
	schema, err := graphql.ParseSchema(bulkChangeTestSDL(t), nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatal(err)
	}
	variables := map[string]any{
		"token": "t", "digest": "reviewed",
		"criteria": map[string]any{"pageNumber": 1, "pageSize": 1000},
		"request": map[string]any{
			"token": "t", "name": "test", "kind": "config.set",
			"configSet": map[string]any{"key": "k", "unset": true}, "deviceTokens": []any{"d"},
		},
	}
	for name, document := range map[string]string{
		"plan": planQuery, "create": createQuery, "start": startQuery, "status": statusQuery,
		"lines": linesQuery, "pause": pauseQuery, "resume": resumeQuery, "abort": abortQuery,
		"resume preview": resumePreviewQuery,
	} {
		if errs := schema.ValidateWithVariables(document, variables); len(errs) != 0 {
			t.Errorf("%s: %v", name, errs)
		}
	}
}

// The SDL lives outside this module; run with -count=1 after schema edits.
func TestReadinessDocumentMatchesServedSchema(t *testing.T) {
	dir := filepath.Join("..", "..", "services", "device-management", "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil || !served {
		t.Fatalf("tenant SDL: served=%v, err=%v", served, err)
	}
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{readinessQuery, legacyReadinessQuery} {
		if errs := schema.Validate(query); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
}
