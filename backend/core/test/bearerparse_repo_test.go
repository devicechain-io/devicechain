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
)

// These doors authenticate device/integration credentials rather than platform JWTs.
var bearerParseSites = map[string]string{
	"core/auth/bearer.go": "the shared JWT bearer parser",
	"services/event-sources/processor/http_integration.go":   "integration credentials",
	"services/event-sources/processor/http_device_uplink.go": "device credentials",
	"services/mcp/server/server.go":                          "SDK RequireBearerToken middleware",
}

type bearerSite struct {
	file              string
	reads, constructs bool
	pkg               string
}

func bearerSites(root string) ([]bearerSite, error) {
	var sites []bearerSite
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			if rel == "sims" || rel == "tools" {
				return filepath.SkipDir
			}
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		site := bearerSite{file: filepath.ToSlash(rel), pkg: f.Name.Name}
		constants := map[string]string{}
		writes := map[*ast.IndexExpr]bool{}
		value := func(e ast.Expr) string {
			if l, ok := e.(*ast.BasicLit); ok && l.Kind == token.STRING {
				s, _ := strconv.Unquote(l.Value)
				return s
			}
			return ""
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if a, ok := n.(*ast.AssignStmt); ok && a.Tok == token.ASSIGN {
				for _, lhs := range a.Lhs {
					if idx, ok := lhs.(*ast.IndexExpr); ok {
						writes[idx] = true
					}
				}
			}
			if v, ok := n.(*ast.ValueSpec); ok {
				for i, e := range v.Values {
					if i < len(v.Names) {
						constants[v.Names[i].Name] = value(e)
					}
				}
			}
			return true
		})
		literal := func(e ast.Expr) string {
			if id, ok := e.(*ast.Ident); ok {
				return constants[id.Name]
			}
			return value(e)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
					if sel.Sel.Name == "Get" && len(x.Args) > 0 && strings.EqualFold(literal(x.Args[0]), "Authorization") {
						site.reads = true
					}
					if sel.Sel.Name == "RequireBearerToken" {
						site.reads = true
					}
					switch sel.Sel.Name {
					case "HasPrefix", "EqualFold", "TrimPrefix":
						for _, a := range x.Args {
							if strings.EqualFold(literal(a), "Bearer ") {
								site.reads = true
							}
						}
					}
				}
			case *ast.IndexExpr:
				if !writes[x] && strings.EqualFold(literal(x.Index), "Authorization") {
					site.reads = true
				}
			case *ast.BinaryExpr:
				if x.Op == token.EQL || x.Op == token.NEQ {
					if strings.EqualFold(literal(x.X), "Bearer ") || strings.EqualFold(literal(x.Y), "Bearer ") {
						site.reads = true
					}
				}
			case *ast.CompositeLit:
				if sel, ok := x.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "BearerAuth" {
					site.constructs = true
				}
			}
			return true
		})
		if site.reads || site.constructs {
			sites = append(sites, site)
		}
		return nil
	})
	return sites, err
}

func bearerViolations(root string, allowed map[string]string) ([]string, error) {
	sites, err := bearerSites(root)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var failures []string
	for _, s := range sites {
		if s.reads {
			seen[s.file] = true
			if _, ok := allowed[s.file]; !ok {
				failures = append(failures, s.file+": hand-rolled bearer parsing")
			}
		}
		if s.constructs {
			matches, err := filepath.Glob(filepath.Join(root, filepath.Dir(s.file), "*_test.go"))
			if err != nil {
				return nil, err
			}
			covered := false
			for _, path := range matches {
				f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
				if err != nil {
					return nil, err
				}
				if f.Name.Name != s.pkg && f.Name.Name != s.pkg+"_test" {
					continue
				}
				tables := map[string]bool{}
				for _, imp := range f.Imports {
					path, _ := strconv.Unquote(imp.Path.Value)
					if path == "github.com/devicechain-io/dc-microservice/auth/authtest" {
						name := "authtest"
						if imp.Name != nil {
							name = imp.Name.Name
						}
						tables[name] = true
					}
				}
				ast.Inspect(f, func(n ast.Node) bool {
					if c, ok := n.(*ast.CallExpr); ok {
						if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "RunBearerCases" {
							if pkg, ok := sel.X.(*ast.Ident); ok && tables[pkg.Name] {
								covered = true
							}
						}
					}
					return true
				})
			}
			if !covered {
				failures = append(failures, s.file+": BearerAuth has no same-package RunBearerCases test")
			}
		}
	}
	for file := range allowed {
		if !seen[file] {
			failures = append(failures, file+": stale bearer parser allowlist entry")
		}
	}
	return failures, nil
}

