// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
)

// UnpacedReadLoop is one function the scan found: it reads from a message reader and can
// come back for another read after a read error, without a core.ReadPacer to bound how
// long the failures may go on.
//
// Why is the function the unit rather than the loop? Because that is where the fix goes.
// A pacer is a field on the processor and a call in the loop, and both halves are read
// together when someone opens the function. A finding keyed to a line number would move
// on every unrelated edit above it.
type UnpacedReadLoop struct {
	File     string // absolute path, carried separately so callers need not parse Pos
	Pos      string // file:line:col of the read itself, for the failure message
	Function string // the enclosing function's name
	Shape    string // "loop" (retries in its own for) or "helper" (the caller's loop)
}

// readMethod is the reader entry point this guard follows, matched by NAME.
//
// Matching by name is what lets the scan run across modules core does not import, and it
// is also the limit worth stating: a differently-named wrapper around a read would not be
// matched, and a same-named method on something that is not a MessageReader would be. The
// tree has one MessageReader interface and one method on it, so today the name and the
// thing coincide.
const readMethod = "ReadMessage"

// pacerMethod is the call that proves a read loop is bounded. It is the one on
// core.ReadPacer; the pacer's OTHER half (Succeeded) is not checked here, because a loop
// that paces but never resets is a different defect with a different fix, and the pacer's
// own tests are where that one is pinned.
const pacerMethod = "PauseAfterError"

// unpacedReadLoopsUnder walks root and reports every function that retries a failing read
// without a pacer, along with the set of directories the walk descended into.
//
// 🔴 WHY THIS IS A REPOSITORY-WIDE GUARD AND NOT A CHECKLIST. The eleven loops that now
// carry a pacer were adopted in five separate passes, and the remaining set was
// hand-counted from grep output THREE times across those passes — as "three left", then
// as "seven plus one", then as "eight". The tree says nine, of which one is not a defect
// at all. Every one of those numbers was written down in a commit message or a PR body by
// someone who had just read the grep output. Enumerating this by hand does not converge,
// which is the argument for asking the tree instead.
//
// 🔴 WHAT MAKES A READ LOOP A FINDING, precisely, because "calls ReadMessage" is not it:
//
//   - The read sits in a for loop, and a read error can lead back to another read. That is
//     the unbounded retry — the pod goes on reporting ready while consuming nothing, and
//     spinning slower is not making progress. See core.ReadPacer.
//   - Or the read sits in a function with no loop of its own, which means the loop is in a
//     caller this scan cannot see. Those are required to pace unconditionally, and that is
//     deliberately the fail-closed side: six such readers exist today and all six pace, so
//     the rule costs nothing now and catches the seventh.
//
// 🔴 WHAT IT DELIBERATELY DOES NOT FLAG. A loop that FAILS CLOSED on a read error — every
// guard on the error variable leaves the loop, by return or by break — never retries the
// error, so there is nothing to bound. ResolvedEventsProcessor.drainFactToHead is the
// worked example: it returns the error up to a caller that aborts the catch-up. Requiring
// a pacer there would mean adding a retry in order to bound it.
//
// Two consequences worth knowing before this fails on you, both inherited from the guards
// next door:
//
//   - It reads files outside this module, so `-count=1` is load-bearing. Go's test cache
//     does not track them, and a cached PASS would survive a loop added elsewhere.
//   - A loop added in a service module is reported by THIS module's test run. The message
//     names the offending file, line and function.
func unpacedReadLoopsUnder(root string) ([]UnpacedReadLoop, map[string]bool, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	visited := map[string]bool{}
	var files []string

	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != abs && skipDirInScan(d.Name()) {
				return fs.SkipDir
			}
			visited[path] = true
			return nil
		}
		// Only production sources. A test reads in a loop all the time — driving a fake
		// reader to exhaustion is how the pacer's own behaviour is measured — and none of
		// those loops outlive the test binary.
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("no non-test .go files found under %s, so the scan asserts nothing", abs)
	}

	var found []UnpacedReadLoop
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing %s: %w", file, err)
		}
		found = append(found, unpacedReadLoopsIn(fset, parsed)...)
	}
	return found, visited, nil
}

