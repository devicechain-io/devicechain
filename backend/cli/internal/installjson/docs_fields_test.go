// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installjson

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// installEventsPages are the published reference page of the stream and its
// translations. Field names are code, so every locale lists the same ones.
var installEventsPages = []string{
	"docs/docs/reference/install-events.md",
	"docs/i18n/es/docusaurus-plugin-content-docs/current/reference/install-events.md",
	"docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/reference/install-events.md",
}

// repoRoot is the repository root, four levels above this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

var backticked = regexp.MustCompile("`([^`]+)`")

// tableRows returns the cells of each body row of the first table in the section
// whose heading carries anchor.
func tableRows(t *testing.T, page, anchor string) [][]string {
	t.Helper()
	b, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	i := strings.Index(text, "{#"+anchor+"}")
	if i < 0 {
		t.Fatalf("%s has no section {#%s}", page, anchor)
	}
	section := text[i:]
	if j := strings.Index(section[1:], "\n## "); j >= 0 {
		section = section[:j+1]
	}
	var rows [][]string
	header := 0
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "|") {
			if header > 0 {
				break // the table ended
			}
			continue
		}
		if header++; header <= 2 {
			continue // the header and its separator
		}
		cells := strings.Split(strings.Trim(line, "|"), " | ")
		for k := range cells {
			cells[k] = strings.TrimSpace(cells[k])
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		t.Fatalf("%s: the {#%s} section has no table", page, anchor)
	}
	return rows
}

// jsonFields is every field a value of type typ marshals, by its JSON name.
func jsonFields(typ reflect.Type) []string {
	var out []string
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// names is the backticked names in a cell, sorted.
func names(cell string) []string {
	var out []string
	for _, m := range backticked.FindAllStringSubmatch(cell, -1) {
		out = append(out, m[1])
	}
	slices.Sort(out)
	return out
}

// 🔴 THE PAGE'S FIELD NAMES ARE THE CODE'S. The event kinds table lists, for each kind,
// exactly the fields its payload type marshals, and the envelope table exactly the
// envelope's. Both are read from the types, by reflection on the decoder's own
// payloads, so a field added, renamed or removed in code fails here until every locale
// says the same. A page that names a field no payload has (or misses one it has) is
// the drift this exists for.
func TestInstallEventsPageFieldsAreTheCode(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile("decode.go")
	if err != nil {
		t.Fatal(err)
	}
	decoderKinds := strings.Count(string(src), "case installflow.Kind")
	envelope := jsonFields(reflect.TypeOf(wireEnvelope{}))

	for _, rel := range installEventsPages {
		page := filepath.Join(root, filepath.FromSlash(rel))
		t.Run(rel, func(t *testing.T) {
			rows := tableRows(t, page, "event-kinds")
			if len(rows) != decoderKinds {
				t.Errorf("the event kinds table has %d rows, the decoder knows %d kinds", len(rows), decoderKinds)
			}
			for _, row := range rows {
				if len(row) != 3 {
					t.Errorf("row %q: want 3 cells (kind, fields, meaning), got %d", row, len(row))
					continue
				}
				kind := names(row[0])
				if len(kind) != 1 {
					t.Errorf("row %q: the first cell names no single kind", row)
					continue
				}
				p, err := decodePayload(installflow.Kind(kind[0]), []byte("{}"))
				if err != nil {
					t.Errorf("the page documents kind %q, which the decoder does not know: %v", kind[0], err)
					continue
				}
				if got, want := names(row[1]), jsonFields(reflect.TypeOf(p)); !slices.Equal(got, want) {
					t.Errorf("%s: the page lists fields %v, the payload has %v", kind[0], got, want)
				}
			}

			var documented []string
			for _, row := range tableRows(t, page, "envelope") {
				documented = append(documented, names(row[0])...)
			}
			slices.Sort(documented)
			if !slices.Equal(documented, envelope) {
				t.Errorf("the envelope table lists %v, the envelope has %v", documented, envelope)
			}
		})
	}
}
