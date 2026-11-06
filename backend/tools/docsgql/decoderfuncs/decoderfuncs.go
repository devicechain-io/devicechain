// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package decoderfuncs renders the payload-decoder function reference from the library
// itself: the function table (name, signature, description, cost) and the limits are read
// from core/payloaddecode, so the page cannot describe a function the library does not have
// or leave one out.
//
// The page is COMMITTED, not built with the docs site, for the reason the dcctl command
// reference is: the docs build has no Go toolchain, so a page that cannot be regenerated there
// must not be able to drift here. TestCommittedReferenceIsCurrent regenerates it and diffs,
// and names the command that fixes a stale page.
//
// The descriptions are the library's own English text in every locale, as the dcctl
// reference's are; the headings, column titles and the paragraphs around the tables are
// translated.
package decoderfuncs

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/payloaddecode"
)

// Regenerate is the command that rewrites the pages, run from backend/tools/docsgql.
const Regenerate = "cd backend/tools/docsgql && go run ./cmd/decoderfuncs --out ../../../docs"

// Page is one locale's copy of the reference.
type Page struct {
	// Path is relative to the docs root (the directory holding docusaurus.config.ts).
	Path   string
	Locale string
}

// Pages are every locale's copy, English first.
var Pages = []Page{
	{Path: "docs/reference/decoder-functions.md", Locale: "en"},
	{Path: "i18n/es/docusaurus-plugin-content-docs/current/reference/decoder-functions.md", Locale: "es"},
	{Path: "i18n/zh-CN/docusaurus-plugin-content-docs/current/reference/decoder-functions.md", Locale: "zh-CN"},
}

// family groups functions on the page. A function the table below does not name falls into
// "other", so a function added to the library appears on the page (and fails the drift test
// until it is regenerated) rather than being left out.
type family struct {
	id    string
	match func(name string) bool
}

func hasPrefixIn(prefixes ...string) func(string) bool {
	return func(n string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(n, p) {
				return true
			}
		}
		return false
	}
}

func isOneOf(names ...string) func(string) bool {
	return func(n string) bool {
		for _, m := range names {
			if n == m {
				return true
			}
		}
		return false
	}
}

var families = []family{
	{"integers", func(n string) bool {
		return len(n) >= 2 && (n[0] == 'u' || n[0] == 'i') && n[1] >= '0' && n[1] <= '9'
	}},
	{"floats", func(n string) bool { return len(n) >= 2 && n[0] == 'f' && n[1] >= '0' && n[1] <= '9' }},
	{"bits", isOneOf("bit", "bits", "sbits")},
	{"bcd", isOneOf("bcd")},
	{"bytes", isOneOf("slice")},
	{"text", hasPrefixIn("hex.", "base64.")},
	{"codecs", hasPrefixIn("lpp.")},
	{"math", hasPrefixIn("math.")},
}

const otherFamily = "other"

type limit struct {
	id    string
	value string
}

// limits are the numbers a decoder is held to, read from the library's constants.
func limits() []limit {
	n := func(v int) string { return commas(v) }
	return []limit{
		{"frame", n(payloaddecode.MaxFrameBytes)},
		{"json", n(payloaddecode.MaxJSONBytes)},
		{"depth", n(payloaddecode.MaxJSONDepth)},
		{"document", n(payloaddecode.MaxDocumentBytes)},
		{"expression", n(payloaddecode.MaxExpressionBytes)},
		{"lets", n(payloaddecode.MaxLets)},
		{"sections", n(payloaddecode.MaxSections)},
		{"readings", n(payloaddecode.MaxDecoderReadings)},
		{"vectors", n(payloaddecode.MaxVectors)},
		{"text", n(payloaddecode.MaxTextBytes)},
		{"inlist", n(payloaddecode.MaxInKeys)},
		{"name", n(payloaddecode.MaxNameBytes)},
		{"key", n(payloaddecode.MaxKeyBytes)},
		{"exprcost", n(payloaddecode.ExprCostCeiling)},
		{"decodercost", n(payloaddecode.DecoderCostCeiling)},
	}
}

func commas(v int) string {
	s := fmt.Sprint(v)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// cell makes library text safe inside a markdown table cell on a page Docusaurus parses as
// MDX: a bare `<`, `{`, `}` or `&` outside code would open JSX or an expression, and a `|`
// would end the cell.
func cell(s string) string {
	var b strings.Builder
	inCode := false
	for _, r := range s {
		switch {
		case r == '`':
			inCode = !inCode
			b.WriteRune(r)
		case r == '|':
			b.WriteString(`\|`)
		case !inCode && strings.ContainsRune("<{}&", r):
			b.WriteRune(rune(92))
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Render is one locale's page. It fails on a function with no description or cost, which the
// library's own tests also forbid, so an empty cell is never published.
func Render(locale string) ([]byte, error) {
	tx, ok := texts[locale]
	if !ok {
		return nil, fmt.Errorf("decoderfuncs: no text for locale %q", locale)
	}
	fns := payloaddecode.Functions()
	byFamily := map[string][]payloaddecode.FunctionDoc{}
	for _, f := range fns {
		if f.Name == "" || f.Signature == "" || f.Doc == "" || f.Cost == "" {
			return nil, fmt.Errorf("decoderfuncs: function %q has an empty name, signature, description or cost", f.Name)
		}
		fam := otherFamily
		for _, c := range families {
			if c.match(f.Name) {
				fam = c.id
				break
			}
		}
		byFamily[fam] = append(byFamily[fam], f)
	}

	var b bytes.Buffer
	b.WriteString("---\nsidebar_position: " + tx.position + "\ntitle: " + tx.title + "\n---\n\n")
	fmt.Fprintf(&b, "# %s {#decoder-functions}\n\n%s\n\n", tx.title, tx.intro)

	fmt.Fprintf(&b, "## %s {#decoder-functions-library}\n\n%s\n\n", tx.libraryHeading, fmt.Sprintf(tx.libraryIntro, payloaddecode.EnvVersion))
	order := make([]string, 0, len(families)+1)
	for _, f := range families {
		order = append(order, f.id)
	}
	order = append(order, otherFamily)
	for _, id := range order {
		group := byFamily[id]
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s {#decoder-functions-%s}\n\n", tx.familyHeading(id), id)
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n| --- | --- | --- | --- |\n", tx.colFunction, tx.colSignature, tx.colDescription, tx.colCost)
		for _, f := range group {
			fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s |\n", f.Name, cell(f.Signature), cell(f.Doc), cell(f.Cost))
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## %s {#decoder-functions-limits}\n\n%s\n\n", tx.limitsHeading, tx.limitsIntro)
	fmt.Fprintf(&b, "| %s | %s |\n| --- | --- |\n", tx.colLimit, tx.colValue)
	for _, l := range limits() {
		label, ok := tx.limitLabels[l.id]
		if !ok {
			return nil, fmt.Errorf("decoderfuncs: no %s label for limit %q", locale, l.id)
		}
		fmt.Fprintf(&b, "| %s | %s |\n", label, l.value)
	}
	return b.Bytes(), nil
}
