// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

func attemptAt(channel, status string, at time.Time) *NotificationAttempt {
	return &NotificationAttempt{
		CreatedAt: at, AlarmToken: "a-" + channel, Trigger: AttemptTriggerAlarm,
		ChannelToken: channel, ChannelType: ChannelTypeSMTP, Status: status, Attempt: 1, MaxAttempts: 3,
	}
}

func mustRecordAttempt(t *testing.T, api *Api, ctx context.Context, a *NotificationAttempt) {
	t.Helper()
	if err := api.RecordNotificationAttempt(ctx, a); err != nil {
		t.Fatalf("record: %v", err)
	}
}

// Health counts failures SINCE the last success, ignores NOT_ATTEMPTED (the platform's
// fault, not the channel's), and keeps channels apart.
func TestChannelHealthCountsFailuresSinceTheLastSuccess(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

	mustRecordAttempt(t, api, ctx, attemptAt("c1", AttemptStatusFailed, t0))
	mustRecordAttempt(t, api, ctx, attemptAt("c1", AttemptStatusSent, t0.Add(time.Minute)))
	mustRecordAttempt(t, api, ctx, attemptAt("c1", AttemptStatusFailed, t0.Add(2*time.Minute)))
	mustRecordAttempt(t, api, ctx, attemptAt("c1", AttemptStatusNotAttempted, t0.Add(3*time.Minute)))
	mustRecordAttempt(t, api, ctx, attemptAt("c1", AttemptStatusRefused, t0.Add(4*time.Minute)))
	// An attempt the dispatch budget cut short is the platform's, not the channel's.
	cut := attemptAt("c1", AttemptStatusFailed, t0.Add(4*time.Minute+30*time.Second))
	cut.ErrorCode = sql.NullString{String: AttemptErrorBudgetSpent, Valid: true}
	mustRecordAttempt(t, api, ctx, cut)
	mustRecordAttempt(t, api, ctx, attemptAt("c2", AttemptStatusFailed, t0.Add(5*time.Minute)))

	h, err := api.ChannelHealthByToken(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if !h.LastSuccessAt.Time.Equal(t0.Add(time.Minute)) {
		t.Fatalf("lastSuccessAt = %v", h.LastSuccessAt)
	}
	if !h.LastFailureAt.Time.Equal(t0.Add(4 * time.Minute)) {
		t.Fatalf("lastFailureAt = %v", h.LastFailureAt)
	}
	if h.ConsecutiveFailures != 2 {
		t.Fatalf("consecutiveFailures = %d, want 2 (FAILED + REFUSED after the success, NOT_ATTEMPTED and BUDGET_SPENT excluded)",
			h.ConsecutiveFailures)
	}

	never, err := api.ChannelHealthByToken(ctx, "c2")
	if err != nil {
		t.Fatal(err)
	}
	if never.LastSuccessAt.Valid || never.ConsecutiveFailures != 1 {
		t.Fatalf("c2 = %+v, want never succeeded and 1 failure", never)
	}

	unseen, err := api.ChannelHealthByToken(ctx, "nope")
	if err != nil {
		t.Fatal(err)
	}
	if unseen.LastSuccessAt.Valid || unseen.LastFailureAt.Valid || unseen.ConsecutiveFailures != 0 {
		t.Fatalf("unseen = %+v, want empty", unseen)
	}
}

// The ledger is tenant-scoped on read, health and prune.
func TestTheLedgerIsTenantScoped(t *testing.T) {
	api := newTestApi(t)
	now := time.Now().UTC()
	mustRecordAttempt(t, api, tenantCtx("A"), attemptAt("c1", AttemptStatusFailed, now))
	mustRecordAttempt(t, api, tenantCtx("B"), attemptAt("c1", AttemptStatusSent, now))

	page, err := api.NotificationAttempts(tenantCtx("A"), NotificationAttemptSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 || page.Results[0].Status != AttemptStatusFailed {
		t.Fatalf("tenant A reads %+v, want only its own row", page.Results)
	}
	h, err := api.ChannelHealthByToken(tenantCtx("A"), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if h.LastSuccessAt.Valid {
		t.Fatalf("tenant A's health saw tenant B's success: %+v", h)
	}

	removed, err := api.PruneNotificationAttempts(tenantCtx("A"), now.Add(time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("prune A removed %d (err %v), want 1", removed, err)
	}
	tenants, err := api.DistinctAttemptTenants(core.WithSystemContext(context.Background()))
	if err != nil || len(tenants) != 1 || tenants[0] != "B" {
		t.Fatalf("tenants after pruning A = %v (err %v), want [B]", tenants, err)
	}
}

// Filters narrow the page; prune keeps rows inside the window.
func TestLedgerFiltersAndRetention(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	now := time.Now().UTC()
	old := attemptAt("c1", AttemptStatusFailed, now.Add(-40*24*time.Hour))
	mustRecordAttempt(t, api, ctx, old)
	mustRecordAttempt(t, api, ctx, attemptAt("c1", AttemptStatusSent, now))
	mustRecordAttempt(t, api, ctx, attemptAt("c2", AttemptStatusSent, now))

	status := AttemptStatusSent
	channel := "c1"
	page, err := api.NotificationAttempts(ctx, NotificationAttemptSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}, Status: &status, ChannelToken: &channel})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 || page.Pagination.TotalRecords != 1 {
		t.Fatalf("filtered page = %+v", page)
	}
	alarm := "a-c2"
	page, err = api.NotificationAttempts(ctx, NotificationAttemptSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}, AlarmToken: &alarm})
	if err != nil || len(page.Results) != 1 || page.Results[0].ChannelToken != "c2" {
		t.Fatalf("alarm filter = %+v (err %v)", page, err)
	}

	since := now.Add(-time.Hour).Format(time.RFC3339Nano)
	page, err = api.NotificationAttempts(ctx, NotificationAttemptSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}, Since: &since})
	if err != nil || len(page.Results) != 2 {
		t.Fatalf("since filter = %d rows (err %v), want the 2 recent ones", len(page.Results), err)
	}
	until := now.Add(-time.Hour).Format(time.RFC3339Nano)
	page, err = api.NotificationAttempts(ctx, NotificationAttemptSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}, Until: &until})
	if err != nil || len(page.Results) != 1 {
		t.Fatalf("until filter = %d rows (err %v), want the old one", len(page.Results), err)
	}
	bad := "yesterday"
	if _, err := api.NotificationAttempts(ctx, NotificationAttemptSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}, Since: &bad}); err == nil {
		t.Fatal("an unparseable bound must fail the request, not be ignored")
	}

	removed, err := api.PruneNotificationAttempts(ctx, now.Add(-30*24*time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("prune removed %d (err %v), want only the 40-day-old row", removed, err)
	}
	page, _ = api.NotificationAttempts(ctx, NotificationAttemptSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	if len(page.Results) != 2 {
		t.Fatalf("after prune %d rows, want 2", len(page.Results))
	}
}
