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

// This file is the SECOND watcher, and it exists because the first one was reasoned
// from the writing side only.
//
// configdirliteral_test.go confines the CONSTRUCTED spelling — a bare ".devicechain"
// handed to filepath.Join — to this package, and exempts prose on the grounds that
// "~/.devicechain/%s" builds nothing. That is true and it is not the whole property.
// A path an operator is TOLD is a path an operator ACTS ON: two of the messages this
// guard now covers end in `rm` or "by hand", and one of them names the file to delete
// to change what a destroy will do. A described path that no longer exists is not
// inert documentation — it is an instruction that silently does nothing.
//
// 🔑 THE TWO GUARDS PARTITION THE SPELLING, WHICH IS WHY NEITHER NEEDS AN EXCEPTION
// FOR THE OTHER. The constructed form starts at the element (".devicechain/..."); the
// described form carries the home prefix a human reads ("~/.devicechain/..."). Guard
// one owns the first, this owns the second, and a string cannot be both.
//
// What this checks is narrow and total: wherever prose names something UNDER the
// config directory, the first segment must be one dcdir actually creates. Prose that
// names the directory itself is left alone — "~/.devicechain itself is the one an
// older dcctl created at 0755" is a true sentence about the root and always will be.
//
// 🔴 COMMENTS ARE WALKED, NOT JUST LITERALS, and that is deliberate. When instances
// moved under instances/ the printed messages and the comments explaining them went
// stale together; a guard reading only literals would have passed a tree in which
// ListInstances still claimed to enumerate "every instance directory under
// ~/.devicechain". The cost is that a comment wanting to name a NON-member path as an
// example — dcdir.go's own "a hand-made ~/.devicechain/notes would be swallowed" —
// has to live in this package, which is the package that owns the layout anyway.
//
// 🔴 AND THERE IS NO SUPPRESSION MARKER, DELIBERATELY. The guard's first real false
// positive was a correct sentence in cmd/instances.go explaining that an operator with
// a pre-nesting directory is told where this command looked — true prose that had to
// name the old path. It was rewritten to describe that layout instead of spelling it,
// which cost the sentence nothing. An escape hatch would have been reached for there,
// and the next stale path would have reached for it too.

// layoutReference is one prose mention of a path under the config directory whose
// first segment is not in the inventory.
type layoutReference struct {
	file    string
	line    int
	text    string
	segment string
}

// prosePrefix is the described form: the config directory as a human reads it, with
// the home shorthand that distinguishes prose from a constructed path element.
const prosePrefix = "~/" + DirName

// findStaleLayoutReferences walks the Go source under root and reports every prose
// mention of a path under the config directory naming a first segment the inventory
// does not hold.
//
// Test files are exempt for the same reason they are in the other guard: a test that
// derives its expectation from the thing under test cannot detect that thing moving,
// so tests spell layouts by hand on purpose — including layouts that are deliberately
// wrong, which is what a fixture for an old shape IS.
func findStaleLayoutReferences(root string, exempt func(dir string) bool) ([]layoutReference, int, error) {
	var out []layoutReference
	mentions := 0
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || exempt(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		record := func(pos token.Pos, text string) {
			mentions += strings.Count(text, prosePrefix)
			for _, seg := range staleSegments(text) {
				out = append(out, layoutReference{
					file:    path,
					line:    fset.Position(pos).Line,
					text:    text,
					segment: seg,
				})
			}
		}
		for _, group := range f.Comments {
			for _, c := range group.List {
				record(c.Pos(), c.Text)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			record(lit.Pos(), s)
			return true
		})
		return nil
	})
	return out, mentions, err
}

// staleSegments returns, for one piece of prose, every first-segment-under-the-root
// that the inventory does not hold.
//
// 🔴 WHAT IS LOOKED UP AND WHAT IS REPORTED ARE DELIBERATELY DIFFERENT RUNS, and
// collapsing them costs one of the two.
//
// The LOOKUP run stops at the first character a member name cannot contain — the full
// stop included, because prose ends sentences. "nothing under ~/.devicechain/instances."
// is correct English about a correct path, and a scanner that took the trailing period
// as part of the name would report the one message this guard exists to keep right.
//
// The REPORTED run stops only at a separator, so the author is shown what is actually
// written there. The stale shape is a name interpolated straight under the root —
// "~/.devicechain/%s", "~/.devicechain/<instance>" — whose lookup run is EMPTY, and a
// finding that says the empty string is not something dcctl creates names nothing the
// author can search for.
func staleSegments(text string) []string {
	var out []string
	for rest := text; ; {
		i := strings.Index(rest, prosePrefix)
		if i < 0 {
			return out
		}
		rest = rest[i+len(prosePrefix):]
		if !strings.HasPrefix(rest, "/") {
			// Names the directory itself. Always true, never stale.
			continue
		}
		rest = rest[1:]
		shown := 0
		for shown < len(rest) && rest[shown] != '/' && !isSpaceByte(rest[shown]) {
			shown++
		}
		lookup := 0
		for lookup < shown && isSegmentByte(rest[lookup]) {
			lookup++
		}
		if _, ok := Member(rest[:lookup]); !ok {
			// Trailing sentence punctuation is trimmed from the REPORTED run only.
			// It never reached the lookup, and leaving it in the finding makes the
			// name unsearchable — "~/.devicechain/%s)" is not a string the author
			// can grep for in the file they are being sent to.
			out = append(out, strings.TrimRight(rest[:shown], ",.;:)"))
		}
		rest = rest[shown:]
	}
}

func isSegmentByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_', b == '-':
		return true
	}
	return false
}

func isSpaceByte(b byte) bool { return b == ' ' || b == '\t' || b == '\n' }

func TestEveryDescribedConfigDirPathNamesSomethingDcctlCreates(t *testing.T) {
	root := moduleRoot(t)
	self, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	found, mentions, err := findStaleLayoutReferences(root, func(dir string) bool { return dir == self })
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	// 🔴 A CLEAN PASS HERE IS ALSO WHAT A WALK OVER NOTHING LOOKS LIKE. The negative
	// control proves the finder works on a tree it is handed; it says nothing about
	// whether THIS call was handed the real one. A wrong root, a module layout that
	// moved, or an exemption widened to swallow everything all report zero findings
	// and read as green. dcctl describes its own directory in many places, so "saw
	// none at all" is the one answer that cannot be true of a correct run.
	if mentions == 0 {
		t.Fatalf("the walk of %s examined no prose naming ~/%s at all — it is looking "+
			"somewhere other than dcctl's source", root, DirName)
	}
	for _, r := range found {
		rel, rerr := filepath.Rel(root, r.file)
		if rerr != nil {
			rel = r.file
		}
		t.Errorf("%s:%d describes ~/%s/%s, which is not something dcctl creates:\n  %s\n"+
			"  An instance lives under %s/, not directly under the root. If this names a NEW "+
			"member, add it to dcdir's inventory — that is the one place that says what is "+
			"there.", rel, r.line, DirName, r.segment, strings.TrimSpace(r.text), Instances)
	}
}

// TestTheDescribedPathGuardCanFail is the negative control. Everything this guard
// reads is prose, which means every way it can silently look at nothing — the wrong
// root, a parse without ParseComments, a segment scanner that stops too early — ends
// in a clean pass over a tree full of the thing it was written to find.
//
// The fixture carries BOTH carriers on purpose. Dropping ParseComments is the single
// most plausible edit to this file, it is invisible in the live run, and it would
// have let through the stale ListInstances comment that motivated walking comments at
// all — so a control that planted only string literals would keep passing after it.
//
// The count is 3 rather than 6 because the fixture tree also holds the two things the
// walk must SKIP, and both are there because neither is exercised by the live run:
//
//   - a NESTED testdata directory with two more stale references. The live run exempts
//     dcdir wholesale and dcdir's own testdata sits inside it, so without a testdata
//     directory the exemption does not reach, dropping the skip changes no result.
//   - a _test.go file with one. No test in this module names a stale path today, so
//     that exemption likewise protects nothing measurable — an exemption everyone
//     believes in and nothing has checked is one an edit can remove for free.
func TestTheDescribedPathGuardCanFail(t *testing.T) {
	planted := filepath.Join("testdata", "stalelayout")
	found, _, err := findStaleLayoutReferences(planted, func(string) bool { return false })
	if err != nil {
		t.Fatalf("walking %s: %v", planted, err)
	}
	if len(found) != 3 {
		t.Fatalf("planted stale references found = %d, want 3: %+v", len(found), found)
	}
	want := map[string]bool{"%s": false, "<instance>": false, "prod": false}
	for _, r := range found {
		if _, ok := want[r.segment]; !ok {
			t.Errorf("unexpected stale segment %q in %q", r.segment, r.text)
			continue
		}
		want[r.segment] = true
	}
	for seg, seen := range want {
		if !seen {
			t.Errorf("the guard did not catch the planted segment %q", seg)
		}
	}
}

// TestTheDescribedPathGuardAcceptsTheRootAndEveryMember pins the other half. A guard
// that flagged everything would also pass its negative control, and the two sentences
// it must not flag are the ones the live tree is full of: prose about the root itself,
// and prose about a directory that IS in the inventory.
//
// The member half is derived from MemberNames rather than a literal list, because the
// property is "every member is accepted" — a new member that this guard rejected would
// be a gate failing on a correct tree, which is how a gate gets suppressed.
func TestTheDescribedPathGuardAcceptsTheRootAndEveryMember(t *testing.T) {
	for _, text := range []string{
		"~/" + DirName,
		"~/" + DirName + " itself is the one an older dcctl created at 0755",
		"removing ~/" + DirName + ", which holds every instance",
	} {
		if got := staleSegments(text); len(got) != 0 {
			t.Errorf("prose about the root was flagged: %q -> %v", text, got)
		}
	}
	for _, name := range MemberNames() {
		for _, text := range []string{
			"~/" + DirName + "/" + name,
			"nothing under ~/" + DirName + "/" + name + ".",
			"~/" + DirName + "/" + name + "/%s/infra holds the state",
		} {
			if got := staleSegments(text); len(got) != 0 {
				t.Errorf("prose about the member %q was flagged: %q -> %v", name, text, got)
			}
		}
	}
}
