// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/tenantpurge"
)

func entry(schema, name string, class tenantpurge.Class) tenantpurge.Entry {
	return tenantpurge.Entry{
		Table: tenantpurge.Table{Schema: schema, Name: name},
		Class: class,
	}
}

// The four buckets have to be disjoint AND exhaustive, because the report states a
// total and a reader is entitled to add the four numbers up and get it. A table
// counted twice, or dropped, makes every subsequent claim arithmetic nobody checked.
func TestReconcileSortsEveryTableIntoExactlyOneBucket(t *testing.T) {
	actionable := []tenantpurge.Entry{
		entry("device-management", "devices", tenantpurge.ClassDirect),
		entry("device-management", "device_relationships", tenantpurge.ClassTransitive),
		entry("device-state", "device_states", tenantpurge.ClassDirect),
		entry("event-management", "events", tenantpurge.ClassDirect),
	}
	rows := map[string]int64{
		"device-management.devices": 3,
		"event-management.events":   17,
	}
	exempt := map[string]string{
		"device-state.device_states": "the load harness owns the device plane",
		"event-management.events":    "this reason is now false",
	}

	rep := reconcileTableCoverage(actionable, rows, exempt)

	if rep.Total != 4 {
		t.Fatalf("Total = %d, want 4", rep.Total)
	}
	sum := len(rep.Covered) + len(rep.Uncovered) + len(rep.Exempt) + len(rep.Stale)
	if sum != rep.Total {
		t.Fatalf("buckets hold %d tables, Total says %d — they are not exhaustive and disjoint",
			sum, rep.Total)
	}

	seen := map[string]int{}
	for _, b := range [][]tableCoverage{rep.Covered, rep.Uncovered, rep.Exempt, rep.Stale} {
		for _, tc := range b {
			seen[tc.Table]++
		}
	}
	for _, e := range actionable {
		if n := seen[e.Table.String()]; n != 1 {
			t.Errorf("%s appears in %d buckets, want exactly 1", e.Table, n)
		}
	}
}

// The finding. A tenant-scoped table with no rows and no exemption is the thing this
// whole subcommand exists to report.
func TestReconcileFindsAnUncoveredTable(t *testing.T) {
	rep := reconcileTableCoverage(
		[]tenantpurge.Entry{entry("device-management", "facet_keys", tenantpurge.ClassDirect)},
		map[string]int64{},
		map[string]string{},
	)
	if len(rep.Uncovered) != 1 || rep.Uncovered[0].Table != "device-management.facet_keys" {
		t.Fatalf("an empty, unexempted table was not reported: %+v", rep)
	}
	if rep.ok() {
		t.Error("the report says ok() with an uncovered table in it")
	}
	if rep.Uncovered[0].Class != "direct" {
		t.Errorf("class = %q; the finding must carry it, so a reader can tell a table the "+
			"seed could write from one reached only through a join", rep.Uncovered[0].Class)
	}
}

// THE COUNTERWEIGHT for the case above. Without this, a reconciler that reported every
// table as uncovered would pass the finding test perfectly, and an exemption would buy
// nothing.
func TestReconcileAcceptsAnExemptedEmptyTable(t *testing.T) {
	rep := reconcileTableCoverage(
		[]tenantpurge.Entry{entry("device-state", "latest_measurements", tenantpurge.ClassDirect)},
		map[string]int64{},
		map[string]string{"device-state.latest_measurements": "the load harness owns the device plane"},
	)
	if len(rep.Uncovered) != 0 {
		t.Fatalf("an exempted table was still reported as a finding: %+v", rep.Uncovered)
	}
	if len(rep.Exempt) != 1 {
		t.Fatalf("Exempt = %v, want the one exempted table", rep.Exempt)
	}
	if rep.Exempt[0].Reason == "" {
		t.Error("the exemption's reason was dropped; the report would name a table nobody can judge")
	}
	if !rep.ok() {
		t.Error("a fully exempted instance did not report ok()")
	}
}

// THE OTHER COUNTERWEIGHT, and the one that makes exemptions safe to grant at all. An
// exemption states that a table SHOULD be empty. Once it is not, that statement is
// false and nobody is watching it — so it has to fail, in the opposite direction from
// a missing exemption.
func TestReconcileRejectsAStaleExemption(t *testing.T) {
	rep := reconcileTableCoverage(
		[]tenantpurge.Entry{entry("device-management", "geo_fences", tenantpurge.ClassDirect)},
		map[string]int64{"device-management.geo_fences": 2},
		map[string]string{"device-management.geo_fences": "the baseline cannot express geofences"},
	)
	if len(rep.Stale) != 1 {
		t.Fatalf("an exemption on a table with rows was not reported stale: %+v", rep)
	}
	if len(rep.Covered) != 0 {
		t.Error("a stale exemption was ALSO counted as covered; it must be one bucket, not two")
	}
	if rep.ok() {
		t.Error("the report says ok() with a stale exemption in it")
	}
	if rep.Stale[0].Rows != 2 {
		t.Errorf("Rows = %d, want 2 — the count is what shows the exemption is false", rep.Stale[0].Rows)
	}
}

