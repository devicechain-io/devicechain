// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package clitree

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-docsgql/apicompat/decl"
)

func tree(cmds ...Command) Tree {
	return Tree{Commands: append([]Command{{Path: ""}}, cmds...)}
}

func kindsOf(cs []decl.Change) map[string]decl.Severity {
	m := map[string]decl.Severity{}
	for _, c := range cs {
		m[c.Coord+" "+c.Kind] = c.Severity
	}
	return m
}

func TestEveryRuleClassifiesAsDocumented(t *testing.T) {
	base := tree(
		Command{Path: "install", Aliases: []string{"setup"}, Flags: []Flag{
			{Name: "dev", Type: "bool", Default: "false", Shorthand: "d"},
			{Name: "gone", Type: "string"},
			{Name: "retype", Type: "string"},
			{Name: "port", Type: "int", Default: "80"},
			{Name: "quiet", Type: "bool", Default: "false"},
			{Name: "moved", Type: "string"},
		}},
		Command{Path: "old"},
		Command{Path: "install sub"},
	)
	head := tree(
		Command{Path: "install", Flags: []Flag{
			{Name: "dev", Type: "bool", Default: "false"},
			{Name: "retype", Type: "int"},
			{Name: "port", Type: "int", Default: "8080"},
			{Name: "quiet", Type: "bool", Default: "false", Shorthand: "q"},
			{Name: "fresh", Type: "string"},
			{Name: "moved", Type: "string", Hidden: true},
		}},
		Command{Path: "install sub"},
		Command{Path: "new"},
	)
	got := kindsOf(Compare(base, head))
	want := map[string]decl.Severity{
		"install:--gone flag-removed":          decl.Breaking,
		"install:--retype flag-type-changed":   decl.Breaking,
		"install:--dev flag-shorthand-changed": decl.Breaking,
		"install@setup alias-removed":          decl.Breaking,
		"old command-removed":                  decl.Breaking,
		"install:--port flag-default-changed":  decl.Dangerous,
		"install:--moved flag-hidden":          decl.Dangerous,
		"install:--quiet flag-shorthand-added": decl.Safe,
		"install:--fresh flag-added":           decl.Safe,
		"new command-added":                    decl.Safe,
	}
	for k, sev := range want {
		if g, ok := got[k]; !ok || g != sev {
			t.Errorf("%q: got %v (present %v), want %v", k, g, ok, sev)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d changes, want %d: %v", len(got), len(want), got)
	}
}

func TestIdenticalTreesHaveNoChanges(t *testing.T) {
	tr := tree(Command{Path: "a", Flags: []Flag{{Name: "x", Type: "string"}}})
	if cs := Compare(tr, tr); len(cs) != 0 {
		t.Fatalf("got %v", cs)
	}
}

// Removing a command is reported once, for the command, not once per flag it had.
func TestARemovedCommandIsNotReportedPerFlag(t *testing.T) {
	base := tree(Command{Path: "a", Flags: []Flag{{Name: "x", Type: "string"}, {Name: "y", Type: "string"}}})
	cs := Compare(base, tree())
	if len(cs) != 1 || cs[0].Kind != "command-removed" {
		t.Fatalf("got %v", cs)
	}
	if !strings.Contains(cs[0].String(), "a command-removed") {
		t.Fatalf("the failure must name the command: %s", cs[0])
	}
}

func TestEveryKindIsSeverityClassified(t *testing.T) {
	for _, k := range []string{"command-removed", "alias-removed", "flag-removed", "flag-type-changed", "flag-shorthand-changed"} {
		if Kinds[k] != decl.Breaking {
			t.Errorf("%s must be breaking", k)
		}
	}
}

// A flag that becomes required, or joins a flag group, can refuse an invocation the
// baseline accepted; a renamed persistent flag is removed from every command that
// inherited it; a command moved under another parent is a removal.
func TestNarrowingsOfWhatAnInvocationMayBe(t *testing.T) {
	base := tree(
		Command{Path: "a", Use: "a <token>", Flags: []Flag{
			{Name: "x", Type: "string"},
			{Name: "y", Type: "string"},
			{Name: "tenant", Type: "string"},
			{Name: "list", Type: "string"},
		}},
		Command{Path: "a b", Flags: []Flag{{Name: "tenant", Type: "string"}}},
		Command{Path: "c"},
	)
	head := tree(
		Command{Path: "a", Use: "a <token> <name>", Flags: []Flag{
			{Name: "x", Type: "string", Required: true},
			{Name: "y", Type: "string", Groups: []string{"mutually_exclusive:x y"}},
			{Name: "tenant-id", Type: "string"},
			{Name: "list", Type: "stringSlice"},
		}},
		Command{Path: "a b", Flags: []Flag{{Name: "tenant-id", Type: "string"}}},
		Command{Path: "a c"},
	)
	got := kindsOf(Compare(base, head))
	for _, k := range []string{
		"a:--x flag-required",
		"a:--y#mutually_exclusive:x,y flag-group-added",
		"a:--tenant flag-removed",
		"a.b:--tenant flag-removed",
		"a:--list flag-type-changed",
		"c command-removed",
	} {
		if got[k] != decl.Breaking {
			t.Errorf("%q: want breaking, got %v (all: %v)", k, got[k], got)
		}
	}
	if got["a command-usage-changed"] != decl.Dangerous {
		t.Errorf("a changed usage line must be reported: %v", got)
	}
}

func TestAnUnlistedKindPanicsRatherThanPassing(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("an emitted kind missing from Kinds must not quietly take the Safe severity")
		}
	}()
	delete(Kinds, "flag-required")
	defer func() { Kinds["flag-required"] = decl.Breaking }()
	Compare(tree(Command{Path: "a", Flags: []Flag{{Name: "x"}}}), tree(Command{Path: "a", Flags: []Flag{{Name: "x", Required: true}}}))
}
