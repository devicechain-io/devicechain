// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The package import graph of device-management, frozen.
//
// device-management grew as one package: model holds most of the service, and nothing stops
// any file in it from calling any other. The plan is to carve it into packages (identity,
// then alarms, then automation), with cross-domain writes kept as explicit single
// transactions. A carve only pays off if the boundaries it draws stay drawn, and the
// compiler enforces a boundary between packages only in one direction: it forbids a cycle,
// not an edge nobody meant to add.
//
// So the graph is a LEDGER. Every package of this module and every import edge between them
// (non-test files only; a test may reach wherever its fixture needs) is listed below, and the
// test fails on any difference, in either direction:
//
//   - a new package, or a new edge, fails until it is added here. Adding one is a design
//     decision a reviewer should see in the diff, not a side effect of a refactor.
//   - a package or edge that no longer exists fails until it is deleted here, so the ledger
//     never describes a looser graph than the real one.
//
// Paths are relative to the module root; "." is the module root (package main).
var frozenPackages = []string{
	".",
	"automationreview",
	"config",
	"contract",
	"gqlclient",
	"graphql",
	"internal/intentseam",
	"internal/selector",
	"model",
	"model/identity",
	"processor",
	"proto",
	"provisioning",
	"revocationupload",
	"ruleverify",
	"schema",
	"test",
}

var frozenPackageEdges = []string{
	". -> automationreview",
	". -> config",
	". -> graphql",
	". -> model",
	". -> processor",
	". -> provisioning",
	". -> revocationupload",
	". -> ruleverify",
	". -> schema",
	"automationreview -> model",
	"gqlclient -> model",
	"graphql -> config",
	"graphql -> internal/intentseam",
	"graphql -> internal/selector",
	"graphql -> model",
	"graphql -> proto",
	"model -> config",
	"model -> contract",
	"internal/intentseam -> internal/selector",
	"model -> internal/intentseam",
	"model -> internal/selector",
	"model -> model/identity",
	"processor -> config",
	"processor -> model",
	"processor -> proto",
	"proto -> contract",
	"provisioning -> model",
	"revocationupload -> model",
	"ruleverify -> model",
	"test -> model",
}

// mustNotImport names edges that are not merely unlisted but wrong by design. They are
// checked as well as the ledger, so adding one to the ledger by mistake still fails.
var mustNotImport = map[string]string{
	"contract -> *": "contract is the wire contract other services import; importing any package of " +
		"device-management would hand them that package too",
	"proto -> model": "proto is imported by other services; it encodes contract, never model",
	"model/identity -> *": "identity is carved leaf first: it may import nothing of this module until a " +
		"later step of the carve says otherwise, so model stays the only way in",
}

const dagModulePath = "github.com/devicechain-io/dc-device-management"

