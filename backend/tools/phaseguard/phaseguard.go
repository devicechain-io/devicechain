// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package phaseguard answers one question about a lifecycle-driven service: which
// lifecycle PHASE does a given piece of work run in, and is the thing it constructs
// allowed to be constructed there?
//
// # Why the question is worth asking
//
// core.LifecycleManager gives a component four steps and two of them run a different
// number of times. Its own state allow lists say so:
//
//	initializeFrom = {Uninitialized}
//	startFrom      = {Initialized, Stopped}
//
// So ExecuteInitialize runs AT MOST ONCE for a component instance — the state machine
// refuses a second one — while ExecuteStart runs again after every stop. That is not a
// naming convention, it is an invariant with a gate behind it, and it splits everything
// a service builds into two kinds:
//
//   - Build-once things, which MUST be on the initialize path. A ServeMux route and a
//     Prometheus collector are both duplicate-PANIC on a second registration, so
//     building one per start turns the second start into a crash.
//   - Build-per-start things, which MUST NOT be on the initialize path. core.HttpServer
//     wraps one *http.Server for its lifetime, and net/http latches an http.Server's
//     shutting-down flag permanently: a server retained across a stop binds and then
//     serves nothing. core.HttpServer.Start refuses that outright rather than serving
//     nothing, so the failure is loud — but it is still a service that will not restart,
//     found at restart time rather than at review time.
//
// Today every construction site of each kind happens to sit in the right phase, and
// nothing but a comment says it has to.
//
// # Why this cannot be a name-based sweep, or a grep
//
// 🔴 THE PHASE A FUNCTION RUNS IN IS A PROPERTY OF ITS CALLER, NOT OF ITS NAME. This
// repository has produced three separate proofs of that, each of which defeats a
// different shortcut:
//
//   - createNatsComponents reads like initialization work and is invoked on START. A
//     sweep that classified by name would put it in the wrong phase.
//   - user-management's initializer callback is an ANONYMOUS CLOSURE. There is no name
//     to key on, even in principle.
//   - device-management's InboundEventsProcessor builds in its own ExecuteInitialize,
//     which the framework reaches through INTERFACE DISPATCH. No walk that follows only
//     statically-named callees arrives there.
//
// So the traversal starts at the entry points the FRAMEWORK defines for each phase and
// follows the call graph out of them, resolving every callee through go/types and
// expanding interface calls by class-hierarchy analysis over the loaded program.
//
// Grepping is worse than merely weak here for the same reason it was for the default-mux
// guard: the watched names appear in this tree in prose. NewHttpServer is named in doc
// comments in core/core/http.go and core/graphql/graphql.go, and RegisterProbes explains
// itself in a comment that says it must be called at most once. A text scan reports all
// of that on its first run and is then narrowed with exclusions until it is quiet and
// wrong. An AST walk never sees a comment, and a types-resolved callee is the same match
// through an import alias, a dot-import or a line break.
package phaseguard

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// CorePkg is the import path of the module-shared library whose lifecycle types define
// the phases and whose constructors are the watched symbols.
const CorePkg = "github.com/devicechain-io/dc-microservice/core"

// Options are the knobs a run needs.
//
// Core is settable ONLY so the self-test can point the whole engine at a miniature
// framework of its own — one that declares the same lifecycle types and constructors
// under a different import path, in a module with no dependencies. Without that the
// fixtures could not type-check, and a guard whose analysis is types-based cannot be
// self-tested on fixtures that do not type-check. It fails closed if it is ever pointed
// somewhere wrong: the watched symbols then resolve to nothing and the run exits 2.
type Options struct {
	// Dir is the directory the patterns are resolved from.
	Dir string
	// Core is the import path standing in for CorePkg.
	Core string
}

// RulesFor re-points the rule table at an import path, for the self-test's fixtures.
func RulesFor(pkg string) []Rule {
	out := make([]Rule, 0, len(Rules))
	for _, r := range Rules {
		syms := make([]Symbol, 0, len(r.Symbols))
		for _, s := range r.Symbols {
			if s.Pkg == CorePkg {
				s.Pkg = pkg
			}
			syms = append(syms, s)
		}
		r.Symbols = syms
		out = append(out, r)
	}
	return out
}

