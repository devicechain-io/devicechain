// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// docsRoot is the repository's docs/ directory, relative to this package.
var docsRoot = filepath.Join("..", "..", "..", "docs")

const regenerate = "regenerate with: cd backend/cli && go run . docs --out ../../docs"

// normalize makes the comparison independent of the checkout's line endings: a Windows
// checkout may materialize the committed pages with CRLF.
func normalize(b []byte) string { return strings.ReplaceAll(string(b), "\r\n", "\n") }

// TestCommittedReferenceIsCurrent is the drift gate: the committed pages must be exactly
// what the command definitions render now, in every locale. A flag added, renamed or
// re-described without regenerating fails here, naming the page.
func TestCommittedReferenceIsCurrent(t *testing.T) {
	pages, err := renderReferencePages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 {
		t.Fatalf("expected the en, es and zh-CN pages, rendered %d", len(pages))
	}
	for rel, want := range pages {
		got, err := os.ReadFile(filepath.Join(docsRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("%s is not committed (%v); %s", rel, err, regenerate)
		}
		if normalize(got) != normalize(want) {
			t.Errorf("%s is stale; %s", rel, regenerate)
		}
	}
}

// TestReferenceListsEveryDocumentedCommandAndNoHiddenOne pins the page's membership by
// value: a command that is public must be on it (with its flags), and the hidden
// generator must not be.
func TestReferenceListsEveryDocumentedCommandAndNoHiddenOne(t *testing.T) {
	body, err := renderReferenceBody()
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)

	var cmds []*cobra.Command
	collectDocumented(rootCmd, &cmds)
	if len(cmds) < 25 {
		t.Fatalf("only %d documented commands; the walk is losing part of the tree", len(cmds))
	}
	for _, c := range cmds {
		if !strings.Contains(text, "{#"+referenceAnchor(c)+"}") {
			t.Errorf("%q has no section on the page", c.CommandPath())
		}
		c.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
			if f.Hidden {
				return
			}
			if !strings.Contains(text, "--"+f.Name) {
				t.Errorf("%q: flag --%s is missing from the page", c.CommandPath(), f.Name)
			}
		})
	}
	if strings.Contains(text, "dcctl docs") {
		t.Error("the hidden docs command appears on the page it generates")
	}
	if strings.Contains(text, "ADR-") {
		t.Error("an ADR reference reached the public page; keep it in a source comment, not in help text")
	}
}

// TestUndocumentedCommandsNowHaveHelpText covers the commands the reference used to
// leave bare: each must say what it does beyond its one-line summary.
func TestUndocumentedCommandsNowHaveHelpText(t *testing.T) {
	for _, c := range []*cobra.Command{resgenCmd, haVerifyDbCmd, secretsEscrowShowCmd, secretsEscrowVerifyCmd} {
		if strings.TrimSpace(c.Short) == "" || strings.TrimSpace(c.Long) == "" {
			t.Errorf("%q needs both Short and Long help text", c.CommandPath())
		}
	}
}

// TestHelpAndCompletionAreFilteredOut: cobra only adds `help` and `completion` once a
// command executes, so without forcing them in, the filter never has anything to
// remove and its removal is untested.
func TestHelpAndCompletionAreFilteredOut(t *testing.T) {
	rootCmd.InitDefaultHelpCmd()
	rootCmd.InitDefaultCompletionCmd()
	present := map[string]bool{}
	for _, c := range rootCmd.Commands() {
		present[c.Name()] = true
	}
	if !present["help"] || !present["completion"] {
		t.Fatalf("precondition: cobra did not add help/completion (%v); the filter would go untested", present)
	}
	body, err := renderReferenceBody()
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"{#dcctl-help}", "{#dcctl-completion", "dcctl completion"} {
		if strings.Contains(string(body), banned) {
			t.Errorf("page contains %q", banned)
		}
	}
}

func TestEscapeMDX(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"angle", "use <instance> here", `use \<instance> here`},
		{"braces", "a {b} c", `a \{b\} c`},
		{"entity", "fish &amp; chips", `fish \&amp; chips`},
		{"html comment", "<!-- hidden -->", `\<!-- hidden -->`},
		{"inline code untouched", "run `dcctl <x> {y}` now", "run `dcctl <x> {y}` now"},
		{"double backtick span", "see ``a ` <b>`` and <c>", "see ``a ` <b>`` and " + `\<c>`},
		{"odd backticks: unclosed run is literal", "tick ` then <x>", "tick ` then " + `\<x>`},
		{"mismatched run lengths", "``a` <x>", "``a` " + `\<x>`},
		{"backtick fence untouched", "```\n<x> {y}\n```", "```\n<x> {y}\n```"},
		{"four backtick fence holds a three backtick line", "````\n```\n<x>\n````\n<y>", "````\n```\n<x>\n````\n" + `\<y>`},
		{"tilde fence untouched", "~~~\n<x> {y}\n~~~\n<z>", "~~~\n<x> {y}\n~~~\n" + `\<z>`},
		{"indented run is fenced, not escaped", "intro\n  # a comment\n  dcctl x <y>\nafter <z>", "intro\n```\n  # a comment\n  dcctl x <y>\n```\nafter " + `\<z>`},
		{"blank line inside an indented run stays in it", "  a\n\n  b\nend", "```\n  a\n\n  b\n```\nend"},
		{"tab indented", "\tx <y>", "```\n\tx <y>\n```"},
	}
	for _, c := range cases {
		if got := escapeMDX(c.in); got != c.want {
			t.Errorf("%s:\n  in:   %q\n  got:  %q\n  want: %q", c.name, c.in, got, c.want)
		}
	}
}
