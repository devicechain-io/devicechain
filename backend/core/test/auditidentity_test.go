// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// scanAuditFixture runs the scan over one in-line source file. Driving the scanner rather
// than the assertion is what lets these tests read what it FOUND; an assertion-only
// harness can show a guard failing but not show it failing for the right reason.
func scanAuditFixture(t *testing.T, body string, exempt ...string) []AnonymousMutation {
	t.Helper()
	src := "package fixture\n\nfunc scope() {\n" + body + "\n}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the fixture: %v\n%s", err, src)
	}
	set := map[string]bool{}
	for _, name := range exempt {
		set["dir\x00"+name] = true
	}
	return anonymousMutationsIn(fset, f, "dir", set)
}

// 🔴 THE NEGATIVE CONTROL, and it is the only reason the rest of this file means
// anything: a check is worth nothing until it has been shown to fail. This is the exact
// shape that shipped twenty-two times.
func TestTheGuardFiresOnAKeyedMutationThroughAZeroValueModel(t *testing.T) {
	found := scanAuditFixture(t, `
		res := api.RDB.DB(ctx).Model(&Command{}).
			Where("id = ? AND status IN ?", id, claimableStatusStrings()).
			Updates(map[string]any{"status": CommandSent.String()})
		_ = res`)

	if len(found) != 1 {
		t.Fatalf("the guard found %d anonymous mutations, want 1: %+v", len(found), found)
	}
	if found[0].Model != "Command" {
		t.Errorf("reported model %q, want Command", found[0].Model)
	}
	if found[0].Operation != "Updates" {
		t.Errorf("reported operation %q, want Updates", found[0].Operation)
	}
	// The condition is carried so the failure message can show WHY the row was
	// considered identified. Without it a reader has to open the file to tell this
	// finding apart from a condition-only update the guard is supposed to ignore.
	if !strings.Contains(found[0].Condition, "id = ?") {
		t.Errorf("reported condition %q does not show the primary-key constraint", found[0].Condition)
	}
}

// 🔴 THE TRAP A TEXTUAL VERSION OF THIS CHECK FELL INTO, twice, while it was being
// written. A gorm chain puts its "." at the END of the line, so the next line begins
// "Updates(" with nothing before it — and a scan looking for ".Updates(" finds no
// mutation at all and reports the whole repository clean.
//
// This is the same statement as the test above with the breaks moved, and it must be
// found identically. It is here because the failure mode is silent: the guard reports
// success, which is indistinguishable from there being nothing to report.
func TestTheChainIsReadAcrossLineBreaksWhereverTheDotsFall(t *testing.T) {
	forms := map[string]string{
		"dot trailing": `_ = db.Model(&Command{}).
			Where("id = ?", id).
			Updates(fields)`,
		"dot leading": `_ = db.Model(&Command{}).Where("id = ?", id).Updates(fields)`,
		"split wide": `_ = db.
			Model(&Command{}).
			Where("id = ?", id).
			Updates(fields)`,
	}
	for name, body := range forms {
		t.Run(name, func(t *testing.T) {
			if found := scanAuditFixture(t, body); len(found) != 1 {
				t.Errorf("the guard found %d, want 1 — a layout it cannot read reports "+
					"the tree clean rather than reporting an error: %+v", len(found), found)
			}
		})
	}
}

// 🔴 A FOREIGN KEY NAMES A DIFFERENT ROW THAN THE ONE BEING MUTATED, so it must not be
// read as the row's own identity. These conditions are real: both appear in
// device-management's version tables next to a genuine "id = ?" in the same statement.
func TestAForeignKeyIsNotMistakenForThePrimaryKey(t *testing.T) {
	for _, cond := range []string{
		"asset_type_id = ? AND version = ?",
		"entity_group_id = ? AND version = ?",
		"device_profile_id = ?",
		"tenant_id = ? AND function = ?",
	} {
		t.Run(cond, func(t *testing.T) {
			body := "_ = db.Model(&AssetTypeVersion{}).Where(\"" + cond + "\", a, b).Updates(fields)"
			if found := scanAuditFixture(t, body); len(found) != 0 {
				t.Errorf("condition %q was read as naming the mutated row's own primary key; "+
					"it names a different row, and the mutation is condition-only: %+v", cond, found)
			}
		})
	}
}

// The two shapes that are ALLOWED to record nothing. If the guard fired on these it
// would be unfixable noise: there is no primary key for the callback to record, so
// an empty EntityPK is the truthful entry rather than a defect.
func TestTheGuardIsQuietWhereNoSingleRowIsIdentified(t *testing.T) {
	cases := map[string]string{
		"dynamic condition built by the caller": `_ = db.Model(&Command{}).
			Where(where, args...).
			Where("status = ?", CommandSent.String()).
			Updates(fields)`,
		"natural-key CAS": `_ = db.Model(&NotificationState{}).
			Where("alarm_token = ? AND escalation_level = ? AND acknowledged_at IS NULL", tok, lvl).
			Updates(fields)`,
		"batch by foreign key": `_ = db.Model(&Command{}).
			Where("batch_id = ? AND status IN ?", batchId, cancellableStatusStrings()).
			Updates(fields)`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if found := scanAuditFixture(t, body); len(found) != 0 {
				t.Errorf("the guard fired where no single row is identified, so there is "+
					"nothing the audit callback could have recorded: %+v", found)
			}
		})
	}
}