// Phase is one lifecycle step, identified by the framework rather than by a name a
// service chose.
type Phase int

const (
	// Initialize is the at-most-once step. Reached through a component's
	// ExecuteInitialize and through the Initializer callback pair.
	Initialize Phase = iota
	// Start is the once-per-start step, which runs again after every stop. Reached
	// through ExecuteStart and the Starter callback pair.
	Start
)

func (p Phase) String() string {
	if p == Initialize {
		return "initialize"
	}
	return "start"
}

// execMethod is the LifecycleComponent method the framework invokes for this phase.
func (p Phase) execMethod() string {
	if p == Initialize {
		return "ExecuteInitialize"
	}
	return "ExecuteStart"
}

// callbackField is the LifecycleCallbacks field holding this phase's callback pair.
func (p Phase) callbackField() string {
	if p == Initialize {
		return "Initializer"
	}
	return "Starter"
}

// Symbol names one watched function: a package-level function, or a method on a named
// type in that package.
//
// It is matched against the *types.Func the type checker resolved, never against source
// text, so an import alias, a dot-import, a call split across lines and a method value
// that is never called are all the same match.
type Symbol struct {
	Pkg  string // import path
	Recv string // named receiver type, or "" for a package-level function
	Name string
}

func (s Symbol) String() string {
	base := s.Pkg[strings.LastIndex(s.Pkg, "/")+1:]
	if s.Recv == "" {
		return base + "." + s.Name
	}
	return "(" + base + "." + s.Recv + ")." + s.Name
}

func (s Symbol) matches(fn *types.Func) bool {
	if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != s.Pkg || fn.Name() != s.Name {
		return false
	}
	sig, _ := fn.Type().(*types.Signature)
	recv := ""
	if sig != nil && sig.Recv() != nil {
		t := sig.Recv().Type()
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		if named, ok := t.(*types.Named); ok {
			recv = named.Obj().Name()
		}
	}
	return recv == s.Recv
}

// Rule is one phase constraint: these symbols must not be reachable from Forbidden's
// entry points, and are expected to be reachable from Expect's.
//
// 🔴 THE `Expect` HALF IS NOT DECORATION, IT IS THE LIVENESS PROBE. Zero findings is
// what a clean tree reports AND what a matcher that has gone blind reports, and this
// guard has more ways to go blind than most: a renamed entry-point method, a callback
// pair the literal scan stops recognising, a call-graph edge kind that stops resolving.
// Every one of those drives the FORBIDDEN count to zero and looks like success. It also
// drives the EXPECTED count to zero, which cannot look like success — so the guard runs
// itself in the direction that must be non-empty and refuses to report on a tree where
// that came back short.
type Rule struct {
	Name        string
	Symbols     []Symbol
	Forbidden   Phase
	Expect      Phase
	MinExpected int
	Why         string
	Remedy      string
}

// Finding is one reference to a watched symbol from the phase that must not reach it.
type Finding struct {
	Pos   token.Position
	Rule  string
	Sym   Symbol
	Chain []string // entry point first, the referencing function last
	Why   string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d:%d: %s: %s is reachable from the %s path\n    via %s",
		f.Pos.Filename, f.Pos.Line, f.Pos.Column, f.Rule, f.Sym, f.Why,
		strings.Join(f.Chain, " -> "))
}

// Result is what a scan saw, INCLUDING how much it saw. A caller that reads only
// Findings cannot tell a clean tree from a tree the scan never read.
type Result struct {
	Packages int
	Files    int
	// EntryPoints counts the phase entry points discovered, per phase.
	EntryPoints map[Phase]int
	// Reached counts watched-symbol references found on each phase's path, per rule.
	// Reached[rule][phase].
	Reached map[string]map[Phase]int
	// Expected is where each rule's symbols were found on the path they are ALLOWED
	// to be on. It is the evidence behind the liveness floor: a floor that fails
	// should say what it did see, or the person reading it cannot tell a moved site
	// from a scan that stopped working.
	Expected map[string][]Finding
	Findings []Finding
	// Unresolved names watched symbols that do not exist in the loaded program at
	// all. A renamed constructor lands here, and it is an instrument failure rather
	// than a clean tree.
	Unresolved []Symbol
}

