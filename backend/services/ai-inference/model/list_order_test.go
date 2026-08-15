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

// The three creation timestamps the ordering fixture is built from. Two providers share
// providerTied on purpose — see seedProvidersForOrdering.
var (
	providerOldest = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	providerTied   = time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	providerNewest = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
)

// seedProvidersForOrdering writes four providers ANTI-SORTED and returns the api + ctx.
//
// 🔴 THE INSERTION ORDER IS DELIBERATELY NOT THE EXPECTED OUTPUT ORDER. SQLite hands
// rows back in insertion order when a query names no ORDER BY, so a fixture seeded in
// its expected order passes whether or not the read orders anything at all — which is
// exactly how unordered list endpoints reach production with a green suite. Rows go in
// as charlie, delta, alpha, bravo; the declared order reads them back as delta, bravo,
// charlie, alpha.
//
// charlie and bravo carry the SAME created_at, which is the only thing that exercises
// the tiebreak half of the clause, and they are inserted charlie-then-bravo so
// `token ASC` has to invert their insertion order to be observed.
//
// The harness runs ai-inference's REAL migrations, so this also proves the clause's
// table qualifier ("ai_providers.") resolves against the table the service actually
// ships — the one model in the set whose TableName is not schema-prefixed.
//
// created_at is stamped explicitly with UpdateColumn rather than left to the clock:
// gorm writes now() on Create, and four creates inside one test do not reliably share a
// tick (or reliably differ), so the tie has to be asserted into existence.
//
// Both halves of the clause were watched to fail: deleting `ai_providers.token ASC`
// returns the tie group in insertion order (delta,charlie,bravo,alpha), and flipping the
// leading column to ASC returns alpha,bravo,charlie,delta.
//
// 🔴 Note this differs from the near-identical connector fixture, where deleting the
// tiebreak stays GREEN: connectors are tenant-scoped, so that read's tenant predicate
// matches a (tenant_id, token) index SQLite then drives the scan from, and token order
// reappears by accident. A provider is instance-scoped with no such predicate, so it
// falls back to a rowid scan and the deletion is observable. Which negative control is
// discriminating is a property of the TABLE'S INDEXES, not of the fixture shape — do not
// assume a control that bites here bites there.
func seedProvidersForOrdering(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api := newTestApi(t)
	ctx := context.Background()

	for _, row := range []struct {
		token     string
		createdAt time.Time
	}{
		{"charlie", providerTied},
		{"delta", providerNewest},
		{"alpha", providerOldest},
		{"bravo", providerTied},
	} {
		_, err := api.CreateAIProvider(ctx, claudeReq(row.token, nil))
		require.NoError(t, err, "seeding %s", row.token)

		// A provider is instance-scoped, so the stamp goes through the same system
		// context the write path uses.
		result := api.RDB.DB(core.WithSystemContext(ctx)).Model(&AIProvider{}).
			Where("token = ?", row.token).UpdateColumn("created_at", row.createdAt)
		require.NoError(t, result.Error, "stamping created_at on %s", row.token)
		require.EqualValues(t, 1, result.RowsAffected, "stamping created_at on %s", row.token)
	}
	return api, ctx
}

// providerTokens renders a page as a comma-joined token sequence so a mismatch reports
// the whole ordering rather than the first differing element.
func providerTokens(rows []AIProvider) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Token)
	}
	return strings.Join(out, ",")
}

// The operator's provider list reads newest-first, and two providers registered in the
// same tick come back in token order rather than in the order they were written.
func TestAIProvidersReadNewestFirstWithTokenTiebreak(t *testing.T) {
	api, ctx := seedProvidersForOrdering(t)

	results, err := api.AIProviders(ctx, AIProviderSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
	})
	require.NoError(t, err)

	const want = "delta,bravo,charlie,alpha"
	if got := providerTokens(results.Results); got != want {
		t.Fatalf("provider order = %s, want %s (insertion order was charlie,delta,alpha,bravo)", got, want)
	}
}

// Walking the list two rows at a time yields every provider exactly once — the
// repeat-and-skip property a missing ORDER BY actually breaks, which a single-page
// order assertion does not imply.
func TestAIProvidersPageAcrossBoundaryWithoutDuplicatesOrGaps(t *testing.T) {
	api, ctx := seedProvidersForOrdering(t)

	seen := make([]string, 0, 4)
	for page := int32(1); page <= 2; page++ {
		results, err := api.AIProviders(ctx, AIProviderSearchCriteria{
			Pagination: rdb.Pagination{PageNumber: page, PageSize: 2},
		})
		require.NoError(t, err, "page %d", page)
		require.Len(t, results.Results, 2, "page %d", page)
		seen = append(seen, strings.Split(providerTokens(results.Results), ",")...)
	}

	const want = "delta,bravo,charlie,alpha"
	if got := strings.Join(seen, ","); got != want {
		t.Fatalf("paged walk = %s, want %s (each provider exactly once)", got, want)
	}
}
