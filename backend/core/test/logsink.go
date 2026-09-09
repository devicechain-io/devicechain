// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	zlog "github.com/rs/zerolog/log"
)

// LogSink is a process-wide destination for the global zerolog logger that a test
// can switch collection on and off against.
//
// 🔴 IT IS A SINK WITH A SWITCH RATHER THAN A LOGGER A TEST SWAPS IN AND OUT, AND
// that is the whole point. zlog.Logger is a plain package-level variable with no
// synchronization, while the code under test may log from goroutines it does not
// own — the NATS client's async callback dispatcher, a sampler, a retry loop. A
// helper that assigns to zlog.Logger for the duration of a test and restores it in
// t.Cleanup is therefore writing that variable while those goroutines are reading
// it, which is a genuine data race and one the race detector reports (the restoring
// Cleanup on one side, zerolog's own read of the logger on the other). Ordering the
// restore after the last writer is not something a test can establish: the writer
// belongs to a library, may outlive the connection that spawned it, and the race is
// reported even when the value written happens to be the same one.
//
// Installing the sink once, before any of those goroutines exist, and never
// reassigning removes the race by construction. What a test toggles instead is the
// mutex-guarded capture flag below, which every writer already contends on properly.
//
// Everything is passed through to the underlying writer whether or not a capture is
// running, so capturing never swallows the log output of the rest of the package.
//
// 🔴 A DETACHED SINK IS SILENT, NOT LOUD. If anything reassigns the global logger
// after the sink is installed, every capture goes empty — and an assertion that only
// checks for the ABSENCE of a message then passes for the wrong reason, saying
// nothing at all while looking like a pass. Production code does reassign it:
// core/core/microservice.go configures the global logger during startup, so a test
// that builds a real microservice detaches the sink from that point on. A package
// combining the two needs its negative assertions paired with a positive one — find
// SOMETHING in the capture — so that an empty buffer fails rather than passes.
type LogSink struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	capturing bool
	out       io.Writer
}

// InstallLogSink points the global zerolog logger at a new sink writing through to
// stderr, and returns the sink.
//
// 🔴 Call it from TestMain, before m.Run — that is the only moment at which writing
// zlog.Logger is safe, because no test has started and so nothing else can be
// logging yet. It is deliberately never restored: nothing after m.Run needs the
// original, and assigning there would be the same race in a different place.
//
// The logger's existing configuration (timestamps, hooks, level) is preserved; only
// its writer changes.
//
// 🔴 A SECOND CALL PANICS, and that is the point rather than tidiness. This function
// is the one sanctioned write to zlog.Logger in test code, so it is also the obvious
// place for the pattern it replaces to come back: a helper that every test calls to
// get "its own" capture would write the global once per test, from inside a package
// the source scan below cannot see, and would race exactly as the swaps it replaced
// did. Refusing the second install makes that shape fail on its first run instead of
// under -race months later. One install per test binary is all the contract needs —
// each package's tests are their own process, so every TestMain gets its own.
func InstallLogSink() *LogSink {
	if !sinkInstalled.CompareAndSwap(false, true) {
		panic("the shared log sink is already installed in this test binary: " +
			"InstallLogSink writes the global zerolog logger, which is safe only once, " +
			"from TestMain, before any test has started a goroutine that logs. Call it " +
			"once per test binary and toggle capture with Capture(t) instead")
	}
	sink := NewLogSink(os.Stderr)
	zlog.Logger = zlog.Logger.Output(sink)
	return sink
}

// sinkInstalled records whether this test binary has already had a sink installed.
var sinkInstalled atomic.Bool

// NewLogSink returns a sink that passes through to out. Prefer InstallLogSink; this
// exists for tests of the sink itself and for a caller that has somewhere other than
// stderr to pass writes through to.
func NewLogSink(out io.Writer) *LogSink {
	return &LogSink{out: out}
}