// node is one function body in the call graph: either a declared function or method, or
// an anonymous function literal (which is what user-management's initializer callback
// is, and what nothing keyed on a name can reach).
type node struct {
	fn   *types.Func // nil for a literal
	lit  *ast.FuncLit
	pkg  *packages.Package
	body *ast.BlockStmt
	name string
}

func (n *node) key() any {
	if n.fn != nil {
		return n.fn
	}
	return n.lit
}

type scanner struct {
	fset *token.FileSet
	pkgs []*packages.Package

	// decls maps a declared function to the body the traversal walks. A *types.Func
	// with no entry here is a function whose source was not loaded — anything in the
	// standard library or a third-party dependency — and the traversal stops there.
	decls map[*types.Func]*node
	// litNodes memoises the node for a function literal.
	litNodes map[*ast.FuncLit]*node
	// litPkg maps a function literal to the package that declares it, which is where
	// the TypesInfo for resolving anything inside it lives.
	litPkg map[*ast.FuncLit]*packages.Package

	// named is every named type declared in the loaded packages, which is the class
	// hierarchy the interface expansion searches.
	named []*types.Named
	// chaCache memoises the expansion of one interface method.
	chaCache map[*types.Func][]*types.Func

	// entries are the phase entry points, discovered from the framework's contract
	// rather than from names a service chose.
	entries map[Phase][]*node
	// corePkg is the import path whose lifecycle types define the phases.
	corePkg string
	// classified is every function literal that a LifecycleCallback literal assigns to
	// a phase. The traversal must not descend into one from its lexical parent: a
	// Starter closure written inside afterMicroserviceInitialized runs at START, and
	// walking into it because of where it was TYPED is exactly the caller-versus-name
	// mistake this guard exists to avoid.
	classified map[*ast.FuncLit]bool
}

