// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// backdateSentTime pushes a row's sent_time into the past, which is how a test ages a
// command without waiting. It writes the column directly rather than going through an
// API, because no API exists to make a command older and none should.
func backdateSentTime(t *testing.T, api *Api, ctx context.Context, id uint, age time.Duration) {
	t.Helper()
	res := api.RDB.DB(ctx).Model(&Command{}).Where("id = ?", id).
		Update("sent_time", time.Now().Add(-age))
	if res.Error != nil {
		t.Fatalf("backdating %d: %v", id, res.Error)
	}
	if res.RowsAffected != 1 {
		t.Fatalf("backdating %d touched %d rows, want 1", id, res.RowsAffected)
	}
}

// TestScanFindsOnlyRowsPastTheHorizon carries its own NEGATIVE CONTROL, and that is the
// point of its shape.
//
// 🔴 A SCAN TEST THAT ONLY ASSERTS "THE OLD ROW WAS FOUND" IS PASSED BY A QUERY WITH NO
// HORIZON AT ALL. Both halves are needed: the fresh row must be INVISIBLE first, and then
// the SAME row must become visible once it is aged. Without the first half, a
// StrandedSentCommands that ignored olderThan entirely would sail through — and that is
// exactly the mutation that would put this pass to work on commands the platform is still
// actively retrying.
func TestScanFindsOnlyRowsPastTheHorizon(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	id := seedWithStatus(t, api, ctx, "in-flight", CommandQueued)
	if _, claimed, err := api.MarkSent(ctx, id); err != nil || !claimed {
		t.Fatalf("MarkSent: claimed=%v err=%v", claimed, err)
	}

	horizon := time.Now().Add(-5 * time.Minute)

	// Negative control: freshly dispatched, so the platform could still be working on it.
	found, _, err := api.StrandedSentCommands(ctx, StrandedCursor{}, horizon, 10)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("a command sent moments ago was reported as stranded (%v); the horizon is not "+
			"being applied, so this pass would race the redelivery it exists to wait for", tokensOf(found))
	}

	// The same row, aged past the horizon.
	backdateSentTime(t, api, ctx, id, 10*time.Minute)
	found, _, err = api.StrandedSentCommands(ctx, StrandedCursor{}, horizon, 10)
	if err != nil {
		t.Fatalf("scan after backdating: %v", err)
	}
	if len(found) != 1 || found[0].Token != "in-flight" {
		t.Fatalf("aged command not found: got %v, want [in-flight]", tokensOf(found))
	}
}

// TestScanIgnoresRowsThatWereNeverDispatched pins the NULL case, and it exists because the
// refactor that breaks it is a plausible one.
//
// A reader who notices sent_time is nullable may "correct" the predicate to
// `sent_time IS NULL OR sent_time < ?`, reasoning that a NULL is certainly older than the
// horizon. It is not: a NULL sent_time means the command was NEVER DISPATCHED. Sweeping
// those in would have the reconciler park commands still waiting to be sent for the first
// time, which is both wrong and silent.
func TestScanIgnoresRowsThatWereNeverDispatched(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	// A row forced to SENT without ever going through MarkSent, so it has no sent_time.
	// Contrived on purpose: it isolates the NULL from every other property.
	id := seedWithStatus(t, api, ctx, "never-sent", CommandSent)
	matches, err := api.CommandsById(ctx, []uint{id})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if matches[0].SentTime.Valid {
		t.Fatal("fixture is wrong: this row was supposed to have a NULL sent_time, so the " +
			"case under test is not the case being exercised")
	}

	found, _, err := api.StrandedSentCommands(ctx, StrandedCursor{}, time.Now(), 10)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("a command that was never dispatched was reported as stranded (%v); parking it "+
			"would re-arm a command the device was never sent", tokensOf(found))
	}
}

