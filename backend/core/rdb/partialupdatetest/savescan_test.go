// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partialupdatetest

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The whole-row-Save guard.
//
// An exported Update* method on a service model Api that loads a row, applies the request's
// named fields to the struct and then calls .Save(row) rewrites EVERY column from the
// stale read, so a concurrent writer's change to a field the request omitted is silently
// reverted (customers #2447, areas #2452 and notification channels were each fixed one at
// a time). The fix shape is to write only the named columns (Model(row).Omit(clause.
// Associations).Updates(map) or rdb.AdvancingFrom), validating before writing.
//
// This test parses every non-test source file under backend/services/*/model and fails on
// any Save call inside an exported Update* function that is not (a) version-guarded by
// rdb.AdvancingFrom in the same call chain, or (b) listed, with a reason, in
// savescan_allowlist.txt. A listed entry that no longer offends also fails, so a fix PR
// must delete its line. It reads files outside its own module, so it is only as fresh as
// `go test -count=1` (CI passes it).
//
// Scope limit: only Save calls written directly inside the Update* function body (closures
// included). A Save hidden in a helper the method calls is not seen.

const savescanAllowlist = "savescan_allowlist.txt"

// savescanFinding is one offending (file, function) pair.
type savescanFinding struct {
	File string // slash path relative to backend/services
	Func string
	Line int
}

func (f savescanFinding) key() string { return f.File + " " + f.Func }

// scanSaveOffenders reports every exported Update* function in src that calls .Save(...)
// outside an rdb.AdvancingFrom chain.
func scanSaveOffenders(file, src string) ([]savescanFinding, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		return nil, err
	}
	var out []savescanFinding
	for _, d := range parsed.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !fn.Name.IsExported() || !strings.HasPrefix(fn.Name.Name, "Update") {
			continue
		}
		seen := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if seen {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Save" {
				return true
			}
			if chainCalls(sel.X, "AdvancingFrom") {
				return true // version-guarded: a concurrent change fails instead of reverting
			}
			seen = true
			out = append(out, savescanFinding{File: file, Func: fn.Name.Name, Line: fset.Position(call.Pos()).Line})
			return false
		})
	}
	return out, nil
}

// chainCalls reports whether expr's receiver chain contains a call to a function named name.
func chainCalls(expr ast.Expr, name string) bool {
	for {
		call, ok := expr.(*ast.CallExpr)
		if !ok {
			return false
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fun.Sel.Name == name {
				return true
			}
			expr = fun.X
		case *ast.Ident:
			return fun.Name == name
		default:
			return false
		}
	}
}

func loadSavescanAllowlist(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(savescanAllowlist)
	if err != nil {
		t.Fatalf("open allowlist: %v", err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry, reason, found := strings.Cut(line, "#")
		fields := strings.Fields(entry)
		if len(fields) != 2 || !found || strings.TrimSpace(reason) == "" {
			t.Fatalf("allowlist line %q: want `<file> <Func> # <reason>`; a reason is mandatory", line)
		}
		out[fields[0]+" "+fields[1]] = strings.TrimSpace(reason)
	}
	return out
}

func TestNoWholeRowSaveInUpdateMethods(t *testing.T) {
	root := filepath.Join("..", "..", "..", "services")
	files, err := filepath.Glob(filepath.Join(root, "*", "model", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no model sources found under %s (err=%v): the guard would pass vacuously", root, err)
	}
	var found []savescanFinding
	for _, p := range files {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, p)
		fs, err := scanSaveOffenders(filepath.ToSlash(rel), string(b))
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		found = append(found, fs...)
	}
	allow := loadSavescanAllowlist(t)
	hit := map[string]bool{}
	var bad []string
	for _, f := range found {
		hit[f.key()] = true
		if _, ok := allow[f.key()]; !ok {
			bad = append(bad, f.key()+" (line "+strconv.Itoa(f.Line)+")")
		}
	}
	var stale []string
	for k := range allow {
		if !hit[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(bad)
	sort.Strings(stale)
	if len(bad) > 0 {
		t.Errorf("Update* methods that Save a whole loaded row (a concurrent writer's change to an omitted field is reverted).\n"+
			"Write only the named columns (Model(row).Omit(clause.Associations).Updates(map) / rdb.AdvancingFrom) with a race test, or,\n"+
			"for a genuine whole-object replacement, add a justified line to %s:\n  %s", savescanAllowlist, strings.Join(bad, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("stale %s entries (no longer offending; delete them):\n  %s", savescanAllowlist, strings.Join(stale, "\n  "))
	}
	if len(found) == 0 {
		t.Errorf("scanner found no offenders at all: either every debt is paid (then delete this guard's allowlist expectations) or the scanner is broken")
	}
}

// TestSaveScannerCatchesAPlantedOffender is the negative control: a scanner that cannot
// fail guards nothing.
func TestSaveScannerCatchesAPlantedOffender(t *testing.T) {
	const bad = `package m
func (api *Api) UpdateWidget(ctx context.Context, token string) (*W, error) {
	found, _ := api.WidgetByToken(ctx, token)
	found.Name = "x"
	result := api.RDB.DB(ctx).Omit("Foo").Save(found)
	return found, result.Error
}
func (api *Api) UpdateInTx(ctx context.Context) error {
	return api.RDB.Tx(ctx, func(tx *gorm.DB) error { return tx.Save(row).Error })
}
`
	const good = `package m
func (api *Api) UpdateWidget(ctx context.Context) error {
	return api.RDB.DB(ctx).Model(row).Updates(map[string]any{"name": "x"}).Error
}
func (api *Api) UpdateGuarded(ctx context.Context) error {
	return rdb.AdvancingFrom(tx, readAt).Omit("Rules").Save(p).Error
}
func (api *Api) createThing(ctx context.Context) error { return db.Save(x).Error }
func (api *Api) UpdateNested(ctx context.Context) error {
	return rdb.AdvancingFrom(tx, readAt).Where("a").Omit("Rules").Save(p).Error
}
`
	got, err := scanSaveOffenders("m/x.go", bad)
	if err != nil || len(got) != 2 || got[0].Func != "UpdateWidget" || got[1].Func != "UpdateInTx" {
		t.Fatalf("planted offenders not caught: %+v err=%v", got, err)
	}
	if got, err := scanSaveOffenders("m/y.go", good); err != nil || len(got) != 0 {
		t.Fatalf("false positives on safe shapes: %+v err=%v", got, err)
	}
}