// unpacedReadLoopsIn reports the findings in one parsed file.
func unpacedReadLoopsIn(fset *token.FileSet, f *ast.File) []UnpacedReadLoop {
	var found []UnpacedReadLoop
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		for _, site := range readSites(fn.Body) {
			if u, bad := judgeReadSite(fset, fn, site); bad {
				found = append(found, u)
			}
		}
	}
	return found
}

// readSite is one ReadMessage call together with the ancestors between it and the
// function body, outermost first. The ancestry is what distinguishes a read that retries
// from one that does not: which for loop encloses it, and whether a closure comes first.
type readSite struct {
	call  *ast.CallExpr
	stack []ast.Node
}

// readSites returns every ReadMessage call in body, each with its ancestor stack.
func readSites(body *ast.BlockStmt) []readSite {
	var sites []readSite
	var stack []ast.Node
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == readMethod {
				sites = append(sites, readSite{call: call, stack: append([]ast.Node(nil), stack...)})
			}
		}
		return true
	})
	return sites
}

// judgeReadSite decides whether one read is an unpaced retry.
func judgeReadSite(fset *token.FileSet, fn *ast.FuncDecl, site readSite) (UnpacedReadLoop, bool) {
	loopBody, scope, shape := readScope(site.stack)
	if scope == nil {
		scope = fn.Body
	}
	if callsWithin(scope, pacerMethod) {
		return UnpacedReadLoop{}, false
	}
	// A loop that leaves on every read error retries nothing, so there is no run of
	// failures to bound. A helper has no loop of its own to read, and is required to pace.
	if shape == "loop" && failsClosed(loopBody, errVarOf(site.stack)) {
		return UnpacedReadLoop{}, false
	}
	at := fset.Position(site.call.Pos())
	return UnpacedReadLoop{File: at.Filename, Pos: at.String(), Function: fn.Name.Name, Shape: shape}, true
}

// readScope walks outward from a read and returns the body of the loop it retries in (nil
// if none), the block a pacer must be found in, and which of the two shapes it is.
//
// The scope is the LOOP's body rather than the whole function on purpose: a function
// holding two read loops, one paced and one not, would otherwise read as clean because
// the paced one's call satisfies the unpaced one.
//
// A closure stops the walk. A read inside a func literal is scoped to that literal — its
// pacer has to be reachable from inside it, and a for loop OUTSIDE the closure does not
// make the closure's own read a retry.
func readScope(stack []ast.Node) (loopBody *ast.BlockStmt, scope *ast.BlockStmt, shape string) {
	for i := len(stack) - 1; i >= 0; i-- {
		switch n := stack[i].(type) {
		case *ast.ForStmt:
			return n.Body, n.Body, "loop"
		case *ast.RangeStmt:
			return n.Body, n.Body, "loop"
		case *ast.FuncLit:
			return nil, n.Body, "helper"
		}
	}
	return nil, nil, "helper"
}

// errVarOf returns the name the read's error is bound to, or "" when it is discarded or
// the read is not part of an assignment. An empty name makes failsClosed answer false,
// which is the fail-closed direction: a read whose error nobody names cannot be shown to
// leave the loop.
func errVarOf(stack []ast.Node) string {
	for i := len(stack) - 1; i >= 0; i-- {
		assign, ok := stack[i].(*ast.AssignStmt)
		if !ok {
			continue
		}
		if len(assign.Lhs) == 0 {
			return ""
		}
		if ident, ok := assign.Lhs[len(assign.Lhs)-1].(*ast.Ident); ok && ident.Name != "_" {
			return ident.Name
		}
		return ""
	}
	return ""
}

// failsClosed reports whether every read error leaves the loop, so that no error can lead
// back around to another read.
//
// It answers true only when BOTH hold:
//
//   - the loop has a catch-all `if <err> != nil` guard whose body leaves the loop, and
//   - every other guard testing that variable leaves the loop too.
//
// The catch-all is required, and it is not a formality. A loop carrying only an
// `errors.Is(err, io.EOF)` guard would otherwise be exempted while being the WORST case
// this guard exists to catch: a non-EOF error falls past the guard into the handler with
// a zero-value message, at whatever rate the reader returns it.
func failsClosed(body *ast.BlockStmt, errVar string) bool {
	if body == nil || errVar == "" {
		return false
	}
	catchAll, all := false, true
	for _, g := range errorGuardsIn(body, errVar) {
		exits := terminatesBlock(g.stmt.Body, g.underSwitch)
		if !exits {
			all = false
		}
		if isNilComparison(g.stmt.Cond, errVar) && exits {
			catchAll = true
		}
	}
	return catchAll && all
}

