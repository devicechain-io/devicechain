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
	"sync"
	"testing"
)

// reservedAuthorities are in the vocabulary a minor before anything checks them, so that
// a previous-release user-management pod already accepts a role naming them when the
// release that consumes them rolls out. Until then a grant of one must reach NOTHING.
// The constant name and the wire spelling are both listed: a resolver may check either.
//
// When the consuming feature ships, its entry leaves this table in the same change.
var reservedAuthorities = []struct{ constant, name string }{
	{"IntentRead", "intent:read"},
	{"IntentWrite", "intent:write"},
}

const authPackagePath = "github.com/devicechain-io/dc-microservice/auth"

// The declaring file names each reserved constant exactly twice: its declaration and its
// vocabulary entry. A third mention there (an OAuth ceiling, a service list, an implied
// grant) is a consumer like any other.
const declaringFile = "backend/core/auth/authority.go"

// TestReservedAuthoritiesHaveNoConsumer: no production Go code in the workspace checks,
// grants, lists or implies a reserved authority. Tests are exempt — they are where the
// reservation itself is pinned.
//
// It reads files outside this module, so `-count=1` is load-bearing (see
// TestNoTestInTheRepositorySwapsTheGlobalLogger).
func TestReservedAuthoritiesHaveNoConsumer(t *testing.T) {
	root := workspaceRoot(t)
	modules := workspaceModuleDirs(t, root)
	for _, r := range reservedAuthorities {
		consumers, declared := authorityConsumers(t, root, modules, r.constant, r.name)
		if declared != 2 {
			t.Errorf("%s: %s names %s %d times; want its declaration and its vocabulary entry only",
				r.name, declaringFile, r.constant, declared)
		}
		for _, c := range consumers {
			t.Errorf("%s is reserved and must grant nothing yet, but production code references it at %s", r.name, c)
		}
	}
}

// The negative control for the scan above: an authority that IS consumed must be found,
// both by constant and by spelling. Without this, a scan that parsed nothing would report
// every reserved authority as unconsumed for the same reason a correct one does.
func TestReservedAuthorityScanFindsAConsumedAuthority(t *testing.T) {
	root := workspaceRoot(t)
	consumers, declared := authorityConsumers(t, root, workspaceModuleDirs(t, root), "FleetOperate", "fleet:operate")
	if declared != 2 {
		t.Errorf("control: %s names FleetOperate %d times, want 2", declaringFile, declared)
	}
	var byConstant, byName bool
	for _, c := range consumers {
		byConstant = byConstant || strings.Contains(c, "auth.FleetOperate")
		byName = byName || strings.Contains(c, `"fleet:operate"`)
	}
	if !byConstant || !byName {
		t.Fatalf("control: the scan must find fleet:operate's resolver checks by constant (%v) and by spelling (%v); found %v",
			byConstant, byName, consumers)
	}
}

// The roles guide is the one public page that lists what a custom role may grant beyond
// the checklist itself; each reserved authority is described there in every locale.
func TestReservedAuthoritiesAreDocumentedInEveryLocale(t *testing.T) {
	root := workspaceRoot(t)
	for _, page := range []string{
		"docs/docs/user-guide/members-roles-and-customer-users.md",
		"docs/i18n/es/docusaurus-plugin-content-docs/current/user-guide/members-roles-and-customer-users.md",
		"docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/user-guide/members-roles-and-customer-users.md",
	} {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(page)))
		if err != nil {
			t.Fatalf("reading %s: %v", page, err)
		}
		for _, r := range reservedAuthorities {
			if !strings.Contains(string(content), "`"+r.name+"`") {
				t.Errorf("%s does not describe the reserved authority %s", page, r.name)
			}
		}
	}
}

