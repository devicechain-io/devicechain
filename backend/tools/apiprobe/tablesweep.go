// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// THE THIRD INSTRUMENT, AND THE ONLY ONE THAT LOOKS AT THE DATABASE.
//
// `seed` and `verify` are a round trip: they prove every row this tool wrote reads back
// unchanged. `readsweep` is a breadth pass over the served schema: it proves every door
// the platform serves still answers. Both are clients. Neither can answer the question
// this file exists for, and the question is not subtle:
//
//	IS "ONE OF EVERY ENTITY" TRUE?
//
// entities.go is the coverage CLAIM, written as data so a missing entity is an absent
// row somebody can count. But nobody has ever counted it against the thing it claims to
// cover. A table no entity writes to is invisible from the API side by construction —
// there is no query that returns "the rows you did not create" — so the claim has been
// asserted for as long as the tool has existed and checked exactly never.
//
// 🔴 IT IS DERIVED, NOT LISTED, AND THAT IS THE WHOLE POINT. A hand-written list of
// tables to check answers "what did somebody remember", which is the same failure the
// claim already has, one level down. So the set comes from core/tenantpurge, which
// enumerates tenant-scoped tables FROM THE POSTGRES CATALOG — the identical derivation
// the tenant purge trusts to erase a customer's data, chosen for exactly this reason
// there too. A functional area that lands after this file is written is swept without
// anyone remembering it.
//
// 🔴 AND IT DOES NOT RE-EXPRESS THAT DERIVATION. `tenantpurge.Residue` already counts
// this tenant's rows per table, using the SAME predicate the sweep deletes with —
// including the transitive joins, which are the part a re-implementation would get
// subtly wrong and never be told about. Residue omits tables contributing zero rows,
// so the set this file wants is `Actionable()` minus what Residue reported. Writing
// the counting query here instead would create a second source of truth for "which
// rows belong to this tenant", inside the tool whose arc exists to eliminate exactly
// that pattern.
//
// WHAT IT DOES NOT PROVE. That a table holds a row says nothing about whether the row
// is right — `verify` owns that, for the entities it knows. And a table holding rows
// for the seeded tenant does not mean the SEED wrote them: a service writing its own
// bookkeeping under the tenant counts here too. This measures reach, not authorship.

// tableSweepFloor is the smallest number of tenant-scoped tables a real instance can
// have. Measured: 52 on a bootstrapped instance in August 2026.
//
// 🔴 This exists because EVERY OTHER CHECK IN THIS FILE PASSES VACUOUSLY ON AN EMPTY
// PLAN. Zero actionable tables means zero uncovered tables and zero stale exemptions,
// which reads as a perfect result and is in fact a connection to the wrong database, a
// schema that never migrated, or a classifier that returned nothing. tenantpurge's own
// checkSweepable refuses an empty plan for the same reason; this is the same refusal
// one size up, because a plan with four tables in it is not empty and is just as wrong.
const tableSweepFloor = 30

// tableCoverage is one table's verdict.
type tableCoverage struct {
	Table  string
	Class  string
	Rows   int64
	Reason string
}

// tableSweepReport is the full reconciliation. The four buckets are exhaustive and
// disjoint, so `len` of the four always sums to the actionable count — which is what
// lets the report state a total a reader can check rather than a total it asserts.
type tableSweepReport struct {
	Total int
	// Covered: the seed reached it. The everyday case.
	Covered []tableCoverage
	// Uncovered: no row for this tenant, and no exemption. THE FINDING.
	Uncovered []tableCoverage
	// Exempt: no row, and a written reason why that is expected.
	Exempt []tableCoverage
	// Stale: an exemption on a table that now HAS rows. Also a finding, and the
	// counterweight that makes exemptions safe to grant at all — an exemption nobody
	// is watching is a coverage gap with paperwork.
	Stale []tableCoverage
}

func (r tableSweepReport) ok() bool { return len(r.Uncovered) == 0 && len(r.Stale) == 0 }

