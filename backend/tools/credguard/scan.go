// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package credguard finds production code that compares a secret — against a bcrypt
// hash or in constant time — anywhere but the credential primitive, unless a named
// exemption says why that compare is not a guessable credential check.
//
// # Why this exists
//
// A compare is what a guess costs the server. backend/core/credential owns the one
// compare that authenticates people and OAuth clients, and puts a per-principal
// backoff in front of it, so the login mutation and the OAuth token endpoint cannot
// run a compare the limiter does not count. That property holds only while nothing
// else compares a presented secret: a second call site is an unthrottled guessing
// oracle the moment it is reachable from a request, and nothing about it looks wrong
// in review — it is the obvious way to check a password.
//
// # What it watches
//
// The members in Watched: bcrypt.CompareHashAndPassword, subtle.ConstantTimeCompare
// and hmac.Equal — the three ways this codebase, or code written the obvious way,
// checks a secret. Every site outside the primitive must carry an exemption that
// names its directory, its function AND the member it calls, with a reason. The
// member is part of the key so that an exemption granted for one kind of compare
// does not silently admit a different kind added to the same function later.
//
// # What it does NOT cover
//
//   - == or bytes.Equal on a secret. Neither is distinguishable from ordinary
//     equality by syntax, so a secret compared that way is invisible here.
//   - subtle.ConstantTimeEq, subtle.ConstantTimeByteEq and the rest of crypto/subtle:
//     they compare integers, not presented secrets, and are not watched.
//   - A database equality lookup used as authentication. A device ACCESS_TOKEN is
//     exactly that: the bearer IS the credential id, matched by WHERE credential_id
//     = ?, with no compare for this guard to see.
//
// "No watched compare outside the primitive without a stated reason" is the claim;
// "nothing checks a credential unthrottled" is not.
//
// # Why it parses rather than greps
//
// The shapes that defeat a text scan all compile and survive gofmt: an import alias
// (`b "golang.org/x/crypto/bcrypt"`), a call split across lines, and the function
// VALUE assigned to a variable and called through it (`var cmp =
// bcrypt.CompareHashAndPassword`). The selector is present in the AST wherever the
// function is named, called or not, under whatever local name the file bound the import
// to. A dot-import erases the selector, so a dot-import of any watched package is
// refused outright.
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

// Member is one watched function: the import path of its package, its name, and the
// label an exemption uses to name it.
type Member struct {
	Path  string
	Name  string
	Label string
}

// Watched is every member the guard reports. Each is resolved against a file's imports
// separately, so an alias bound to one watched package can never mask another.
var Watched = []Member{
	{Path: "golang.org/x/crypto/bcrypt", Name: "CompareHashAndPassword", Label: "bcrypt.CompareHashAndPassword"},
	{Path: "crypto/subtle", Name: "ConstantTimeCompare", Label: "subtle.ConstantTimeCompare"},
	{Path: "crypto/hmac", Name: "Equal", Label: "hmac.Equal"},
}

// memberByLabel finds a watched member by its exemption label.
func memberByLabel(label string) (Member, bool) {
	for _, m := range Watched {
		if m.Label == label {
			return m, true
		}
	}
	return Member{}, false
}

// OwnerDir is the directory — matched as a trailing run of path components — of the
// one package allowed to name a watched compare: the credential primitive. Its
// subpackages are NOT included: credentialtest is a fake store, and has no business
// comparing.
const OwnerDir = "backend/core/credential"

// Exemption allows ONE watched member inside one named function of one directory, for
// a reason that is not a guessable credential check. Dir is matched as a trailing run
// of path components, so it is written from a stable root ("backend/core/natsauth").
type Exemption struct {
	Dir    string
	Func   string
	Member string // a Watched Label
	Reason string
}

// String is the exemption's key, dir.func@member. Two exemptions with the same key
// are refused (ParseExemptions), so the key identifies one entry.
func (e Exemption) String() string { return e.Dir + "." + e.Func + "@" + e.Member }