// TestScanIgnoresRowsThatAreNotSent is the other half of the predicate: PARKED, QUEUED and
// terminal rows are none of this pass's business even when they are old.
func TestScanIgnoresRowsThatAreNotSent(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	for _, status := range []CommandStatus{CommandQueued, CommandHeld, CommandParked, CommandTimeout} {
		id := seedWithStatus(t, api, ctx, "cmd-"+status.String(), CommandQueued)
		if _, claimed, err := api.MarkSent(ctx, id); err != nil || !claimed {
			t.Fatalf("MarkSent: claimed=%v err=%v", claimed, err)
		}
		// Age it first, so the only reason it can be excluded is its status.
		backdateSentTime(t, api, ctx, id, time.Hour)
		if err := forceStatus(api, ctx, id, status); err != nil {
			t.Fatalf("forcing %s: %v", status, err)
		}
	}

	found, _, err := api.StrandedSentCommands(ctx, StrandedCursor{}, time.Now(), 10)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("the scan returned rows that are not SENT (%v)", tokensOf(found))
	}
}

// TestScanCursorAdvancesPastRowsItCannotMove is the test for the failure the cursor exists
// to prevent, and it is the one that would be missed by testing paging generically.
//
// 🔴 THE RECONCILER DECLINES MOST OF WHAT IT READS, AND A DECLINED ROW STAYS EXACTLY AS
// ELIGIBLE AS IT WAS. So the walk cannot rely on rows leaving the result set to make
// progress the way a drain does. If the cursor did not advance past rows nothing moved,
// an MQTT-heavy instance would re-read its first page forever and never reach the LwM2M
// rows behind it: the pass would run on schedule, report work, and be a permanent no-op.
//
// It also pins the compound cursor. Every row here shares one sent_time — which is what a
// dispatch sweep marking a batch sent inside one tick actually produces — so a cursor
// carrying only the timestamp would either loop on the tie or skip the rest of it.
func TestScanCursorAdvancesPastRowsItCannotMove(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	shared := time.Now().Add(-time.Hour)
	ids := make([]uint, 0, 4)
	for _, token := range []string{"a", "b", "c", "d"} {
		id := seedWithStatus(t, api, ctx, token, CommandQueued)
		if _, claimed, err := api.MarkSent(ctx, id); err != nil || !claimed {
			t.Fatalf("MarkSent %s: claimed=%v err=%v", token, claimed, err)
		}
		// Identical sent_time for all four: the tie the compound cursor has to break.
		if res := api.RDB.DB(ctx).Model(&Command{}).Where("id = ?", id).
			Update("sent_time", shared); res.Error != nil {
			t.Fatalf("stamping %s: %v", token, res.Error)
		}
		ids = append(ids, id)
	}

	horizon := time.Now()
	seen := make([]string, 0, 4)
	cursor := StrandedCursor{}
	for pass := 0; pass < 2; pass++ {
		// Nothing is moved between pages, so every row stays eligible — the condition
		// under test.
		page, next, err := api.StrandedSentCommands(ctx, cursor, horizon, 2)
		if err != nil {
			t.Fatalf("page %d: %v", pass, err)
		}
		if len(page) != 2 {
			t.Fatalf("page %d returned %d rows (%v), want 2", pass, len(page), tokensOf(page))
		}
		seen = append(seen, tokensOf(page)...)
		cursor = next
	}

	if seen[0] == seen[2] || seen[1] == seen[3] {
		t.Fatalf("the walk re-read rows it had already returned (%v); with nothing moving them "+
			"out of the set, this pass would never reach the rows behind them", seen)
	}
	want := []string{"a", "b", "c", "d"}
	for i, token := range want {
		if seen[i] != token {
			t.Fatalf("walk order = %v, want %v (ties on sent_time must break by id)", seen, want)
		}
	}
	_ = ids
}