// Scan loads the given patterns, discovers each phase's entry points, and reports every
// watched symbol reachable from the phase that must not reach it.
//
// dir is the directory the patterns are resolved from — the workspace root, so a single
// load covers every module in go.work.
func Scan(opts Options, rules []Rule, patterns ...string) (Result, error) {
	corePkg := opts.Core
	if corePkg == "" {
		corePkg = CorePkg
	}
	res := Result{
		EntryPoints: map[Phase]int{},
		Reached:     map[string]map[Phase]int{},
		Expected:    map[string][]Finding{},
	}

	// 🔴 THE FileSet IS SUPPLIED RATHER THAN LEFT NIL. packages.Load makes its own when
	// this is nil and does not hand it back, so every position rendered afterwards
	// resolves against an empty file set and prints as :0:0 — a finding with no file and
	// no line, which is an alarm rather than a gate.
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedTypesSizes |
			packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:   opts.Dir,
		Fset:  token.NewFileSet(),
		Tests: false,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return res, fmt.Errorf("loading packages: %w", err)
	}

	// 🔴 A TYPE ERROR IS AN INSTRUMENT FAILURE, NOT A FINDING. Every resolution this
	// guard performs comes from the type checker, so a package that did not type-check
	// yields a call graph with silent holes in it — and holes report as clean. Refuse
	// the whole run rather than measure part of the tree.
	var typeErrs []string
	for _, p := range pkgs {
		for _, e := range p.Errors {
			typeErrs = append(typeErrs, fmt.Sprintf("%s: %v", p.PkgPath, e))
		}
	}
	if len(typeErrs) > 0 {
		sort.Strings(typeErrs)
		if len(typeErrs) > 10 {
			typeErrs = append(typeErrs[:10], fmt.Sprintf("... and %d more", len(typeErrs)-10))
		}
		return res, fmt.Errorf("the loaded program does not type-check, so its call graph "+
			"cannot be trusted:\n  %s", strings.Join(typeErrs, "\n  "))
	}

	s := &scanner{
		fset:       cfg.Fset,
		pkgs:       pkgs,
		decls:      map[*types.Func]*node{},
		litNodes:   map[*ast.FuncLit]*node{},
		litPkg:     map[*ast.FuncLit]*packages.Package{},
		chaCache:   map[*types.Func][]*types.Func{},
		entries:    map[Phase][]*node{},
		classified: map[*ast.FuncLit]bool{},
		corePkg:    corePkg,
	}
	s.index()
	s.findEntryPoints()

	res.Packages = len(pkgs)
	for _, p := range pkgs {
		res.Files += len(p.Syntax)
	}
	for _, ph := range []Phase{Initialize, Start} {
		res.EntryPoints[ph] = len(s.entries[ph])
	}

	for _, rule := range rules {
		res.Unresolved = append(res.Unresolved, s.unresolved(rule)...)
		res.Reached[rule.Name] = map[Phase]int{}
		for _, ph := range []Phase{Initialize, Start} {
			hits := s.referencesFrom(ph, rule)
			res.Reached[rule.Name][ph] = len(hits)
			if ph == rule.Expect {
				for i := range hits {
					hits[i].Why = ph.String()
				}
				res.Expected[rule.Name] = hits
			}
			if ph != rule.Forbidden {
				continue
			}
			for _, h := range hits {
				h.Why = ph.String()
				res.Findings = append(res.Findings, h)
			}
		}
	}

	sort.Slice(res.Findings, func(i, j int) bool {
		a, b := res.Findings[i], res.Findings[j]
		if a.Pos.Filename != b.Pos.Filename {
			return a.Pos.Filename < b.Pos.Filename
		}
		if a.Pos.Line != b.Pos.Line {
			return a.Pos.Line < b.Pos.Line
		}
		return a.Rule < b.Rule
	})
	return res, nil
}

// index records every function body the traversal can walk, and every named type the
// interface expansion can search.
func (s *scanner) index() {
	for _, p := range s.pkgs {
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				if lit, ok := n.(*ast.FuncLit); ok {
					s.litPkg[lit] = p
				}
				return true
			})
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				obj, _ := p.TypesInfo.Defs[fd.Name].(*types.Func)
				if obj == nil {
					continue
				}
				s.decls[obj] = &node{fn: obj, pkg: p, body: fd.Body, name: funcName(obj)}
			}
		}
		if p.Types == nil {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			if named, ok := tn.Type().(*types.Named); ok {
				s.named = append(s.named, named)
			}
		}
	}
}

func funcName(fn *types.Func) string {
	sig, _ := fn.Type().(*types.Signature)
	if sig != nil && sig.Recv() != nil {
		t := sig.Recv().Type()
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		if named, ok := t.(*types.Named); ok {
			return "(" + named.Obj().Name() + ")." + fn.Name()
		}
	}
	if fn.Pkg() != nil {
		p := fn.Pkg().Path()
		return p[strings.LastIndex(p, "/")+1:] + "." + fn.Name()
	}
	return fn.Name()
}

func (s *scanner) litNode(lit *ast.FuncLit, name string) *node {
	if n, ok := s.litNodes[lit]; ok {
		return n
	}
	n := &node{lit: lit, pkg: s.litPkg[lit], body: lit.Body, name: name}
	s.litNodes[lit] = n
	return n
}