// Write records the line when a capture is running and always passes it through.
//
// The pass-through happens under the same lock rather than after releasing it, which
// makes the sink safe in front of ANY writer. Doing it outside would be safe only for
// an os.File, whose Write is one syscall; a caller handing this a bytes.Buffer would
// get a data race between two logging goroutines that this type is specifically here
// to prevent. Serializing log writes across a test binary costs nothing worth having.
func (s *LogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.capturing {
		s.buf.Write(p)
	}
	return s.out.Write(p)
}

// Capture starts collecting log output for the remainder of t and returns the sink,
// so the usual shape is `logs := sink.Capture(t)` followed by reads of logs.String().
// Collection stops and the buffer is dropped in a t.Cleanup.
//
// It fails the test if a capture is already running: the sink is process-wide, so two
// tests capturing at once would each read the other's output. In practice that means a
// package using this must not run the capturing tests with t.Parallel().
func (s *LogSink) Capture(t *testing.T) *LogSink {
	t.Helper()
	s.mu.Lock()
	if s.capturing {
		s.mu.Unlock()
		t.Fatal("a log capture is already running; the sink is process-wide, so two " +
			"concurrent captures would read each other's output. Do not call Capture " +
			"from a test that has called t.Parallel()")
	}
	s.buf.Reset()
	s.capturing = true
	s.mu.Unlock()
	t.Cleanup(func() {
		s.mu.Lock()
		s.capturing = false
		s.buf.Reset()
		s.mu.Unlock()
	})
	return s
}

