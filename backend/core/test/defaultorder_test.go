// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeOrderFixture puts one Go source file in its own directory and returns the root to
// scan. Fixtures are written rather than embedded so each one is readable as the thing it
// is: a small package that does or does not break the rule.
func writeOrderFixture(t *testing.T, name, src string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte(src), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return root
}

func scanOrders(t *testing.T, root string) []unqualifiedOrder {
	t.Helper()
	found, _, _, err := unqualifiedOrdersUnder(root)
	if err != nil {
		t.Fatalf("scanning fixture: %v", err)
	}
	return found
}

// The defect itself: a clause naming bare columns.
func TestAnUnqualifiedOrderIsReported(t *testing.T) {
	root := writeOrderFixture(t, "m.go", `package m

type Row struct{}

func (Row) DefaultOrder() string { return "occurred_time DESC, id DESC" }
`)
	found := scanOrders(t, root)
	if len(found) != 1 {
		t.Fatalf("expected the unqualified order to be reported, got %d findings: %+v", len(found), found)
	}
	if found[0].Type != "Row" {
		t.Errorf("reported receiver %q, want Row", found[0].Type)
	}
	if !strings.Contains(found[0].Reason, "occurred_time") || !strings.Contains(found[0].Reason, "id") {
		t.Errorf("the reason names neither offending column: %q", found[0].Reason)
	}
}

// 🔴 THE COUNTERWEIGHT. A guard that reported everything would pass the test above and be
// useless; this is the half that says the rule can be SATISFIED.
func TestAQualifiedOrderIsNotReported(t *testing.T) {
	root := writeOrderFixture(t, "m.go", `package m

type Row struct{}

func (Row) DefaultOrder() string { return "rows.occurred_time DESC, rows.id DESC" }
`)
	if found := scanOrders(t, root); len(found) != 0 {
		t.Fatalf("a properly qualified order was reported: %+v", found)
	}
}

// Direction and null-placement keywords follow the column, so the check has to read the
// FIRST token of each term rather than the term as a whole. A real model orders this way.
func TestAQualifiedOrderCarryingNullsPlacementIsNotReported(t *testing.T) {
	root := writeOrderFixture(t, "m.go", `package m

type Row struct{}

func (Row) DefaultOrder() string {
	return "creds.expires_at DESC NULLS FIRST, creds.id DESC"
}
`)
	if found := scanOrders(t, root); len(found) != 0 {
		t.Fatalf("a qualified order with NULLS FIRST was reported: %+v", found)
	}
}

// Only the offending term is reported when a clause mixes both — otherwise the failure
// message sends the reader to the column that was already correct.
func TestOnlyTheUnqualifiedTermOfAMixedClauseIsNamed(t *testing.T) {
	root := writeOrderFixture(t, "m.go", `package m

type Row struct{}

func (Row) DefaultOrder() string { return "rows.occurred_time DESC, id DESC" }
`)
	found := scanOrders(t, root)
	if len(found) != 1 {
		t.Fatalf("expected one finding, got %d: %+v", len(found), found)
	}
	if strings.Contains(found[0].Reason, "occurred_time") {
		t.Errorf("the reason names the qualified column too: %q", found[0].Reason)
	}
	if !strings.Contains(found[0].Reason, "id") {
		t.Errorf("the reason does not name the unqualified column: %q", found[0].Reason)
	}
}

// 🔴 MATCHED BY SIGNATURE, NOT BY NAME ALONE. A same-named method taking an argument is a
// different thing, and reading its body would report a defect against code that has none.
func TestASameNamedMethodWithADifferentSignatureIsNotReported(t *testing.T) {
	root := writeOrderFixture(t, "m.go", `package m

type Row struct{}

func (Row) DefaultOrder(table string) string { return "id DESC" }

type Real struct{}

func (Real) DefaultOrder() string { return "reals.id DESC" }
`)
	if found := scanOrders(t, root); len(found) != 0 {
		t.Fatalf("a differently-signed DefaultOrder was read as a Sortable: %+v", found)
	}
}