// ParseExemption parses "dir.func@member=reason", where member is a Watched label.
func ParseExemption(s string) (Exemption, error) {
	key, reason, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(reason) == "" {
		return Exemption{}, fmt.Errorf("exemption %q has no reason; write it as dir.func@member=reason", s)
	}
	fnKey, member, ok := strings.Cut(key, "@")
	if !ok {
		return Exemption{}, fmt.Errorf("exemption %q names no member; write it as dir.func@member=reason "+
			"so it cannot admit a different kind of compare in the same function", key)
	}
	if _, known := memberByLabel(member); !known {
		labels := make([]string, 0, len(Watched))
		for _, m := range Watched {
			labels = append(labels, m.Label)
		}
		return Exemption{}, fmt.Errorf("exemption %q names %q, which is not a watched member (%s)",
			key, member, strings.Join(labels, ", "))
	}
	i := strings.LastIndex(fnKey, ".")
	if i <= 0 || i == len(fnKey)-1 {
		return Exemption{}, fmt.Errorf("exemption %q is not dir.func@member", key)
	}
	return Exemption{Dir: fnKey[:i], Func: fnKey[i+1:], Member: member, Reason: reason}, nil
}

// ParseExemptions parses every entry and refuses a DUPLICATE key. Two identical
// entries would share one usage count, so one of them could be stale without ever
// being reported.
func ParseExemptions(specs []string) ([]Exemption, error) {
	out := make([]Exemption, 0, len(specs))
	seen := map[string]bool{}
	for _, s := range specs {
		e, err := ParseExemption(s)
		if err != nil {
			return nil, err
		}
		if seen[e.String()] {
			return nil, fmt.Errorf("duplicate exemption %s", e)
		}
		seen[e.String()] = true
		out = append(out, e)
	}
	return out, nil
}

// Finding is one place a file names a watched compare outside the primitive.
type Finding struct {
	Pos     token.Position
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d:%d: %s", f.Pos.Filename, f.Pos.Line, f.Pos.Column, f.Message)
}

// Result is what a scan saw, including how much. PerRoot counts parsed files per root —
// a caller that does not check it cannot tell a clean tree from one it never read.
// Used counts the sites each exemption matched, keyed by Exemption.String(); an
// exemption that matched nothing is stale, and the caller reports it.
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

	// local import name -> selector name -> the watched member it resolves to. EVERY
	// local name bound to a watched package is recorded, not the last one: a file may
	// import the same package twice under two names, and a single-name map would watch
	// only one. Keyed per import, so `b "crypto/subtle"` next to `bc
	// "golang.org/x/crypto/bcrypt"` resolves b.ConstantTimeCompare to subtle, not to
	// whatever b last meant.
	names := map[string]map[string]Member{}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		for _, m := range Watched {
			if m.Path != p {
				continue
			}
			local := p[strings.LastIndex(p, "/")+1:]
			switch {
			case imp.Name == nil:
			case imp.Name.Name == ".":
				out = append(out, Finding{
					Pos: fset.Position(imp.Pos()),
					Message: "dot-imports " + p + ", which makes a call to " + m.Name +
						" unanalyzable here",
				})
				continue
			case imp.Name.Name == "_":
				continue
			default:
				local = imp.Name.Name
			}
			if names[local] == nil {
				names[local] = map[string]Member{}
			}
			names[local][m.Name] = m
		}
	}
	if len(names) == 0 {
		return out
	}

	for _, decl := range file.Decls {
		// A closure is attributed to its enclosing FuncDecl, so an exemption for a
		// function covers the closures it builds — and, because the member is part of
		// the match, ONLY for the member the exemption names.
		fn := ""
		if fd, ok := decl.(*ast.FuncDecl); ok {
			fn = fd.Name.Name
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			m, ok := names[id.Name][sel.Sel.Name]
			if !ok {
				return true
			}
			for _, e := range exemptions {
				if fn != "" && fn == e.Func && e.Member == m.Label && hasDirSuffix(dir, e.Dir) {
					used[e.String()]++
					return true
				}
			}
			named := id.Name + "." + m.Name
			if named != m.Label {
				named += " (" + m.Label + ")"
			}
			out = append(out, Finding{
				Pos: fset.Position(sel.Pos()),
				Message: fmt.Sprintf("names %s outside %s: compare a presented secret "+
					"through credential.Checker, which counts the attempt, or exempt this "+
					"function for this member with the reason it is not a guessable check",
					named, OwnerDir),
			})
			return true
		})
	}
	return out
}