// String returns what has been captured so far.
func (s *LogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// AssertNoGlobalLoggerSwapUnder fails t if any _test.go file ANYWHERE under root
// writes to the global zerolog logger — by assigning to it by name, by taking its
// address, or by calling a method that mutates it in place — or dot-imports the
// package that holds it.
//
// This is the gate that keeps the sink above adopted once a package has adopted it. It
// is worth having as a separate check because the failure it guards against is only
// visible under `go test -race`, which the required CI gates do not run — so a
// reintroduced swap would otherwise be caught by nobody until the next person to run
// -race by hand, at the moment they are chasing something else.
//
// 🔴 IT SCANS A TREE, NOT A DIRECTORY, AND THAT IS THE DIFFERENCE THAT MAKES IT A
// GATE. The first version took one directory and was called from one package's test,
// which meant every other package in the repository was outside its view — not by a
// filter anyone could find and delete, but because nothing pointed it there. Three
// packages went on swapping the logger for as long as that was true, and a fourth
// created tomorrow would have been just as invisible. Coverage a package has to opt
// into is coverage of whoever remembered; pointed at the workspace root, this covers
// a package the moment it exists.
//
// It PARSES rather than greps: a lexical match is evaded by an import alias and fires
// on the same text inside a comment. Production code that configures the global logger
// at startup is none of this function's business, so only _test.go files are examined.
//
// mustVisit names directories the walk is required to have descended into, and it is
// the vacuity check: a scan that reached nothing reports the same clean result as a
// scan that found nothing, so the caller supplies a second, independent list of where
// the tree is (the workspace's own module list, for the repository-wide caller) and
// this fails if the walk did not get there.
//
// # What it cannot see
//
//  1. A write from a NON-TEST file. Only _test.go is examined, so a helper that swaps
//     the logger on a test's behalf is invisible here. That is not hypothetical —
//     centralising a swap into a helper is exactly the shape a migration onto this
//     sink invites. It is answered on the other side instead, by InstallLogSink
//     refusing to run twice: the one sanctioned helper cannot become a per-test swap.
//     Widening the scan to every .go file is not the answer, because configuring the
//     global logger at startup is what production code is supposed to do — the
//     microservice bootstrap and every simulator main do it legitimately.
//  2. Reflection. Setting the logger through reflect.Value.Set writes it without
//     naming it in an assignable position — though reaching it that way has to take
//     its address first, which this does catch.
//
// The mutating-method set is pinned by NAME against zerolog v1.35.1, where
// UpdateContext is the only method with a pointer receiver that writes through it. A
// future release adding another is a hole until this list grows.
func AssertNoGlobalLoggerSwapUnder(t *testing.T, root string, mustVisit []string) {
	t.Helper()
	swaps, visited, err := globalLoggerSwapsUnder(root)
	if err != nil {
		t.Fatalf("scanning %s for writes to the global logger: %v", root, err)
	}
	for _, dir := range mustVisit {
		abs, err := filepath.Abs(dir)
		if err != nil {
			t.Fatalf("resolving %s: %v", dir, err)
		}
		if !visited[abs] {
			t.Errorf("the scan of %s never descended into %s, so whatever it reports about "+
				"that tree it did not look at. A clean scan and a scan that reached nothing "+
				"are the same answer, which is why this is checked separately", root, abs)
		}
	}
	for _, at := range swaps {
		t.Errorf("%s %s. The global zerolog logger has no synchronization and the code "+
			"under test logs from goroutines this test does not own, so writing it races "+
			"their reads. Install the shared sink from TestMain — InstallLogSink in "+
			"core/test — and toggle capture on it instead", at.pos, at.what)
	}
}

// loggerWrite is one thing the scan found: where it is, and which form it took. The
// form is carried so that a failure names what it saw, rather than leaving a reader to
// work out why a line the guard calls an assignment has no "=" in it.
type loggerWrite struct {
	pos  string
	what string
}

// globalLoggerSwaps returns every write to the global zerolog logger in dir's _test.go
// files. It is separate from the assertion above so that this package's own tests can
// drive it against fixtures and see what it found — a guard whose only report is a
// failed test cannot be shown to fire, and this one has to be shown to fire against
// each evasion separately.
//
// The forms it recognizes, each of which reaches the same variable:
//
//   - assignment, including through parentheses: `log.Logger = x`, `(log.Logger) = x`
//   - a range clause writing into it: `for _, log.Logger = range xs`
//   - taking its address: `p := &log.Logger`, `set(&log.Logger, x)`. The write itself
//     happens elsewhere — through the pointer, or inside the callee — so the address
//     is the last point at which it is still recognizable
//   - a mutating method call: UpdateContext writes the logger's context in place and
//     races concurrent reads exactly as an assignment does. It is the natural way to
//     add a field to the global logger without appearing to swap it
//   - a dot-import of the log package, reported on its own account. It puts Logger in
//     file scope, where a write is a bare `Logger = ...` naming no package for the rest
//     of this scan to match. Nothing in a test needs it, so it is refused rather than
//     followed.
//
// It returns an error rather than an empty result when dir holds no _test.go files at
// all: an empty scan and a clean one are otherwise the same answer, and the empty one
// means the caller pointed the guard somewhere it asserts nothing.
func globalLoggerSwaps(dir string) ([]loggerWrite, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no _test.go files found under %s, so the scan asserts nothing", dir)
	}
	fset := token.NewFileSet()
	var found []loggerWrite
	for _, file := range files {
		writes, err := scanTestFile(fset, file)
		if err != nil {
			return nil, err
		}
		found = append(found, writes...)
	}
	return found, nil
}

// globalLoggerSwapsUnder walks root and scans every _test.go file beneath it, returning
// what it found and the set of directories it descended into. The second return is what
// makes an empty result meaningful: see AssertNoGlobalLoggerSwapUnder's mustVisit.
//
// Directories the go tool itself does not treat as packages are skipped — those whose
// name begins with "." or "_", plus testdata, vendor and node_modules. That rule is
// borrowed rather than invented, and it is load-bearing twice over: it keeps the walk
// out of _legacy, the archived pre-migration tree that is deliberately not maintained,
// and out of .git and any git worktrees checked out under a dot-directory, whose
// contents are a DIFFERENT commit of this repository and would otherwise be reported as
// findings against this one.
func globalLoggerSwapsUnder(root string) ([]loggerWrite, map[string]bool, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	visited := map[string]bool{}
	var found []loggerWrite
	files := 0
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
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		files++
		writes, err := scanTestFile(fset, path)
		if err != nil {
			return err
		}
		found = append(found, writes...)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if files == 0 {
		return nil, nil, fmt.Errorf("no _test.go files found under %s, so the scan asserts nothing", abs)
	}
	return found, visited, nil
}

