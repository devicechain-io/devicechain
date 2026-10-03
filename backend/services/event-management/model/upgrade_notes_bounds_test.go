// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The upgrade notes tell an operator, in both locales and several places each, the two
// bounds above which the time-leading key rebuild refuses to start: the uncompressed rows
// it may have to index (timeLeadingKeysDefaultTiming.maxRows) and the chunks one table may
// have (eventStoreMaxChunks). An operator decides from those figures whether to upgrade in
// place or to recreate the instance, which discards its data, so a published figure that
// drifts from the constant is a wrong answer to that decision, not a typo.
//
// The pages live outside this module and Go's test cache does not track them, so a plain
// `go test` can serve a stale PASS over an edit there; CI runs -count=1.

// upgradeNotesPages are the English page and its Spanish twin.
var upgradeNotesPages = map[string]string{
	"en": filepath.Join("..", "..", "..", "..", "docs", "docs", "deployment", "releases-and-upgrades.md"),
	"es": filepath.Join("..", "..", "..", "..", "docs", "i18n", "es", "docusaurus-plugin-content-docs", "current",
		"deployment", "releases-and-upgrades.md"),
}

// publishedRowBound matches a figure the page gives as a number of rows: "4,000,000 rows",
// "4,000,000 such rows", "4 000 000 de filas", "4 000 000 de esas filas", "4 million rows",
// "4 millones de filas", and the bare figure the count is compared with, "more than
// `4000000`" / "más de `4000000`". Up to three words may stand between the figure and
// "rows"/"filas", so a figure qualified some other way ("4,000,000 uncompressed rows",
// "4 000 000 de las filas") is still read and held to the bound: a pattern that allowed
// only today's qualifiers would stop reading a reworded figure in both locales at once,
// and the per-locale counts below would still agree. Smaller figures ("100,000 reporting
// devices") are not rows and do not match.
var publishedRowBound = regexp.MustCompile(
	`(\d{1,3}(?:[,\x{00a0}\x{202f} ]\d{3})+|\d+\s+(?:million|millones))\s+(?:\S+\s+){0,3}?(?:rows|filas)\b` +
		"|(?:more than|más de)\\s+`(\\d+)`")

// publishedChunkBound matches a figure given as a number of chunks ("500 chunks", "**500
// chunks**", "500 fragmentos"), but not the drop_chunks batch size, which the page gives
// as "at most 100 chunks" / "como mucho 100 fragmentos" and the procedure fixes in its
// own SQL.
var publishedChunkBound = regexp.MustCompile(`(at most|como mucho)?\s+\**(\d[\d,]*)\**\s+(?:chunks|fragmentos)\b`)

// parsePublishedCount turns a matched figure into a count: thousands separators of either
// locale are dropped, and "N million(es)" is N×1,000,000.
func parsePublishedCount(t *testing.T, s string) int64 {
	t.Helper()
	mult := int64(1)
	if f := strings.Fields(s); len(f) == 2 && (f[1] == "million" || f[1] == "millones") {
		s, mult = f[0], 1_000_000
	}
	s = strings.NewReplacer(",", "", " ", "", " ", "", " ", "").Replace(s)
	n, err := strconv.ParseInt(s, 10, 64)
	require.NoError(t, err, "parsing a published figure %q", s)
	return n * mult
}