// TestScanOrdersOnSentTimeThenId pins the tie-break, and it asserts on the STATEMENT
// rather than on the rows — deliberately, and with a reason worth stating plainly.
//
// 🔴🔴 THIS PROPERTY IS NOT OBSERVABLE FROM BEHAVIOUR ON THE TEST HARNESS, AND A
// BEHAVIOURAL TEST HERE WOULD BE A FAKE PASS. A mutation harness is what established it:
// deleting `, id ASC` from the ORDER BY leaves every row-level test in this file green.
// The reason is that the tests run on SQLite, where `id` IS the rowid, so a table scan
// already returns rows sharing a sent_time in id order — the tie-break has nothing left to
// do, and its absence changes no result that can be seen from here.
//
// It changes results on POSTGRES, which is where this runs. Rows sharing a sent_time come
// back in whatever order the plan produces, and the cursor records the LAST row of a page;
// so without a total order, a tied row with a lower id than that last row is never
// returned again — silently skipped for the lifetime of the instance. A dispatch sweep
// marking a batch sent inside one tick is exactly how a large tie gets created, so this is
// the ordinary case rather than a corner.
//
// ⚠️ So this test pins the clause, which is the strongest guard available here, and NOT
// the behaviour, which no unit test on this harness can reach. Saying so is the point: a
// reader who assumes the row-level tests cover ordering would delete this as redundant.
func TestScanOrdersOnSentTimeThenId(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	id := seedWithStatus(t, api, ctx, "one", CommandQueued)
	if _, claimed, err := api.MarkSent(ctx, id); err != nil || !claimed {
		t.Fatalf("MarkSent: claimed=%v err=%v", claimed, err)
	}
	backdateSentTime(t, api, ctx, id, time.Hour)

	// A session logger captures what the real call sends, so this cannot drift from the
	// implementation the way a hand-rebuilt query would.
	var statements []string
	rec := api.RDB.DB(ctx).Session(&gorm.Session{Logger: sqlRecorder{captured: &statements}})
	recorded := &Api{RDB: &rdb.RdbManager{Database: rec}}
	if _, _, err := recorded.StrandedSentCommands(ctx, StrandedCursor{}, time.Now(), 10); err != nil {
		t.Fatalf("StrandedSentCommands failed: %v", err)
	}
	if len(statements) != 1 {
		t.Fatalf("the scan issued %d statements, want exactly 1: %v", len(statements), statements)
	}

	got := statements[0]
	upper := strings.ToUpper(got)
	if n := strings.Count(upper, "ORDER BY"); n != 1 {
		t.Fatalf("the scan has %d ORDER BY clauses, want 1: %s", n, got)
	}
	orderBy := got[strings.Index(upper, "ORDER BY"):]
	if !strings.Contains(orderBy, "sent_time") {
		t.Fatalf("the scan's ORDER BY does not name sent_time: %q", orderBy)
	}
	if !strings.Contains(orderBy, "id") {
		t.Fatalf("the scan's ORDER BY does not name id: %q. Without a tie-break, rows sharing a "+
			"sent_time have no total order, and the cursor silently skips every tied row that "+
			"sorts after the one it recorded", orderBy)
	}
	if strings.Index(orderBy, "sent_time") > strings.Index(orderBy, "id") {
		t.Fatalf("the scan orders by id before sent_time (%q); the horizon is the range "+
			"predicate, so sent_time must lead or the index cannot bound the scan", orderBy)
	}
}

// TestScanRestartsWhenTheWalkRunsOut pins the short-page rule. Answering a non-zero cursor
// at the end would park the walk past the last row, and every later pass would read
// nothing at all — a reconciler that silently stops reconciling.
func TestScanRestartsWhenTheWalkRunsOut(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	id := seedWithStatus(t, api, ctx, "only", CommandQueued)
	if _, claimed, err := api.MarkSent(ctx, id); err != nil || !claimed {
		t.Fatalf("MarkSent: claimed=%v err=%v", claimed, err)
	}
	backdateSentTime(t, api, ctx, id, time.Hour)

	page, next, err := api.StrandedSentCommands(ctx, StrandedCursor{}, time.Now(), 10)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(page) != 1 {
		t.Fatalf("got %d rows, want 1", len(page))
	}
	if !next.AtStart() {
		t.Fatalf("a short page answered cursor %+v; it must answer the zero cursor so the next "+
			"pass restarts the walk instead of reading past the end forever", next)
	}
}