// findEntryPoints locates the two doors into each phase.
//
// 1. The LifecycleComponent method the manager invokes for that phase. Every
// implementation is an entry point, whoever ends up dispatching to it — which is the
// only way to reach an implementation the framework calls through the interface.
//
// 2. The Preprocess/Postprocess pair of the phase's LifecycleCallback. These are values
// handed to the manager, so the function they name may be anonymous and is never called
// by name anywhere; the declaration site of the pair is the only place the phase is
// stated.
func (s *scanner) findEntryPoints() {
	// Door 1: the exec methods.
	for fn, n := range s.decls {
		sig, _ := fn.Type().(*types.Signature)
		if sig == nil || sig.Recv() == nil {
			continue
		}
		for _, ph := range []Phase{Initialize, Start} {
			if fn.Name() == ph.execMethod() && isPhaseSignature(sig) {
				s.entries[ph] = append(s.entries[ph], n)
			}
		}
	}

	// Door 2: the callback pairs. Found by walking every composite literal whose TYPE
	// is core.LifecycleCallbacks, which makes an import alias or a dot-import
	// irrelevant, and reading the field by name OR by position — an unkeyed literal
	// assigns the same fields and names none of them.
	for _, p := range s.pkgs {
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if !isNamed(p.TypesInfo.TypeOf(lit), s.corePkg, "LifecycleCallbacks") {
					return true
				}
				for _, ph := range []Phase{Initialize, Start} {
					v := structField(p, lit, ph.callbackField())
					if v == nil {
						continue
					}
					s.recordCallbackPair(p, ph, v)
				}
				return true
			})
		}
	}
}

// isPhaseSignature reports whether a method has the LifecycleComponent shape,
// func(context.Context) error. Checked so a method that merely shares the name — on a
// type with nothing to do with the lifecycle — is not seeded as an entry point.
func isPhaseSignature(sig *types.Signature) bool {
	if sig.Params().Len() != 1 || sig.Results().Len() != 1 || sig.Variadic() {
		return false
	}
	if !isNamed(sig.Params().At(0).Type(), "context", "Context") {
		return false
	}
	named, ok := sig.Results().At(0).Type().(*types.Named)
	return ok && named.Obj().Pkg() == nil && named.Obj().Name() == "error"
}

// recordCallbackPair reads a phase's LifecycleCallback value and seeds whatever its
// Preprocess and Postprocess name.
//
// A value that is not a composite literal — a call, or a variable — cannot be read here.
// That is reported by the caller through the entry-point floor rather than guessed at:
// seeding nothing silently is how a guard reports clean on a tree it did not analyse.
func (s *scanner) recordCallbackPair(p *packages.Package, ph Phase, v ast.Expr) {
	lit, ok := unparen(v).(*ast.CompositeLit)
	if !ok || !isNamed(p.TypesInfo.TypeOf(lit), s.corePkg, "LifecycleCallback") {
		return
	}
	for _, field := range []string{"Preprocess", "Postprocess"} {
		fv := structField(p, lit, field)
		if fv == nil {
			continue
		}
		s.seedCallback(p, ph, field, unparen(fv))
	}
}

func (s *scanner) seedCallback(p *packages.Package, ph Phase, field string, v ast.Expr) {
	switch e := v.(type) {
	case *ast.FuncLit:
		// 🔴 THE ANONYMOUS CASE, which is the whole reason this door exists. There is
		// no name to key on; the literal IS the callback.
		s.classified[e] = true
		s.entries[ph] = append(s.entries[ph], s.litNode(e, ph.callbackField()+"."+field+" (closure)"))
	case *ast.Ident, *ast.SelectorExpr:
		fn, _ := objOf(p, e).(*types.Func)
		if fn == nil {
			return
		}
		for _, target := range s.resolve(fn) {
			if n, ok := s.decls[target]; ok {
				s.entries[ph] = append(s.entries[ph], n)
			}
		}
	}
}

// structField returns the value assigned to the named field of a struct literal,
// whether the literal is keyed or positional. A positional literal names no field at
// all, which is what makes it the obvious way to slip past a keyed-only reader.
func structField(p *packages.Package, lit *ast.CompositeLit, name string) ast.Expr {
	st, ok := deref(p.TypesInfo.TypeOf(lit)).Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	for i, el := range lit.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == name {
				return kv.Value
			}
			continue
		}
		if i < st.NumFields() && st.Field(i).Name() == name {
			return el
		}
	}
	return nil
}

