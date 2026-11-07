// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package redlines

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// tree writes files (slash path -> body) under a fresh temp dir and returns it. The
// process chdirs there so scan paths come out relative, as they do in CI.
func tree(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for p, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

const okMod = "module example.com/m\n\ngo 1.26\n"

func depFindings(t *testing.T, allows []Allow) *DepResult {
	t.Helper()
	res, err := ScanDeps(allows, ".")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func wantDep(t *testing.T, res *DepResult, file string, line int, banned string) {
	t.Helper()
	for _, f := range res.Findings {
		if f.File == file && f.Line == line && f.Banned == banned {
			return
		}
	}
	t.Fatalf("no finding %s:%d for %s; got %v", file, line, banned, res.Findings)
}

func TestDepsPlantedImport(t *testing.T) {
	tree(t, map[string]string{
		"go.mod": okMod,
		"x.go":   "package p\n\nimport (\n\t\"fmt\"\n\t\"github.com/dop251/goja\"\n)\n\nvar _ = fmt.Sprint\n",
	})
	res := depFindings(t, nil)
	wantDep(t, res, "x.go", 5, "github.com/dop251/goja")
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %v", res.Findings)
	}
}

func TestDepsEveryShapeOfReference(t *testing.T) {
	cases := map[string]map[string]string{
		"require":           {"go.mod": okMod + "\nrequire github.com/dop251/goja v0.0.0\n"},
		"grouped require":   {"go.mod": okMod + "\nrequire (\n\tgithub.com/a/b v1.0.0\n\tgithub.com/tetratelabs/wazero v1.2.3 // indirect\n)\n"},
		"replace target":    {"go.mod": okMod + "\nreplace github.com/a/b => go.starlark.net v0.0.0\n"},
		"replace source":    {"go.mod": okMod + "\nreplace github.com/traefik/yaegi => ../fork\n"},
		"exclude":           {"go.mod": okMod + "\nexclude github.com/robertkrimen/otto v1.0.0\n"},
		"tool directive":    {"go.mod": okMod + "\ntool github.com/bytecodealliance/wasmtime-go/v14\n"},
		"go.sum":            {"go.mod": okMod, "go.sum": "github.com/dop251/goja v1.0.0 h1:abc=\ngithub.com/dop251/goja v1.0.0/go.mod h1:abc=\n"},
		"go.work replace":   {"go.mod": okMod, "go.work": "go 1.26\nreplace github.com/dop251/goja => ./x\n"},
		"major suffix":      {"go.mod": okMod + "\nrequire github.com/tetratelabs/wazero/v2 v2.0.0\n"},
		"subpackage import": {"go.mod": okMod, "x.go": "package p\nimport _ \"github.com/dop251/goja/parser\"\n"},
		"alias import":      {"go.mod": okMod, "x.go": "package p\nimport js \"github.com/robertkrimen/otto\"\nvar _ = js.New\n"},
		"blank import":      {"go.mod": okMod, "x.go": "package p\nimport _ \"go.starlark.net/starlark\"\n"},
		"build tagged":      {"go.mod": okMod, "x.go": "//go:build never\n\npackage p\nimport _ \"github.com/dop251/goja\"\n"},
		"case folded":       {"go.mod": okMod + "\nrequire GitHub.com/Dop251/Goja v1.0.0\n"},
		"test file":         {"go.mod": okMod, "x_test.go": "package p\nimport _ \"github.com/dop251/goja\"\n"},
		"quoted":            {"go.mod": okMod + "\nrequire \"github.com/dop251/goja\" v1.0.0\n"},
		"nested module":     {"go.mod": okMod, "deep/er/go.mod": okMod + "\nrequire github.com/d5/tengo/v2 v2.0.0\n"},
		"testdata go.mod":   {"go.mod": okMod, "a/testdata/go.mod": okMod + "\nrequire github.com/yuin/gopher-lua v1.0.0\n"},
		"v8go":              {"go.mod": okMod + "\nrequire rogchap.com/v8go v0.9.0\n"},
		"goja nodejs":       {"go.mod": okMod + "\nrequire github.com/dop251/goja_nodejs v0.0.0\n"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			tree(t, files)
			res := depFindings(t, nil)
			if len(res.Findings) == 0 {
				t.Fatal("a tenant code runtime reference was not reported")
			}
		})
	}
}

func TestDepsCleanShapes(t *testing.T) {
	cases := map[string]map[string]string{
		"plain": {"go.mod": okMod + "\nrequire github.com/google/cel-go v0.20.0\n", "x.go": "package p\nimport _ \"github.com/google/cel-go/cel\"\n"},
		// A lookalike that merely starts with a banned name is a different module.
		// A go.mod-only checksum line is the module graph, not the build; go.work.sum is
		// workspace-wide residue that nothing tidies.
		"sum go.mod line":   {"go.mod": okMod, "go.sum": "github.com/tetratelabs/wazero v1.0.2/go.mod h1:abc=\n"},
		"go.work.sum":       {"go.mod": okMod, "go.work.sum": "go.starlark.net v1.0.0 h1:abc=\n"},
		"prefix lookalike":  {"go.mod": okMod + "\nrequire github.com/dop251/gojafoo v1.0.0\n"},
		"unrelated wazero":  {"go.mod": okMod + "\nrequire github.com/tetratelabs/wazeroish v1.0.0\n"},
		"comment mentions":  {"go.mod": okMod + "\n// we do not use github.com/dop251/goja\n"},
		"testdata source":   {"go.mod": okMod, "a/testdata/x.go": "package p\nimport _ \"github.com/dop251/goja\"\n"},
		"skipped directory": {"go.mod": okMod, "node_modules/p/go.mod": okMod + "\nrequire github.com/dop251/goja v1.0.0\n"},
		"legacy":            {"go.mod": okMod, "_legacy/go.mod": okMod + "\nrequire github.com/dop251/goja v1.0.0\n"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			tree(t, files)
			res := depFindings(t, nil)
			if len(res.Findings) != 0 {
				t.Fatalf("false positive: %v", res.Findings)
			}
		})
	}
}

