// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package decoderfuncs

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/payloaddecode"
)

// docsRoot is the docs root relative to this package. The page is read from outside the
// module, which Go's test cache does not track, so run with -count=1 (CI and the CLAUDE.md
// sweep both do).
const docsRoot = "../../../../docs"

// TestCommittedReferenceIsCurrent fails when the function library, the limits or the page text
// changed and the committed pages were not regenerated.
func TestCommittedReferenceIsCurrent(t *testing.T) {
	for _, p := range Pages {
		want, err := Render(p.Locale)
		if err != nil {
			t.Fatalf("%s: %v", p.Locale, err)
		}
		got, err := os.ReadFile(filepath.Join(docsRoot, filepath.FromSlash(p.Path)))
		if err != nil {
			t.Fatalf("%s is not committed (%v); regenerate with: %s", p.Path, err, Regenerate)
		}
		// The page is written with LF; a checkout that converts line endings must not read as drift.
		got = bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n"))
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale; regenerate with: %s", p.Path, Regenerate)
		}
	}
}

// TestEveryFunctionIsOnEveryPage is the check the diff cannot make by itself: that the
// generator really lists the library, so a bug that dropped functions from both the page and
// the expectation would not pass.
func TestEveryFunctionIsOnEveryPage(t *testing.T) {
	fns := payloaddecode.Functions()
	if len(fns) < 30 {
		t.Fatalf("the library lists only %d functions; the reference would be nearly empty", len(fns))
	}
	for _, p := range Pages {
		page, err := Render(p.Locale)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fns {
			if !strings.Contains(string(page), "| `"+f.Name+"` |") {
				t.Errorf("%s: the page has no row for %s", p.Locale, f.Name)
			}
			if !strings.Contains(string(page), cell(f.Doc)) {
				t.Errorf("%s: the page lacks the description of %s", p.Locale, f.Name)
			}
		}
		for _, l := range limits() {
			if !strings.Contains(string(page), "| "+l.value+" |") {
				t.Errorf("%s: the page lacks the limit %s = %s", p.Locale, l.id, l.value)
			}
		}
	}
}

// TestPagesAreSafeMDX: Docusaurus parses .md as MDX, where a bare `<`, `{`, `}` or `&` outside code
// fails the whole site build, and a bare `|` splits a table cell.
func TestPagesAreSafeMDX(t *testing.T) {
	for _, p := range Pages {
		page, err := Render(p.Locale)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(page), "\n") {
			if !strings.HasPrefix(line, "| ") {
				continue
			}
			// Strip code spans, then look for characters MDX would read.
			var prose strings.Builder
			inCode := false
			for _, r := range line {
				if r == '`' {
					inCode = !inCode
					continue
				}
				if !inCode {
					prose.WriteRune(r)
				}
			}
			clean := strings.NewReplacer(`\<`, "", `\{`, "", `\}`, "", `\&`, "", `\|`, "").Replace(prose.String())
			if strings.ContainsAny(clean, "<{}&") {
				t.Errorf("%s line %d has a bare MDX character outside code: %s", p.Locale, i+1, line)
			}
			// Every row must have the same number of cells as its header.
			cells := strings.Count(strings.ReplaceAll(line, `\|`, ""), "|")
			if cells != 5 && cells != 3 {
				t.Errorf("%s line %d has %d column separators: %s", p.Locale, i+1, cells, line)
			}
		}
	}
}

func TestUnknownFunctionFallsInOther(t *testing.T) {
	for _, f := range families {
		if f.match("zzz.unknown") {
			t.Fatalf("family %s claims an unknown function", f.id)
		}
	}
}
