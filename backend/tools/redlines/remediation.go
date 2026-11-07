// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package redlines

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// remediationName is the identifier the guard watches. agenttext.Remediation is the type;
// the same word names the struct field and the setter an error type uses to carry it.
const remediationName = "Remediation"

// setterNames are call names whose arguments are remediation text.
var setterNames = map[string]bool{
	"Remediation": true, "WithRemediation": true, "NewRemediation": true, "SetRemediation": true,
}

// RemFinding is one remediation value that cannot be shown to be a compile-time constant.
type RemFinding struct {
	File   string
	Line   int
	Reason string
}

func (f RemFinding) String() string { return fmt.Sprintf("%s:%d: %s", f.File, f.Line, f.Reason) }

// RemResult is what ScanRemediation found.
type RemResult struct {
	Findings []RemFinding
	Sites    int // watched sites examined
	GoFiles  int
}

type dirInfo struct {
	consts   map[string]bool // package-level const names
	others   map[string]bool // package-level var, func and type names
	mapTypes map[string]bool // package-level named map types
}

type parsedFile struct {
	path    string
	dir     string
	file    *ast.File
	fset    *token.FileSet
	imports map[string]string // local name -> import path
}

// ScanRemediation reports every watched remediation site whose value is not provably a
// compile-time constant.
//
// A watched site is: a keyed struct-literal field named Remediation; an assignment to a
// selector named Remediation; a var or const declared with the name Remediation or the
// type Remediation; a call or conversion named Remediation, WithRemediation,
// NewRemediation or SetRemediation; a bare reference to Remediation that is not such a
// call (a function value would let a caller convert out of sight); and a dot-import of a package named
// agenttext.
//
// A value is constant-shaped when it is a string literal, a parenthesised constant, a
// concatenation of constants, a conversion of a constant, an identifier that resolves to
// a const (a local const, or a package-level one in the same directory), or pkg.Name
// where pkg is an import whose directory is inside the scanned tree and Name is a
// package-level const there. Anything else is refused, including every call that is not
// a conversion, so the guard fails closed on a shape it does not understand.
//
// 🔴 WHAT IT CANNOT SEE. It is syntactic. Resolution is by name within a directory, so a
// const is accepted by name and not by type-checked value; a const that is only declared
// under a build tag the file does not carry is still accepted. Text that reaches a
// Remediation through reflection, JSON decoding, a generic function, or a field the type
// of which it cannot determine without type information is not seen. The type is the
// first line (a runtime string cannot become a Remediation without a conversion, and a
// conversion is a watched site); this is the second.
func ScanRemediation(roots ...string) (*RemResult, error) {
	var files []*parsedFile
	dirs := map[string]*dirInfo{}
	modules := map[string]string{} // module path -> dir

	for _, root := range roots {
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("root %q is not a readable directory", root)
		}
		err := walk(root, skipFixtures, func(p string) error {
			base := path.Base(p)
			if base == "go.mod" {
				if m := moduleOf(p); m != "" {
					modules[m] = path.Dir(p)
				}
				return nil
			}
			if !strings.HasSuffix(base, ".go") || strings.HasSuffix(base, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			af, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return fmt.Errorf("cannot parse %s: %w", p, err)
			}
			pf := &parsedFile{path: p, dir: path.Dir(p), file: af, fset: fset, imports: map[string]string{}}
			for _, imp := range af.Imports {
				ip, uerr := strconv.Unquote(imp.Path.Value)
				if uerr != nil {
					continue
				}
				name := importName(ip)
				if imp.Name != nil {
					name = imp.Name.Name
				}
				pf.imports[name] = ip
			}
			files = append(files, pf)
			collectDecls(dirs, pf)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	aliases := remediationAliases(files)
	res := &RemResult{GoFiles: len(files)}
	for _, pf := range files {
		c := &remChecker{pf: pf, dirs: dirs, modules: modules, res: res, aliases: aliases}
		c.run()
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

// remediationAliases returns the names of type aliases of Remediation, directly or through
// another alias, anywhere in the scanned tree: `type Hint = agenttext.Remediation` makes
// Hint(x) a conversion into the protected type. Matching is by name, tree-wide, so it errs
// toward watching a same-named unrelated alias rather than missing one.
func remediationAliases(files []*parsedFile) map[string]bool {
	out := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, pf := range files {
			// Inspect, not Decls: an alias declared inside a function converts as well.
			ast.Inspect(pf.file, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok || !ts.Assign.IsValid() || out[ts.Name.Name] {
					return true
				}
				if name, _ := calleeName(ts.Type); name == remediationName || out[name] {
					out[ts.Name.Name] = true
					changed = true
				}
				return true
			})
		}
	}
	return out
}

// importName guesses a package's name from its path: the last element, skipping a /vN
// major-version suffix and a gopkg.in-style .vN.
func importName(p string) string {
	parts := strings.Split(p, "/")
	last := parts[len(parts)-1]
	if len(parts) > 1 && len(last) > 1 && last[0] == 'v' {
		if _, err := strconv.Atoi(last[1:]); err == nil {
			last = parts[len(parts)-2]
		}
	}
	if i := strings.Index(last, ".v"); i > 0 {
		last = last[:i]
	}
	return strings.ReplaceAll(last, "-", "_")
}

func collectDecls(dirs map[string]*dirInfo, pf *parsedFile) {
	di := dirs[pf.dir]
	if di == nil {
		di = &dirInfo{consts: map[string]bool{}, others: map[string]bool{}, mapTypes: map[string]bool{}}
		dirs[pf.dir] = di
	}
	for _, d := range pf.file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				di.others[d.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						if d.Tok == token.CONST {
							di.consts[n.Name] = true
						} else {
							di.others[n.Name] = true
						}
					}
				case *ast.TypeSpec:
					di.others[sp.Name.Name] = true
					if _, ok := sp.Type.(*ast.MapType); ok {
						di.mapTypes[sp.Name.Name] = true
					}
				}
			}
		}
	}
}

