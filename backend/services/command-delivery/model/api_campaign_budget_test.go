// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Keep failed assertions visible instead of flooding gate output with full bulk INSERTs.
func init() { logger.Default = logger.Default.LogMode(logger.Silent) }

func newPlanState() *campaignPlanState {
	return &campaignPlanState{targets: map[string]*batchTargets{}, sites: map[string]map[string]Site{}}
}

func TestCampaignPlanningRowBudgetAndCompletedPrefix(t *testing.T) {
	api := newCampaignTestApi(t)
	resolver := &pagingResolver{members: []string{"a", "b"}}
	api.GroupTargetResolver = resolver
	c, err := api.CreateCampaign(tenantCtx(), cronRequest("hourly", "0 * * * *"))
	if err != nil {
		t.Fatal(err)
	}
	now := at("2026-06-21T12:00:00Z")
	state := newPlanState()
	state.rows = campaignPlanRows - 2
	if err := api.planOrRecord(tenantCtx(), c, now, state); err != nil {
		t.Fatal(err)
	}
	if !c.PlannedThrough.Valid || !c.PlannedThrough.Time.Equal(now.Add(time.Hour)) {
		t.Fatalf("cursor = %v, want only the completed first cycle", c.PlannedThrough)
	}
	if err := api.planOrRecord(tenantCtx(), c, now, state); !errors.Is(err, errCampaignPlanBudget) {
		t.Fatalf("second cycle: %v, want budget deferral", err)
	}
	var rows int64
	if err := api.RDB.DB(tenantCtx()).Model(&CampaignElement{}).Count(&rows).Error; err != nil || rows != 2 {
		t.Fatalf("rows = %d, err = %v; want exactly the first cycle", rows, err)
	}
	// Reload as a subsequent scheduler pass would, with a fresh allowance.
	if err := api.RDB.DB(tenantCtx()).First(c, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := api.planOrRecord(tenantCtx(), c, now, newPlanState()); err != nil {
		t.Fatal(err)
	}
	if err := api.RDB.DB(tenantCtx()).Model(&CampaignElement{}).Count(&rows).Error; err != nil || rows != 4 {
		t.Fatalf("resumed rows = %d, err = %v; want two distinct cycles", rows, err)
	}
}

func TestCampaignPlanningCycleBudgetResumesAndSharesResolution(t *testing.T) {
	api := newCampaignTestApi(t)
	r := &pagingResolver{members: []string{"a"}}
	api.GroupTargetResolver = r
	for i := 0; i < 4; i++ {
		if _, err := api.CreateCampaign(tenantCtx(), cronRequest(fmt.Sprintf("hourly-%d", i), "0 * * * *")); err != nil {
			t.Fatal(err)
		}
	}
	now := at("2026-06-21T12:00:30Z")
	tick(t, api, now, "plan")
	var n int64
	if err := api.RDB.DB(tenantCtx()).Model(&CampaignCycle{}).Count(&n).Error; err != nil || n != campaignPlanCycles {
		t.Fatalf("cycles = %d, err = %v, want %d", n, err, campaignPlanCycles)
	}
	if r.pages != 1 {
		t.Fatalf("group resolved %d times in one planning pass, want once", r.pages)
	}
	var campaigns []Campaign
	if err := api.RDB.DB(tenantCtx()).Find(&campaigns).Error; err != nil {
		t.Fatal(err)
	}
	for _, c := range campaigns {
		var count int64
		api.RDB.DB(tenantCtx()).Model(&CampaignCycle{}).Where("campaign_id = ?", c.ID).Count(&count)
		if count != campaignPlanCycles/4 {
			t.Fatalf("campaign %s got %d cycles: planning did not share turns", c.Token, count)
		}
	}
	tick(t, api, now, "resume")
	if err := api.RDB.DB(tenantCtx()).Model(&CampaignCycle{}).Count(&n).Error; err != nil || n != 4*36 {
		t.Fatalf("resumed cycles = %d, err = %v, want 144", n, err)
	}
}

type blockedCampaignGroup struct {
	started, other, release chan struct{}
}

func (r *blockedCampaignGroup) ResolveGroupTargets(ctx context.Context, group string, _ *int32, _ string, _ int) (*GroupTargetPage, error) {
	if group == "blocked" {
		close(r.started)
		select {
		case <-r.release:
			return nil, errors.New("test dependency released")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	select {
	case <-r.started:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-r.other:
	default:
		close(r.other)
	}
	return &GroupTargetPage{DeviceTokens: []string{"b"}}, nil
}

func TestCampaignTenantProgressWhileAnotherPlans(t *testing.T) {
	api := newCampaignTestApi(t)
	r := &blockedCampaignGroup{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	api.GroupTargetResolver = r
	for i, tenant := range []string{"acme", "other"} {
		req := cronRequest("hourly", "0 * * * *")
		if i == 0 {
			req.GroupToken = "blocked"
		}
		if _, err := api.CreateCampaign(core.WithTenant(context.Background(), tenant), req); err != nil {
			t.Fatal(err)
		}
	}
	sent := make(chan struct{}, 1)
	if err := api.RDB.Database.Callback().Create().After("gorm:create").Register("test:other-tenant-fired", func(tx *gorm.DB) {
		if tx.Statement.Table == "commands" && tx.Error == nil {
			select {
			case sent <- struct{}{}:
			default:
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	setClock(api, at("2026-06-21T12:00:00Z"))
	done := make(chan error, 1)
	go func() { done <- api.TickCampaigns(context.Background(), at("2026-06-21T12:00:00Z"), "fair") }()
	select {
	case <-sent:
	case <-time.After(time.Second):
		close(r.release)
		<-done
		t.Fatal("another tenant could not fire while the first dependency was blocked")
	}
	close(r.release)
	<-done // the blocked dependency's error does not cancel the other tenant
	var n int64
	ctx := core.WithTenant(context.Background(), "other")
	if err := api.RDB.DB(ctx).Model(&Command{}).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("other tenant commands = %d, err = %v", n, err)
	}
}

func TestCampaignPlanningDeadlineLeavesNoPartialWindow(t *testing.T) {
	api := newCampaignTestApi(t)
	r := &blockedCampaignGroup{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	api.GroupTargetResolver = r
	req := cronRequest("hourly", "0 * * * *")
	req.GroupToken = "blocked"
	c, err := api.CreateCampaign(tenantCtx(), req)
	if err != nil {
		t.Fatal(err)
	}
	saved := campaignPlanBudget
	campaignPlanBudget = 20 * time.Millisecond
	defer func() { campaignPlanBudget = saved }()
	done := make(chan error, 1)
	go func() { done <- api.tickTenant(tenantCtx(), at("2026-06-21T12:00:00Z"), "deadline") }()
	select {
	case err := <-done:
		// Reported by the pass, so a resolver that never answers is not a silent stall.
		if !errors.Is(err, errCampaignResolutionTimeout) {
			t.Fatalf("timed-out resolution = %v, want it reported", err)
		}
	case <-time.After(time.Second):
		close(r.release)
		<-done
		t.Fatal("planning did not cancel its slow dependency")
	}
	if err := api.RDB.DB(tenantCtx()).First(c, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if c.PlannedThrough.Valid || c.LastError.Valid {
		t.Fatalf("deferred planning marked complete or failed: %+v", c)
	}
}

type campaignPoolProbe struct {
	started chan struct{}
	release chan struct{}
	active  atomic.Int32
	peak    atomic.Int32
}

func (r *campaignPoolProbe) ResolveGroupTargets(ctx context.Context, _ string, _ *int32, _ string, _ int) (*GroupTargetPage, error) {
	n := r.active.Add(1)
	defer r.active.Add(-1)
	for peak := r.peak.Load(); n > peak; peak = r.peak.Load() {
		if r.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	r.started <- struct{}{}
	select {
	case <-r.release:
	case <-ctx.Done():
	}
	return nil, errors.New("test dependency unavailable")
}

func TestCampaignTenantWorkerPoolIsBoundedAndPassesHaveDeadlines(t *testing.T) {
	api := newCampaignTestApi(t)
	const tenants = 6
	r := &campaignPoolProbe{started: make(chan struct{}, tenants), release: make(chan struct{})}
	api.GroupTargetResolver = r
	for i := 0; i < tenants; i++ {
		ctx := core.WithTenant(context.Background(), fmt.Sprintf("tenant-%d", i))
		if _, err := api.CreateCampaign(ctx, cronRequest("hourly", "0 * * * *")); err != nil {
			t.Fatal(err)
		}
	}
	var checked atomic.Int32
	if err := api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:campaign-pass-deadline", func(tx *gorm.DB) {
		if tx.Statement.Table != "campaigns" {
			return
		}
		if _, ok := core.TenantFromContext(tx.Statement.Context); !ok {
			return // the initial system enumeration has no tenant deadline
		}
		deadline, ok := tx.Statement.Context.Deadline()
		if !ok || time.Until(deadline) > campaignPassTimeout {
			t.Error("tenant query has no bounded pass deadline")
		}
		checked.Add(1)
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- api.TickCampaigns(context.Background(), at("2026-06-21T12:00:30Z"), "pool") }()
	for i := 0; i < campaignTenantWorkers; i++ {
		select {
		case <-r.started:
		case <-time.After(time.Second):
			close(r.release)
			<-done
			t.Fatal("tenant workers did not start")
		}
	}
	select {
	case <-r.started:
		t.Error("more than four tenant passes started concurrently")
	case <-time.After(30 * time.Millisecond):
	}
	close(r.release)
	<-done
	if r.peak.Load() > 4 || checked.Load() < tenants {
		t.Fatalf("peak concurrency = %d, deadline checks = %d", r.peak.Load(), checked.Load())
	}
}

// The planning budget admits work; it never cancels it. A cycle whose insert alone
// outlasts the budget must still be planned, or the largest campaigns would be rolled
// back on every pass and never planned at all.
func TestCampaignCycleInsertOutlastingThePlanBudgetCompletes(t *testing.T) {
	api := newCampaignTestApi(t)
	api.GroupTargetResolver = &pagingResolver{members: []string{"a", "b"}}
	c, err := api.CreateCampaign(tenantCtx(), cronRequest("hourly", "0 * * * *"))
	if err != nil {
		t.Fatal(err)
	}
	saved := campaignPlanBudget
	campaignPlanBudget = time.Millisecond
	defer func() { campaignPlanBudget = saved }()
	if err := api.RDB.Database.Callback().Create().Before("gorm:create").Register("test:slow-element-insert", func(tx *gorm.DB) {
		if tx.Statement.Table == "campaign_elements" {
			time.Sleep(20 * time.Millisecond)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := api.tickTenant(tenantCtx(), at("2026-06-21T12:00:30Z"), "slow-insert"); err != nil {
		t.Fatal(err)
	}
	if err := api.RDB.DB(tenantCtx()).First(c, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !c.PlannedThrough.Valid || c.LastError.Valid {
		t.Fatalf("a cycle started inside the budget was not planned: %+v", c)
	}
	var elements int64
	if err := api.RDB.DB(tenantCtx()).Model(&CampaignElement{}).Where("campaign_id = ?", c.ID).Count(&elements).Error; err != nil || elements == 0 {
		t.Fatalf("elements = %d, err = %v", elements, err)
	}
}
