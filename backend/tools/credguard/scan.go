// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package credguard finds production code that compares a secret against a bcrypt hash
// anywhere but the credential primitive.
//
// # Why this exists
//
// A bcrypt compare is what a password guess costs the server. backend/core/credential
// owns the ONLY one that authenticates anybody, and puts a per-principal backoff in
// front of it, so the login mutation and the OAuth token endpoint cannot run a compare
// the limiter does not count. That property holds only while nothing else calls
// bcrypt.CompareHashAndPassword: a second call site is an unthrottled guessing oracle
// the moment it is reachable from a request, and nothing about it looks wrong in
// review — it is the obvious way to check a password.
//
// # What it does NOT cover
//
// It watches the bcrypt class only. A secret compared some other way —
// subtle.ConstantTimeCompare against a stored token, an HMAC check — is a credential
// check this guard cannot see, and several exist in device-management today (claim,
// provisioning and device credential secrets). "Nothing compares a bcrypt hash outside
// the primitive" is the claim; "nothing checks a credential unthrottled" is not.
//
// # Why it parses rather than greps
//
// The shapes that defeat a text scan all compile and survive gofmt: an import alias
// (`b "golang.org/x/crypto/bcrypt"`), a call split across lines, and the function
// VALUE assigned to a variable and called through it (`var cmp =
// bcrypt.CompareHashAndPassword`). The selector is present in the AST wherever the
// function is named, called or not, under whatever local name the file bound the import
// to. A dot-import erases the selector, so one is refused outright.
package credguard

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

// bcryptPath is the package whose compare is watched.
const bcryptPath = "golang.org/x/crypto/bcrypt"

// compareFunc is the watched member.
const compareFunc = "CompareHashAndPassword"

// OwnerDir is the directory — matched as a trailing run of path components — of the
// one package allowed to name the compare: the credential primitive. Its subpackages
// are NOT included: credentialtest is a fake store, and has no business comparing.
const OwnerDir = "backend/core/credential"

// Exemption allows the compare inside one named function of one directory, for a
// reason that is not authentication. Dir is matched as a trailing run of path
// components, so it is written from a stable root ("backend/core/natsauth").
type Exemption struct {
	Dir    string
	Func   string
	Reason string
}

func (e Exemption) String() string { return e.Dir + "." + e.Func }

// ParseExemption parses "dir.func=reason".
func ParseExemption(s string) (Exemption, error) {
	key, reason, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(reason) == "" {
		return Exemption{}, fmt.Errorf("exemption %q has no reason; write it as dir.func=reason", s)
	}
	i := strings.LastIndex(key, ".")
	if i <= 0 || i == len(key)-1 {
		return Exemption{}, fmt.Errorf("exemption %q is not dir.func", key)
	}
	return Exemption{Dir: key[:i], Func: key[i+1:], Reason: reason}, nil
}

// Finding is one place a file names the compare outside the primitive.
type Finding struct {
	Pos     token.Position
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d:%d: %s", f.Pos.Filename, f.Pos.Line, f.Pos.Column, f.Message)
}

// Result is what a scan saw, including how much. PerRoot counts parsed files per root —
// a caller that does not check it cannot tell a clean tree from one it never read.
// Used counts the sites each exemption matched; an exemption that matched nothing is
// stale, and the caller reports it.
type Result struct {
	PerRoot  map[string]int
	Findings []Finding
	Used     map[string]int
}

// Files is the total parsed across every root.
func (r Result) Files() int {
	n := 0
	for _, c := range r.PerRoot {
		n += c
	}
	return n
}

// Scan parses every non-test .go file under each root.
//
// Skipped, as go build skips them or because they are not production: _test.go files
// (tests compare hashes to check what they minted), testdata, vendor, node_modules and
// _legacy (the archived tree, which is not built). A build-tagged file is parsed
// whatever its tags, so the scan over-reports rather than under-reports.
func Scan(exemptions []Exemption, roots ...string) (Result, error) {
	res := Result{PerRoot: map[string]int{}, Used: map[string]int{}}
	for _, e := range exemptions {
		res.Used[e.String()] = 0
	}
	fset := token.NewFileSet()

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
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
			dir := filepath.ToSlash(filepath.Dir(path))
			if hasDirSuffix(dir, OwnerDir) {
				return nil
			}
			res.Findings = append(res.Findings, scanFile(fset, file, dir, exemptions, res.Used)...)
			return nil
		})
		if err != nil {
			return res, err
		}
	}

	sort.Slice(res.Findings, func(i, j int) bool {
		a, b := res.Findings[i].Pos, res.Findings[j].Pos
		if a.Filename != b.Filename {
			return a.Filename < b.Filename
		}
		return a.Line < b.Line
	})
	return res, nil
}

// hasDirSuffix reports whether dir ends with the path components of suffix — whole
// components only, so "backend/core/credentials" does not match "core/credential".
func hasDirSuffix(dir, suffix string) bool {
	return dir == suffix || strings.HasSuffix(dir, "/"+suffix)
}

func scanFile(fset *token.FileSet, file *ast.File, dir string, exemptions []Exemption, used map[string]int) []Finding {
	var out []Finding

	// EVERY local name bound to bcrypt, not the last one: a file may import the same
	// package twice under two names, and a single-name loop would watch only one.
	names := map[string]bool{}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != bcryptPath {
			continue
		}
		switch {
		case imp.Name == nil:
			names["bcrypt"] = true
		case imp.Name.Name == ".":
			out = append(out, Finding{
				Pos: fset.Position(imp.Pos()),
				Message: "dot-imports " + bcryptPath + ", which makes a call to " + compareFunc +
					" unanalyzable here",
			})
		case imp.Name.Name == "_":
		default:
			names[imp.Name.Name] = true
		}
	}
	if len(names) == 0 {
		return out
	}

	for _, decl := range file.Decls {
		fn := ""
		if fd, ok := decl.(*ast.FuncDecl); ok {
			fn = fd.Name.Name
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != compareFunc {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || !names[id.Name] {
				return true
			}
			for _, e := range exemptions {
				if fn != "" && fn == e.Func && hasDirSuffix(dir, e.Dir) {
					used[e.String()]++
					return true
				}
			}
			out = append(out, Finding{
				Pos: fset.Position(sel.Pos()),
				Message: fmt.Sprintf("names %s.%s outside %s: compare a presented secret through "+
					"credential.Checker, which counts the attempt", id.Name, compareFunc, OwnerDir),
			})
			return true
		})
	}
	return out
}