// referencesFrom walks the call graph out of one phase's entry points and reports every
// reference to one of the rule's symbols found on the way.
//
// A REFERENCE, not only a call: `build := ms.NewHttpServer` names the constructor
// without calling it there, and the call through build is then invisible to anything
// that looks for a CallExpr. Naming it is evidence enough.
func (s *scanner) referencesFrom(ph Phase, rule Rule) []Finding {
	var out []Finding
	seen := map[any]bool{}
	// parent records how each node was reached, so a finding can print the chain
	// rather than only the line — the chain is what makes "this runs at initialize"
	// checkable by the person reading the failure.
	parent := map[any]*node{}

	var queue []*node
	for _, e := range s.entries[ph] {
		if seen[e.key()] {
			continue
		}
		seen[e.key()] = true
		queue = append(queue, e)
	}

	// 🔴 THE OTHER PHASE'S ENTRY POINTS ARE BARRIERS. A component whose Start is
	// invoked from an initializer callback would otherwise drag every ExecuteStart in
	// the program into the initialize phase through the interface expansion, and the
	// five legitimate per-start constructions would all report as findings. Stopping
	// is not a hole: work under an ExecuteStart runs again on every start whatever
	// else also reached it, so it is governed by the start-phase rule.
	barrier := map[any]bool{}
	for _, ph2 := range []Phase{Initialize, Start} {
		if ph2 == ph {
			continue
		}
		for _, e := range s.entries[ph2] {
			barrier[e.key()] = true
		}
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.pkg == nil || cur.body == nil {
			continue
		}
		info := cur.pkg.TypesInfo

		// 🔴 ast.Inspect VISITS A SELECTOR AND THEN ITS OWN Sel IDENTIFIER, so
		// `Microservice.NewHttpServer(port)` arrives twice and every count this guard
		// reports would be inflated — including the liveness floor, which would then
		// be cleared by half a working scan. Consumed marks the identifier the
		// selector already accounted for; the parent is always visited first, so the
		// mark is set before the child is reached.
		consumed := map[*ast.Ident]bool{}

		// The visitor's false result means "do not descend", which is how a closure
		// belonging to another phase is skipped without skipping its siblings.
		ast.Inspect(cur.body, func(n ast.Node) bool {
			if lit, ok := n.(*ast.FuncLit); ok {
				// A closure classified as another phase's callback is walked from
				// THAT phase, never from the function it happens to be written in.
				if s.classified[lit] {
					return false
				}
				return true
			}
			id, sel := identOf(n)
			if id == nil {
				return true
			}
			if sel != nil {
				consumed[sel.Sel] = true
			} else if consumed[id] {
				return true
			}
			fn, _ := info.Uses[id].(*types.Func)
			if fn == nil {
				return true
			}
			// A call to an instantiated generic resolves to the instantiation, whose
			// identity differs from the declaration the body was indexed under.
			fn = fn.Origin()
			for _, sym := range rule.Symbols {
				if sym.matches(fn) {
					out = append(out, Finding{
						Pos:   s.fset.Position(nodePos(n, sel)),
						Rule:  rule.Name,
						Sym:   sym,
						Chain: chainOf(cur, parent),
					})
					// 🔴 A WATCHED SYMBOL IS WHERE THE TRAVERSAL STOPS. Without this
					// one misplaced construction cascades: NewHttpServer delegates to
					// NewHttpServerForHandler, which delegates again, so a single
					// defect in a service reports three findings, two of them at
					// lines in core that are entirely correct. A guard that points at
					// correct code alongside the defect is one people learn to skim.
					return true
				}
			}
			// The edge. Every reference is an edge, not only a call: a function
			// handed somewhere as a value still runs.
			for _, target := range s.resolve(fn) {
				next, ok := s.decls[target]
				if !ok || seen[next.key()] || barrier[next.key()] {
					continue
				}
				seen[next.key()] = true
				parent[next.key()] = cur
				queue = append(queue, next)
			}
			return true
		})
	}
	return out
}

