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

func TestV110GraduatedHelp(t *testing.T) {
	for _, c := range []*cobra.Command{testCmd, productCmd} {
		t.Run(c.Name(), func(t *testing.T) {
			help := strings.ToLower(c.Short + "\n" + c.Long)
			if strings.Contains(help, "preview") || strings.Contains(help, "may change") {
				t.Error("graduated commands must not claim Preview compatibility")
			}
			for _, want := range []string{"covered by the versioning policy from v1.10", "v1alpha1"} {
				if !strings.Contains(c.Long, want) {
					t.Errorf("graduated help missing %q", want)
				}
			}
		})
	}
}

func TestV110ReadinessHelp(t *testing.T) {
	for _, c := range []*cobra.Command{bulkChangeReadinessCmd, fleetOpReadinessCmd} {
		t.Run(c.CommandPath(), func(t *testing.T) {
			for _, want := range []string{
				"Exit status is zero only when enforcement is ENFORCED and orphanedAttributeRows is zero",
				"From v1.10, an upgraded instance with enforcement ENFORCED and zero orphaned rows lists",
				"config.set as executable",
				"If the orphan count cannot be read",
			} {
				if !strings.Contains(c.Long, want) {
					t.Errorf("readiness help missing %q", want)
				}
			}
			for _, stale := range []string{"not that any operation kind is executable", "expected until the release"} {
				if strings.Contains(c.Long, stale) {
					t.Errorf("readiness help still claims %q", stale)
				}
			}
		})
	}
}

// Keep the evaluator on one published-image laptop recipe across all living locales:
// `dcctl install local --compact`, then a plain-HTTP bootstrap on localhost. The recipe
// keeps root-key escrow (no --dev, no --no-escrow); keeping backups as well is an
// optional variant the page offers beside it, never the main path.
func TestLaptopRecipeInPublishedDocs(t *testing.T) {
	for _, tree := range []string{
		"docs",
		"i18n/es/docusaurus-plugin-content-docs/current",
		"i18n/zh-CN/docusaurus-plugin-content-docs/current",
	} {
		t.Run(tree, func(t *testing.T) {
			read := func(page string) string {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(docsRoot, filepath.FromSlash(tree), filepath.FromSlash(page)))
				if err != nil {
					t.Fatal(err)
				}
				return normalize(data)
			}
			quickstart := read("quickstart/first-device.md")
			recipe := "```bash\ndcctl install local --compact\ndcctl bootstrap local devicechain --host localhost --no-tls\n```\n"
			if !strings.Contains(quickstart, recipe) {
				t.Error("the laptop recipe must be `dcctl install local --compact` then " +
					"`dcctl bootstrap local devicechain --host localhost --no-tls`, on published images")
			}
			for _, required := range []string{"4", "8 GiB", "Git Bash", "WSL", "Pending", "../deployment/bootstrap.md#--compact"} {
				if !strings.Contains(quickstart, required) {
					t.Errorf("laptop prerequisites missing %q", required)
				}
			}
			bootstrap := read("deployment/bootstrap.md")
			if !strings.Contains(bootstrap, "../quickstart/first-device.md#1-bring-up-an-instance") {
				t.Error("bootstrap must link to the canonical laptop recipe")
			}
			if strings.Contains(bootstrap, "dcctl install local --dev\ndcctl bootstrap local devicechain --dev") {
				t.Error("bootstrap still offers the competing source-build laptop recipe")
			}
		})
	}
}

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
