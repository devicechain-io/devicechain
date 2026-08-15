// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// The three creation timestamps both ordering fixtures below are built from. Two rows
// share notifyTied on purpose — see the seed helpers.
var (
	notifyOldest = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	notifyTied   = time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	notifyNewest = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
)

// orderingFixture is the shared shape of both seeds: four rows written in an order that
// is NOT the order they must be read back in.
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
// `token ASC` has to invert their insertion order to be observed at all.
var orderingFixture = []struct {
	token     string
	createdAt time.Time
}{
	{"charlie", notifyTied},
	{"delta", notifyNewest},
	{"alpha", notifyOldest},
	{"bravo", notifyTied},
}

// wantNotifyOrder is what both reads must return for the fixture above.
const wantNotifyOrder = "delta,bravo,charlie,alpha"

// stampCreatedAt writes an exact created_at onto one already-created row. gorm stamps
// now() on create, and four creates inside one test do not reliably share a tick (or
// reliably differ), so the tie has to be asserted into existence rather than hoped for.
// UpdateColumn is used so the write does not disturb updated_at or fire hooks.
func stampCreatedAt(t *testing.T, api *Api, ctx context.Context, model any, token string, at time.Time) {
	t.Helper()
	result := api.RDB.DB(ctx).Model(model).Where("token = ?", token).UpdateColumn("created_at", at)
	if result.Error != nil {
		t.Fatalf("stamping created_at on %q: %v", token, result.Error)
	}
	if result.RowsAffected != 1 {
		t.Fatalf("stamping created_at on %q touched %d rows, want 1", token, result.RowsAffected)
	}
}

func seedChannelsForOrdering(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api := newTestApi(t)
	ctx := tenantCtx("acme")

	for _, row := range orderingFixture {
		if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
			Token:       row.token,
			Name:        strPtr(row.token),
			ChannelType: ChannelTypeSMTP,
			Config:      strPtr(`{"host":"smtp.example.com","port":587}`),
			Enabled:     true,
		}); err != nil {
			t.Fatalf("seeding channel %s: %v", row.token, err)
		}
		stampCreatedAt(t, api, ctx, &NotificationChannel{}, row.token, row.createdAt)
	}
	return api, ctx
}

func seedPoliciesForOrdering(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api := newTestApi(t)
	ctx := tenantCtx("acme")

	for _, row := range orderingFixture {
		if _, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
			Token:   row.token,
			Name:    strPtr(row.token),
			Enabled: true,
		}); err != nil {
			t.Fatalf("seeding policy %s: %v", row.token, err)
		}
		stampCreatedAt(t, api, ctx, &NotificationPolicy{}, row.token, row.createdAt)
	}
	return api, ctx
}

func channelTokens(rows []NotificationChannel) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Token)
	}
	return strings.Join(out, ",")
}

func policyTokens(rows []NotificationPolicy) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Token)
	}
	return strings.Join(out, ",")
}

// The channel list reads newest-first, and two channels created in the same tick come
// back in token order rather than in the order they were written.
func TestNotificationChannelsReadNewestFirstWithTokenTiebreak(t *testing.T) {
	api, ctx := seedChannelsForOrdering(t)

	results, err := api.NotificationChannels(ctx, NotificationChannelSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
	})
	if err != nil {
		t.Fatalf("NotificationChannels: %v", err)
	}
	if got := channelTokens(results.Results); got != wantNotifyOrder {
		t.Fatalf("channel order = %s, want %s (insertion order was charlie,delta,alpha,bravo)", got, wantNotifyOrder)
	}
}

// Walking the channel list two rows at a time yields every channel exactly once — the
// repeat-and-skip property a missing ORDER BY actually breaks, which a single-page order
// assertion does not imply.
func TestNotificationChannelsPageAcrossBoundaryWithoutDuplicatesOrGaps(t *testing.T) {
	api, ctx := seedChannelsForOrdering(t)

	seen := make([]string, 0, 4)
	for page := int32(1); page <= 2; page++ {
		results, err := api.NotificationChannels(ctx, NotificationChannelSearchCriteria{
			Pagination: rdb.Pagination{PageNumber: page, PageSize: 2},
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(results.Results) != 2 {
			t.Fatalf("page %d returned %d rows, want 2", page, len(results.Results))
		}
		seen = append(seen, strings.Split(channelTokens(results.Results), ",")...)
	}
	if got := strings.Join(seen, ","); got != wantNotifyOrder {
		t.Fatalf("paged walk = %s, want %s (each channel exactly once)", got, wantNotifyOrder)
	}
}

// The policy list reads newest-first, and two policies created in the same tick come
// back in token order rather than in the order they were written.
//
// The search closure preloads Rules and Rules.Channel, and NotificationChannel carries a
// token column of its own — so this read is also the reason the clause is table-qualified
// rather than a bare `token ASC`.
func TestNotificationPoliciesReadNewestFirstWithTokenTiebreak(t *testing.T) {
	api, ctx := seedPoliciesForOrdering(t)

	results, err := api.NotificationPolicies(ctx, NotificationPolicySearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
	})
	if err != nil {
		t.Fatalf("NotificationPolicies: %v", err)
	}
	if got := policyTokens(results.Results); got != wantNotifyOrder {
		t.Fatalf("policy order = %s, want %s (insertion order was charlie,delta,alpha,bravo)", got, wantNotifyOrder)
	}
}

// Walking the policy list two rows at a time yields every policy exactly once.
func TestNotificationPoliciesPageAcrossBoundaryWithoutDuplicatesOrGaps(t *testing.T) {
	api, ctx := seedPoliciesForOrdering(t)

	seen := make([]string, 0, 4)
	for page := int32(1); page <= 2; page++ {
		results, err := api.NotificationPolicies(ctx, NotificationPolicySearchCriteria{
			Pagination: rdb.Pagination{PageNumber: page, PageSize: 2},
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(results.Results) != 2 {
			t.Fatalf("page %d returned %d rows, want 2", page, len(results.Results))
		}
		seen = append(seen, strings.Split(policyTokens(results.Results), ",")...)
	}
	if got := strings.Join(seen, ","); got != wantNotifyOrder {
		t.Fatalf("paged walk = %s, want %s (each policy exactly once)", got, wantNotifyOrder)
	}
}