func isRemediationType(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name == remediationName
	case *ast.SelectorExpr:
		return e.Sel.Name == remediationName
	}
	return false
}

type remChecker struct {
	pf      *parsedFile
	dirs    map[string]*dirInfo
	modules map[string]string
	res     *RemResult
	aliases map[string]bool
	// elidedMaps marks type-less literals whose element type is a map: the inner {...} of
	// map[K]map[K2]V{k: {...}} or []map[K]V{{...}}.
	elidedMaps map[*ast.CompositeLit]bool
	// handled marks the Remediation selector or identifier nodes already examined as the
	// callee or the key of a watched site, so the bare-reference rule does not double-count.
	handled map[ast.Node]bool
}

func (c *remChecker) add(n ast.Node, format string, args ...any) {
	c.res.Findings = append(c.res.Findings, RemFinding{
		File: c.pf.path, Line: c.pf.fset.Position(n.Pos()).Line, Reason: fmt.Sprintf(format, args...),
	})
}

func (c *remChecker) run() {
	c.handled = map[ast.Node]bool{}
	c.elidedMaps = map[*ast.CompositeLit]bool{}
	ast.Inspect(c.pf.file, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		var elem ast.Expr
		switch t := cl.Type.(type) {
		case *ast.MapType:
			elem = t.Value
		case *ast.ArrayType:
			elem = t.Elt
		}
		if _, isMap := elem.(*ast.MapType); !isMap {
			return true
		}
		for _, e := range cl.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				e = kv.Value
			}
			if inner, ok := e.(*ast.CompositeLit); ok && inner.Type == nil {
				c.elidedMaps[inner] = true
			}
		}
		return true
	})
	for _, imp := range c.pf.file.Imports {
		if imp.Name != nil && imp.Name.Name == "." {
			if ip, err := strconv.Unquote(imp.Path.Value); err == nil && importName(ip) == "agenttext" {
				c.add(imp, "dot-import of %s hides every Remediation conversion from the guard; import it by name", ip)
			}
		}
	}
	// First pass: sites that name their callee or key, so those nodes are marked handled.
	ast.Inspect(c.pf.file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			name, node := calleeName(n.Fun)
			if setterNames[name] || c.aliases[name] || c.mentionsRemediation(n.Fun) {
				if name == "" {
					name = "conversion to " + remediationName
				}
				if node != nil {
					c.handled[node] = true
				}
				c.res.Sites++
				for _, a := range n.Args {
					c.require(a, "argument of "+name)
				}
			}
		case *ast.CompositeLit:
			c.compositeLit(n)
		case *ast.AssignStmt:
			c.assign(n)
		case *ast.ValueSpec:
			c.valueSpec(n)
		}
		return true
	})
	// Second pass: a bare reference to Remediation that no first-pass site accounts for
	// is a function value or an unrecognised use. A type position is fine.
	typePos := map[ast.Node]bool{}
	ast.Inspect(c.pf.file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Field:
			markType(n.Type, typePos)
		case *ast.ValueSpec:
			if n.Type != nil {
				markType(n.Type, typePos)
			}
		case *ast.TypeSpec:
			markType(n.Type, typePos)
		case *ast.CompositeLit:
			if n.Type != nil {
				markType(n.Type, typePos)
			}
		case *ast.ArrayType:
			markType(n.Elt, typePos)
		case *ast.MapType:
			markType(n.Key, typePos)
			markType(n.Value, typePos)
		case *ast.StarExpr:
			markType(n.X, typePos)
		}
		return true
	})
	ast.Inspect(c.pf.file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if x, ok := n.X.(*ast.Ident); ok && n.Sel.Name == remediationName && !typePos[n] && !c.handled[n] &&
				importName(c.pf.imports[x.Name]) == "agenttext" {
				c.add(n, "bare reference to %s outside a conversion; a function value would let a runtime string be converted out of the guard's sight", remediationName)
			}
		}
		return true
	})
}

