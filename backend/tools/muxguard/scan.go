// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package muxguard finds registrations on net/http's package-global
// http.DefaultServeMux, and the several ways a server ends up serving it by omission.
//
// # Why this exists
//
// Every HTTP server in this repository serves an explicit Handler — the mux its
// Microservice owns. That makes http.DefaultServeMux a mux NOTHING SERVES. A
// registration on it still compiles, still runs and still returns; the route simply is
// not there, with no error and no log line, and the endpoint answers 404 to whatever
// asked for it — a chart probe, the ingress, or a peer service fetching JWKS.
//
// # Why it parses rather than greps
//
// A text scan of this was written first and was defeated in minutes, three ways that
// all compile and all survive gofmt: an alias through a variable (`var reg =
// http.Handle`), a call split across lines, and a helper placed outside the subtree the
// scan walked. Parsing answers the first two by construction — the AST has no line
// breaks, and the selector is present wherever the function VALUE is named, whether it
// is called there or not.
//
// # Why the checks below are broader than "http.Handle"
//
// The first parsing version was defeated too, and every one of those evasions is now a
// case here. The lesson worth keeping is that "registers on the default mux" has many
// more spellings than the obvious one: a second import of net/http under another name,
// a package whose init registers, a Handler field present but nil, and a whole family of
// net/http functions that take the handler as an argument and read nil as "the default
// mux". Scan's doc comment lists the ones still open.
package muxguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// registrars are the net/http members that reach the package-global mux. Naming any of
// them is a finding: Handle and HandleFunc write to it, and DefaultServeMux itself is
// how it gets handed to a registrar that takes a *http.ServeMux — the indirect shape
// that hid two of user-management's ten registration sites from the first enumeration
// of this work.
var registrars = map[string]string{
	"Handle":          "registers on http.DefaultServeMux",
	"HandleFunc":      "registers on http.DefaultServeMux",
	"DefaultServeMux": "names http.DefaultServeMux",
}

// nilHandlerFuncs take the handler as their LAST argument and read a nil there as "use
// http.DefaultServeMux". None of them names a watched symbol, so they defeat any check
// looking only for Handle, HandleFunc or DefaultServeMux.
//
// The httptest pair is here for the same reason and is genuinely reachable: a test
// server built with a nil handler serves the default mux, which is how a test can pass
// against routes production does not serve.
var nilHandlerFuncs = map[string]map[string]bool{
	"net/http":          {"ListenAndServe": true, "ListenAndServeTLS": true, "Serve": true, "ServeTLS": true},
	"net/http/httptest": {"NewServer": true, "NewTLSServer": true, "NewUnstartedServer": true},
}

// initRegistrars register on http.DefaultServeMux from their own package init, so
// IMPORTING THEM IS THE REGISTRATION. A blank import is the usual spelling and names
// nothing at all at a call site.
//
// 🔴 THIS IS THE EVASION A DEVELOPER IS MOST LIKELY TO WRITE BY ACCIDENT. Someone adds
// pprof to profile a service; it mounts /debug/pprof/ on a mux nobody serves; the
// profiling endpoint answers 404 and the reason is one import line with no call beside
// it. Each was read in the dependency's own source rather than taken on report — the
// init functions are at net/http/pprof/pprof.go:96, expvar/expvar.go:380 and
// x/net/trace/trace.go:130.
var initRegistrars = map[string]string{
	"net/http/pprof":         "net/http/pprof mounts /debug/pprof/ on http.DefaultServeMux from its init",
	"expvar":                 "expvar mounts /debug/vars on http.DefaultServeMux from its init",
	"golang.org/x/net/trace": "golang.org/x/net/trace mounts /debug/requests on http.DefaultServeMux from its init",
}

// Finding is one place a file reaches the default mux.
type Finding struct {
	Pos     token.Position
	Message string
	Source  string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d:%d: %s: %s", f.Pos.Filename, f.Pos.Line, f.Pos.Column, f.Message, f.Source)
}

// Result is what a scan saw, including how much it saw. PerRoot is the count of files
// parsed under each root — a caller that does not check it cannot tell a clean tree from
// a tree it never read.
type Result struct {
	PerRoot  map[string]int
	Findings []Finding
}

// Files is the total parsed across every root.
func (r Result) Files() int {
	n := 0
	for _, c := range r.PerRoot {
		n += c
	}
	return n
}

