// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// optsInToScopedCallers matches the production code by which a module admits a member
// whose access is limited to part of the tenant (ADR-081): a resolver that opts in at the
// gate, or a model that declares a scope rule at the store.
var optsInToScopedCallers = regexp.MustCompile(`auth\.AuthorizeScoped\(|auth\.AuthorizeCatalogue\(|\)\s*ScopedBy\(\)\s*rdb\.ScopeRule`)

// runsTheSweep matches a test that runs the SDL-generated isolation sweep.
var runsTheSweep = regexp.MustCompile(`isolationsweep\.Run\(`)

// scopedModules returns, for every workspace module outside core, whether its production
// code admits scoped callers and whether its tests run the sweep. It reads go.work for
// the module list, so a module added there is covered with no edit here.
func scopedModules(t *testing.T, root string) (optedIn, swept map[string]bool, scanned int) {
	t.Helper()
	optedIn, swept = map[string]bool{}, map[string]bool{}
	for _, dir := range workspaceModuleDirs(t, root) {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatal(err)
		}
		rel = filepath.ToSlash(rel)
		if rel == "backend/core" {
			continue // the gates and the rules are DEFINED there
		}
		err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != dir && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			if strings.HasSuffix(path, "_test.go") {
				if runsTheSweep.Match(content) {
					swept[rel] = true
				}
			} else if optsInToScopedCallers.Match(content) {
				optedIn[rel] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scanning %s: %v", dir, err)
		}
	}
	return optedIn, swept, scanned
}

// 🔑 EVERY MODULE THAT ADMITS A SCOPED CALLER RUNS THE ISOLATION SWEEP. Opting a resolver
// in, or giving a model a scope rule, is what makes a module reachable by a customer's
// member at all — before that, auth.Authorize and the store refuse every scoped call with
// no code of the module's own. From that moment the module's answers to such a member are
// its own responsibility, and the sweep (backend/core/test/isolationsweep) is the judge
// that reads them from the served schema rather than from a list. The trigger is derived
// from the tree, so the first opt-in in a new module fails here until the sweep is there.
func TestEveryModuleThatAdmitsScopedCallersRunsTheIsolationSweep(t *testing.T) {
	root := workspaceRoot(t)
	optedIn, swept, scanned := scopedModules(t, root)
	if scanned == 0 {
		t.Fatal("the scan read no Go file, so it would report every module clean")
	}
	// 🔴 The negative control: the detector must find the module that is known to opt in.
	// A pattern that stopped matching would otherwise make this check pass over nothing.
	if !optedIn["backend/services/device-management"] {
		t.Fatalf("the scan did not find device-management's scope rules, so it cannot be trusted "+
			"to find anyone's (opted-in: %v)", optedIn)
	}
	var missing []string
	for m := range optedIn {
		if !swept[m] {
			missing = append(missing, m)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("these modules admit scoped callers but no test runs isolationsweep.Run over their "+
			"served schema: %v", missing)
	}
}