// reconcileTableCoverage sorts every actionable table into exactly one bucket.
//
// Split out from the database entirely so it can be tested: Classify and Residue need
// a live PostgreSQL catalog, this needs neither, and the bucket boundaries are where
// the interesting mistakes live.
func reconcileTableCoverage(
	actionable []tenantpurge.Entry,
	rows map[string]int64,
	exempt map[string]string,
) tableSweepReport {
	rep := tableSweepReport{Total: len(actionable)}
	for _, e := range actionable {
		name := e.Table.String()
		tc := tableCoverage{
			Table:  name,
			Class:  e.Class.String(),
			Rows:   rows[name],
			Reason: exempt[name],
		}
		_, isExempt := exempt[name]
		switch {
		case tc.Rows > 0 && isExempt:
			rep.Stale = append(rep.Stale, tc)
		case tc.Rows > 0:
			rep.Covered = append(rep.Covered, tc)
		case isExempt:
			rep.Exempt = append(rep.Exempt, tc)
		default:
			rep.Uncovered = append(rep.Uncovered, tc)
		}
	}
	return rep
}

// tableSweepExemptions names tenant-scoped tables the seed is NOT expected to reach,
// keyed by `schema.table`, with the reason it is acceptable.
//
// 🔴 IT IS EMPTY ON PURPOSE, AND THIS COMMENT IS THE REASON RATHER THAN AN APOLOGY.
// The entries belong to whatever a live instance actually reports, and every plausible
// way to pre-populate it is a guess: from the roadmap notes (a measurement taken on a
// v0.11.0 baseline, which cannot express geofences, dashboards, connectors or command
// batches, so it counted their tables empty for a reason that no longer applies), or
// from reading the seed and reasoning about what it must miss (which is precisely the
// "what did somebody remember" this file exists to replace). An exemption asserts that
// a table SHOULD be empty. Writing one from memory and having CI agree with it teaches
// nothing, because CI would agree with a wrong one just as readily.
//
// So the first live run is the measurement, and it is safe to take that way round: the
// Stale bucket means a wrong exemption fails just as loudly as a missing one, in the
// opposite direction. There is no way to be quietly wrong here.
//
// Each entry must say WHY the table is legitimately empty. The two kinds expected:
//
//   - device-plane tables — state, presence, telemetry projections, alarms. The load
//     harness drives those with real oracles; a second, weaker set of assertions here
//     would be worse than none.
//   - tables written only by a flow the seed deliberately does not exercise.
//
// An entry whose reason is "the seed does not write it" and nothing more is not an
// exemption, it is the finding restated. Extend the seed instead.
var tableSweepExemptions = map[string]string{}