// A package-level function is not a model's declared order, so it is not this guard's
// business even when it is spelled the same way.
func TestAPackageLevelFunctionOfTheSameNameIsNotReported(t *testing.T) {
	root := writeOrderFixture(t, "m.go", `package m

func DefaultOrder() string { return "id DESC" }

type Real struct{}

func (Real) DefaultOrder() string { return "reals.id DESC" }
`)
	if found := scanOrders(t, root); len(found) != 0 {
		t.Fatalf("a package-level function was read as a model's order: %+v", found)
	}
}

// A body this guard cannot read is REPORTED, not skipped. Skipping it would make
// "returns something computed" the way to opt out of the rule.
func TestABodyThatIsNotAStringLiteralIsReportedRatherThanSkipped(t *testing.T) {
	root := writeOrderFixture(t, "m.go", `package m

var order = "id DESC"

type Row struct{}

func (Row) DefaultOrder() string { return order }
`)
	found := scanOrders(t, root)
	if len(found) != 1 {
		t.Fatalf("expected the unreadable body to be reported, got %d: %+v", len(found), found)
	}
	if !strings.Contains(found[0].Reason, "string literal") {
		t.Errorf("the reason does not say why the clause could not be read: %q", found[0].Reason)
	}
}

// 🔴 A CLAUSE THE SPLITTER CANNOT HANDLE IS REFUSED, NOT PASSED. Splitting COALESCE(a, b)
// on commas would examine its arguments as though they were columns — and the half that
// happens to contain a dot would pass. A scanner's failure mode is reporting clean.
func TestAClauseContainingACallExpressionIsRefusedRatherThanPassed(t *testing.T) {
	root := writeOrderFixture(t, "m.go", `package m

type Row struct{}

func (Row) DefaultOrder() string { return "COALESCE(rows.a, rows.b) DESC, rows.id DESC" }
`)
	found := scanOrders(t, root)
	if len(found) != 1 {
		t.Fatalf("expected the unsplittable clause to be refused, got %d: %+v", len(found), found)
	}
	if !strings.Contains(found[0].Reason, "call expression") {
		t.Errorf("the reason does not say the clause could not be split: %q", found[0].Reason)
	}
}

// An empty clause means ListOf applies no ORDER BY at all, which is the unstable-paging
// defect every DefaultOrder exists to prevent. It is a finding, not a pass.
func TestAnEmptyOrderIsReported(t *testing.T) {
	root := writeOrderFixture(t, "m.go", `package m

type Row struct{}

func (Row) DefaultOrder() string { return "" }
`)
	found := scanOrders(t, root)
	if len(found) != 1 {
		t.Fatalf("expected an empty order to be reported, got %d: %+v", len(found), found)
	}
	if !strings.Contains(found[0].Reason, "empty") {
		t.Errorf("the reason does not identify the empty clause: %q", found[0].Reason)
	}
}

// 🔴🔴 THE ONE THAT MATTERS MOST. A scan that parses files and recognizes nothing in them
// returns the same empty result as a scan of a clean tree. If the matcher ever breaks —
// a renamed method, a signature check that rejects everything — this guard must say so
// rather than going quietly green over a tree it no longer understands.
func TestAnOrderScanThatRecognizesNothingRefusesRatherThanReportingClean(t *testing.T) {
	root := writeOrderFixture(t, "m.go", `package m

type Row struct{}

func (Row) SomeOtherMethod() string { return "id DESC" }
`)
	_, _, _, err := unqualifiedOrdersUnder(root)
	if err == nil {
		t.Fatal("a scan that recognized no DefaultOrder reported success; a broken matcher " +
			"would produce exactly this result and be indistinguishable from a clean tree")
	}
	if !strings.Contains(err.Error(), orderMethod) {
		t.Errorf("the refusal does not name the method it failed to find: %v", err)
	}
}

