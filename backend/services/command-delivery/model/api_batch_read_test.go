// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// pagedCriteria builds search criteria over the first page, large enough that paging
// never truncates a fixture.
func pagedCriteria() CommandBatchSearchCriteria {
	return CommandBatchSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100}}
}

// commandTokensOf names the rows a failing filter returned. The identity of the extra
// rows is the diagnosis, and %+v on a Command dumps a screen of timestamps per row.
func commandTokensOf(commands []Command) []string {
	tokens := make([]string, 0, len(commands))
	for _, cmd := range commands {
		tokens = append(tokens, cmd.Token)
	}
	return tokens
}

// TestBatchTokenFilterNarrowsToOneBatchesCommands is the test for the reader the
// BatchToken column was carried for.
//
// 🔴 THE ASSERTION IS ON THE ROWS RETURNED, NOT ON THE CRITERIA STRUCT, because the
// failure mode is a filter that binds to nothing and therefore filters nothing — which
// returns MORE rows, not an error. A test that checked the criteria was populated would
// pass against a criteria builder that never used it.
func TestBatchTokenFilterNarrowsToOneBatchesCommands(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	if _, err := api.CreateCommandBatch(ctx, batchRequest("batch-one", []string{"pump-a", "pump-b"})); err != nil {
		t.Fatalf("create batch-one: %v", err)
	}
	if _, err := api.CreateCommandBatch(ctx, batchRequest("batch-two", []string{"pump-c"})); err != nil {
		t.Fatalf("create batch-two: %v", err)
	}
	// A command issued one at a time, which carries no batch token at all. It is the
	// control: a filter that binds to nothing would sweep this in alongside the rest.
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "solo", DeviceToken: "pump-z", Name: "reboot",
	}); err != nil {
		t.Fatalf("create solo command: %v", err)
	}

	found, err := api.Commands(ctx, CommandSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
		BatchToken: strPtr("batch-one"),
	})
	if err != nil {
		t.Fatalf("search by batch token: %v", err)
	}
	if len(found.Results) != 2 {
		t.Fatalf("batch-one queued 2 commands, the filter returned %d (%v)",
			len(found.Results), commandTokensOf(found.Results))
	}
	for _, cmd := range found.Results {
		if !cmd.BatchToken.Valid || cmd.BatchToken.String != "batch-one" {
			t.Fatalf("a command from another batch (or none) leaked through the filter: %+v", cmd)
		}
	}

	// The whole point of the filter is that it is COMBINABLE with status — "of this
	// batch, what has actually gone out?" is the question the stored counts cannot
	// answer, because they are frozen at creation time.
	sent := CommandSent.String()
	if err := api.RDB.DB(ctx).Model(&Command{}).
		Where("token = ?", found.Results[0].Token).Update("status", sent).Error; err != nil {
		t.Fatalf("force one command to SENT: %v", err)
	}
	narrowed, err := api.Commands(ctx, CommandSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
		BatchToken: strPtr("batch-one"),
		Status:     &sent,
	})
	if err != nil {
		t.Fatalf("search by batch token and status: %v", err)
	}
	if len(narrowed.Results) != 1 {
		t.Fatalf("expected 1 SENT command in batch-one, got %d", len(narrowed.Results))
	}
}

// TestBatchSearchFiltersDoNotLeakAcrossBatches pins each criterion against a fixture
// that would be returned if the criterion bound to nothing.
func TestBatchSearchFiltersDoNotLeakAcrossBatches(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	reboot := batchRequest("b-reboot", []string{"pump-a"})
	if _, err := api.CreateCommandBatch(ctx, reboot); err != nil {
		t.Fatalf("create reboot batch: %v", err)
	}
	flush := &CommandBatchCreateRequest{Token: "b-flush", Name: "flush",
		DeviceTokens: &[]string{"pump-b"}}
	if _, err := api.CreateCommandBatch(ctx, flush); err != nil {
		t.Fatalf("create flush batch: %v", err)
	}

	all, err := api.CommandBatches(ctx, pagedCriteria())
	if err != nil {
		t.Fatalf("unfiltered search: %v", err)
	}
	if len(all.Results) != 2 {
		t.Fatalf("the fixture has 2 batches, the unfiltered search found %d", len(all.Results))
	}

	byName := pagedCriteria()
	byName.Name = strPtr("flush")
	narrowed, err := api.CommandBatches(ctx, byName)
	if err != nil {
		t.Fatalf("search by name: %v", err)
	}
	if len(narrowed.Results) != 1 || narrowed.Results[0].Token != "b-flush" {
		t.Fatalf("the name filter did not narrow to the flush batch, got %+v", narrowed.Results)
	}

	byKind := pagedCriteria()
	byKind.TargetKind = strPtr(string(BatchTargetGroup))
	none, err := api.CommandBatches(ctx, byKind)
	if err != nil {
		t.Fatalf("search by target kind: %v", err)
	}
	if len(none.Results) != 0 {
		t.Fatalf("both fixtures are device-list batches, so a GROUP filter must match "+
			"nothing; got %d", len(none.Results))
	}
}

// TestBatchesByTokenIsTenantScoped guards the property every read in this service
// depends on and no filter of its own can provide.
func TestBatchesByTokenIsTenantScoped(t *testing.T) {
	api := newBatchTestApi(t)
	tenantA := core.WithTenant(context.Background(), "A")
	tenantB := core.WithTenant(context.Background(), "B")

	if _, err := api.CreateCommandBatch(tenantA, batchRequest("shared-token", []string{"pump-a"})); err != nil {
		t.Fatalf("create in tenant A: %v", err)
	}

	found, err := api.CommandBatchesByToken(tenantB, []string{"shared-token"})
	if err != nil {
		t.Fatalf("read from tenant B: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("tenant B read tenant A's batch: %+v", found)
	}

	own, err := api.CommandBatchesByToken(tenantA, []string{"shared-token"})
	if err != nil {
		t.Fatalf("read from tenant A: %v", err)
	}
	if len(own) != 1 {
		t.Fatalf("tenant A must see its own batch, got %d — if this fails the test above "+
			"proves nothing, because a read returning nothing for EVERYONE would pass it", len(own))
	}
}