// skipDirInScan reports whether the walk should refuse to descend into a directory of
// this name. It mirrors the go tool's own rule for what is not a package directory.
func skipDirInScan(name string) bool {
	switch name {
	case "testdata", "vendor", "node_modules":
		return true
	}
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// scanTestFile parses one _test.go file and returns every write to the global zerolog
// logger in it.
func scanTestFile(fset *token.FileSet, file string) ([]loggerWrite, error) {
	var found []loggerWrite
	parsed, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	names, dotImport := zerologLogPackageNames(parsed)
	if dotImport != token.NoPos {
		found = append(found, loggerWrite{
			pos: fset.Position(dotImport).String(),
			what: "dot-imports the zerolog log package, which puts the global Logger in " +
				"file scope, where a write to it names no package and cannot be checked",
		})
	}
	if len(names) == 0 {
		return found, nil
	}
	isGlobal := func(e ast.Expr) bool {
		sel, ok := ast.Unparen(e).(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Logger" {
			return false
		}
		ident, ok := ast.Unparen(sel.X).(*ast.Ident)
		return ok && names[ident.Name]
	}
	record := func(e ast.Expr, what string) {
		found = append(found, loggerWrite{pos: fset.Position(e.Pos()).String(), what: what})
	}
	ast.Inspect(parsed, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if isGlobal(lhs) {
					record(lhs, "assigns to the global zerolog logger")
				}
			}
		case *ast.RangeStmt:
			// Only `for k, v = range` writes existing variables; `:=` declares new ones.
			if node.Tok != token.ASSIGN {
				return true
			}
			for _, e := range []ast.Expr{node.Key, node.Value} {
				if e != nil && isGlobal(e) {
					record(e, "assigns to the global zerolog logger in a range clause")
				}
			}
		case *ast.UnaryExpr:
			if node.Op == token.AND && isGlobal(node.X) {
				record(node.X, "takes the address of the global zerolog logger, which "+
					"hands a writer to it somewhere this check cannot follow")
			}
		case *ast.CallExpr:
			fun, ok := ast.Unparen(node.Fun).(*ast.SelectorExpr)
			if !ok || !mutatingLoggerMethods[fun.Sel.Name] || !isGlobal(fun.X) {
				return true
			}
			record(fun, "calls "+fun.Sel.Name+" on the global zerolog logger, which "+
				"writes it in place")
		}
		return true
	})
	return found, nil
}

// mutatingLoggerMethods are the zerolog.Logger methods that write through a pointer
// receiver. As of v1.35.1 UpdateContext is the only one: every other pointer-receiver
// method on Logger emits an event or reads the level, and the methods that look like
// mutators (Output, Level, Hook, With) take a value receiver and return a new logger.
var mutatingLoggerMethods = map[string]bool{"UpdateContext": true}

// zerologLogPackageNames returns the names under which file refers to the zerolog log
// package, plus the position of a dot-import of it if there is one. A blank import
// writes nothing and is ignored.
func zerologLogPackageNames(file *ast.File) (map[string]bool, token.Pos) {
	names := map[string]bool{}
	dotImport := token.NoPos
	for _, imp := range file.Imports {
		if imp.Path == nil || imp.Path.Value != `"github.com/rs/zerolog/log"` {
			continue
		}
		if imp.Name == nil {
			names["log"] = true
			continue
		}
		switch imp.Name.Name {
		case ".":
			dotImport = imp.Pos()
		case "_":
		default:
			names[imp.Name.Name] = true
		}
	}
	return names, dotImport
}