func runTableSweep(ctx context.Context, argv []string) error {
	fs := flagSetFor("tablesweep")
	var dsn, tenant string
	fs.StringVar(&dsn, "dsn", "",
		"PostgreSQL connection `string` for the instance database, WITHOUT a password (use PGPASSWORD)")
	fs.StringVar(&tenant, "tenant", "apiprobe", "tenant `token` the seed wrote under")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if dsn == "" {
		return failWith(exitSetup, "--dsn is required")
	}
	if err := checkDSNCarriesNoPassword(dsn); err != nil {
		return err
	}
	if tenant == "" {
		return failWith(exitSetup, "--tenant may not be empty")
	}

	// Silent logger: gorm's default prints every statement it runs, and the statements
	// here are catalog queries nobody wants and row counts the report states better.
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return failWith(exitSetup, "could not open the instance database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return failWith(exitSetup, "could not reach the instance database: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()
	if err := sqlDB.PingContext(ctx); err != nil {
		return failWith(exitSetup, "could not reach the instance database: %w", err)
	}

	plan, err := tenantpurge.Classify(ctx, db)
	if err != nil {
		return failWith(exitSetup, "could not classify the instance database: %w", err)
	}
	actionable := plan.Actionable()
	if len(actionable) < tableSweepFloor {
		return failWith(exitSetup,
			"the classifier found only %d tenant-scoped table(s), and a real instance has at least %d.\n"+
				"    Every check below would pass vacuously on a set this small, so this refuses to\n"+
				"    report a result. The usual causes are a connection to the wrong database on the\n"+
				"    right server, or an instance whose migrations have not run.",
			len(actionable), tableSweepFloor)
	}

	// Residue counts THIS tenant's rows per table with the sweep's own predicate, and
	// omits the tables contributing none — so its result is the covered set, and the
	// gap is everything in Actionable() it did not mention.
	res, err := tenantpurge.Residue(ctx, db, plan, tenant)
	if err != nil {
		return failWith(exitSetup, "could not count rows for tenant %q: %w", tenant, err)
	}
	rows := make(map[string]int64, len(res.Tables))
	for _, t := range res.Tables {
		rows[t.Table.String()] = t.Rows
	}

	rep := reconcileTableCoverage(actionable, rows, tableSweepExemptions)
	printTableSweep(rep, tenant)
	if rep.ok() {
		return nil
	}
	return failWith(exitCoverage, "%s", tableSweepFailure(rep))
}

func printTableSweep(rep tableSweepReport, tenant string) {
	fmt.Printf("TABLE SWEEP — tenant %q, %d tenant-scoped table(s)\n", tenant, rep.Total)
	fmt.Printf("    %d covered, %d exempt, %d UNCOVERED, %d STALE exemption(s)\n",
		len(rep.Covered), len(rep.Exempt), len(rep.Uncovered), len(rep.Stale))
}

func tableSweepFailure(rep tableSweepReport) string {
	var b strings.Builder
	if n := len(rep.Uncovered); n > 0 {
		fmt.Fprintf(&b, "%d of %d tenant-scoped table(s) hold no row for the seeded tenant:\n",
			n, rep.Total)
		for _, tc := range sorted(rep.Uncovered) {
			fmt.Fprintf(&b, "    %-56s %s\n", tc.Table, tc.Class)
		}
		b.WriteString(
			"\n  \"One of every entity\" is not true of this instance. Two ways to make it true,\n" +
				"  and they are not interchangeable:\n\n" +
				"    1. EXTEND THE SEED in entities.go so the table gets a row. This is the answer\n" +
				"       whenever the table belongs to something a tenant can create through the API,\n" +
				"       and it is the answer that widens what the upgrade drill can see.\n" +
				"    2. ADD AN EXEMPTION to tableSweepExemptions, WITH THE REASON the table is\n" +
				"       legitimately empty. A reason amounting to \"the seed does not write it\" is the\n" +
				"       finding restated, not an exemption.\n")
	}
	if n := len(rep.Stale); n > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d exemption(s) name a table that now HOLDS rows:\n", n)
		for _, tc := range sorted(rep.Stale) {
			fmt.Fprintf(&b, "    %-56s %d row(s)\n        exempted because: %s\n",
				tc.Table, tc.Rows, tc.Reason)
		}
		b.WriteString(
			"\n  Delete those entries. An exemption is a standing statement that a table SHOULD be\n" +
				"  empty; once it is not, the statement is false and nobody is watching it. This is\n" +
				"  the counterweight that makes granting exemptions safe, so it is a failure rather\n" +
				"  than a note.\n")
	}
	return b.String()
}

func sorted(in []tableCoverage) []tableCoverage {
	out := append([]tableCoverage(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Table < out[j].Table })
	return out
}

// checkDSNCarriesNoPassword refuses a connection string with the password in it.
//
// 🔴 REFUSED RATHER THAN REDACTED, and the difference is the whole point. A password on
// argv is readable by any process on the machine through /proc/<pid>/cmdline for as long
// as this runs, and it is one `set -x` away from a CI step log that anyone with
// repository read can fetch for ninety days. Redacting it from THIS tool's error
// messages would address neither, while looking like it had.
//
// There is no cost to refusing, which is why it can be a hard rule: the driver reads
// PGPASSWORD. Measured against a real PostgreSQL 17, three ways, because "the library
// probably supports the libpq variables" is exactly the assumption that would leave the
// tool silently connecting as a passwordless user if it were wrong — no password at all
// FAILS, the correct PGPASSWORD with a bare DSN CONNECTS, and a wrong PGPASSWORD FAILS.
// The middle case alone would have passed with the variable being ignored and trust
// authentication doing the work.
func checkDSNCarriesNoPassword(dsn string) error {
	const guidance = "Pass it in PGPASSWORD instead: a password on the command line is readable " +
		"by\n    any local process through /proc, and survives in whatever log captured the command."

	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return failWith(exitSetup, "--dsn is not a valid connection URL: %w", err)
		}
		if _, set := u.User.Password(); set {
			return failWith(exitSetup, "--dsn carries a password.\n    %s", guidance)
		}
		return nil
	}
	// The key=value form. `password=` is the only spelling libpq accepts for it.
	for _, field := range strings.Fields(dsn) {
		if strings.HasPrefix(field, "password=") {
			return failWith(exitSetup, "--dsn carries a password.\n    %s", guidance)
		}
	}
	return nil
}
