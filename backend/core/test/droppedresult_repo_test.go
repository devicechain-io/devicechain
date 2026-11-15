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
type droppedResultEntry struct {
	count  int
	reason string
}

var droppedResultLedger = map[string]droppedResultEntry{
	"backend/services/device-state/processor/StateProcessor.go:StateProcessor.dispose":                                       {1, "Give-up after the delivery cap; discard the rebuildable projection, not a policy refusal."},
	"backend/services/device-state/processor/batch_integration_test.go:contendSweepWithBatches":                              {1, "Asserts no give-up during contention, not a policy refusal."},
	"backend/services/device-state/processor/batch_integration_test.go:waitDispositions":                                     {1, "Counts give-up dispositions while waiting for settlement, not policy refusals."},
	"backend/services/device-state/processor/batch_poison_test.go:TestAPoisonWriteDoesNotFailItsBatchMates":                  {1, "Asserts give-up at the delivery cap, not a policy refusal."},
	"backend/services/device-state/processor/envelope_merge_test.go:TestAPoisonMemberDoesNotBlockItsEnvelopeMates":           {1, "Asserts a member's give-up at the delivery cap, not a policy refusal."},
	"backend/services/device-state/processor/single_event_tx_test.go:TestABatchOfOneLeavesTheProjectionThePerMessagePathDid": {1, "Compares give-up counts between projection paths, not policy refusals."},
}

// Literal uses are checked in non-test services and core (except the metric
// vocabulary itself). These existing uses are not processor dispositions.
var droppedLiteralLedger = map[string]droppedResultEntry{
	"backend/core/cmddownlink/fetcher.go:Fetcher.Pending":                                                                    {1, "Structured-log key counting rejected backlog rows."},
	"backend/services/lwm2m-ingest/downlink/fetcher.go:CommandFetcher.Pending":                                               {1, "Structured-log key counting rejected backlog rows."},
	"backend/services/event-sources/adapter/ingest.go:Emitter.dropTooOld":                                                    {1, "Structured-log key counting readings outside the age limit."},
	"backend/services/event-processing/processor/ResolvedEventsProcessor.go:ResolvedEventsProcessor.reconcileDetectionState": {1, "Structured-log key counting removed detection state."},
	"backend/services/notification-management/processor/attempt_ledger.go:<package>":                                         {1, "Separate ledger_writes_total outcome: a ledger row given up before writing, not a processor result."},
}