func TestBearerParsingIsCentralized(t *testing.T) {
	failures, err := bearerViolations("../..", bearerParseSites)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range failures {
		t.Error(failure)
	}
}

// The planted bypass must be named, even when the prefix is stored in a constant.
func TestBearerParsingGuardNegativeControls(t *testing.T) {
	const header = "// Copyright The DeviceChain Authors\n// SPDX-License-Identifier: Apache-2.0\n\n"

	for name, subject := range map[string]string{
		"prefix":            `const prefix="Bearer "; func f(s string)bool{return strings.HasPrefix(s,prefix)}`,
		"header_get":        `func f(h http.Header)string{return h.Get("aUtHoRiZaTiOn")}`,
		"header_index":      `func f(h http.Header)[]string{return h["authorization"]}`,
		"uncovered_adopter": `var a=auth.BearerAuth{}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "planted.go")
			if err := os.WriteFile(path, []byte(header+"package planted\n"+subject), 0600); err != nil {
				t.Fatal(err)
			}
			failures, err := bearerViolations(root, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(failures) != 1 || !strings.Contains(failures[0], "planted.go") {
				t.Fatalf("planted bypass not named: %v", failures)
			}
		})
	}
	t.Run("client_setters", func(t *testing.T) {
		root := t.TempDir()
		src := header + `package client
  func f(h http.Header){h.Set("Authorization","Bearer token");h["Authorization"]=[]string{"Bearer token"}}`
		if err := os.WriteFile(filepath.Join(root, "client.go"), []byte(src), 0600); err != nil {
			t.Fatal(err)
		}
		failures, err := bearerViolations(root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 0 {
			t.Fatalf("client setters rejected: %v", failures)
		}
	})

	for _, tc := range []struct {
		name, path string
		covered    bool
	}{
		{"aliased_table", "github.com/devicechain-io/dc-microservice/auth/authtest", true},
		{"unrelated_table", "example.invalid/fake", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			subject := header + "package planted\nimport auth \"github.com/devicechain-io/dc-microservice/auth\"\nvar a=auth.BearerAuth{}\n"
			if err := os.WriteFile(filepath.Join(root, "planted.go"), []byte(subject), 0600); err != nil {
				t.Fatal(err)
			}
			src := header + "package planted\nimport (\"testing\"; cases " + strconv.Quote(tc.path) + ")\nfunc TestBearer(t *testing.T){cases.RunBearerCases(t,\"\",nil)}\n"
			if err := os.WriteFile(filepath.Join(root, "planted_test.go"), []byte(src), 0600); err != nil {
				t.Fatal(err)
			}
			failures, err := bearerViolations(root, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if tc.covered {
				want = 0
			}
			if len(failures) != want {
				t.Fatalf("coverage failures=%v, want %d", failures, want)
			}
		})
	}

	t.Run("stale_allowlist", func(t *testing.T) {
		failures, err := bearerViolations(t.TempDir(), map[string]string{"gone.go": "stale"})
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 1 || !strings.Contains(failures[0], "gone.go") {
			t.Fatalf("stale entry not named: %v", failures)
		}
	})
}
