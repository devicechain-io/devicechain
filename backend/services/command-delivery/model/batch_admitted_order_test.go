// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// multiChunkDevices is deliberately larger than batchInsertChunk so the insert splits
// into MORE THAN ONE STATEMENT. Everything the multi-chunk test measures lives at that
// seam: a single-chunk batch cannot show it, which is why every other batch test in this
// package — all of which use a handful of devices — is silent on the question.
//
// 2,500 gives three chunks of uneven size (1000/1000/500), so a defect shows up as
// BLOCKS rather than as a clean reversal that a symmetric fixture could confuse with a
// plain DESC. It sits under the non-service caller's effective limit of 8,000
// (RestrictedCommandLimit over the 10,000 platform ceiling), so nothing is refused.
const multiChunkDevices = 2500

// singleChunkDevices fits inside one INSERT. It is the CONTROL: it exercises the same
// creation path, the same paged read and the same comparison, differing only in whether
// the insert was split. A failure there would mean the harness itself is wrong — the
// paging walk, the token comparison, the tenant fence — rather than anything about the
// chunk seam.
const singleChunkDevices = 500

// TestBatchCommandsReadInAdmittedOrder measures the order `commands(batchToken: …)`
// returns a MULTI-CHUNK batch's rows in, against the order the batch admitted them.
//
// 🔴 THE ADMITTED ORDER IS A PROMISE, not an incidental. A partially-admitted batch
// admits in the order the caller gave, so position IS the caller's stated priority —
// which makes "which devices got it, and which were left out" a question answered by
// reading the rows back in that order. The token carries it: batchCommandToken
// zero-pads the admitted index, so token ASC and admitted order are the same sequence.
//
// The read goes through Api.Commands (hence rdb.ListOf, hence Command.DefaultOrder) and
// pages at MaxPageSize, because that is what a console list does. Reading it unbounded
// would measure a query no external caller can issue.
//
// 🔑 IT ASSERTS THE CONTRACT, NOT A MECHANISM. Several different fixes could make a
// batch read back in admitted order — one created_at for the whole batch, a dedicated
// token-ASC read, a different default order — and a test pinned to any one of them
// would fail the other two while the contract held.
func TestBatchCommandsReadInAdmittedOrder(t *testing.T) {
	if multiChunkDevices <= batchInsertChunk {
		t.Fatalf("fixture of %d rows fits in one INSERT of %d, so it cannot observe "+
			"anything about how the chunks order against each other",
			multiChunkDevices, batchInsertChunk)
	}
	assertBatchReadsInAdmittedOrder(t, "batch-order", multiChunkDevices)
}

// TestSingleChunkBatchReadsInAdmittedOrder is the no-op control described on
// singleChunkDevices: the identical measurement on a batch small enough to be inserted
// by ONE statement.
func TestSingleChunkBatchReadsInAdmittedOrder(t *testing.T) {
	if singleChunkDevices > batchInsertChunk {
		t.Fatalf("control fixture of %d rows exceeds the %d-row INSERT chunk, so it is "+
			"no longer a single-chunk control", singleChunkDevices, batchInsertChunk)
	}
	assertBatchReadsInAdmittedOrder(t, "batch-order-control", singleChunkDevices)
}

func assertBatchReadsInAdmittedOrder(t *testing.T, batchToken string, devices int) {
	t.Helper()
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	tokens := deviceTokens(devices)
	batch, err := api.CreateCommandBatch(ctx, batchRequest(batchToken, tokens))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	if batch.Accepted != devices {
		t.Fatalf("fixture admitted %d of %d devices; the ceiling refused some, so this "+
			"is not measuring a full batch", batch.Accepted, devices)
	}

	// ── Instrument 1: what did the insert actually stamp? ─────────────────────────
	//
	// The leading sort key is created_at. If the whole batch shares ONE value the key
	// is constant and token ASC — the declared totality rule — carries the order on its
	// own. If it varies, it varies by INSERT statement, and the spread reported here is
	// what says whether the two chunks were far enough apart for a stored timestamp to
	// tell them apart at all. A run where they tied would pass this test by accident of
	// speed while production, whose chunks are milliseconds apart, did not.
	groups := createdAtGroups(t, api.RDB.Database, batch.Token)
	t.Logf("created_at for a %d-row batch (chunk size %d): %s",
		devices, batchInsertChunk, renderGroups(groups))
	if len(groups) == 1 {
		t.Logf("the whole batch shares one created_at, so the leading sort key is " +
			"constant and token ASC carries the order")
	} else {
		t.Logf("created_at varies across the batch in %d groups, spanning %s — the "+
			"leading sort key therefore ORDERS the chunks against each other",
			len(groups), groups[len(groups)-1].At.Sub(groups[0].At))
	}

	// ── Instrument 2: which access path served the read? ──────────────────────────
	//
	// SQLite hands back rowid order for a table scan, and rowid order here happens to
	// EQUAL admitted order — so a scan would produce the right answer for a reason the
	// ORDER BY had no part in. The plan is logged so a pass can be read against it.
	sql, vars := captureListSQL(t, api, ctx, batch.Token)
	t.Logf("read SQL: %s", sql)
	t.Logf("EXPLAIN QUERY PLAN: %s", explainQueryPlan(t, api.RDB.Database, sql, vars))

	// ── The measurement ───────────────────────────────────────────────────────────
	got := readAllPages(t, api, ctx, batch.Token)
	if len(got) != devices {
		t.Fatalf("paged read returned %d rows, want %d", len(got), devices)
	}
	want := append([]string(nil), got...)
	sort.Strings(want)

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("a batch's commands do not read back in admitted order.\n"+
				"first divergence at position %d: got %q, want %q\n"+
				"leading tokens: %v\ncreated_at: %s",
				i, got[i], want[i], got[:min(12, len(got))], renderGroups(groups))
		}
	}
}

