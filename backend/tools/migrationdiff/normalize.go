// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"regexp"
	"sort"
	"strings"
)

// normalizeDump turns raw `pg_dump --schema-only` output into a canonical form two
// dumps can be compared by. The goal: two schemas that are STRUCTURALLY identical
// compare equal even if the incremental chain and a squashed baseline built them in a
// different order, while a real structural difference (a missing index, a different
// column type or physical column order, an extra constraint) still shows up.
//
// The output is statements joined by "\n", and a statement MAY ITSELF SPAN LINES — so
// this text is not line-per-statement and must never be compared line by line. Read it
// back with splitNormalized, which is the exact inverse of the join. Comparing the
// joined form as lines was a real defect: it made the harness blind to the removal of
// any column whose DDL text appears in another table of the same schema, which is
// between 23% and 54% of every golden (see statementDiff).
//
// It does three things:
//   - drops non-structural noise (psql meta-commands, SET/set_config preamble,
//     comments, blank lines, the pg_dump version banner);
//   - scrubs values that are identity-stable but not structural (TimescaleDB assigns
//     sequential internal hypertable numbers and background-job ids that depend on how
//     many objects were created before them — different between a chain and a baseline
//     yet not a schema difference);
//   - splits into statements and SORTS them. Statement internals — column order, types,
//     constraints — are left intact, because those ARE the schema and a divergence there
//     is exactly what this harness must catch.
//
// The sort is no longer what makes creation order irrelevant to the COMPARISON: statementDiff
// compares multisets and is order-insensitive on its own, and the golden is read verbatim
// rather than re-sorted. What the sort still buys is a stable, reviewable golden file on
// disk — an unsorted one would churn wholesale whenever a migration was inserted mid-chain.
func normalizeDump(raw string) string {
	stmts := splitStatements(raw)
	out := make([]string, 0, len(stmts))
	for _, s := range stmts {
		s = scrub(s)
		if s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// splitNormalized reads an ALREADY-NORMALIZED body (the joined form normalizeDump
// returns, which is what a golden file contains) back into its statement list. It is
// the inverse of the join, and it applies no noise filtering or scrubbing because
// normalizeDump has already done both.
//
// 🔴 BOTH SIDES OF A COMPARISON MUST GO THROUGH THIS, including the freshly dumped one.
// The statement rule is a heuristic — accumulate until a line ends with ';' — so if it
// ever mis-splits, it must mis-split the golden and the live dump IDENTICALLY or the
// harness invents a difference that is an artefact of its own parser. Running the live
// side through normalizeDump and then this, rather than keeping an in-memory statement
// list, is what buys that symmetry for free.
func splitNormalized(body string) []string {
	var stmts []string
	var cur strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" && cur.Len() == 0 {
			continue
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(line)
		if strings.HasSuffix(strings.TrimSpace(line), ";") {
			stmts = append(stmts, cur.String())
			cur.Reset()
		}
	}
	// A trailing fragment means the body did not end on a ';'. Keep it rather than
	// dropping it: silently discarding the tail of a schema is the one outcome worse
	// than reporting a confusing diff.
	if rest := strings.TrimSpace(cur.String()); rest != "" {
		stmts = append(stmts, cur.String())
	}
	return stmts
}

// splitStatements breaks a dump into statements, dropping line noise first. A
// statement is accumulated until a line ends with ';' (adequate for DDL; DeviceChain
// migrations do not define pl/pgsql bodies with embedded semicolons).
func splitStatements(raw string) []string {
	var stmts []string
	var cur strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if isNoiseLine(trimmed) {
			continue
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(line)
		if strings.HasSuffix(trimmed, ";") {
			stmts = append(stmts, cur.String())
			cur.Reset()
		}
	}
	if rest := strings.TrimSpace(cur.String()); rest != "" {
		stmts = append(stmts, cur.String())
	}
	return stmts
}

// isNoiseLine reports whether a line carries no schema structure and must be dropped
// before statements are assembled.
func isNoiseLine(trimmed string) bool {
	switch {
	case trimmed == "":
		return true
	case strings.HasPrefix(trimmed, "--"): // comment / banner
		return true
	case strings.HasPrefix(trimmed, "SET "):
		return true
	case strings.HasPrefix(trimmed, "SELECT pg_catalog.set_config"):
		return true
	case strings.HasPrefix(trimmed, `\`): // psql meta-command (\connect, \restrict, ...)
		return true
	}
	return false
}

var (
	// TimescaleDB internal materialization hypertables carry a sequential number
	// (_materialized_hypertable_7) that depends on how many hypertables preceded them.
	reTsMatHypertable = regexp.MustCompile(`_materialized_hypertable_\d+`)
	// A continuous aggregate's generated view references its materialized hypertable by
	// that same internal id (cagg_watermark(6)) — chain-order-dependent, not structural.
	reTsCaggWatermark = regexp.MustCompile(`cagg_watermark\(\d+\)`)
	// Background job ids (continuous-aggregate refresh, compression, retention) are
	// likewise assigned sequentially.
	reTsJobID = regexp.MustCompile(`(?i)(job_id|_timescaledb_internal\._partial_view_|_timescaledb_internal\._direct_view_)\d+`)
	// A continuous-aggregate refresh policy's config embeds the same internal id
	// (`"mat_hypertable_id": 6`), which reaches the golden through the catalog probe
	// rather than through pg_dump. Same sequential-id reasoning as the two above; it is
	// listed separately because the key does not match reTsJobID's alternation.
	reTsMatHypertableID = regexp.MustCompile(`"mat_hypertable_id": \d+`)
	// Collapse runs of whitespace so incidental reflow does not create false diffs.
	reWhitespace = regexp.MustCompile(`[ \t]+`)
)

// scrub removes identity-stable-but-not-structural values from a single statement and
// normalizes whitespace. Returns "" if the statement was pure noise.
func scrub(stmt string) string {
	stmt = reTsMatHypertable.ReplaceAllString(stmt, "_materialized_hypertable_N")
	stmt = reTsCaggWatermark.ReplaceAllString(stmt, "cagg_watermark(N)")
	stmt = reTsJobID.ReplaceAllString(stmt, "${1}N")
	stmt = reTsMatHypertableID.ReplaceAllString(stmt, `"mat_hypertable_id": N`)
	stmt = reWhitespace.ReplaceAllString(stmt, " ")
	return strings.TrimSpace(stmt)
}