// mentionsRemediation reports whether a call's function is a type expression built from
// Remediation or one of its aliases: (*Remediation)(&s) converts a *string without ever
// naming Remediation as the callee.
func (c *remChecker) mentionsRemediation(fun ast.Expr) bool {
	switch f := ast.Unparen(fun).(type) {
	case *ast.StarExpr:
		return c.mentionsRemediation(f.X)
	case *ast.ArrayType:
		return c.mentionsRemediation(f.Elt)
	case *ast.Ident:
		return f.Name == remediationName || c.aliases[f.Name]
	case *ast.SelectorExpr:
		return f.Sel.Name == remediationName || c.aliases[f.Sel.Name]
	}
	return false
}

func markType(e ast.Expr, m map[ast.Node]bool) {
	switch e := e.(type) {
	case *ast.Ident:
		m[e] = true
	case *ast.SelectorExpr:
		m[e] = true
		m[e.Sel] = true
	case *ast.StarExpr:
		markType(e.X, m)
	case *ast.ArrayType:
		markType(e.Elt, m)
	case *ast.MapType:
		markType(e.Key, m)
		markType(e.Value, m)
	case *ast.Ellipsis:
		markType(e.Elt, m)
	}
}

func calleeName(fun ast.Expr) (string, ast.Node) {
	switch f := ast.Unparen(fun).(type) {
	case *ast.Ident:
		return f.Name, f
	case *ast.SelectorExpr:
		return f.Sel.Name, f
	}
	return "", nil
}

// isMapLit reports whether a literal is a map: spelled as one, of a named map type in
// this package or an imported one in the tree, or an elided literal inside a map-of-maps.
func (c *remChecker) isMapLit(n *ast.CompositeLit) bool {
	switch t := n.Type.(type) {
	case *ast.MapType:
		return true
	case *ast.Ident:
		return c.dirs[c.pf.dir] != nil && c.dirs[c.pf.dir].mapTypes[t.Name]
	case *ast.SelectorExpr:
		if x, ok := t.X.(*ast.Ident); ok {
			if dir, ok := c.dirOfImport(c.pf.imports[x.Name]); ok && c.dirs[dir] != nil {
				return c.dirs[dir].mapTypes[t.Sel.Name]
			}
		}
	case nil:
		return c.elidedMaps[n]
	}
	return false
}

