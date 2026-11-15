// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

var updateSizeDocs = flag.Bool("update-size-docs", false, "rewrite the generated sizing tables in the docs")

// sizingDocPages is every language's copy of the page that carries the generated tables.
var sizingDocPages = []string{
	"docs/docs/deployment/sizing.md",
	"docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/sizing.md",
	"docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/sizing.md",
}

// sizeDocsRoot is the repository root, from this package's directory.
func sizeDocsRoot() string { return filepath.Join("..", "..", "..") }

// sizeTableBlock finds the lines of one marked table in a page: the marker lines, and between them
// a header row, a separator row and the body rows.
func sizeTableBlock(lines []string, id string) (start, end int, ok bool) {
	start, end = -1, -1
	for i, l := range lines {
		switch strings.TrimSpace(l) {
		case "<!-- size-table:" + id + " -->":
			start = i
		case "<!-- /size-table:" + id + " -->":
			end = i
		}
	}
	return start, end, start >= 0 && end > start+3
}

// TestPublishedSizingTables holds the sizing page's tables, in all three languages, to the Go table
// they are generated from. The headers are translated, so only the body rows are compared; every
// cell in them is a setting name or a quantity, which do not translate. Regenerate with
// -update-size-docs.
func TestPublishedSizingTables(t *testing.T) {
	tables := map[string][]string{}
	for name, p := range sizePresets {
		tables[name+":areas"] = p.docAreaRows()
		tables[name+":infra"] = p.docInfraRows()
	}
	for _, page := range sizingDocPages {
		path := filepath.Join(sizeDocsRoot(), filepath.FromSlash(page))
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", page, err)
			continue
		}
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		lines := strings.Split(text, "\n")
		changed := false
		for id, want := range tables {
			start, end, ok := sizeTableBlock(lines, id)
			if !ok {
				t.Errorf("%s: no <!-- size-table:%s --> block with a header, a separator and rows", page, id)
				continue
			}
			got := lines[start+3 : end]
			// A blank line before the closing marker is allowed.
			for len(got) > 0 && strings.TrimSpace(got[len(got)-1]) == "" {
				got = got[:len(got)-1]
			}
			if strings.Join(got, "\n") == strings.Join(want, "\n") {
				continue
			}
			if !*updateSizeDocs {
				t.Errorf("%s: the %s table is not what the sizing table generates. Rows are:\n%s\nwant:\n%s\n"+
					"regenerate with go test ./bootstrap -run TestPublishedSizingTables -update-size-docs",
					page, id, strings.Join(got, "\n"), strings.Join(want, "\n"))
				continue
			}
			rebuilt := append([]string{}, lines[:start+3]...)
			rebuilt = append(rebuilt, want...)
			rebuilt = append(rebuilt, lines[end:]...)
			lines = rebuilt
			changed = true
		}
		if changed {
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

var sizeDocsNumber = regexp.MustCompile(`\b[0-9]+\b`)

func checkSizeDocs(text string) error {
	numbers := make(map[string]bool)
	for _, number := range sizeDocsNumber.FindAllString(text, -1) {
		numbers[number] = true
	}
	if numbers["799"] {
		return fmt.Errorf("contains incorrect 799-byte event size")
	}
	if numbers["345"] && numbers["454"] {
		return fmt.Errorf("contains unverified per-table split")
	}
	if !numbers[strconv.Itoa(storedEventBytes)] {
		return fmt.Errorf("missing canonical %d-byte event size", storedEventBytes)
	}
	return nil
}

// These docs live outside the Go module, so Go's test cache cannot track changes
// to them. Run with -count=1 to read the current files on every invocation.
func TestSizeDocs(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	root := filepath.Join(filepath.Dir(source), "..", "..", "..")
	for _, docs := range []string{
		"docs/docs",
		"docs/i18n/es/docusaurus-plugin-content-docs/current",
		"docs/i18n/zh-CN/docusaurus-plugin-content-docs/current",
	} {
		for _, page := range []string{
			"tuning-an-instance.md",
			"sizing.md",
			"bootstrap.md",
			"alert-runbook.md",
		} {
			t.Run(docs+"/"+page, func(t *testing.T) {
				content, err := os.ReadFile(filepath.Join(root, docs, "deployment", page))
				if err != nil {
					t.Fatal(err)
				}
				text := string(content)
				if err := checkSizeDocs(text); err != nil {
					t.Fatal(err)
				}
				if page == "tuning-an-instance.md" {
					if !strings.Contains(text, "{#event-store-sizing}") {
						t.Error("missing canonical sizing anchor")
					}
				} else if !strings.Contains(text, "./tuning-an-instance.md#event-store-sizing") {
					t.Error("missing link to canonical sizing statement")
				}
			})
		}
	}
}

func TestSizeDocsNegativeControls(t *testing.T) {
	canonical := strconv.Itoa(storedEventBytes)
	for _, text := range []string{canonical, "event size: " + canonical + " bytes"} {
		if err := checkSizeDocs(text); err != nil {
			t.Fatalf("valid control rejected: %v", err)
		}
	}
	for _, text := range []string{
		"799 bytes per event",
		canonical + " bytes per event; 799 bytes per event",
		canonical + " bytes (345 in events and 454 in measurements)",
		"345 in events and 454 in measurements",
		"no storage figure",
	} {
		if err := checkSizeDocs(text); err == nil {
			t.Errorf("negative control accepted: %q", text)
		}
	}
}
