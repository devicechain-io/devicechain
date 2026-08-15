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
	"github.com/stretchr/testify/require"
)

// The three creation timestamps the ordering fixture is built from. Two rows share
// orderTied on purpose — see seedConnectorsForOrdering.
var (
	orderOldest = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	orderTied   = time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	orderNewest = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
)

// seedConnectorsForOrdering writes four connectors ANTI-SORTED and returns the api +
// tenant context.
//
// 🔴 THE INSERTION ORDER IS DELIBERATELY NOT THE EXPECTED OUTPUT ORDER. SQLite hands
// rows back in insertion order when a query names no ORDER BY, so a fixture seeded in
// its expected order passes whether or not the read orders anything at all — which is
// exactly how unordered list endpoints reach production with a green suite. Rows go in
// as charlie, delta, alpha, bravo; the declared order reads them back as delta, bravo,
// charlie, alpha.
//
// charlie and bravo carry the SAME created_at, which is the only thing that exercises
// the tiebreak half of the clause: created_at DESC alone leaves that pair free to
// reshuffle between calls, and it is inserted charlie-then-bravo so `token ASC` has to
// invert the insertion order to be observed at all.
//
// created_at is stamped explicitly with UpdateColumn rather than left to the clock:
// gorm writes now() on Create, and four creates inside one test do not reliably share
// a tick (or reliably differ), so the tie has to be asserted into existence.
//
// 🔑 A NOTE ON HOW THIS FIXTURE WAS PROVEN TO FAIL, because the obvious negative control
// does NOT work here. Deleting `connectors.token ASC` outright leaves these assertions
// GREEN, and the query plan says why: newTestApi creates the per-tenant partial unique
// index the real migration creates, the read's tenant predicate matches it, and SQLite
// drives the scan from it —
//
//	SEARCH connectors USING INDEX uix_connectors_tenant_token (tenant_id=?)
//	USE TEMP B-TREE FOR ORDER BY
//
// so rows reach the sort already in token order and the tie group keeps it. Token order
// is then reproduced by ACCIDENT, from an index, on a clause that no longer asks for it.
//
// The control that does bite is REVERSING the tiebreak (`token ASC` → `token DESC`),
// which flips this pair and fails both tests. Reverse the clause, do not remove it, when
// re-proving this file.
//
// 🔴 And do not generalize either way: the near-identical ai-inference fixture DOES fail
// on a plain deletion, because its provider table is instance-scoped, carries no tenant
// predicate to match an index on, and so falls back to a rowid scan. Which control is
// discriminating is a property of the table's indexes, not of the fixture shape.
func seedConnectorsForOrdering(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	for _, row := range []struct {
		token     string
		createdAt time.Time
	}{
		{"charlie", orderTied},
		{"delta", orderNewest},
		{"alpha", orderOldest},
		{"bravo", orderTied},
	} {
		_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
			Token:  row.token,
			Name:   strp(row.token),
			Type:   string(ConnectorTypeMQTT),
			Config: mqttConfig,
		})
		require.NoError(t, err, "seeding %s", row.token)

		result := api.RDB.DB(ctx).Model(&Connector{}).Where("token = ?", row.token).
			UpdateColumn("created_at", row.createdAt)
		require.NoError(t, result.Error, "stamping created_at on %s", row.token)
		require.EqualValues(t, 1, result.RowsAffected, "stamping created_at on %s", row.token)
	}
	return api, ctx
}

// connectorTokens renders a page as a comma-joined token sequence so a mismatch reports
// the whole ordering rather than the first differing element.
func connectorTokens(rows []Connector) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Token)
	}
	return strings.Join(out, ",")
}

// The connector list reads newest-first, and the two connectors registered in the same
// tick come back in token order rather than in the order they were written.
func TestConnectorsReadNewestFirstWithTokenTiebreak(t *testing.T) {
	api, ctx := seedConnectorsForOrdering(t)

	results, err := api.Connectors(ctx, ConnectorSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
	})
	require.NoError(t, err)

	const want = "delta,bravo,charlie,alpha"
	if got := connectorTokens(results.Results); got != want {
		t.Fatalf("connector order = %s, want %s (insertion order was charlie,delta,alpha,bravo)", got, want)
	}
}

// Walking the list two rows at a time yields every connector exactly once. This is the
// property a missing ORDER BY actually breaks — not "the order looks odd" but "a row is
// handed out on two pages and another is never handed out at all" — and a single-page
// assertion does not imply it.
func TestConnectorsPageAcrossBoundaryWithoutDuplicatesOrGaps(t *testing.T) {
	api, ctx := seedConnectorsForOrdering(t)

	seen := make([]string, 0, 4)
	for page := int32(1); page <= 2; page++ {
		results, err := api.Connectors(ctx, ConnectorSearchCriteria{
			Pagination: rdb.Pagination{PageNumber: page, PageSize: 2},
		})
		require.NoError(t, err, "page %d", page)
		require.Len(t, results.Results, 2, "page %d", page)
		seen = append(seen, strings.Split(connectorTokens(results.Results), ",")...)
	}

	const want = "delta,bravo,charlie,alpha"
	if got := strings.Join(seen, ","); got != want {
		t.Fatalf("paged walk = %s, want %s (each connector exactly once)", got, want)
	}
}