// createdAtGroup is one distinct created_at a batch's rows carry, with the admitted-index
// range it covers. The RANGE is what names the mechanism: one value per contiguous block
// of batchInsertChunk rows is a stamp taken once per INSERT statement, and nothing else
// produces that shape.
type createdAtGroup struct {
	At       time.Time
	Rows     int
	MinToken string
	MaxToken string
}

func createdAtGroups(t *testing.T, db *gorm.DB, batchToken string) []createdAtGroup {
	t.Helper()
	groups := make([]createdAtGroup, 0, 4)
	err := db.Raw(`SELECT created_at AS at, count(*) AS rows,
			min(token) AS min_token, max(token) AS max_token
		FROM commands WHERE batch_token = ? GROUP BY created_at ORDER BY created_at`,
		batchToken).Scan(&groups).Error
	if err != nil {
		t.Fatalf("read created_at groups: %v", err)
	}
	if len(groups) == 0 {
		t.Fatalf("batch %q wrote no command rows", batchToken)
	}
	return groups
}

func renderGroups(groups []createdAtGroup) string {
	rendered := make([]string, 0, len(groups))
	for _, g := range groups {
		rendered = append(rendered, fmt.Sprintf("%s ×%d [%s…%s]",
			g.At.Format(time.RFC3339Nano), g.Rows, g.MinToken, g.MaxToken))
	}
	return strings.Join(rendered, "  |  ")
}

// captureListSQL records the SELECT that Api.Commands issues for its DATA query, by
// hanging a callback off the same gorm instance the Api reads through.
//
// It captures rather than reconstructs on purpose: a hand-written equivalent would be a
// second copy of the criteria builder and of the default order, and what the REAL read
// does is the entire question.
func captureListSQL(t *testing.T, api *Api, ctx context.Context, batchToken string) (string, []any) {
	t.Helper()
	var sql string
	var vars []any
	const name = "test:capture_list_sql"
	err := api.RDB.Database.Callback().Query().After("gorm:query").
		Register(name, func(tx *gorm.DB) {
			// ListOf issues a COUNT before the data query; only the ordered one is the
			// read under measurement.
			if strings.Contains(tx.Statement.SQL.String(), "ORDER BY") {
				sql = tx.Statement.SQL.String()
				vars = tx.Statement.Vars
			}
		})
	if err != nil {
		t.Fatalf("register capture callback: %v", err)
	}
	defer func() {
		if err := api.RDB.Database.Callback().Query().Remove(name); err != nil {
			t.Fatalf("remove capture callback: %v", err)
		}
	}()

	if _, err := api.Commands(ctx, batchPageCriteria(batchToken, 1)); err != nil {
		t.Fatalf("search commands: %v", err)
	}
	if sql == "" {
		t.Fatal("captured no ordered SELECT; the read did not go through the query callbacks")
	}
	return sql, vars
}

// explainQueryPlan renders SQLite's chosen access path for the captured read.
func explainQueryPlan(t *testing.T, db *gorm.DB, sql string, vars []any) string {
	t.Helper()
	rows := make([]struct{ Detail string }, 0, 4)
	if err := db.Raw("EXPLAIN QUERY PLAN "+sql, vars...).Scan(&rows).Error; err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	details := make([]string, 0, len(rows))
	for _, row := range rows {
		details = append(details, row.Detail)
	}
	return strings.Join(details, " / ")
}

// batchPageCriteria is the read a batch-detail list issues: one batch, one page, at the
// largest page size the platform permits.
func batchPageCriteria(batchToken string, page int32) CommandSearchCriteria {
	return CommandSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: page, PageSize: rdb.MaxPageSize},
		BatchToken: &batchToken,
	}
}

// readAllPages walks every page of a batch's commands and returns the tokens in the
// order the pages delivered them.
func readAllPages(t *testing.T, api *Api, ctx context.Context, batchToken string) []string {
	t.Helper()
	seen := make([]string, 0, rdb.MaxPageSize)
	for page := int32(1); ; page++ {
		found, err := api.Commands(ctx, batchPageCriteria(batchToken, page))
		if err != nil {
			t.Fatalf("search commands page %d: %v", page, err)
		}
		if len(found.Results) == 0 {
			return seen
		}
		seen = append(seen, commandTokensOf(found.Results)...)
		if len(seen) >= int(found.Pagination.TotalRecords) {
			return seen
		}
	}
}