// TestUpgradeNotesPublishTheRebuildBounds holds every row figure and every chunk figure on
// the upgrade notes, in both locales, to the constants the migration refuses at. It also
// requires both locales to state each bound the same number of times: a figure dropped from
// one locale, or written in a form these patterns no longer read, shows as a count mismatch
// rather than as one fewer thing checked.
func TestUpgradeNotesPublishTheRebuildBounds(t *testing.T) {
	rows := map[string]int{}
	chunks := map[string]int{}
	for locale, path := range upgradeNotesPages {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		page := string(raw)
		line := func(at int) int { return strings.Count(page[:at], "\n") + 1 }

		for _, m := range publishedRowBound.FindAllStringSubmatchIndex(page, -1) {
			var fig string
			if m[2] >= 0 {
				fig = page[m[2]:m[3]]
			} else {
				fig = page[m[4]:m[5]]
			}
			rows[locale]++
			assert.EqualValues(t, timeLeadingKeysDefaultTiming.maxRows, parsePublishedCount(t, fig),
				"%s line %d publishes %q as the row bound; the rebuild refuses above %d rows",
				locale, line(m[0]), page[m[0]:m[1]], timeLeadingKeysDefaultTiming.maxRows)
		}
		for _, m := range publishedChunkBound.FindAllStringSubmatchIndex(page, -1) {
			if m[2] >= 0 {
				continue // the drop_chunks batch size, not the bound
			}
			chunks[locale]++
			assert.EqualValues(t, eventStoreMaxChunks, parsePublishedCount(t, page[m[4]:m[5]]),
				"%s line %d publishes %q as the chunk bound; the rebuild refuses above %d chunks",
				locale, line(m[0]), strings.TrimSpace(page[m[0]:m[1]]), eventStoreMaxChunks)
		}
	}

	// Nothing read is nothing checked: a page that moved, or a rewording these patterns
	// miss everywhere, must fail here rather than pass over zero figures.
	require.NotZero(t, rows["en"], "found no row bound on the English upgrade notes")
	require.NotZero(t, chunks["en"], "found no chunk bound on the English upgrade notes")
	assert.Equal(t, rows["en"], rows["es"], "row figures read: en %d, es %d", rows["en"], rows["es"])
	assert.Equal(t, chunks["en"], chunks["es"], "chunk figures read: en %d, es %d", chunks["en"], chunks["es"])
}

// TestUpgradeNotesLocalesCarryTheSameAnchors requires both locales of the upgrade notes to
// give their headings the same explicit ids, in the same order. The notes, and the pages
// that point at them, send an operator to a step by id (#v0190-row-count,
// #v0190-chunk-count). The docs build refuses a broken link to an id, but a Spanish heading
// that loses or renames an id nothing in the site links to still builds, and a link to it
// from outside the site (a release's notes, an answer to an operator) lands at the top of
// the page.
func TestUpgradeNotesLocalesCarryTheSameAnchors(t *testing.T) {
	headingID := regexp.MustCompile(`(?m)^#{2,6} .*\{#([a-z0-9-]+)\}\s*$`)
	ids := map[string][]string{}
	for locale, path := range upgradeNotesPages {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, m := range headingID.FindAllStringSubmatch(string(raw), -1) {
			ids[locale] = append(ids[locale], m[1])
		}
	}
	// The release's notes link to the version's section by an id derived from the version
	// being released, the same derivation as anchor_for in
	// hack/build-release-notes-header.sh (v0.19.0 -> v0190-upgrade). That script refuses a
	// missing section only when a tag is pushed; holding it here makes a renamed or lost
	// heading fail on the pull request that renames it.
	hl, err := os.ReadFile(filepath.Join("..", "..", "..", "..", ".github", "release-highlights.json"))
	require.NoError(t, err)
	var highlights struct {
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal(hl, &highlights))
	require.Regexp(t, `^v\d+\.\d+\.\d+(-.+)?$`, highlights.Version, "release-highlights.json version")
	base, _, _ := strings.Cut(highlights.Version, "-")
	upgradeID := strings.ReplaceAll(base, ".", "") + "-upgrade"
	for _, locale := range []string{"en", "es"} {
		require.Contains(t, ids[locale], upgradeID,
			"the %s upgrade notes have no {#%s} heading for %s, which the release's notes link to",
			locale, upgradeID, highlights.Version)
	}
	require.Contains(t, ids["en"], "v0190-row-count")
	require.Contains(t, ids["en"], "v0190-chunk-count")
	assert.Equal(t, ids["en"], ids["es"], "the English and Spanish upgrade notes give their headings different ids")
}