// packageGraph reads the module rooted at root: its packages (directories holding a non-test
// Go file) and the module-internal imports of their non-test files.
func packageGraph(root string) (packages []string, edges []string, err error) {
	pkgSet := map[string]bool{}
	edgeSet := map[string]bool{}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			// WalkDir does not follow a link, and Go can resolve an import through one, so a
			// linked directory would be a package this walk never sees. Refused outright.
			return fmt.Errorf("%s is a symbolic link; the import graph cannot be read through it", p)
		}
		if d.IsDir() {
			// Nothing is pruned but .git: Go lets a package under testdata or vendor be
			// imported by path, so a pruned directory could hide one.
			if p != root && d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		pkgSet[pkg] = true
		for _, im := range f.Imports {
			ip, _ := strconv.Unquote(im.Path.Value)
			switch {
			case ip == dagModulePath:
				edgeSet[pkg+" -> ."] = true
			case strings.HasPrefix(ip, dagModulePath+"/"):
				edgeSet[pkg+" -> "+strings.TrimPrefix(ip, dagModulePath+"/")] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for p := range pkgSet {
		packages = append(packages, p)
	}
	for e := range edgeSet {
		edges = append(edges, e)
	}
	sort.Strings(packages)
	sort.Strings(edges)
	if len(packages) == 0 {
		return nil, nil, fmt.Errorf("no Go package found under %s", root)
	}
	return packages, edges, nil
}

// packageGraphErrors compares a graph with the ledger, both ways, and with mustNotImport.
func packageGraphErrors(packages, edges, wantPackages, wantEdges []string, forbidden map[string]string) []string {
	var issues []string
	diff := func(kind string, got, want []string) {
		gotSet, wantSet := map[string]bool{}, map[string]bool{}
		for _, g := range got {
			gotSet[g] = true
		}
		for _, w := range want {
			wantSet[w] = true
		}
		for _, g := range got {
			if !wantSet[g] {
				issues = append(issues, fmt.Sprintf("new %s %q: not in the frozen graph (package_dag_test.go). "+
					"If the design calls for it, add it there so the change is reviewed; otherwise remove it", kind, g))
			}
		}
		for _, w := range want {
			if !gotSet[w] {
				issues = append(issues, fmt.Sprintf("stale %s %q: the frozen graph lists it but the code no longer has it; delete it", kind, w))
			}
		}
	}
	diff("package", packages, wantPackages)
	diff("import edge", edges, wantEdges)
	for _, e := range edges {
		from, to, _ := strings.Cut(e, " -> ")
		for rule, why := range forbidden {
			rf, rt, _ := strings.Cut(rule, " -> ")
			if rf == from && (rt == "*" || rt == to) {
				issues = append(issues, fmt.Sprintf("forbidden import edge %q: %s", e, why))
			}
		}
	}
	sort.Strings(issues)
	return issues
}

func TestPackageImportGraphIsFrozen(t *testing.T) {
	packages, edges, err := packageGraph(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range packageGraphErrors(packages, edges, frozenPackages, frozenPackageEdges, mustNotImport) {
		t.Error(issue)
	}
}

// The negative control: a planted module, checked against a ledger that describes it, then
// with each defect planted in turn. Every defect must fail.
func TestPackageImportGraphGuardRejectsPlantedDefects(t *testing.T) {
	base := map[string]string{
		"main.go":          `package main; import _ "` + dagModulePath + `/model"`,
		"model/m.go":       `package model; import _ "` + dagModulePath + `/contract"`,
		"model/m_test.go":  `package model; import _ "` + dagModulePath + `/proto"`,
		"contract/c.go":    `package contract`,
		"proto/p.go":       `package proto; import _ "` + dagModulePath + `/contract"`,
		"testdata/x.json":  `{}`,
		".git/ignored.go":  `package ignored; import _ "` + dagModulePath + `/model"`,
		"model/doc.go":     `package model`,
		"proto/encoder.go": `package proto`,
	}
	wantPackages := []string{".", "contract", "model", "proto"}
	wantEdges := []string{". -> model", "model -> contract", "proto -> contract"}
	plant := func(t *testing.T, files map[string]string) string {
		t.Helper()
		root := t.TempDir()
		for rel, src := range files {
			if src == "" {
				continue
			}
			p := filepath.Join(root, filepath.FromSlash(rel))
			if err := mkdirAllAndWrite(p, src); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	check := func(t *testing.T, files map[string]string) []string {
		t.Helper()
		packages, edges, err := packageGraph(plant(t, files))
		if err != nil {
			t.Fatal(err)
		}
		return packageGraphErrors(packages, edges, wantPackages, wantEdges, mustNotImport)
	}
	if issues := check(t, base); len(issues) != 0 {
		t.Fatalf("the clean planted module failed: %v", issues)
	}
	for _, tc := range []struct {
		name  string
		plant map[string]string
		want  string
	}{
		{"new edge", map[string]string{"proto/bad.go": `package proto; import _ "` + dagModulePath + `/model"`}, `new import edge "proto -> model"`},
		{"forbidden edge", map[string]string{"proto/bad.go": `package proto; import _ "` + dagModulePath + `/model"`}, `forbidden import edge "proto -> model"`},
		{"contract imports anything", map[string]string{"contract/bad.go": `package contract; import _ "` + dagModulePath + `/proto"`}, `forbidden import edge "contract -> proto"`},
		{"new package", map[string]string{"identity/i.go": `package identity`}, `new package "identity"`},
		{"package under testdata", map[string]string{"testdata/bridge/b.go": `package bridge; import _ "` + dagModulePath + `/model"`}, `new import edge "testdata/bridge -> model"`},
		{"import of the module root", map[string]string{"model/bad.go": `package model; import _ "` + dagModulePath + `"`}, `new import edge "model -> ."`},
		{"package in a dot directory", map[string]string{".bridge/b.go": `package bridge; import _ "` + dagModulePath + `/model"`}, `new import edge ".bridge -> model"`},
		{"stale edge", map[string]string{"proto/p.go": `package proto`}, `stale import edge "proto -> contract"`},
		{"stale package", map[string]string{"contract/c.go": "", "model/m.go": `package model`, "proto/p.go": `package proto`}, `stale package "contract"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{}
			for k, v := range base {
				files[k] = v
			}
			for k, v := range tc.plant {
				files[k] = v
			}
			issues := check(t, files)
			if !strings.Contains(strings.Join(issues, "\n"), tc.want) {
				t.Fatalf("planted defect passed (want an issue containing %s), got: %v", tc.want, issues)
			}
		})
	}
	// A symbolic link anywhere in the module is an error: the walk cannot see through it.
	t.Run("symbolic link", func(t *testing.T) {
		root := plant(t, base)
		if err := os.Symlink(filepath.Join(root, "model"), filepath.Join(root, "bridge")); err != nil {
			t.Skipf("this platform cannot create a symbolic link here (%v); CI's Linux runners can", err)
		}
		if _, _, err := packageGraph(root); err == nil {
			t.Fatal("a module holding a symbolic link scanned cleanly")
		}
	})
	// An empty tree is an error, never an empty graph that matches an empty ledger.
	if _, _, err := packageGraph(t.TempDir()); err == nil {
		t.Fatal("an empty module scanned cleanly")
	}
}

func mkdirAllAndWrite(p, src string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(src), 0o644)
}
