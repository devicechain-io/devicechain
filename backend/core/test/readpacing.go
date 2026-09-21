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

// unpacedReadLoop is one function the scan found: it reads from a message reader and can
// come back for another read after a read error, without a core.ReadPacer to bound how
// long the failures may go on.
//
// Why is the function the unit rather than the loop? Because that is where the fix goes.
// A pacer is a field on the processor and a call in the loop, and both halves are read
// together when someone opens the function. A finding keyed to a line number would move
// on every unrelated edit above it.
type unpacedReadLoop struct {
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
// tree has one MessageReader interface and one read method on it, so today the name and the
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
//     deliberately the fail-closed side: seven such readers exist today and all seven pace, so
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
func unpacedReadLoopsUnder(root string) ([]unpacedReadLoop, map[string]bool, error) {
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

	var found []unpacedReadLoop
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
func unpacedReadLoopsIn(fset *token.FileSet, f *ast.File) []unpacedReadLoop {
	var found []unpacedReadLoop
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
// from one that does not: which for loop encloses it, whether ANOTHER loop encloses that
// one, and whether a closure comes first.
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

// scope describes where one read sits: the loop it retries in, the block a pacer must be
// found in, which shape it is, and whether a further loop encloses the first.
type scope struct {
	loopBody *ast.BlockStmt
	block    *ast.BlockStmt
	shape    string
	nested   bool
}

// judgeReadSite decides whether one read is an unpaced retry.
func judgeReadSite(fset *token.FileSet, fn *ast.FuncDecl, site readSite) (unpacedReadLoop, bool) {
	sc := readScope(site.stack)
	block := sc.block
	if block == nil {
		block = fn.Body
	}
	errVar := errVarOf(site.stack)
	if pacesThisRead(block, errVar) {
		return unpacedReadLoop{}, false
	}
	// A loop that leaves on every read error retries nothing, so there is no run of
	// failures to bound. A helper has no loop of its own to read, and is required to pace.
	if sc.shape == "loop" && failsClosed(sc.loopBody, errVar, sc.nested) {
		return unpacedReadLoop{}, false
	}
	at := fset.Position(site.call.Pos())
	return unpacedReadLoop{File: at.Filename, Pos: at.String(), Function: fn.Name.Name, Shape: sc.shape}, true
}

// readScope walks outward from a read and describes where it sits.
//
// The block a pacer must appear in is the LOOP's body rather than the whole function on
// purpose: a function holding two read loops, one paced and one not, would otherwise read
// as clean because the paced one's call answers for the other's absence.
//
// A closure stops the walk. A read inside a func literal is scoped to that literal — its
// pacer has to be reachable from inside it, and a for loop OUTSIDE the closure does not
// make the closure's own read a retry. The cost is stated rather than hidden: a read
// wrapped in an inline closure for timing or tracing inside an otherwise paced loop is
// reported as a helper, and wants its pacer inside the wrapper.
//
// 🔴 NESTED IS THE FIELD THAT MATTERS AND IT WAS MISSING. A `break` leaves the innermost
// loop — which, when that loop sits inside another, lands in the OUTER loop and can read
// again. Without this, every read loop nested in a second loop was silently exempt, on
// the strength of a break that ends nothing.
func readScope(stack []ast.Node) scope {
	for i := len(stack) - 1; i >= 0; i-- {
		var body *ast.BlockStmt
		switch n := stack[i].(type) {
		case *ast.ForStmt:
			body = n.Body
		case *ast.RangeStmt:
			body = n.Body
		case *ast.FuncLit:
			return scope{block: n.Body, shape: "helper"}
		}
		if body == nil {
			continue
		}
		return scope{loopBody: body, block: body, shape: "loop", nested: enclosedByAnotherLoop(stack[:i])}
	}
	return scope{shape: "helper"}
}

// enclosedByAnotherLoop reports whether a further loop encloses the one just found, with
// no closure between them.
func enclosedByAnotherLoop(outer []ast.Node) bool {
	for i := len(outer) - 1; i >= 0; i-- {
		switch outer[i].(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			return true
		case *ast.FuncLit:
			return false
		}
	}
	return false
}

// walkNoClosures visits every node under root WITHOUT descending into func literals,
// carrying each node's ancestor stack.
//
// Not descending is the point, not an optimisation. A closure's control flow is its own: a
// `return` in it returns from the closure, and a call in it may never run at all. An
// earlier version of this file said exactly that in a comment while the code descended
// anyway, which made both claims below false.
func walkNoClosures(root ast.Node, visit func(ast.Node, []ast.Node)) {
	var stack []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if _, isLit := n.(*ast.FuncLit); isLit && n != root {
			return false
		}
		stack = append(stack, n)
		visit(n, stack)
		return true
	})
}

// errVarOf returns the name the read's error is bound to, or "" when it is discarded or
// the read is not part of an assignment. An empty name makes both failsClosed and
// pacesThisRead answer false, which is the fail-closed direction: a read whose error
// nobody names cannot be shown to be bounded at all.
func errVarOf(stack []ast.Node) string {
	for i := len(stack) - 1; i >= 0; i-- {
		assign, ok := stack[i].(*ast.AssignStmt)
		if !ok {
			continue
		}
		if ident, ok := assign.Lhs[len(assign.Lhs)-1].(*ast.Ident); ok && ident.Name != "_" {
			return ident.Name
		}
		return ""
	}
	return ""
}

// pacesThisRead reports whether the block bounds THIS read's failures.
//
// 🔴 THREE CONDITIONS, AND EACH ONE IS A WAY THE CHECK WAS SILENT BEFORE. Asking only
// whether the name PauseAfterError appears somewhere in the loop — which is what this did
// — passed a call parked in a closure nobody invokes, a call under `if false`, a call
// whose verdict is thrown away with `_ =` so the loop never actually stops, and a loop
// holding TWO reads where only the first is paced. So the call must:
//
//   - be reachable in the loop itself, not inside a func literal;
//   - take THIS read's error variable as an argument, which is what ties the pacer to the
//     read rather than to a sibling read in the same body; and
//   - have its verdict consumed — as an if condition, a return operand, or an assignment.
//     PauseAfterError reports whether the loop must STOP, and a caller that discards it
//     goes on reading past an exhausted budget, which is the defect wearing the fix.
//
// Like readMethod, the pacer is matched BY NAME. A same-named method on some other type
// would satisfy this, and a differently-named wrapper around a pacer would not.
func pacesThisRead(block *ast.BlockStmt, errVar string) bool {
	if errVar == "" {
		return false
	}
	found := false
	walkNoClosures(block, func(n ast.Node, stack []ast.Node) {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != pacerMethod {
			return
		}
		for _, arg := range call.Args {
			if mentions(arg, errVar) && verdictConsumed(stack) {
				found = true
			}
		}
	})
	return found
}

// verdictConsumed reports whether a call's boolean result is actually read, given the
// call's ancestor stack. A bare call statement drops it.
func verdictConsumed(stack []ast.Node) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		switch n := stack[i].(type) {
		case *ast.ParenExpr, *ast.UnaryExpr, *ast.BinaryExpr:
			continue
		case *ast.IfStmt, *ast.ReturnStmt, *ast.SwitchStmt, *ast.CaseClause, *ast.ForStmt:
			return true
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && ident.Name != "_" {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

// failsClosed reports whether every read error leaves the loop, so that no error can lead
// back around to another read.
//
// It answers true only when BOTH hold:
//
//   - the loop has a catch-all `if <err> != nil` guard that is UNCONDITIONALLY reached and
//     whose body leaves the loop, and
//   - every other guard testing that variable leaves the loop too.
//
// The catch-all is required, and it is not a formality. A loop carrying only an
// `errors.Is(err, io.EOF)` guard would otherwise be exempted while being the WORST case
// this guard exists to catch: a non-EOF error falls past the guard into the handler with
// a zero-value message, at whatever rate the reader returns it.
//
// 🔴 AND IT MUST BE UNCONDITIONAL, which is a second way this was silent. A catch-all
// parked inside `if cfg.strict { … }` handles nothing on the other branch, and the loop
// spins there exactly as if the guard were absent.
func failsClosed(body *ast.BlockStmt, errVar string, nested bool) bool {
	if body == nil || errVar == "" {
		return false
	}
	catchAll, all := false, true
	for _, g := range errorGuardsIn(body, errVar) {
		exits := terminatesBlock(g.stmt.Body, exitRules{
			bareBreak:     g.canBreak && !nested,
			labelledBreak: !nested,
		})
		if !exits {
			all = false
		}
		if isNilComparison(g.stmt.Cond, errVar) && exits && g.unconditional {
			catchAll = true
		}
	}
	return catchAll && all
}

// errorGuard is one `if` in the loop that tests the read”'s error, plus the two facts about
// where it sits that decide how to read it: whether a bare `break` in it would leave the
// loop, and whether it is reached on every pass.
type errorGuard struct {
	stmt          *ast.IfStmt
	canBreak      bool
	unconditional bool
}

// errorGuardsIn returns every if-statement under body whose condition names errVar,
// excluding those inside closures.
//
// It records each guard”'s switch nesting rather than assuming there is none, because a
// `break` inside a switch or select breaks THAT and not the loop. Reading such a break as
// an exit would exempt a loop that is still retrying, and an exemption is the one kind of
// mistake a guard cannot report.
func errorGuardsIn(body *ast.BlockStmt, errVar string) []errorGuard {
	var guards []errorGuard
	walkNoClosures(body, func(n ast.Node, stack []ast.Node) {
		ifs, ok := n.(*ast.IfStmt)
		if !ok || !mentions(ifs.Cond, errVar) {
			return
		}
		guards = append(guards, errorGuard{
			stmt:          ifs,
			canBreak:      !underSwitch(stack),
			unconditional: reachedEveryPass(stack, errVar),
		})
	})
	return guards
}

// underSwitch reports whether a switch or select sits between a node and the loop body.
func underSwitch(stack []ast.Node) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		switch stack[i].(type) {
		case *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
			return true
		}
	}
	return false
}

// reachedEveryPass reports whether a guard is evaluated on every pass through the loop —
// that is, whether everything between it and the loop body is either a plain block or a
// further test of the SAME error variable.
func reachedEveryPass(stack []ast.Node, errVar string) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		switch n := stack[i].(type) {
		case *ast.BlockStmt:
			continue
		case *ast.IfStmt:
			if !mentions(n.Cond, errVar) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// terminatesBlock reports whether a block”'s control flow leaves the enclosing loop on
// every path, judged from its LAST statement.
//
// Reading only the last statement is the approximation, and it is the conservative one: a
// block that exits early but ends in a `continue` is read as retrying, which it is.
// exitRules says which kinds of break actually leave the read loop at a given spot. The
// two differ: a bare break leaves the INNERMOST enclosing construct, so a switch, a select
// or a second loop all take it; a labelled one names its loop and is stopped only by the
// ambiguity of which loop it names when the read loop is nested.
type exitRules struct {
	bareBreak     bool
	labelledBreak bool
}

// terminatesBlock reports whether a block'"'"'s control flow leaves the enclosing loop on
// every path, judged from its LAST statement.
//
// Reading only the last statement is the approximation, and it is the conservative one: a
// block that exits early but ends in a `continue` is read as retrying, which it is.
func terminatesBlock(b *ast.BlockStmt, rules exitRules) bool {
	if b == nil || len(b.List) == 0 {
		return false
	}
	return terminatesStmt(b.List[len(b.List)-1], rules)
}

func terminatesStmt(s ast.Stmt, rules exitRules) bool {
	switch x := s.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		if x.Tok != token.BREAK {
			// `continue` and `goto` are retries, by definition and by assumption.
			return false
		}
		if x.Label != nil {
			return rules.labelledBreak
		}
		return rules.bareBreak
	case *ast.BlockStmt:
		return terminatesBlock(x, rules)
	case *ast.IfStmt:
		return terminatesBlock(x.Body, rules) && x.Else != nil && terminatesStmt(x.Else, rules)
	case *ast.ExprStmt:
		return isFatalCall(x.X)
	}
	return false
}

// isFatalCall reports whether an expression is a call that does not return: panic(),
// os.Exit(), or zerolog”'s Fatal()/Panic() terminals, which a read loop is entitled to use
// instead of pacing because the process ends either way.
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
		case "Exit":
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" {
				return true
			}
			return false
		}
		inner, ok := sel.X.(*ast.CallExpr)
		if !ok {
			return false
		}
		call = inner
	}
}

// isNilComparison reports whether cond is the catch-all `name != nil`, written either way
// round.
func isNilComparison(cond ast.Expr, name string) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	isName := func(e ast.Expr) bool { i, ok := e.(*ast.Ident); return ok && i.Name == name }
	isNil := func(e ast.Expr) bool { i, ok := e.(*ast.Ident); return ok && i.Name == "nil" }
	return (isName(bin.X) && isNil(bin.Y)) || (isNil(bin.X) && isName(bin.Y))
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