func TestDepsAllowanceIsExactDirectoryAndStaleIsReported(t *testing.T) {
	tree(t, map[string]string{
		"go.mod":           okMod,
		"sandbox/x.go":     "package s\nimport _ \"github.com/tetratelabs/wazero\"\n",
		"sandbox/sub/y.go": "package sub\nimport _ \"github.com/tetratelabs/wazero\"\n",
		"other/z.go":       "package o\nimport _ \"github.com/tetratelabs/wazero\"\n",
	})
	allow := []Allow{{Dir: "sandbox", Reason: "ADR-999 typed decode ABI"}}
	res := depFindings(t, allow)
	if res.Allowed["sandbox"] != 1 {
		t.Fatalf("allowance admitted %d, want exactly 1 (sandbox/x.go)", res.Allowed["sandbox"])
	}
	files := map[string]bool{}
	for _, f := range res.Findings {
		files[f.File] = true
	}
	if !files["sandbox/sub/y.go"] || !files["other/z.go"] || files["sandbox/x.go"] || len(res.Findings) != 2 {
		t.Fatalf("allowance must cover its own directory only; findings = %v", res.Findings)
	}
	if len(res.Stale) != 0 {
		t.Fatalf("stale = %v", res.Stale)
	}

	res = depFindings(t, []Allow{{Dir: "nowhere", Reason: "ADR-999 x"}})
	if len(res.Stale) != 1 || res.Stale[0].Dir != "nowhere" {
		t.Fatalf("a stale allowance must be reported, got %v", res.Stale)
	}
}

func TestParseAllow(t *testing.T) {
	good, err := ParseAllow(" backend/x/ = ADR-123: typed decode ABI ")
	if err != nil || good.Dir != "backend/x" || !strings.Contains(good.Reason, "ADR-123") {
		t.Fatalf("good = %+v, %v", good, err)
	}
	for _, bad := range []string{"", "dir", "dir=", "=reason ADR-1", "dir=because we want it", "dir=ADR-x"} {
		if _, err := ParseAllow(bad); err == nil {
			t.Errorf("ParseAllow(%q) accepted", bad)
		}
	}
}

func TestDepsUnreadableInputs(t *testing.T) {
	tree(t, map[string]string{"go.mod": okMod, "bad.go": "package p\nimport (\n"})
	if _, err := ScanDeps(nil, "."); err == nil {
		t.Fatal("a Go file that does not parse must be an error, not clean")
	}
	if _, err := ScanDeps(nil, "does-not-exist"); err == nil {
		t.Fatal("a missing root must be an error")
	}
}

// Module paths are case-sensitive, and some runtimes are published with capitals; the
// match folds case on both sides so neither spelling slips by.
func TestBannedMatchFoldsCase(t *testing.T) {
	for _, p := range []string{"github.com/Shopify/go-lua", "github.com/shopify/go-lua", "github.com/second-state/WasmEdge-go/wasmedge", "github.com/second-state/wasmedge-go"} {
		if _, ok := matchBanned(p); !ok {
			t.Errorf("%s is not banned", p)
		}
	}
}

func TestDepsFindingsAreOrderedByFileThenLine(t *testing.T) {
	tree(t, map[string]string{
		"go.mod": okMod,
		"a/x.go": "package a\n\nimport _ \"go.starlark.net/starlark\"\nimport _ \"github.com/dop251/goja\"\n",
		"b/y.go": "package b\nimport _ \"github.com/traefik/yaegi\"\n",
	})
	res := depFindings(t, nil)
	var got []string
	for _, f := range res.Findings {
		got = append(got, f.File+":"+strconv.Itoa(f.Line))
	}
	want := []string{"a/x.go:3", "a/x.go:4", "b/y.go:2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestDepsCounts(t *testing.T) {
	tree(t, map[string]string{"go.mod": okMod, "a/go.mod": okMod, "a/x.go": "package a\n", "y.go": "package p\n", "y_test.go": "package p\n"})
	res := depFindings(t, nil)
	if res.GoMods != 2 || res.GoFiles != 3 {
		t.Fatalf("counts = %d mods, %d files", res.GoMods, res.GoFiles)
	}
}

func TestBannedListCoversTheNamedRuntimes(t *testing.T) {
	for _, m := range []string{"github.com/dop251/goja", "github.com/robertkrimen/otto", "go.starlark.net",
		"github.com/traefik/yaegi", "github.com/tetratelabs/wazero", "github.com/bytecodealliance/wasmtime-go"} {
		if _, ok := matchBanned(m); !ok {
			t.Errorf("%s is not banned", m)
		}
		if _, ok := matchBanned(m + "/v9"); !ok {
			t.Errorf("%s/v9 is not banned", m)
		}
	}
}