// errorGuard is one `if` in the loop that tests the read's error, plus whether a switch or
// select sits between it and the loop — which decides what a bare `break` in it breaks.
type errorGuard struct {
	stmt        *ast.IfStmt
	underSwitch bool
}

// errorGuardsIn returns every if-statement under body whose condition names errVar.
//
// It records each guard's switch nesting rather than assuming there is none, because a
// `break` inside a switch or select breaks THAT and not the loop. Reading such a break as
// an exit would exempt a loop that is still retrying, and an exemption is the one kind of
// mistake a guard cannot report.
func errorGuardsIn(body *ast.BlockStmt, errVar string) []errorGuard {
	var guards []errorGuard
	depth := 0
	var stack []ast.Node
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			switch stack[len(stack)-1].(type) {
			case *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
				depth--
			}
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		switch x := n.(type) {
		case *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
			depth++
		case *ast.FuncLit:
			// A closure's control flow is its own; a return in it returns from the
			// closure, not from the loop.
			_ = x
		case *ast.IfStmt:
			if mentions(x.Cond, errVar) {
				guards = append(guards, errorGuard{stmt: x, underSwitch: depth > 0})
			}
		}
		return true
	})
	return guards
}

// terminatesBlock reports whether a block's control flow leaves the enclosing loop on
// every path, judged from its LAST statement.
//
// Reading only the last statement is the approximation, and it is the conservative one: a
// block that exits early but ends in a `continue` is read as retrying, which it is.
func terminatesBlock(b *ast.BlockStmt, underSwitch bool) bool {
	if b == nil || len(b.List) == 0 {
		return false
	}
	return terminatesStmt(b.List[len(b.List)-1], underSwitch)
}

func terminatesStmt(s ast.Stmt, underSwitch bool) bool {
	switch x := s.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		// A labelled break names its loop and leaves it. A bare one leaves whatever is
		// innermost, which is the loop only if no switch or select intervenes. `continue`
		// and `goto` are retries by definition and by assumption respectively.
		return x.Tok == token.BREAK && (x.Label != nil || !underSwitch)
	case *ast.BlockStmt:
		return terminatesBlock(x, underSwitch)
	case *ast.IfStmt:
		return terminatesBlock(x.Body, underSwitch) && x.Else != nil && terminatesStmt(x.Else, underSwitch)
	case *ast.ExprStmt:
		return isFatalCall(x.X)
	}
	return false
}

// isFatalCall reports whether an expression is a call that does not return: panic(), or
// zerolog's Fatal()/Panic() terminals, which a read loop is entitled to use instead of
// pacing because the process ends either way.
func isFatalCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	if ident, ok := call.Fun.(*ast.Ident); ok {
		return ident.Name == "panic"
	}
	for {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		switch sel.Sel.Name {
		case "Fatal", "Panic", "Fatalf", "Fatalln":
			return true
		}
		inner, ok := sel.X.(*ast.CallExpr)
		if !ok {
			return false
		}
		call = inner
	}
}

// isNilComparison reports whether cond is the catch-all `name != nil`.
func isNilComparison(cond ast.Expr, name string) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	lhs, lok := bin.X.(*ast.Ident)
	rhs, rok := bin.Y.(*ast.Ident)
	return lok && rok && lhs.Name == name && rhs.Name == "nil"
}

// mentions reports whether an expression names the given identifier.
func mentions(e ast.Expr, name string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && ident.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// callsWithin reports whether a block contains a call to the named method.
func callsWithin(b *ast.BlockStmt, method string) bool {
	found := false
	ast.Inspect(b, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == method {
			found = true
		}
		return !found
	})
	return found
}