// 🔴 THE COUNTERWEIGHT. "Fires on the bad shape" is satisfied just as well by a guard
// that fires on everything, which would be reverted within a day. Each of these is a
// CORRECT way to write the statement and must stay silent — otherwise the guard cannot
// be satisfied and the fix it asks for does not exist.
func TestTheGuardIsQuietOnceTheStatementCarriesTheIdentity(t *testing.T) {
	cases := map[string]string{
		"the loaded row is handed in": `_ = db.Model(&existing).
			Where("id = ?", existing.ID).
			Updates(fields)`,
		"the key is seeded into the literal": `_ = db.Model(&Command{Model: gorm.Model{ID: id}}).
			Where("id = ? AND status = ?", id, CommandQueued.String()).
			Updates(fields)`,
		"a pointer variable": `_ = db.Model(current).
			Where("id = ?", current.ID).
			Updates(fields)`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if found := scanAuditFixture(t, body); len(found) != 0 {
				t.Errorf("the guard fired on a statement that already carries the row's "+
					"identity, so it is asking for a change that is already made: %+v", found)
			}
		})
	}
}

// A model outside the journal has no entry to be anonymous. The exemption is read from
// the model's own AuditExempt declaration rather than a list kept here, so a newly
// exempted table needs no edit to this guard — and, more to the point, a table that
// QUIETLY STOPS being exempt starts being checked on the same commit.
func TestAnAuditExemptModelIsNotReported(t *testing.T) {
	body := `_ = db.Model(&DeviceState{}).Where("id = ?", id).Updates(fields)`

	if found := scanAuditFixture(t, body); len(found) != 1 {
		t.Fatalf("precondition: without the exemption this shape must be reported, "+
			"or the next assertion proves nothing. Found %d: %+v", len(found), found)
	}
	if found := scanAuditFixture(t, body, "DeviceState"); len(found) != 0 {
		t.Errorf("an AuditExempt model was reported; it writes no audit row at all: %+v", found)
	}
}

// The exemption is keyed by DIRECTORY, so one service's opt-out cannot silence another
// service's identically-named type. Nothing in the tree relies on this today — every
// AuditExempt sits in the same package as the call sites it covers — which is exactly
// why it is pinned here rather than left to be discovered when two services collide.
func TestAnExemptionDoesNotTravelBetweenPackages(t *testing.T) {
	src := "package fixture\n\nfunc scope() {\n" +
		`_ = db.Model(&Snapshot{}).Where("id = ?", id).Updates(fields)` + "\n}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	exempt := map[string]bool{"other-service\x00Snapshot": true}
	if found := anonymousMutationsIn(fset, f, "this-service", exempt); len(found) != 1 {
		t.Errorf("an exemption declared in another package silenced this one; found %d: %+v",
			len(found), found)
	}
}

// 🔴 THE HOLE THIS GUARD SHIPPED WITH, found by running it against the real tree and
// noticing it called dashboard-management clean while that file held the exact defect.
//
// A statement that gains a condition behind an `if` cannot be one expression, so it is
// parked in a variable and terminated later. The mutating call then reads
// `write.Updates(...)`, whose receiver is an identifier rather than a chain — and a
// walk that stops at the identifier finds no Model() and reports success. This is the
// verbatim shape from dashboard-management/model/api.go.
func TestAChainParkedInAVariableIsStillRead(t *testing.T) {
	found := scanAuditFixture(t, `
		write := api.RDB.DB(ctx).Model(&Dashboard{}).Where("id = ?", current.ID)
		if expectedUpdatedAt != nil {
			write = write.Where("updated_at = ?", current.UpdatedAt)
		}
		res := write.Updates(assignments)
		_ = res`)

	if len(found) != 1 {
		t.Fatalf("a statement built across three lines was not read as one; the guard "+
			"reported %d findings and would call this file clean: %+v", len(found), found)
	}
	if found[0].Model != "Dashboard" {
		t.Errorf("reported model %q, want Dashboard", found[0].Model)
	}
}

// The counterweight to the above: following a variable must not invent findings by
// carrying a chain into a statement that never used it. Here the parked chain is
// abandoned and a DIFFERENT, correctly-written statement does the mutation.
func TestFollowingAVariableDoesNotInventAFinding(t *testing.T) {
	found := scanAuditFixture(t, `
		probe := api.RDB.DB(ctx).Model(&Dashboard{}).Where("id = ?", current.ID)
		_ = probe
		res := api.RDB.DB(ctx).Model(current).Where("id = ?", current.ID).Updates(assignments)
		_ = res`)

	if len(found) != 0 {
		t.Errorf("the guard carried an unrelated parked chain into a correctly-written "+
			"statement: %+v", found)
	}
}
