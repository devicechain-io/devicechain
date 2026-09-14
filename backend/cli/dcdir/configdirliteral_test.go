// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package dcdir

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This file is the watcher. The registry in dcdir.go only works while every
// ~/.devicechain path in dcctl is built through it, and nothing about writing
// `filepath.Join(home, ".devicechain", "sims")` in some other package looks wrong —
// that is precisely how the sibling this package exists for came to be created in a
// place neither consumer of the registry could see.
//
// So the literal is confined to dcdir. An author who needs a new directory under
// ~/.devicechain cannot spell the path where they are; they come here, and the
// registry is on the screen when they do.

// configDirOffender is one string literal naming the config directory, outside the
// package that owns it.
type configDirOffender struct {
	file string
	line int
	lit  string
}

// findConfigDirLiterals walks the Go source under root and reports every string
// literal that NAMES the config directory as a path element.
//
// 🔴 THE MATCH IS DELIBERATELY NARROW, and the narrowness is the design rather than
// a shortcut. Two spellings construct a path — the bare element handed to
// filepath.Join, and a literal that begins with it — and those are the two that can
// create a directory. A literal like "~/.devicechain/%s" is PROSE: it appears in
// messages an operator reads, it builds nothing, and it belongs in the file that
// prints it. A guard that also flagged those would be answered by suppressing it,
// which is how a gate stops being read.
//
// Test files are exempt for a stronger reason than convenience. A test that builds
// its expected path from dcdir.Root() is derived from the thing it is checking and
// cannot detect it moving; tofu_permissions_test.go spelling
// filepath.Join(home, ".devicechain", "prod", "infra") by hand is the independent
// statement of where state actually lands. Forcing those through the constant would
// replace real checks with tautologies.
func findConfigDirLiterals(root string, exempt func(dir string) bool) ([]configDirOffender, error) {
	var out []configDirOffender
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// testdata holds the negative control below — source that is deliberately
			// wrong, and that the Go tool never builds.
			if d.Name() == "testdata" || exempt(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		// Comments are not walked: ParseFile was not given ParseComments, so the
		// tree holds no comment nodes. That matters — several files EXPLAIN the
		// layout in prose, and a grep-shaped guard would have to be taught to
		// ignore its own documentation.
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			if s == DirName || strings.HasPrefix(s, DirName+"/") {
				out = append(out, configDirOffender{
					file: path,
					line: fset.Position(lit.Pos()).Line,
					lit:  s,
				})
			}
			return true
		})
		return nil
	})
	return out, err
}

// moduleRoot walks up from the test's working directory to the enclosing go.mod.
// Scoping the walk to dcctl's own module is the right boundary: ~/.devicechain is
// dcctl's directory, and no other module in the workspace has any business under a
// user's home.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

func TestOnlyThisPackageSpellsTheConfigDirectory(t *testing.T) {
	root := moduleRoot(t)
	self, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	found, err := findConfigDirLiterals(root, func(dir string) bool { return dir == self })
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	for _, o := range found {
		rel, rerr := filepath.Rel(root, o.file)
		if rerr != nil {
			rel = o.file
		}
		t.Errorf("%s:%d spells %q. Build the path through dcdir instead — Root() for the "+
			"directory itself, Sibling(name) for a reserved sibling. If this IS a new "+
			"sibling, it also needs an entry in dcdir's registry, which is the only "+
			"thing that makes instance-name validation and instance enumeration agree "+
			"it is not an instance.", rel, o.line, o.lit)
	}
}

// TestTheConfigDirectoryGuardCanFail is the negative control. A guard that reports
// nothing is indistinguishable from a guard that looked nowhere, and this one walks
// a tree it does not control — a wrong root, a skipped extension or a parser option
// that drops the literals would all read as a clean pass.
//
// The fixture is a real file under testdata/, not a string built here, so it also
// proves the walk's file selection: it is found by the same extension test and the
// same parse that the live run uses.
//
// The count is 2 and not 3 because that tree holds a NESTED testdata directory with
// an offender in it, which the walk must skip. That nesting is doing real work: the
// live run already exempts dcdir, and dcdir's testdata sits inside it, so without a
// testdata tree somewhere the exemption does not cover, dropping the testdata skip
// would be undetectable — while other packages in this module do keep testdata of
// their own, and a fixture there is allowed to spell any path it likes.
func TestTheConfigDirectoryGuardCanFail(t *testing.T) {
	planted := filepath.Join("testdata", "unregistered")
	found, err := findConfigDirLiterals(planted, func(string) bool { return false })
	if err != nil {
		t.Fatalf("walking %s: %v", planted, err)
	}
	if len(found) != 2 {
		t.Fatalf("planted offenders found = %d, want 2: %+v", len(found), found)
	}
	var lits []string
	for _, o := range found {
		lits = append(lits, o.lit)
	}
	want := map[string]bool{DirName: false, DirName + "/mystery": false}
	for _, l := range lits {
		if _, ok := want[l]; !ok {
			t.Errorf("unexpected offender %q", l)
			continue
		}
		want[l] = true
	}
	for l, seen := range want {
		if !seen {
			t.Errorf("the guard did not catch the planted literal %q", l)
		}
	}
}

// TestTheGuardIgnoresProseAndTests pins the two exemptions the live run depends on.
// Both were deliberate (see findConfigDirLiterals), and both are invisible in a run
// that passes — so if either were tightened or dropped, nothing else here would say
// so until the guard started failing on files it was never meant to read.
func TestTheGuardIgnoresProseAndTests(t *testing.T) {
	planted := filepath.Join("testdata", "exempt")
	found, err := findConfigDirLiterals(planted, func(string) bool { return false })
	if err != nil {
		t.Fatalf("walking %s: %v", planted, err)
	}
	if len(found) != 0 {
		t.Fatalf("guard flagged exempt source: %+v", found)
	}
}
