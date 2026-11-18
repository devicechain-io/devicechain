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
	"testing"
)

// Scan references, including function values and test files, rather than just calls.
func safetyGateReferences(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(filepath.Join(root, "backend/services"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		aliases := map[string]bool{}
		dot := false
		for _, imp := range f.Imports {
			name, _ := strconv.Unquote(imp.Path.Value)
			if name != "github.com/devicechain-io/dc-microservice/governance" {
				continue
			}
			alias := "governance"
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			aliases[alias] = true
			dot = dot || alias == "."
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		report := func(n ast.Node, name string) {
			found = append(found, fmt.Sprintf("%s:%d: governance.%s belongs in core/service", filepath.ToSlash(rel), fset.Position(n.Pos()).Line, name))
		}
		banned := func(name string) bool { return name == "NewSafetyGates" || name == "StopRefreshes" }
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] && banned(sel.Sel.Name) {
					report(sel, sel.Sel.Name)
				}
			}
			if id, ok := n.(*ast.Ident); ok && dot && banned(id.Name) {
				report(id, id.Name)
			}
			return true
		})
		return nil
	})
	return found, err
}

func TestServicesUseOwnedSafetyGates(t *testing.T) {
	found, err := safetyGateReferences(workspaceRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatal(strings.Join(found, "\n"))
	}
}

func TestSafetyGateGuardNegativeControl(t *testing.T) {
	for _, alias := range []string{"governance", "gov", ".", "mixed"} {
		t.Run(alias, func(t *testing.T) {
			root := t.TempDir()
			prefix := alias + "."
			if alias == "." {
				prefix = ""
			}
			imports := alias + ` "github.com/devicechain-io/dc-microservice/governance"`
			if alias == "mixed" {
				prefix = ""
				imports = `. "github.com/devicechain-io/dc-microservice/governance"
import gov "github.com/devicechain-io/dc-microservice/governance"
var allowed = gov.GatePresence`
			}
			const file = "backend/services/planted/main.go"
			writeMintFixture(t, root, file, `// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package planted
import `+imports+`
var gates = `+prefix+`NewSafetyGates
var stop = `+prefix+`StopRefreshes
// governance.NewSafetyGates is only a comment.
var text = "governance.StopRefreshes"
`)
			found, err := safetyGateReferences(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(found) != 2 || !strings.Contains(strings.Join(found, "\n"), file) {
				t.Fatalf("planted references escaped or comments counted: %v", found)
			}
		})
	}
}
