// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

type deliveryCapSite struct {
	File, Function string
}

type deliveryCapFinding struct {
	Site      deliveryCapSite
	Kind, Pos string
}

// deliveryCapChecksUnder scans production Go outside core/messaging. It reads other
// modules, so repository tests must run with -count=1. Comparison operands are
// searched recursively, but a count stored in a variable first (n := msg.NumDelivered;
// if n >= messaging.MaxDeliver) is still not caught by this syntax-only rule.
func deliveryCapChecksUnder(root string) ([]deliveryCapFinding, map[string]bool, error) {
	var found []deliveryCapFinding
	visited := map[string]bool{}
	parsed := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case "_legacy", "node_modules", "vendor", ".git", "testdata":
				return filepath.SkipDir
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			visited[abs] = true
			if rel == "backend/core/messaging" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		parsed++
		aliases := map[string]bool{}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if p != "github.com/devicechain-io/dc-microservice/messaging" {
				continue
			}
			name := "messaging"
			if imp.Name != nil {
				name = imp.Name.Name
			}
			aliases[name] = true
		}
		for _, decl := range f.Decls {
			function := "<package>" // Also cover package-level initializers.
			if fd, ok := decl.(*ast.FuncDecl); ok {
				function = funcName(fd)
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				kind := ""
				switch x := n.(type) {
				case *ast.BinaryExpr:
					switch x.Op {
					case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
						if (containsExpression(x.X, deliveredSelector) && containsExpression(x.Y, capName)) ||
							(containsExpression(x.Y, deliveredSelector) && containsExpression(x.X, capName)) {
							kind = "raw"
						}
					}
				case *ast.CallExpr:
					if sel, ok := ast.Unparen(x.Fun).(*ast.SelectorExpr); ok {
						if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] {
							kind = sel.Sel.Name
						}
					} else if id, ok := ast.Unparen(x.Fun).(*ast.Ident); ok && aliases["."] {
						kind = id.Name
					}
					if kind != "Dispose" && kind != "FinalDelivery" {
						kind = ""
					}
				}
				if kind != "" {
					found = append(found, deliveryCapFinding{deliveryCapSite{rel, function}, kind, fset.Position(n.Pos()).String()})
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if parsed == 0 {
		return nil, nil, fmt.Errorf("scanning %s parsed no Go files", root)
	}
	return found, visited, nil
}

// Search only within comparison operands so standalone budget arithmetic is allowed.
func containsExpression(expr ast.Expr, matches func(ast.Expr) bool) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok && matches(e) {
			found = true
		}
		return !found
	})
	return found
}

func deliveredSelector(expr ast.Expr) bool {
	sel, ok := ast.Unparen(expr).(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "NumDelivered"
}

func capName(expr ast.Expr) bool {
	name := ""
	switch x := ast.Unparen(expr).(type) {
	case *ast.Ident:
		name = x.Name
	case *ast.SelectorExpr:
		name = x.Sel.Name
	}
	return name == "MaxDeliver" || name == "ConsumerMaxDeliver"
}

// Both directions are mandatory: an unlisted site and a stale ledger entry fail.
func deliveryCapLedgerErrors(found []deliveryCapFinding, kind string, ledger map[deliveryCapSite]string) []string {
	present := map[deliveryCapSite]bool{}
	var errs []string
	for _, f := range found {
		if f.Kind != kind {
			continue
		}
		present[f.Site] = true
		if _, ok := ledger[f.Site]; !ok {
			errs = append(errs, fmt.Sprintf("%s: unlisted %s in %s", f.Pos, kind, f.Site.Function))
		}
	}
	for site, reason := range ledger {
		if !present[site] {
			errs = append(errs, fmt.Sprintf("stale %s ledger entry: %s %s", kind, site.File, site.Function))
		}
		if strings.TrimSpace(reason) == "" {
			errs = append(errs, fmt.Sprintf("missing reason: %v", site))
		}
	}
	return errs
}
