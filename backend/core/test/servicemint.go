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
	"sort"
	"strconv"
	"strings"
)

const mintModule = "github.com/devicechain-io/dc-microservice/"

type serviceMintSite struct {
	File, Func, Caller string
	Authorities        []string
	Unresolved         string
	Kind               string // direct, forwarder, area, or directory
	Ledgered           bool   // implementation mint, attributed at its callers
}

type mintForwarder struct {
	subjectArg  int
	Authorities []string
}

// Subject is parameter 2 in both constructors (including the grouped string parameters).
var serviceMintForwarders = map[string]mintForwarder{
	"governance.NewTenantLifecycleGate":  {2, []string{"tenant:read"}},
	"governance.NewTenantLifecycleGates": {2, []string{"tenant:read"}},
}

var noServiceToken = map[string]string{
	"mcp": "Forwards the user's token; must never hold a service credential.",
}

// serviceMintSitesUnder parses production backend source. Import paths, rather than
// local package aliases, identify mints. The judgement is separate from discovery.
func serviceMintSitesUnder(root string, forwarders map[string]mintForwarder) ([]serviceMintSite, error) {
	fset := token.NewFileSet()
	vocabulary, err := parser.ParseFile(fset, filepath.Join(root, "backend/core/auth/authority.go"), nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	authorities := map[string]string{}
	for _, decl := range vocabulary.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			v := spec.(*ast.ValueSpec)
			typ, ok := v.Type.(*ast.Ident)
			if !ok || typ.Name != "Authority" {
				continue
			}
			for i, name := range v.Names {
				if i < len(v.Values) {
					if value, ok := mintLiteral(v.Values[i]); ok {
						authorities[name.Name] = value
					}
				}
			}
		}
	}
	if len(authorities) == 0 {
		return nil, fmt.Errorf("no Authority constants found")
	}
	var sites []serviceMintSite
	areas := map[string]string{}
	deadletterAliases, deadletterCalls := 0, 0
	deadletterValid := true
	err = filepath.WalkDir(filepath.Join(root, "backend"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".") {
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
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		deadletter := strings.HasPrefix(rel, "backend/core/deadletter/")
		imports := map[string]string{}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !strings.HasPrefix(p, mintModule) {
				continue
			}
			pkg := strings.TrimPrefix(p, mintModule)
			name := filepath.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			imports[name] = pkg
			if name == "." && (pkg == "svcclient" || pkg == "governance") {
				sites = append(sites, serviceMintSite{File: rel, Func: packageLevelSite, Unresolved: "dot-import of " + pkg})
			}
		}
		qualified := func(expr ast.Expr) string {
			if sel, ok := expr.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && imports[id.Name] != "" {
					return imports[id.Name] + "." + sel.Sel.Name
				}
			}
			if id, ok := expr.(*ast.Ident); ok && strings.HasPrefix(rel, "backend/core/governance/") {
				return "governance." + id.Name
			}
			return ""
		}
		for _, decl := range f.Decls {
			fn := packageLevelSite
			if fun, ok := decl.(*ast.FuncDecl); ok {
				fn = funcSiteName(fun)
			}
			// Match AST identity: a constructor used as an argument or assigned to
			// a variable must not disappear merely because another call contains it.
			callFunctions := map[ast.Node]bool{}
			nonReferences := map[ast.Node]bool{}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					callFunctions[node.Fun] = true
				case *ast.FuncDecl:
					nonReferences[node.Name] = true
				case *ast.SelectorExpr:
					nonReferences[node.Sel] = true
				case *ast.ValueSpec:
					for _, name := range node.Names {
						nonReferences[name] = true
					}
				case *ast.Field:
					for _, name := range node.Names {
						nonReferences[name] = true
					}
				case *ast.TypeSpec:
					nonReferences[node.Name] = true
				}
				return true
			})
			var sanctionedAlias ast.Expr
			if gen, ok := decl.(*ast.GenDecl); ok && deadletter && gen.Tok == token.VAR {
				for _, spec := range gen.Specs {
					v := spec.(*ast.ValueSpec)
					for _, name := range v.Names {
						if name.Name != "tenantLifecycleGate" {
							continue
						}
						deadletterAliases++
						if len(v.Names) == 1 && len(v.Values) == 1 && qualified(v.Values[0]) == "governance.NewTenantLifecycleGate" {
							sanctionedAlias = v.Values[0]
						} else {
							deadletterValid = false
							sites = append(sites, serviceMintSite{File: rel, Func: fn, Unresolved: "tenantLifecycleGate must be initialized to governance.NewTenantLifecycleGate"})
						}
					}
				}
			}
			// Only these exact implementations may forward a dynamic subject. The
			// dead-letter alias is attributed through each service's producer below.
			ledgered := false
			if strings.HasPrefix(rel, "backend/core/governance/") {
				_, ledgered = forwarders["governance."+fn]
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if expr, ok := n.(ast.Expr); ok {
					q := qualified(expr)
					_, forwarded := forwarders[q]
					if (q == "svcclient.New" || forwarded) && !nonReferences[n] && !callFunctions[n] && expr != sanctionedAlias {
						sites = append(sites, serviceMintSite{File: rel, Func: fn, Unresolved: "constructor reference is not a direct call: " + q})
					}
				}
				if sel, ok := n.(*ast.SelectorExpr); ok {
					q := qualified(sel)
					if q == "service.NatsSpec" || q == "deadletter.NewProducer" {
						parts := strings.Split(rel, "/")
						if len(parts) > 3 && parts[1] == "services" {
							areas[parts[2]] = rel
						}
					}
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && deadletter && id.Name == "tenantLifecycleGate" {
					deadletterCalls++
					if len(call.Args) <= 2 {
						deadletterValid = false
						sites = append(sites, serviceMintSite{File: rel, Func: fn, Unresolved: "tenantLifecycleGate missing FunctionalArea subject"})
					} else if subject, ok := call.Args[2].(*ast.SelectorExpr); !ok || subject.Sel.Name != "FunctionalArea" {
						deadletterValid = false
						sites = append(sites, serviceMintSite{File: rel, Func: fn, Unresolved: "tenantLifecycleGate subject must end in .FunctionalArea"})
					}
				}
				q := qualified(call.Fun)
				forward, forwarded := forwarders[q]
				if q != "svcclient.New" && !forwarded {
					return true
				}
				site := serviceMintSite{File: rel, Func: fn, Kind: "direct"}
				subject := 2
				if forwarded {
					subject = forward.subjectArg
					site.Kind = "forwarder"
					site.Authorities = append([]string(nil), forward.Authorities...)
				}
				if subject >= len(call.Args) {
					site.Unresolved = "missing subject"
				} else if caller, ok := mintLiteral(call.Args[subject]); ok {
					site.Caller = caller
				} else {
					site.Unresolved = "non-literal subject"
				}
				if !forwarded {
					if len(call.Args) <= 3 {
						site.Unresolved += " unreadable authorities"
					} else {
						values, ok := mintAuthorities(call.Args[3], qualified, authorities)
						if !ok {
							site.Unresolved += " unreadable authorities"
						} else {
							site.Authorities = values
						}
					}
				}
				if ledgered && site.Caller == "" && site.Unresolved == "non-literal subject" {
					// Verify the implementation still mints exactly its ledger grant.
					want := forwarders["governance."+fn].Authorities
					if strings.Join(site.Authorities, ",") == strings.Join(want, ",") {
						if !forwarded {
							site.Ledgered = true
							sites = append(sites, site)
						}
						return true
					}
				}
				sites = append(sites, site)
				return true
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if deadletterAliases != 1 || deadletterCalls == 0 {
		deadletterValid = false
		sites = append(sites, serviceMintSite{File: "backend/core/deadletter", Func: packageLevelSite, Unresolved: "tenantLifecycleGate requires exactly one constructor alias and at least one FunctionalArea call"})
	}
	areaNames := make([]string, 0, len(areas))
	for area := range areas {
		areaNames = append(areaNames, area)
	}
	sort.Strings(areaNames)
	for _, area := range areaNames {
		if deadletterValid {
			sites = append(sites, serviceMintSite{File: areas[area], Caller: area, Authorities: []string{"tenant:read"}, Kind: "area"})
		}
	}
	dirs, err := os.ReadDir(filepath.Join(root, "backend/services"))
	if err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		if dir.IsDir() && noServiceToken[dir.Name()] == "" {
			sites = append(sites, serviceMintSite{File: "backend/services/" + dir.Name(), Caller: dir.Name(), Kind: "directory"})
		}
	}
	return sites, nil
}

func mintLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

func mintAuthorities(expr ast.Expr, qualified func(ast.Expr) string, vocabulary map[string]string) ([]string, bool) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	array, ok := lit.Type.(*ast.ArrayType)
	if !ok || array.Len != nil {
		return nil, false
	}
	typ, ok := array.Elt.(*ast.Ident)
	if !ok || typ.Name != "string" {
		return nil, false
	}
	var values []string
	for _, elt := range lit.Elts {
		if call, ok := elt.(*ast.CallExpr); ok {
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "string" || len(call.Args) != 1 {
				return nil, false
			}
			elt = call.Args[0]
		}
		q := qualified(elt)
		if !strings.HasPrefix(q, "auth.") {
			return nil, false
		}
		value, ok := vocabulary[strings.TrimPrefix(q, "auth.")]
		if !ok {
			return nil, false
		}
		values = append(values, value)
	}
	return values, true
}

// checkMintSites requires equality, so both new requests and stale grants fail.
func checkMintSites(sites []serviceMintSite, ceiling map[string][]string) (problems []string) {
	found := map[string]map[string]bool{}
	for _, site := range sites {
		if site.Ledgered {
			continue
		}
		where := site.File + ":" + site.Func
		if site.Unresolved != "" {
			problems = append(problems, where+": "+site.Unresolved)
			continue
		}
		allowed, ok := ceiling[site.Caller]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: caller %s absent from ceiling", where, site.Caller))
		}
		if site.Kind == "directory" {
			continue
		}
		if found[site.Caller] == nil {
			found[site.Caller] = map[string]bool{}
		}
		for _, a := range site.Authorities {
			found[site.Caller][a] = true
			present := false
			for _, grant := range allowed {
				if grant == a {
					present = true
				}
			}
			if !present {
				problems = append(problems, fmt.Sprintf("%s: caller %s requests %s outside ceiling", where, site.Caller, a))
			}
		}
	}
	for caller, grants := range ceiling {
		for _, grant := range grants {
			if !found[caller][grant] {
				problems = append(problems, fmt.Sprintf("caller %s: ceiling grant %s has no mint site", caller, grant))
			}
		}
		if found[caller] == nil {
			problems = append(problems, fmt.Sprintf("caller %s: no mint site", caller))
		}
	}
	sort.Strings(problems)
	return problems
}
