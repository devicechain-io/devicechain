// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Every reference outside the core module, including test assertions, must describe
// a give-up rather than a policy refusal. File + enclosing function is stable across
// line shifts. Both directions are checked so this remains an account of the tree.
var droppedResultLedger = map[string]string{
	"backend/services/device-state/processor/StateProcessor.go:StateProcessor.dispose":                                       "Give-up after the delivery cap; discard the rebuildable projection, not a policy refusal.",
	"backend/services/device-state/processor/batch_integration_test.go:contendSweepWithBatches":                              "Asserts no give-up during contention, not a policy refusal.",
	"backend/services/device-state/processor/batch_integration_test.go:waitDispositions":                                     "Counts give-up dispositions while waiting for settlement, not policy refusals.",
	"backend/services/device-state/processor/batch_poison_test.go:TestAPoisonWriteDoesNotFailItsBatchMates":                  "Asserts give-up at the delivery cap, not a policy refusal.",
	"backend/services/device-state/processor/envelope_merge_test.go:TestAPoisonMemberDoesNotBlockItsEnvelopeMates":           "Asserts a member's give-up at the delivery cap, not a policy refusal.",
	"backend/services/device-state/processor/single_event_tx_test.go:TestABatchOfOneLeavesTheProjectionThePerMessagePathDid": "Compares give-up counts between projection paths, not policy refusals.",
}

func TestDroppedResultsAreGiveUps(t *testing.T) {
	issues, err := droppedResultIssues(workspaceRoot(t), droppedResultLedger)
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range issues {
		t.Error(issue)
	}
}

func TestDroppedResultGuardFindsUnlistedAndStaleSites(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "backend", "services", "fixture")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	const src = `// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package fixture
import c "github.com/devicechain-io/dc-microservice/core"
func refuse() { done(c.ResultDropped) }
`
	if err := os.WriteFile(filepath.Join(dir, "policy.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	key := "backend/services/fixture/policy.go:refuse"
	issues, err := droppedResultIssues(root, map[string]string{})
	if err != nil || len(issues) != 1 || !strings.Contains(issues[0], key) {
		t.Fatalf("planted unlisted site: %v, %v", issues, err)
	}
	ledger := map[string]string{key: "Give-up fixture, not a policy refusal."}
	issues, err = droppedResultIssues(root, ledger)
	if err != nil || len(issues) != 0 {
		t.Fatalf("listed site: %v, %v", issues, err)
	}
	if err := os.Remove(filepath.Join(dir, "policy.go")); err != nil {
		t.Fatal(err)
	}
	issues, err = droppedResultIssues(root, ledger)
	if err != nil || len(issues) != 1 || !strings.Contains(issues[0], key) {
		t.Fatalf("planted stale ledger: %v, %v", issues, err)
	}
}

func droppedResultIssues(root string, ledger map[string]string) ([]string, error) {
	var issues []string
	seen := map[string]bool{}
	fset := token.NewFileSet()
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
			case ".git", "node_modules", "vendor", "_legacy", ".agent-os":
				return filepath.SkipDir
			}
			if rel == "backend/core" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		aliases := map[string]bool{}
		for _, imp := range file.Imports {
			value, _ := strconv.Unquote(imp.Path.Value)
			if value == "github.com/devicechain-io/dc-microservice/core" {
				name := "core"
				if imp.Name != nil {
					name = imp.Name.Name
				}
				aliases[name] = true
			}
		}
		check := func(node ast.Node, name string) {
			ast.Inspect(node, func(n ast.Node) bool {
				matched := false
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "ResultDropped" {
					id, ok := sel.X.(*ast.Ident)
					matched = ok && aliases[id.Name]
				}
				if id, ok := n.(*ast.Ident); ok && aliases["."] && id.Name == "ResultDropped" {
					matched = true
				}
				if matched {
					key := rel + ":" + name
					seen[key] = true
					if reason, ok := ledger[key]; !ok || strings.TrimSpace(reason) == "" {
						issues = append(issues, fmt.Sprintf("%s at %s: ResultDropped is not on the give-up ledger; policy refusals must use ResultUnwanted", key, fset.Position(n.Pos())))
					}
				}
				return true
			})
		}
		for _, decl := range file.Decls {
			name := "<package>"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				name = fn.Name.Name
				if fn.Recv != nil {
					typ := fn.Recv.List[0].Type
					if ptr, ok := typ.(*ast.StarExpr); ok {
						typ = ptr.X
					}
					if id, ok := typ.(*ast.Ident); ok {
						name = id.Name + "." + name
					}
				}
			}
			check(decl, name)
		}
		return nil
	})
	for key := range ledger {
		if !seen[key] {
			issues = append(issues, "stale dropped-result ledger entry: "+key)
		}
	}
	return issues, err
}
