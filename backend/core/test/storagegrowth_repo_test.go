// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Scan references, including aliased imports and function values, rather than
// just calls. Registration belongs to core/service; user-management owns its
// manager and is the sole direct RegisterStorageGrowth caller.
func storageGrowthReferences(root string) ([]string, int, error) {
	var violations []string
	direct := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		aliases := map[string]bool{}
		for _, imp := range file.Imports {
			target, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if target != "github.com/devicechain-io/dc-microservice/rdb" {
				continue
			}
			alias := "rdb"
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			aliases[alias] = true
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		ast.Inspect(file, func(node ast.Node) bool {
			var name string
			switch n := node.(type) {
			case *ast.SelectorExpr:
				ident, ok := n.X.(*ast.Ident)
				if ok && aliases[ident.Name] {
					name = n.Sel.Name
				}
			case *ast.Ident:
				if aliases["."] {
					name = n.Name
				}
			}
			switch name {
			case "NewStorageGrowthCollector":
				violations = append(violations, rel+": direct collector reference")
			case "RegisterStorageGrowth":
				if rel == "user-management/main.go" {
					direct++
				} else {
					violations = append(violations, rel+": registration must use RdbSpec")
				}
			}
			return true
		})
		return nil
	})
	return violations, direct, err
}

func TestServicesUseStorageGrowthRegistration(t *testing.T) {
	violations, direct, err := storageGrowthReferences("../../services")
	require.NoError(t, err)
	require.Empty(t, violations)
	require.Equal(t, 1, direct, "user-management must register once, after initializing its manager")
}

func TestStorageGrowthGuardDetectsPlantedReference(t *testing.T) {
	root := t.TempDir()
	// An alias and a function value must not evade the AST guard.
	planted := `// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main
import store "github.com/devicechain-io/dc-microservice/rdb"
var collector = store.NewStorageGrowthCollector
var register = store.RegisterStorageGrowth
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(planted), 0600))
	violations, direct, err := storageGrowthReferences(root)
	require.NoError(t, err)
	require.Equal(t, []string{"main.go: direct collector reference", "main.go: registration must use RdbSpec"}, violations)
	require.Zero(t, direct)
}