// identOf returns the identifier a node resolves a function through, and the selector it
// came from when there was one.
func identOf(n ast.Node) (*ast.Ident, *ast.SelectorExpr) {
	switch e := n.(type) {
	case *ast.SelectorExpr:
		return e.Sel, e
	case *ast.Ident:
		return e, nil
	}
	return nil, nil
}

func nodePos(n ast.Node, sel *ast.SelectorExpr) token.Pos {
	if sel != nil {
		return sel.Pos()
	}
	return n.Pos()
}

func chainOf(n *node, parent map[any]*node) []string {
	var chain []string
	for cur := n; cur != nil; cur = parent[cur.key()] {
		chain = append(chain, cur.name)
		if len(chain) > 32 {
			break
		}
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// resolve turns a callee into the set of function bodies it may enter.
//
// For a concrete function that is itself; for an INTERFACE method it is every method in
// the loaded program that could be dispatched to, found by class-hierarchy analysis.
// That is the step that reaches device-management's InboundEventsProcessor: the
// framework holds it as a LifecycleComponent and no static name reaches its
// ExecuteInitialize.
func (s *scanner) resolve(fn *types.Func) []*types.Func {
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil || sig.Recv() == nil {
		return []*types.Func{fn}
	}
	iface, ok := sig.Recv().Type().Underlying().(*types.Interface)
	if !ok {
		return []*types.Func{fn}
	}
	if cached, ok := s.chaCache[fn]; ok {
		return cached
	}
	var out []*types.Func
	for _, named := range s.named {
		if _, isIface := named.Underlying().(*types.Interface); isIface {
			continue
		}
		if named.TypeParams() != nil && named.TypeParams().Len() > 0 {
			continue
		}
		for _, t := range []types.Type{types.Type(named), types.NewPointer(named)} {
			if !types.Implements(t, iface) {
				continue
			}
			ms := types.NewMethodSet(t)
			if sel := ms.Lookup(fn.Pkg(), fn.Name()); sel != nil {
				if m, ok := sel.Obj().(*types.Func); ok {
					out = append(out, m)
				}
			}
			break
		}
	}
	s.chaCache[fn] = out
	return out
}

// unresolved reports which of a rule's symbols do not exist in the loaded program.
//
// 🔴 THIS IS THE SHARPEST LIVENESS CHECK THE GUARD HAS. A renamed or moved constructor
// makes every reference to it stop matching, which drives the finding count to zero —
// indistinguishable from a clean tree by any other measure. Asking whether the symbol
// still EXISTS turns that into an instrument failure.
func (s *scanner) unresolved(rule Rule) []Symbol {
	var missing []Symbol
	for _, sym := range rule.Symbols {
		if !s.symbolExists(sym) {
			missing = append(missing, sym)
		}
	}
	return missing
}

func (s *scanner) symbolExists(sym Symbol) bool {
	for _, p := range s.pkgs {
		if p.Types == nil || p.PkgPath != sym.Pkg {
			continue
		}
		scope := p.Types.Scope()
		if sym.Recv == "" {
			fn, _ := scope.Lookup(sym.Name).(*types.Func)
			return fn != nil
		}
		tn, _ := scope.Lookup(sym.Recv).(*types.TypeName)
		if tn == nil {
			return false
		}
		named, ok := tn.Type().(*types.Named)
		if !ok {
			return false
		}
		for _, t := range []types.Type{types.Type(named), types.NewPointer(named)} {
			if sel := types.NewMethodSet(t).Lookup(p.Types, sym.Name); sel != nil {
				return true
			}
		}
		return false
	}
	return false
}

func isNamed(t types.Type, pkgPath, name string) bool {
	named, ok := deref(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Name() == name && obj.Pkg() != nil && obj.Pkg().Path() == pkgPath
}

func deref(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	if ptr, ok := t.(*types.Pointer); ok {
		return ptr.Elem()
	}
	return t
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

func objOf(p *packages.Package, e ast.Expr) types.Object {
	switch v := e.(type) {
	case *ast.Ident:
		return p.TypesInfo.Uses[v]
	case *ast.SelectorExpr:
		return p.TypesInfo.Uses[v.Sel]
	}
	return nil
}