// Scan parses every non-test .go file under each root and reports the registrations.
//
// 🔴 WHAT IT CANNOT SEE. The list is long on purpose. A guard's claim of exhaustiveness
// is the thing a reviewer attacks, and this one has already been attacked twice — the
// first version as a grep, the second as a parser — so what remains open is written
// down rather than left to be discovered:
//
//   - A Handler field set to a variable that is nil at run time: `Handler: h` where h is
//     a nil-valued var. Distinguishing that from `Handler: mux` needs type and flow
//     analysis; guessing from the syntax would flag the CORRECT pattern, which two
//     servers in this repository use.
//   - A server built without a composite literal at all — `new(http.Server)`,
//     `var s http.Server` — where Handler is simply never assigned. Flagging those would
//     refuse the legitimate build-then-assign pattern, so they are left open rather than
//     guessed at.
//   - A literal that sets Handler and a later statement that clears it.
//   - A type alias for http.Server, or a struct EMBEDDING it, since the composite
//     literal then names neither http nor Server.
//   - Reflection, and any mux value obtained at run time from beyond the roots. Note
//     that reflection still has to NAME http.Handle or http.DefaultServeMux to get a
//     value to reflect on, so the common shapes are caught by the selector check.
//   - _test.go files are SKIPPED. Tests here deliberately register on the default mux to
//     prove a server does not serve it.
//   - testdata directories are skipped, as go build skips them: a Go tool that parses Go
//     is entitled to keep deliberately malformed fixtures, and a parse error is fatal
//     here.
//   - Anything outside the roots the caller passes, including the module cache.
//   - A build-tagged file excluded from a given build is still parsed — this reads
//     syntax, not a build configuration — so it over-reports rather than under-reports.
//
// A dot-import of net/http is the one hole in the selector analysis, and it is closed by
// refusing it outright rather than by trying to resolve it.
func Scan(roots ...string) (Result, error) {
	res := Result{PerRoot: map[string]int{}}
	fset := token.NewFileSet()

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				// _legacy is the archived pre-migration tree: not in the workspace, not
				// built, and explicitly not maintained. testdata is skipped for the same
				// reason go build skips it — it may hold source that is not meant to
				// compile, and a parse error here is fatal.
				case "_legacy", "vendor", "node_modules", "testdata":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return fmt.Errorf("parsing %s: %w", path, err)
			}
			res.PerRoot[root]++
			res.Findings = append(res.Findings, scanFile(fset, file)...)
			return nil
		})
		if err != nil {
			return res, err
		}
	}

	sort.Slice(res.Findings, func(i, j int) bool {
		if res.Findings[i].Pos.Filename != res.Findings[j].Pos.Filename {
			return res.Findings[i].Pos.Filename < res.Findings[j].Pos.Filename
		}
		return res.Findings[i].Pos.Line < res.Findings[j].Pos.Line
	})
	return res, nil
}

func scanFile(fset *token.FileSet, file *ast.File) []Finding {
	var out []Finding
	at := func(p token.Pos) token.Position { return fset.Position(p) }

	// 🔴 EVERY LOCAL NAME BOUND TO net/http, NOT THE LAST ONE. Go permits the same
	// package to be imported twice under different names in one file, and a loop that
	// assigned a single `local` watched only whichever came last — so the other name
	// registered freely. That is a real registration, not a theoretical one.
	httpNames := map[string]bool{}
	httptestNames := map[string]bool{}

	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}

		// An init-registering package is a finding on the IMPORT, because the import is
		// the registration. Reported whether the import is blank, named or plain.
		if why, found := initRegistrars[p]; found {
			out = append(out, Finding{
				Pos:     at(imp.Pos()),
				Message: why,
				Source:  "import " + imp.Path.Value,
			})
		}

		name := ""
		switch {
		case imp.Name == nil:
			name = filepath.Base(p) // net/http -> http, net/http/httptest -> httptest
		case imp.Name.Name == ".":
			// A dot-import erases the selector this analysis depends on: Handle("/x", h)
			// would be indistinguishable from any other bare call. Refuse it rather than
			// report a clean file that cannot be checked.
			if p == "net/http" {
				out = append(out, Finding{
					Pos:     at(imp.Pos()),
					Message: "dot-imports net/http, which makes a default-mux registration unanalyzable here",
					Source:  "import . " + imp.Path.Value,
				})
			}
			continue
		case imp.Name.Name == "_":
			// A blank import binds no name; its only effect is the init above.
			continue
		default:
			name = imp.Name.Name
		}

		switch p {
		case "net/http":
			httpNames[name] = true
		case "net/http/httptest":
			httptestNames[name] = true
		}
	}
	if len(httpNames) == 0 && len(httptestNames) == 0 {
		return out
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			// 🔴 MATCHED WHEREVER THE NAME APPEARS, NOT ONLY AT A CALL. `var reg =
			// http.Handle` names the function without calling it, and the call through
			// reg is then invisible to anything looking for a call expression.
			id, ok := node.X.(*ast.Ident)
			if !ok || !httpNames[id.Name] {
				return true
			}
			if why, found := registrars[node.Sel.Name]; found {
				out = append(out, Finding{
					Pos:     at(node.Pos()),
					Message: why,
					Source:  id.Name + "." + node.Sel.Name,
				})
			}

		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			pkg := ""
			switch {
			case httpNames[id.Name]:
				pkg = "net/http"
			case httptestNames[id.Name]:
				pkg = "net/http/httptest"
			default:
				return true
			}
			if !nilHandlerFuncs[pkg][sel.Sel.Name] || len(node.Args) == 0 {
				return true
			}
			// The handler is the last argument in every one of these signatures, and a
			// literal nil there means the default mux.
			if isNilIdent(node.Args[len(node.Args)-1]) {
				out = append(out, Finding{
					Pos:     at(node.Pos()),
					Message: "passes a nil handler, which serves http.DefaultServeMux",
					Source:  id.Name + "." + sel.Sel.Name + "(..., nil)",
				})
			}

		case *ast.CompositeLit:
			sel, ok := node.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || !httpNames[id.Name] || sel.Sel.Name != "Server" {
				return true
			}
			// Present-but-nil is the same thing as absent, and reads as deliberate — a
			// check that only asked whether the KEY appeared accepted `Handler: nil`.
			val, present := fieldValue(node, "Handler")
			if !present {
				out = append(out, Finding{
					Pos:     at(node.Pos()),
					Message: "http.Server literal with no Handler, which serves http.DefaultServeMux by omission",
					Source:  id.Name + ".Server{...}",
				})
			} else if isNilIdent(val) {
				out = append(out, Finding{
					Pos:     at(node.Pos()),
					Message: "http.Server literal with an explicitly nil Handler, which serves http.DefaultServeMux",
					Source:  id.Name + ".Server{Handler: nil}",
				})
			}
		}
		return true
	})
	return out
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// fieldValue reports a keyed field's value and whether the key was present at all. The
// two answers are different: absent means the zero value, present-and-nil means somebody
// wrote it.
func fieldValue(lit *ast.CompositeLit, name string) (ast.Expr, bool) {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == name {
			return kv.Value, true
		}
	}
	return nil, false
}
