// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Only the hard-delete cascade may bypass the owner: device deletion holds the
// attribute advisory lock and the device FOR UPDATE before entering this helper.
func attributeWriterAllowed(file, function string) bool {
	return file == "api_attribute_owner.go" ||
		(file == "api_delete.go" && function == "deleteEntityAttributesOnTx")
}

func scanAttributeWriters(name string, source []byte) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, source, 0)
	if err != nil {
		return nil, err
	}
	var violations []string
	bindings := map[string][]ast.Expr{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.ValueSpec:
			for _, name := range v.Names {
				bindings[name.Name] = append(bindings[name.Name], v.Values...)
			}
		case *ast.AssignStmt:
			for _, lhs := range v.Lhs {
				if name, ok := lhs.(*ast.Ident); ok {
					bindings[name.Name] = append(bindings[name.Name], v.Rhs...)
				}
			}
		}
		return true
	})
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || attributeWriterAllowed(filepath.Base(name), fn.Name.Name) {
			continue
		}
		attributes := map[string]bool{}
		var touches func(ast.Node) bool
		touches = func(n ast.Node) bool {
			found := false
			ast.Inspect(n, func(node ast.Node) bool {
				switch v := node.(type) {
				case *ast.Ident:
					found = found || v.Name == "EntityAttribute" || attributes[v.Name]
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						s, _ := strconv.Unquote(v.Value)
						found = found || strings.Contains(strings.ToLower(s), "entity_attributes")
					}
				}
				return !found
			})
			return found
		}
		// Propagate attribute values and GORM model/table handles through aliases.
		// Fixed point also covers assignments inside closures and loop variables.
		for changed := true; changed; {
			changed = false
			mark := func(n ast.Node) {
				ast.Inspect(n, func(v ast.Node) bool {
					if id, ok := v.(*ast.Ident); ok && id.Name != "_" && !attributes[id.Name] {
						attributes[id.Name], changed = true, true
					}
					return true
				})
			}
			if fn.Type.Params != nil {
				for _, field := range fn.Type.Params.List {
					if touches(field.Type) {
						for _, id := range field.Names {
							mark(id)
						}
					}
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.ValueSpec:
					if (v.Type != nil && touches(v.Type)) || anyAttributeExpression(v.Values, touches) {
						for _, id := range v.Names {
							mark(id)
						}
					}
				case *ast.AssignStmt:
					if anyAttributeExpression(v.Rhs, touches) {
						for _, lhs := range v.Lhs {
							if id, ok := lhs.(*ast.Ident); ok {
								mark(id)
							}
						}
					}
				case *ast.RangeStmt:
					if touches(v.X) && v.Value != nil {
						mark(v.Value)
					}
				}
				return true
			})
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "upsertAttributeOnTx" {
				violations = append(violations, fset.Position(call.Pos()).String())
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Create", "Save", "Update", "Updates", "UpdateColumn", "UpdateColumns", "Delete":
				if touches(call) {
					violations = append(violations, fset.Position(call.Pos()).String())
				}
			case "Exec":
				fragments := []string{}
				seen := map[string]bool{}
				var collect func(ast.Node)
				collect = func(n ast.Node) {
					ast.Inspect(n, func(node ast.Node) bool {
						switch v := node.(type) {
						case *ast.BasicLit:
							if v.Kind == token.STRING {
								s, _ := strconv.Unquote(v.Value)
								fragments = append(fragments, strings.ToLower(s))
							}
						case *ast.Ident:
							if !seen[v.Name] {
								seen[v.Name] = true
								for _, expression := range bindings[v.Name] {
									collect(expression)
								}
							}
						}
						return true
					})
				}
				for _, arg := range call.Args {
					collect(arg)
				}
				sql := strings.Join(fragments, " ")
				if strings.Contains(sql, "entity_attributes") && (strings.Contains(sql, "insert") || strings.Contains(strings.ReplaceAll(sql, "for update", ""), "update") || strings.Contains(sql, "delete")) {
					violations = append(violations, fset.Position(call.Pos()).String())
				}
			}
			return true
		})
	}
	return violations, nil
}

func anyAttributeExpression(expressions []ast.Expr, touches func(ast.Node) bool) bool {
	for _, expression := range expressions {
		if touches(expression) {
			return true
		}
	}
	return false
}

func TestAttributeWritersGoThroughPrimitive(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		violations, err := scanAttributeWriters(name, source)
		if err != nil {
			t.Fatal(err)
		}
		for _, violation := range violations {
			t.Errorf("attribute writer bypasses owner: %s", violation)
		}
	}
	// A scratch writer must be caught even in an otherwise allow-listed file.
	for _, scratch := range []string{
		`func scratch(tx *gorm.DB) { tx.Create(&EntityAttribute{}) }`,
		`func scratch(tx *gorm.DB) { sql := "UPDATE entity_attributes SET value = 'bad'"; tx.Exec(sql) }`,
		`const table="entity_attributes"; func scratch(tx *gorm.DB) { tx.Exec("DELETE FROM "+table) }`,
		`func scratch(tx *gorm.DB) { row:=&EntityAttribute{}; upsertAttributeOnTx(tx,row) }`,
		`func scratch(tx *gorm.DB) { var rows []EntityAttribute; tx.Delete(&rows) }`,
		`func scratch(tx *gorm.DB) { q := tx.Model(&EntityAttribute{}); q.Update("value", "bad") }`,
		`func scratch(tx *gorm.DB) { tx.Exec("UPDATE entity_attributes SET value = 'bad'") }`,
	} {
		violations, err := scanAttributeWriters("api_delete.go", []byte("package model\n"+scratch))
		if err != nil || len(violations) == 0 {
			t.Fatalf("scratch violation escaped guard: %s (%v)", scratch, err)
		}
	}
}