// And a scan with no sources at all is an error for the same reason.
//
// 🔑 IT ASSERTS WHICH FLOOR FIRED, not merely that one did. There are two — no files, and
// no recognized methods — and an empty directory trips both. Checking only err != nil, as
// this did at first, passes even when the file floor is deleted, because the method floor
// catches the same case with a different message. A test that cannot tell two guards apart
// is measuring their disjunction, not either of them.
func TestAnOrderScanOfNoSourcesRefuses(t *testing.T) {
	_, _, _, err := unqualifiedOrdersUnder(t.TempDir())
	if err == nil {
		t.Fatal("a scan that found no Go files at all reported success")
	}
	if !strings.Contains(err.Error(), ".go files") {
		t.Errorf("the refusal does not name the missing-sources floor, so this test would "+
			"still pass with that floor deleted and the method floor answering instead: %v", err)
	}
}

// 🔴 S3. The signature check has two halves — no parameters, and exactly one STRING result
// — and only the first was pinned. A same-named, same-arity method returning something else
// is not a Sortable, and reading its body produces a FALSE finding against correct code.
func TestASameNamedMethodReturningANonStringIsNotReported(t *testing.T) {
	root := writeOrderFixture(t, "m.go", `package m

type Row struct{}

func (Row) DefaultOrder() int { return 5 }

type Real struct{}

func (Real) DefaultOrder() string { return "reals.id DESC" }
`)
	if found := scanOrders(t, root); len(found) != 0 {
		t.Fatalf("a DefaultOrder returning a non-string was read as a Sortable: %+v", found)
	}
}

// 🔴🔴 S5, AND THIS IS THE FALSE-NEGATIVE DIRECTION. Qualification is judged on the FIRST
// token of each term, because everything after it is direction and null-placement keywords.
// Judging the whole term instead would let a dot ANYWHERE in it satisfy the rule — and
// "id USING pg_catalog.<" is legal Postgres whose ordered column is bare. That mutation
// makes the scanner report CLEAN on a real defect, which is the failure mode this whole
// file exists to make impossible.
func TestABareColumnIsReportedEvenWhenALaterTokenInTheTermIsDotted(t *testing.T) {
	root := writeOrderFixture(t, "m.go", `package m

type Row struct{}

func (Row) DefaultOrder() string { return "id USING pg_catalog.<" }
`)
	found := scanOrders(t, root)
	if len(found) != 1 {
		t.Fatalf("a bare column went unreported because a later token in its term carried a "+
			"dot, got %d findings: %+v", len(found), found)
	}
	// The message must name the COLUMN, not the whole term: a reader sent to
	// "id USING pg_catalog.<" has to work out which part of it is the problem.
	if !strings.Contains(found[0].Reason, "names id without") {
		t.Errorf("the reason does not name the bare column on its own: %q", found[0].Reason)
	}
}

// 🔑 S13. Including _test.go files is a deliberate choice, and until this existed it was
// only a BELIEF: every fixture here is named m.go and the single DefaultOrder in a real
// test file happens to be qualified, so skipping test files entirely changed no result.
// A fixture model is where the next real model gets copied from.
func TestAnUnqualifiedOrderInATestFileIsReported(t *testing.T) {
	root := writeOrderFixture(t, "m_test.go", `package m

type Row struct{}

func (Row) DefaultOrder() string { return "occurred_time DESC" }
`)
	if found := scanOrders(t, root); len(found) != 1 {
		t.Fatalf("an unqualified order in a _test.go file was not reported, so the scan's "+
			"stated coverage of fixtures is not something it actually does: %+v", found)
	}
}

// S15. A pointer receiver is stripped so the finding names the type rather than "(unknown)",
// which is the difference between a message someone can act on and one they have to grep for.
func TestAPointerReceiverIsNamedInTheFinding(t *testing.T) {
	root := writeOrderFixture(t, "m.go", `package m

type Row struct{}

func (r *Row) DefaultOrder() string { return "occurred_time DESC" }
`)
	found := scanOrders(t, root)
	if len(found) != 1 {
		t.Fatalf("expected one finding, got %d: %+v", len(found), found)
	}
	if found[0].Type != "Row" {
		t.Errorf("a pointer receiver was reported as %q rather than Row", found[0].Type)
	}
}
