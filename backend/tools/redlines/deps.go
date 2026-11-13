// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package redlines

import (
	"bufio"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Banned lists module paths whose purpose is running code a tenant supplied: embeddable
// script interpreters and WebAssembly runtimes. A path matches itself and anything under
// it, so a major-version suffix (/v2) and a sub-package are caught too.
//
// This is a list of what a tenant-code runtime looks like today, not a proof that none
// exists: a new engine is added here when it appears. Declarative evaluators that run no
// tenant program (the CEL predicate core is one) are deliberately not on it.
var Banned = []string{
	"github.com/dop251/goja",
	"github.com/dop251/goja_nodejs",
	"github.com/robertkrimen/otto",
	"go.starlark.net",
	"github.com/google/starlark-go",
	"github.com/traefik/yaegi",
	"github.com/tetratelabs/wazero",
	"github.com/bytecodealliance/wasmtime-go",
	"github.com/wasmerio/wasmer-go",
	"github.com/yuin/gopher-lua",
	"github.com/d5/tengo",
	"rogchap.com/v8go",
	"github.com/rogchap/v8go",
	"github.com/fastschema/qjs",
	"github.com/buke/quickjs-go",
	"github.com/mattn/anko",
	"github.com/cosmos72/gomacro",
	"github.com/arnodel/golua",
	"github.com/Shopify/go-lua",
	"github.com/second-state/WasmEdge-go",
}

// Allow admits findings in the files that sit DIRECTLY in one directory, for a stated reason
// that cites the decision allowing it. It does not extend to subdirectories: an exception
// for a package is not an exception for its siblings or children, and the module that
// requires the runtime needs its own entry for its go.mod and go.sum.
type Allow struct {
	Dir    string // slash-separated, relative to the scan root's working directory
	Reason string
}

var adrCite = regexp.MustCompile(`ADR-\d+`)

// ParseAllow parses "dir=reason". The reason must cite a decision (ADR-nnn): the red line
// allows an exception only behind its own recorded decision, so an allowance that names
// none is refused rather than accepted.
func ParseAllow(spec string) (Allow, error) {
	dir, reason, ok := strings.Cut(spec, "=")
	dir = strings.Trim(strings.TrimSpace(dir), "/")
	if !ok || dir == "" || strings.TrimSpace(reason) == "" {
		return Allow{}, fmt.Errorf("allowance %q must be dir=reason", spec)
	}
	if !adrCite.MatchString(reason) {
		return Allow{}, fmt.Errorf("allowance for %q cites no decision: the reason must name an ADR-nnn", dir)
	}
	return Allow{Dir: dir, Reason: reason}, nil
}

// DepFinding is one forbidden reference.
type DepFinding struct {
	File   string
	Line   int
	Path   string // the reference as written
	Banned string // the Banned entry it matched
}

func (f DepFinding) String() string {
	return fmt.Sprintf("%s:%d: %s is a tenant code runtime (matches %s)", f.File, f.Line, f.Path, f.Banned)
}

// DepResult is what ScanDeps found.
type DepResult struct {
	Findings []DepFinding
	// Allowed counts the findings each allowance admitted, keyed by Allow.Dir.
	Allowed map[string]int
	// Stale lists allowances that admitted nothing.
	Stale   []Allow
	GoFiles int // Go source files whose imports were read
	GoMods  int // go.mod files read
}

// matchBanned returns the Banned entry that p is or is under.
func matchBanned(p string) (string, bool) {
	lp := strings.ToLower(strings.Trim(p, "\"`"))
	for _, b := range Banned {
		lb := strings.ToLower(b)
		if lp == lb || strings.HasPrefix(lp, lb+"/") {
			return b, true
		}
	}
	return "", false
}

// ScanDeps reads every go.mod, go.sum and go.work under the roots, and the
// imports of every Go source file, and reports each reference to a Banned module.
//
// Every token of a module file is tested, not only the require lines, so a replace
// target, an exclude, a retract or a tool directive naming a banned module is caught, as
// is a go.sum line that outlives a removed requirement. Import paths are read from the
// source too, which catches a vendored or replaced copy that go.mod would name by a
// different path. Build constraints are ignored on purpose: a file excluded on this
// platform is still compiled on another.
func ScanDeps(allows []Allow, roots ...string) (*DepResult, error) {
	res := &DepResult{Allowed: map[string]int{}}
	var all []DepFinding
	for _, root := range roots {
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("root %q is not a readable directory", root)
		}
		err := walk(root, skipAlways, func(p string) error {
			base := p[strings.LastIndex(p, "/")+1:]
			switch {
			case base == "go.mod" || base == "go.sum" || base == "go.work":
				if base == "go.mod" {
					res.GoMods++
				}
				f, err := scanModuleFile(p)
				all = append(all, f...)
				return err
			case strings.HasSuffix(base, ".go"):
				if inFixtures(p) {
					return nil
				}
				res.GoFiles++
				f, err := scanImports(p)
				all = append(all, f...)
				return err
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	used := map[string]int{}
	for _, f := range all {
		if a, ok := admitting(allows, f.File); ok {
			used[a.Dir]++
			continue
		}
		res.Findings = append(res.Findings, f)
	}
	res.Allowed = used
	for _, a := range allows {
		if used[a.Dir] == 0 {
			res.Stale = append(res.Stale, a)
		}
	}
	sort.Slice(res.Findings, func(i, j int) bool {
		a, b := res.Findings[i], res.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return res, nil
}

func inFixtures(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "testdata" {
			return true
		}
	}
	return false
}

// admitting returns the allowance whose directory contains file.
func admitting(allows []Allow, file string) (Allow, bool) {
	for _, a := range allows {
		if dirOf(file) == a.Dir {
			return a, true
		}
	}
	return Allow{}, false
}

func scanModuleFile(path string) ([]DepFinding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []DepFinding
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for n := 1; sc.Scan(); n++ {
		// A trailing comment is not read for a go.mod (it may mention a module while
		// explaining why it is absent), but is not trusted to hide one: only text after
		// "//" is dropped, and go.sum has no comments.
		line := sc.Text()
		if strings.HasSuffix(path, ".sum") {
			// A "/go.mod" line records only that a module's go.mod was read to build the
			// module graph, which pruned-graph loading does for dependencies of
			// dependencies that no package here ever imports. A content-hash line means
			// the module's source is part of a build, which is the thing refused.
			if f := strings.Fields(line); len(f) >= 2 && strings.HasSuffix(f[1], "/go.mod") {
				continue
			}
		} else {
			line = stripComment(line)
		}
		for _, tok := range strings.Fields(line) {
			if b, ok := matchBanned(tok); ok {
				out = append(out, DepFinding{File: path, Line: n, Path: strings.Trim(tok, "\"`"), Banned: b})
			}
		}
	}
	return out, sc.Err()
}

func scanImports(path string) ([]DepFinding, error) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		// A file that does not parse cannot be shown clean. Generated or templated
		// files with a Go suffix are the usual cause; they must not hide an import.
		return nil, fmt.Errorf("cannot read imports of %s: %w", path, err)
	}
	var out []DepFinding
	for _, imp := range af.Imports {
		p, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil {
			continue
		}
		if b, ok := matchBanned(p); ok {
			out = append(out, DepFinding{File: path, Line: fset.Position(imp.Pos()).Line, Path: p, Banned: b})
		}
	}
	return out, nil
}

func dirOf(file string) string {
	i := strings.LastIndex(file, "/")
	if i < 0 {
		return "."
	}
	return file[:i]
}