func TestDroppedResultsAreGiveUps(t *testing.T) {
	issues, err := droppedResultIssues(workspaceRoot(t), droppedResultLedger, droppedLiteralLedger)
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
	issues, err := droppedResultIssues(root, nil, nil)
	if err != nil || len(issues) != 1 || !strings.Contains(issues[0], key) {
		t.Fatalf("planted unlisted site: %v, %v", issues, err)
	}
	ledger := map[string]droppedResultEntry{key: {1, "Give-up fixture, not a policy refusal."}}
	issues, err = droppedResultIssues(root, ledger, nil)
	if err != nil || len(issues) != 0 {
		t.Fatalf("listed site: %v, %v", issues, err)
	}
	// Negative control: a second reference in an already ledgered function
	// must fail, and a reviewed count of two must pass.
	duplicate := strings.Replace(src, "done(c.ResultDropped)", "done(c.ResultDropped); done(c.ResultDropped)", 1)
	if err := os.WriteFile(filepath.Join(dir, "policy.go"), []byte(duplicate), 0644); err != nil {
		t.Fatal(err)
	}
	issues, err = droppedResultIssues(root, ledger, nil)
	if err != nil || len(issues) != 1 || !strings.Contains(issues[0], "got 2, want 1") {
		t.Fatalf("planted duplicate: %v, %v", issues, err)
	}
	ledger[key] = droppedResultEntry{2, "Two give-up fixtures."}
	issues, err = droppedResultIssues(root, ledger, nil)
	if err != nil || len(issues) != 0 {
		t.Fatalf("listed duplicate: %v, %v", issues, err)
	}
	// Equality also rejects fewer references while the function still exists.
	if err := os.WriteFile(filepath.Join(dir, "policy.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	issues, err = droppedResultIssues(root, ledger, nil)
	if err != nil || len(issues) != 1 || !strings.Contains(issues[0], "got 1, want 2") {
		t.Fatalf("planted missing reference: %v, %v", issues, err)
	}
	if err := os.Remove(filepath.Join(dir, "policy.go")); err != nil {
		t.Fatal(err)
	}
	issues, err = droppedResultIssues(root, ledger, nil)
	if err != nil || len(issues) != 1 || !strings.Contains(issues[0], key) {
		t.Fatalf("planted stale ledger: %v, %v", issues, err)
	}
}

func TestDroppedResultGuardFindsLiteralBypasses(t *testing.T) {
	for _, scope := range []string{"backend/services/fixture", "backend/core/fixture"} {
		for _, value := range []string{`"dropped"`, "`dropped`", `"dr\x6fpped"`, `"unwanted"`} {
			t.Run(scope+"/"+value, func(t *testing.T) {
				root := t.TempDir()
				dir := filepath.Join(root, filepath.FromSlash(scope))
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				src := "// Copyright The DeviceChain Authors\n// SPDX-License-Identifier: Apache-2.0\n\npackage fixture\nfunc refuse() { done(" + value + ") }\n"
				path := filepath.Join(dir, "policy.go")
				if err := os.WriteFile(path, []byte(src), 0644); err != nil {
					t.Fatal(err)
				}
				key := scope + "/policy.go:refuse"
				issues, err := droppedResultIssues(root, nil, nil)
				if err != nil || len(issues) != 1 || !strings.Contains(issues[0], key) || !strings.Contains(issues[0], "literal") {
					t.Fatalf("planted literal bypass: %v, %v", issues, err)
				}
				ledger := map[string]droppedResultEntry{key: {1, "Reviewed fixture literal."}}
				issues, err = droppedResultIssues(root, nil, ledger)
				if err != nil || len(issues) != 0 {
					t.Fatalf("listed literal: %v, %v", issues, err)
				}
				if err := os.WriteFile(path, []byte(strings.Replace(src, "done("+value+")", "done("+value+"); done("+value+")", 1)), 0644); err != nil {
					t.Fatal(err)
				}
				issues, err = droppedResultIssues(root, nil, ledger)
				if err != nil || len(issues) != 1 || !strings.Contains(issues[0], "got 2, want 1") {
					t.Fatalf("planted duplicate literal: %v, %v", issues, err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				issues, err = droppedResultIssues(root, nil, ledger)
				if err != nil || len(issues) != 1 || !strings.Contains(issues[0], "stale") {
					t.Fatalf("stale literal ledger: %v, %v", issues, err)
				}
			})
		}
	}
}

func droppedResultIssues(root string, ledger, literalLedger map[string]droppedResultEntry) ([]string, error) {
	var issues []string
	seen := map[string]int{}
	literalSeen := map[string]int{}
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
		inCore := strings.HasPrefix(rel, "backend/core/")
		checkLiterals := !strings.HasSuffix(rel, "_test.go") && rel != "backend/core/core/metrics.go" && (inCore || strings.HasPrefix(rel, "backend/services/"))
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
				key := rel + ":" + name
				if matched && !inCore {
					seen[key]++
				}
				if lit, ok := n.(*ast.BasicLit); ok && checkLiterals && lit.Kind == token.STRING {
					value, err := strconv.Unquote(lit.Value)
					if err == nil && (value == "dropped" || value == "unwanted") {
						literalSeen[key]++
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
	compare := func(kind string, counts map[string]int, entries map[string]droppedResultEntry) {
		for key, count := range counts {
			entry, ok := entries[key]
			if !ok || entry.count <= 0 || strings.TrimSpace(entry.reason) == "" {
				issues = append(issues, fmt.Sprintf("%s: %s is not on the ledger with a positive count and reason (got %d)", key, kind, count))
			} else if count != entry.count {
				issues = append(issues, fmt.Sprintf("%s: %s count: got %d, want %d", key, kind, count, entry.count))
			}
		}
		for key := range entries {
			if counts[key] == 0 {
				issues = append(issues, "stale "+kind+" ledger entry: "+key)
			}
		}
	}
	compare("ResultDropped", seen, ledger)
	compare("dropped/unwanted literal", literalSeen, literalLedger)
	return issues, err
}