func (c *remChecker) compositeLit(n *ast.CompositeLit) {
	// Keyed fields named Remediation. The keys of an explicit map literal are values, not
	// field names, so a map that happens to be keyed by a constant of that name is not one.
	isMap := c.isMapLit(n)
	for _, e := range n.Elts {
		if isMap {
			break
		}
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == remediationName {
				c.res.Sites++
				c.require(kv.Value, "field "+remediationName)
			}
		}
	}
}

func (c *remChecker) assign(a *ast.AssignStmt) {
	if a.Tok != token.ASSIGN && a.Tok != token.DEFINE && a.Tok != token.ADD_ASSIGN {
		return
	}
	for i, l := range a.Lhs {
		sel, ok := l.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != remediationName {
			continue
		}
		c.res.Sites++
		if len(a.Lhs) == len(a.Rhs) {
			c.require(a.Rhs[i], "assignment to ."+remediationName)
		} else {
			c.add(a, "assignment to .%s from a multi-value expression cannot be shown constant", remediationName)
		}
	}
}

func (c *remChecker) valueSpec(v *ast.ValueSpec) {
	typed := v.Type != nil && isRemediationType(v.Type)
	for i, n := range v.Names {
		if !typed && n.Name != remediationName {
			continue
		}
		if i < len(v.Values) {
			c.res.Sites++
			c.require(v.Values[i], "declaration of "+n.Name)
		}
	}
}

// require reports e unless it is constant-shaped.
func (c *remChecker) require(e ast.Expr, what string) {
	if why := c.nonConst(e); why != "" {
		c.add(e, "%s is not a compile-time constant: %s", what, why)
	}
}

// nonConst returns why e is not constant-shaped, or "" if it is.
func (c *remChecker) nonConst(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			return ""
		}
		return "a non-string literal"
	case *ast.ParenExpr:
		return c.nonConst(e.X)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "operator " + e.Op.String()
		}
		if w := c.nonConst(e.X); w != "" {
			return w
		}
		return c.nonConst(e.Y)
	case *ast.Ident:
		if e.Obj != nil {
			if e.Obj.Kind == ast.Con {
				return ""
			}
			return "identifier " + e.Name + " is not a constant"
		}
		if di := c.dirs[c.pf.dir]; di != nil && di.consts[e.Name] && !di.others[e.Name] {
			return ""
		}
		return "identifier " + e.Name + " does not resolve to a constant"
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		if !ok {
			return "selector on a non-package expression"
		}
		if x.Obj != nil {
			return x.Name + "." + e.Sel.Name + " reads a field or method of a local value"
		}
		if dir, ok := c.dirOfImport(c.pf.imports[x.Name]); ok {
			if di := c.dirs[dir]; di != nil && di.consts[e.Sel.Name] && !di.others[e.Sel.Name] {
				return ""
			}
			return x.Name + "." + e.Sel.Name + " is not a package-level const of a package in this repository"
		}
		return x.Name + "." + e.Sel.Name + " is not a constant of a package in this repository"
	case *ast.CallExpr:
		name, _ := calleeName(e.Fun)
		if len(e.Args) == 1 && (name == "string" || name == remediationName || c.aliases[name]) {
			return c.nonConst(e.Args[0])
		}
		if name == "" {
			return "a call"
		}
		return "a call to " + name
	}
	return fmt.Sprintf("%T", e)
}

// dirOfImport maps an import path inside the scanned tree to its directory.
func (c *remChecker) dirOfImport(ip string) (string, bool) {
	best, bestDir := "", ""
	for m, d := range c.modules {
		if (ip == m || strings.HasPrefix(ip, m+"/")) && len(m) > len(best) {
			best, bestDir = m, d
		}
	}
	if best == "" {
		return "", false
	}
	return path.Join(bestDir, strings.TrimPrefix(ip, best)), true
}