// authorityConsumers returns every production reference to an authority, by its constant
// (through any import alias of the auth package, or bare inside it) or by its wire
// spelling in a string literal, outside the declaring file; and how many times the
// declaring file names the constant. The declaring file may spell the name once, as the
// constant's value; any other spelling there is reported as a consumer. It fails the test
// if any go.work module held no production Go file the scan reached.
func authorityConsumers(t *testing.T, root string, modules []string, constant, name string) ([]string, int) {
	t.Helper()
	scanOnce.Do(func() {
		names := map[string]string{"FleetOperate": "fleet:operate"}
		for _, r := range reservedAuthorities {
			names[r.constant] = r.name
		}
		scanResults, scanErr = scanAuthorityReferences(root, modules, names)
	})
	if scanErr != nil {
		t.Fatal(scanErr)
	}
	r, ok := scanResults[constant]
	if !ok || r.name != name {
		t.Fatalf("%s was not scanned for", constant)
	}
	return r.consumers, r.declared
}

type authorityScan struct {
	name      string
	consumers []string
	declared  int
	literals  int
}

// One walk serves every test in this file: the workspace is large, and a walk of it
// costs minutes on a slow filesystem.
var (
	scanOnce    sync.Once
	scanResults map[string]*authorityScan
	scanErr     error
)

func scanAuthorityReferences(root string, modules []string, names map[string]string) (map[string]*authorityScan, error) {
	results := map[string]*authorityScan{}
	for constant, name := range names {
		results[constant] = &authorityScan{name: name}
	}
	fset := token.NewFileSet()
	reached := map[string]int{}
	for _, module := range modules {
		err := filepath.WalkDir(module, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path == module {
					return nil
				}
				n := d.Name()
				if n == "testdata" || n == "vendor" || n == "node_modules" || strings.HasPrefix(n, ".") {
					return fs.SkipDir
				}
				// A nested module is scanned under its own go.work entry.
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			reached[module]++
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			inAuth := f.Name.Name == "auth" && strings.HasPrefix(rel, "backend/core/auth/")
			aliases := map[string]bool{}
			for _, imp := range f.Imports {
				if p, _ := strconv.Unquote(imp.Path.Value); p == authPackagePath {
					alias := "auth"
					if imp.Name != nil {
						alias = imp.Name.Name
					}
					aliases[alias] = true
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				for constant, r := range results {
					var hit string
					switch n := n.(type) {
					case *ast.SelectorExpr:
						if x, ok := n.X.(*ast.Ident); ok && aliases[x.Name] && n.Sel.Name == constant {
							hit = "auth." + constant
						}
					case *ast.Ident:
						if inAuth && n.Name == constant {
							if rel == declaringFile {
								r.declared++
								continue
							}
							hit = constant
						}
					case *ast.BasicLit:
						if n.Kind == token.STRING {
							if v, err := strconv.Unquote(n.Value); err == nil && containsAuthority(v, r.name) {
								if rel == declaringFile && v == r.name {
									r.literals++
									continue
								}
								hit = strconv.Quote(r.name)
							}
						}
					}
					if hit != "" {
						r.consumers = append(r.consumers, fmt.Sprintf("%s:%d (%s)", rel, fset.Position(n.Pos()).Line, hit))
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scanning %s: %w", module, err)
		}
	}
	for _, module := range modules {
		if reached[module] == 0 {
			return nil, fmt.Errorf("the scan reached no production Go file in %s", module)
		}
	}
	for _, r := range results {
		// The declaration's own value is the one spelling the declaring file may hold.
		if r.literals != 1 {
			r.consumers = append(r.consumers, fmt.Sprintf("%s (%d literal spellings of %s, want 1)", declaringFile, r.literals, r.name))
		}
	}
	return results, nil
}

// containsAuthority reports whether s mentions name as a whole authority: bounded by the
// string's ends or by a character that cannot belong to an authority name, so
// "intent:readers" is not a mention of intent:read.
func containsAuthority(s, name string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], name)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(name)
		if (start == 0 || !authorityRune(s[start-1])) && (end == len(s) || !authorityRune(s[end])) {
			return true
		}
		i = start + 1
	}
}

func authorityRune(c byte) bool {
	return c == ':' || c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
