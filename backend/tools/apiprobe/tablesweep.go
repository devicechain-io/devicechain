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
// 🔴 EVERY ENTRY WAS MEASURED, NOT REASONED. The list was shipped EMPTY on purpose and
// the first live drill produced it — because an exemption asserts that a table SHOULD be
// empty, and CI would agree with a wrong one just as readily as a right one. Each reason
// below names the code that writes the table, found by following the writer rather than
// by reading the seed and guessing what it misses.
//
// It is safe to grant these because the Stale bucket makes a wrong exemption fail just as
// loudly as a missing one, in the opposite direction. There is no way to be quietly wrong.
//
// 🔑 A reason amounting to "the seed does not write it" is the finding restated, not an
// exemption, and TestEveryExemptionExplainsItself rejects it. The question each answers
// is: why is it CORRECT for this table to be empty after a tenant has been fully seeded?
var tableSweepExemptions = map[string]string{
	// ---- the device plane, which the load harness owns ------------------------
	//
	// These hold what DEVICES produce, not what a tenant creates. The load-test harness
	// drives them hard with real oracles; a second, weaker set of assertions here would
	// be worse than none, and the drill deliberately sends no telemetry.
	"device-state.device_states": "the live last-known-state projection, written from device traffic. " +
		"The drill sends no telemetry by design, and the load harness owns this with real oracles.",
	"device-state.latest_locations": "the latest-location projection, written from device traffic. " +
		"Same owner and same reason as device_states.",
	"device-state.latest_measurements": "the latest-measurement projection, written from device traffic. " +
		"Same owner and same reason as device_states.",
	// 🔑 event-processing.device_attributes IS NOT HERE, AND IT WAS. The exemption said it
	// is "fed from the event pipeline rather than from the tenant API", and that was simply
	// wrong: setEntityAttribute in device-management PROJECTS into it, so seeding a tenant
	// attribute covers it. The Stale bucket caught the mistake on the next run, which is
	// the entire argument for having that bucket — a wrong exemption failed exactly as
	// loudly as a missing one.
	"event-processing.device_attribute_deletions": "the tombstone side of the attribute projection. " +
		"It records a REMOVAL, and the seed sets an attribute and never unsets it — an entry here " +
		"would mean the drill had deleted data it just wrote.",
	"device-management.alarms": "alarm objects are RAISED by the REACT pipeline when a rule fires. " +
		"Nothing fires in a drill that sends no telemetry, and creating one by hand would assert " +
		"the row rather than the engine that produces it.",
	"event-processing.rule_stats": "written only by RuleStatStore.RecordFire, i.e. when a rule actually " +
		"FIRES. No telemetry, no fire, no row.",

	// ---- everything downstream of a rule that RUNS ---------------------------
	//
	// 🔴 ONE REASON, FOUR TABLES, and it is worth stating once rather than four times.
	// Both seeded detection rules are authored DISABLED, deliberately: the probe asserts
	// that a rule document and its scope survive an upgrade, not that the engine runs.
	// Everything below follows from that, through a chain that is not obvious and was
	// measured rather than assumed —
	//
	//   scopedRulesInSnapshot() skips `!r.Enabled`, so a disabled rule contributes no
	//   DetectionRuleScopeRef; syncProfileScopeRefsAndEnroll() is what ENROLLS a group,
	//   and it is called at profile publish with exactly that set; and group membership
	//   is materialised LAZILY by that enrollment, not by publishing the group.
	//
	// Measured proof of the last step: the dynamic group IS published on every run —
	// entity_group_versions is covered — and its memberships are still empty. Publishing
	// a group freezes its selector; it does not resolve it.
	"device-management.detection_rule_scope_refs": "written only for a profile's ENABLED group-scoped " +
		"rules, at profile publish. Both seeded rules are disabled by design (see above), so the " +
		"desired scope-ref set is correctly empty.",
	"device-management.entity_group_facet_refs": "written when a group is ENROLLED, which happens via a " +
		"profile's enabled scoped rules. No enabled scoped rule, no enrollment, no facet refs — even " +
		"though the group is published and its selector does name a facet.",
	"device-management.entity_group_memberships": "materialised lazily by the same enrollment, not by " +
		"publishing the group. Measured: the group publishes successfully on every run and this stays " +
		"empty, which is what shows membership follows enrollment rather than publication.",
	"event-processing.detect_rules": "projected from a DetectionRulesPublishedEvent, and the publish gate " +
		"submits only a profile's ENABLED draft rules. Both seeded detection rules are authored " +
		"DISABLED on purpose: the probe asserts that the rule document and its scope survive an " +
		"upgrade, not that the engine runs. Enabling one to fill this table would change what the " +
		"drill claims, so it is a decision rather than an omission.",

	// ---- per-area journals, correctly empty ----------------------------------
	//
	// 🔑 Confirmed rather than assumed: device-management.audit_events is NOT among the
	// uncovered tables, which is what shows the mechanism is per-area rather than broken.
	"device-state.audit_events": "the audit journal is written by the core mutation callbacks in the " +
		"area that handled the write, and device-state serves no tenant create/update/delete at all " +
		"(its only mutation is demoteAssertedPresence). An empty journal there is the correct state.",
	"event-processing.audit_events": "same mechanism: event-processing's tenant-plane mutations are " +
		"draftDetectionRuleFromText and a stream subscription, neither of which is an entity write.",

	// ---- the record of things that did NOT happen -----------------------------
	"user-management.dead_letters": "a row here means a consumer accepted a message and then " +
		"failed to complete it after every delivery attempt. The drill exercises the platform " +
		"WORKING, so an empty table is the correct outcome — and a drill that produced one would " +
		"be reporting a defect in something else, not coverage of this. It carries tenant_id and " +
		"is swept by the ordinary tenant purge like any other tenant-bearing table.",

	// ---- runtime state of a delivery mechanism -------------------------------
	"notification-management.notification_states": "escalation state for a notification the policy has " +
		"actually dispatched. It follows an ALARM, which follows a rule firing, which the drill does " +
		"not do — so it is downstream of two things that are exempt above for their own reasons.",
}

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
