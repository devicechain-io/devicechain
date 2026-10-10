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