// A covered table on its own must not be enough. This is the vacuity guard for ok():
// an implementation that returned true whenever anything was covered would satisfy
// every other test here.
func TestReportIsNotOKMerelyBecauseSomethingIsCovered(t *testing.T) {
	rep := reconcileTableCoverage(
		[]tenantpurge.Entry{
			entry("device-management", "devices", tenantpurge.ClassDirect),
			entry("device-management", "facet_keys", tenantpurge.ClassDirect),
		},
		map[string]int64{"device-management.devices": 1},
		map[string]string{},
	)
	if rep.ok() {
		t.Fatal("one covered table made the whole report ok() while another was uncovered")
	}
}

// The message is the deliverable. A finding that names the table but not what to do
// about it gets read as "the drill is broken" rather than "the seed is narrower than
// it claims", and the two lead to opposite work.
func TestTableSweepFailureNamesTheTableAndBothWaysOut(t *testing.T) {
	rep := reconcileTableCoverage(
		[]tenantpurge.Entry{entry("device-management", "facet_keys", tenantpurge.ClassDirect)},
		map[string]int64{},
		map[string]string{},
	)
	msg := tableSweepFailure(rep)
	for _, want := range []string{"device-management.facet_keys", "entities.go", "tableSweepExemptions"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the failure message never mentions %q:\n%s", want, msg)
		}
	}
}

func TestTableSweepFailureNamesAStaleExemptionAndItsReason(t *testing.T) {
	rep := reconcileTableCoverage(
		[]tenantpurge.Entry{entry("device-management", "geo_fences", tenantpurge.ClassDirect)},
		map[string]int64{"device-management.geo_fences": 2},
		map[string]string{"device-management.geo_fences": "the baseline cannot express geofences"},
	)
	msg := tableSweepFailure(rep)
	for _, want := range []string{"device-management.geo_fences", "the baseline cannot express geofences"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the failure message never mentions %q:\n%s", want, msg)
		}
	}
	// The reason has to be quoted back, not just the table: an exemption is deleted by
	// a person who has to judge whether it was ever right, and the reason is the only
	// thing that lets them.
	if strings.Contains(msg, "hold no row for the seeded tenant") {
		t.Error("a stale-exemption-only report also printed the uncovered-table prose")
	}
}

// Every exemption must carry a reason that says something. The empty-map case passes
// vacuously today, which is correct and temporary — the table is populated from a live
// measurement, and this is the gate that stops it being populated with shrugs.
func TestEveryExemptionExplainsItself(t *testing.T) {
	for table, reason := range tableSweepExemptions {
		if len(strings.TrimSpace(reason)) < 20 {
			t.Errorf("%s: reason %q is too short to be reviewable", table, reason)
		}
		if !strings.Contains(table, ".") {
			t.Errorf("%q is not schema-qualified; two areas may hold a table of the same name", table)
		}
		low := strings.ToLower(reason)
		for _, shrug := range []string{"the seed does not write", "not seeded", "no seed"} {
			if strings.Contains(low, shrug) {
				t.Errorf("%s: %q restates the finding rather than justifying it. "+
					"Why is it CORRECT for this table to be empty?", table, reason)
			}
		}
	}
}

// A password on argv is readable by any local process through /proc and survives in
// whatever log captured the command. The tool refuses one rather than redacting it,
// because redaction would address neither of those while looking like it had.
func TestDSNWithAPasswordIsRefused(t *testing.T) {
	refused := []string{
		"postgres://dcapp:hunter2@127.0.0.1:5432/upgrig?sslmode=disable",
		"postgresql://dcapp:hunter2@127.0.0.1:5432/upgrig",
		"host=127.0.0.1 user=dcapp password=hunter2 dbname=upgrig",
	}
	for _, dsn := range refused {
		err := checkDSNCarriesNoPassword(dsn)
		if err == nil {
			t.Errorf("a DSN carrying a password was accepted: %q", dsn)
			continue
		}
		if !strings.Contains(err.Error(), "PGPASSWORD") {
			t.Errorf("the refusal does not say what to do instead: %v", err)
		}
		if codeOf(err) != exitSetup {
			t.Errorf("a bad flag exited %d; it is not a verdict about the data", codeOf(err))
		}
		// The refusal must not quote the thing it is refusing to let be quoted.
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("the refusal printed the password it was rejecting: %v", err)
		}
	}
}

// THE COUNTERWEIGHT. A check that refused every DSN would satisfy the test above and
// make the subcommand unusable — including the `user=` form, whose substring 'password'
// does not appear but whose shape is closest to the one being rejected.
func TestDSNWithoutAPasswordIsAccepted(t *testing.T) {
	accepted := []string{
		"postgres://dcapp@127.0.0.1:5432/upgrig?sslmode=disable",
		"postgresql://dcapp@db:5432/upgrig",
		"host=127.0.0.1 user=dcapp dbname=upgrig sslmode=disable",
		"postgres://127.0.0.1:5432/upgrig",
	}
	for _, dsn := range accepted {
		if err := checkDSNCarriesNoPassword(dsn); err != nil {
			t.Errorf("a password-free DSN was refused: %q -> %v", dsn, err)
		}
	}
}
