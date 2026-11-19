// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type campaignRecoveryValidator struct{ err error }

func (v campaignRecoveryValidator) ValidateEnqueueBatch(context.Context, []string, string, []byte) ([]BatchDeviceRefusal, map[string]uint64, error) {
	return nil, nil, v.err
}

type campaignRecoveryGroups struct {
	pagingResolver
	err    error
	before func()
}

func (r *campaignRecoveryGroups) ResolveGroupTargets(ctx context.Context, token string, version *int32, cursor string, limit int) (*GroupTargetPage, error) {
	if r.before != nil {
		f := r.before
		r.before = nil
		f()
	}
	if r.err != nil {
		r.pages++
		return nil, r.err
	}
	return r.pagingResolver.ResolveGroupTargets(ctx, token, version, cursor, limit)
}

// Exercise the real claim, token assignment and dependency-failure path, rather than
// seeding claims that might not match what a failed dispatch actually leaves behind.
func failCampaignBatchCreation(t *testing.T, api *Api, campaign string, due time.Time, members []string) string {
	t.Helper()
	unavailable := errors.New("device validation temporarily unavailable")
	api.BatchValidator = campaignRecoveryValidator{err: unavailable}
	setClock(api, due)
	require.ErrorContains(t, api.TickCampaigns(context.Background(), due, "first"), "validation is unavailable")
	api.BatchValidator = nil
	var token string
	for _, device := range members {
		e := elementOf(t, api, campaign, device)
		require.Equal(t, string(ElementClaimed), e.Status)
		require.Equal(t, "first", e.ClaimedBy.String)
		require.True(t, e.ClaimedAt.Valid)
		require.True(t, e.BatchToken.Valid)
		if token == "" {
			token = e.BatchToken.String
		}
		require.Equal(t, token, e.BatchToken.String)
		require.Zero(t, commandsFor(t, api, device))
	}
	var batches int64
	require.NoError(t, api.RDB.DB(tenantCtx()).Model(&CommandBatch{}).Where("token = ?", token).Count(&batches).Error)
	require.Zero(t, batches)
	return token
}

func failedMembershipCampaign(t *testing.T) (*Api, *campaignRecoveryGroups, time.Time, string) {
	t.Helper()
	api := newCampaignTestApi(t)
	r := &campaignRecoveryGroups{pagingResolver: pagingResolver{members: []string{"a", "b"}}}
	api.GroupTargetResolver = r
	_, err := api.CreateCampaign(tenantCtx(), cronRequest("nightly", "0 20 * * *"))
	require.NoError(t, err)
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	due := at("2026-06-22T00:00:30Z")
	token := failCampaignBatchCreation(t, api, "nightly", due, []string{"a", "b"})
	return api, r, due, token
}

func TestRecoveredCampaignClaimsRecheckMembership(t *testing.T) {
	for _, pass := range []string{"another-replica", "first"} {
		t.Run(pass, func(t *testing.T) {
			api, r, due, token := failedMembershipCampaign(t)
			r.members = []string{"a"}
			pages := r.pages
			tick(t, api, due.Add(campaignClaimStale+time.Second), pass)
			require.Zero(t, commandsFor(t, api, "b"), "removed member must receive no command on claim recovery")
			e := elementOf(t, api, "nightly", "b")
			require.Equal(t, string(ElementSkipped), e.Status)
			require.Equal(t, SkipNotInGroup, e.Reason.String)
			require.Equal(t, 1, commandsFor(t, api, "a"), "remaining member must still dispatch")
			a := elementOf(t, api, "nightly", "a")
			require.Equal(t, string(ElementDispatched), a.Status)
			require.Equal(t, token, a.BatchToken.String, "recovery must preserve the batch token")
			require.Equal(t, pages+1, r.pages)
		})
	}
}

func TestRecoveredCampaignClaimsHoldWhenMembershipUnknown(t *testing.T) {
	for _, failure := range []string{"error", "nil-resolver", "rejected", "unusable"} {
		t.Run(failure, func(t *testing.T) {
			api, r, due, _ := failedMembershipCampaign(t)
			switch failure {
			case "error":
				r.err = errors.New("group lookup temporarily unavailable")
			case "nil-resolver":
				api.GroupTargetResolver = nil
			case "rejected":
				r.rejectWith = "group unavailable"
			case "unusable":
				r.alwaysCursor = true
				r.members = nil
			}
			now := due.Add(campaignClaimStale + time.Second)
			setClock(api, now)
			err := api.TickCampaigns(context.Background(), now, "recover")
			for _, d := range []string{"a", "b"} {
				require.Zero(t, commandsFor(t, api, d), "unknown membership must hold recovered claims")
				require.Equal(t, string(ElementClaimed), elementOf(t, api, "nightly", d).Status)
			}
			require.Error(t, err)
			c, err := api.campaignByToken(tenantCtx(), "nightly")
			require.NoError(t, err)
			require.True(t, c.LastError.Valid, "resolution failure must use the campaign error path")
			pages := r.pages
			tick(t, api, due.Add(DefaultCampaignMaxLateness+time.Minute), "late")
			for _, d := range []string{"a", "b"} {
				e := elementOf(t, api, "nightly", d)
				require.Zero(t, commandsFor(t, api, d))
				require.Equal(t, string(ElementSkipped), e.Status)
				require.Equal(t, SkipMissed, e.Reason.String)
			}
			require.Equal(t, pages, r.pages, "late claims need no membership resolution")
		})
	}
}

func TestRecoveredCampaignClaimsRecheckOtherOwnersInToken(t *testing.T) {
	api, r, due, _ := failedMembershipCampaign(t)
	r.members = []string{"a"}
	now := due.Add(campaignClaimStale + time.Second)
	b := elementOf(t, api, "nightly", "b")
	// The stale member makes this pass recover the token. Its other member is
	// another pass's recent claim: the token's DB membership still includes it.
	require.NoError(t, api.RDB.DB(tenantCtx()).Model(&CampaignElement{}).Where("id = ?", b.ID).
		Updates(map[string]any{"claimed_by": "other-owner", "claimed_at": now}).Error)
	tick(t, api, now, "first")
	require.Zero(t, commandsFor(t, api, "b"))
	b = elementOf(t, api, "nightly", "b")
	require.Equal(t, string(ElementSkipped), b.Status)
	require.Equal(t, SkipNotInGroup, b.Reason.String)
	require.Equal(t, 1, commandsFor(t, api, "a"))
}

func TestRecoveredCampaignClaimsPreserveCommittedBatch(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed", true: "unavailable"}[unavailable], func(t *testing.T) {
			api := newCampaignTestApi(t)
			r := &campaignRecoveryGroups{pagingResolver: pagingResolver{members: []string{"a", "b"}}}
			api.GroupTargetResolver = r
			_, err := api.CreateCampaign(tenantCtx(), cronRequest("nightly", "0 20 * * *"))
			require.NoError(t, err)
			tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
			// Lose settlement after the batch transaction committed, as a dying replica would.
			lost := errors.New("replica died before element settlement")
			require.NoError(t, api.RDB.Database.Callback().Update().Before("gorm:update").Register("test:lost-campaign-settlement", func(tx *gorm.DB) {
				if set, ok := tx.Statement.Dest.(map[string]any); ok && tx.Statement.Table == "campaign_elements" && set["status"] == string(ElementDispatched) {
					tx.AddError(lost)
				}
			}))
			due := at("2026-06-22T00:00:30Z")
			setClock(api, due)
			require.ErrorIs(t, api.TickCampaigns(context.Background(), due, "first"), lost)
			require.NoError(t, api.RDB.Database.Callback().Update().Remove("test:lost-campaign-settlement"))
			for _, d := range []string{"a", "b"} {
				require.Equal(t, 1, commandsFor(t, api, d))
				require.Equal(t, string(ElementClaimed), elementOf(t, api, "nightly", d).Status)
			}
			r.members = []string{"a"}
			if unavailable {
				r.err = errors.New("group lookup unavailable after send")
			}
			pages := r.pages
			tick(t, api, due.Add(campaignClaimStale+time.Second), "recover")
			for _, d := range []string{"a", "b"} {
				require.Equal(t, 1, commandsFor(t, api, d))
				require.Equal(t, string(ElementDispatched), elementOf(t, api, "nightly", d).Status)
			}
			require.Equal(t, pages, r.pages, "an existing batch must settle without resolving the group")
		})
	}
}

func TestRecoveredCampaignMembershipSettleChecksForConcurrentBatch(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed", true: "unavailable"}[unavailable], func(t *testing.T) {
			api, r, due, token := failedMembershipCampaign(t)
			r.members = []string{"a"}
			if unavailable {
				r.err = errors.New("group lookup failed while another replica sent")
			}
			r.before = func() {
				devices := []string{"a", "b"}
				_, err := api.CreateCommandBatch(tenantCtx(), &CommandBatchCreateRequest{Token: token, Name: "lights-on", DeviceTokens: &devices, AllowPartial: true})
				require.NoError(t, err)
			}
			now := due.Add(campaignClaimStale + time.Second)
			setClock(api, now)
			err := api.TickCampaigns(context.Background(), now, "recover")
			if unavailable {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Nil(t, r.before, "recovery must re-resolve before creating an absent batch")
			for _, d := range []string{"a", "b"} {
				require.Equal(t, 1, commandsFor(t, api, d))
				require.Equal(t, string(ElementDispatched), elementOf(t, api, "nightly", d).Status, "committed batch must win over a membership settle")
			}
		})
	}
}

func TestRecoveredCampaignMembershipSharesResolutionWithPendingClaims(t *testing.T) {
	api := newCampaignTestApi(t)
	r := &campaignRecoveryGroups{pagingResolver: pagingResolver{members: []string{"a", "b"}}}
	api.GroupTargetResolver = r
	req := cronRequest("nightly", "0 20 * * *")
	delay := int32(180)
	req.Steps = append(req.Steps, CampaignStepInput{Type: StepCommand, Name: strp("dim"), DelaySeconds: &delay})
	_, err := api.CreateCampaign(tenantCtx(), req)
	require.NoError(t, err)
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	due := at("2026-06-22T00:00:30Z")
	failCampaignBatchCreation(t, api, "nightly", due, []string{"a", "b"})
	r.members = []string{"a"}
	pages := r.pages
	tick(t, api, due.Add(3*time.Minute), "recovered-and-fresh")
	require.Zero(t, commandsFor(t, api, "b"))
	require.Equal(t, 2, commandsFor(t, api, "a"))
	require.Equal(t, pages+1, r.pages, "pending and recovered claims share one resolution in the pass")
}

func TestRecoveredCampaignMembershipExpiresBetweenTokens(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed", true: "unavailable"}[unavailable], func(t *testing.T) {
			api, r, due, token := failedMembershipCampaign(t)
			b := elementOf(t, api, "nightly", "b")
			// Two absent batches let token A stall before token B checks membership.
			require.NoError(t, api.RDB.DB(tenantCtx()).Model(&CampaignElement{}).Where("id = ?", b.ID).
				Update("batch_token", token+"-b").Error)
			now := due.Add(campaignClaimStale + time.Second)
			pages := r.pages
			stalled := false
			campaignDispatchHook = func() {
				campaignDispatchHook = nil
				stalled = true
				r.members = []string{"a"}
				if unavailable {
					r.err = errors.New("group lookup unavailable after token A stalled")
				}
				setClock(api, now.Add(campaignMembershipFreshness+time.Second))
			}
			t.Cleanup(func() { campaignDispatchHook = nil })
			setClock(api, now)
			err := api.TickCampaigns(context.Background(), now, "recover")
			if unavailable {
				require.ErrorContains(t, err, "group resolution is unavailable")
			} else {
				require.NoError(t, err)
			}
			require.True(t, stalled)
			require.Equal(t, 1, commandsFor(t, api, "a"))
			require.Zero(t, commandsFor(t, api, "b"), "stale inclusion must never authorize token B")
			b = elementOf(t, api, "nightly", "b")
			if unavailable {
				require.Equal(t, string(ElementClaimed), b.Status, "unknown membership must hold")
				require.False(t, b.Reason.Valid)
			} else {
				require.Equal(t, string(ElementSkipped), b.Status)
				require.Equal(t, SkipNotInGroup, b.Reason.String)
			}
			require.Equal(t, pages+2, r.pages, "expired membership must be resolved again")
		})
	}
}

func TestCampaignMembershipFailureRecordedOnceForPendingAndRecovered(t *testing.T) {
	api, r, due, _ := failedMembershipCampaign(t)
	now := due.Add(3 * time.Minute)
	a := elementOf(t, api, "nightly", "a")
	// A due step beyond the previous check forces pending work to share recovery's lookup.
	pending := &CampaignElement{CampaignId: a.CampaignId, CycleId: a.CycleId, Step: 1,
		DeviceToken: "a", Status: string(ElementPending), DueAt: now}
	require.NoError(t, api.RDB.DB(tenantCtx()).Create(pending).Error)
	r.err = errors.New("shared membership lookup unavailable")
	pages := r.pages
	var logs bytes.Buffer
	captured := zerolog.New(&logs)
	api.campaignLog = &captured
	setClock(api, now)
	err := api.TickCampaigns(context.Background(), now, "recover")
	require.ErrorContains(t, err, "group resolution is unavailable")
	require.Equal(t, 1, strings.Count(logs.String(), "Campaign planning or membership check failed."), "one shared failure must be recorded once per pass")
	require.Equal(t, pages+1, r.pages)
	for _, device := range []string{"a", "b"} {
		require.Zero(t, commandsFor(t, api, device))
		require.Equal(t, string(ElementClaimed), elementOf(t, api, "nightly", device).Status)
	}
	require.NoError(t, api.RDB.DB(tenantCtx()).First(pending, pending.ID).Error)
	require.Equal(t, string(ElementPending), pending.Status, "unknown membership must hold pending work too")
}

func TestFreshCampaignClaimsKeepTheirMembershipCheck(t *testing.T) {
	api := newCampaignTestApi(t)
	r := &campaignRecoveryGroups{pagingResolver: pagingResolver{members: []string{"a"}}}
	api.GroupTargetResolver = r
	_, err := api.CreateCampaign(tenantCtx(), cronRequest("nightly", "0 20 * * *"))
	require.NoError(t, err)
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	pages := r.pages
	campaignClaimedHook = func() { r.err = errors.New("group lookup failed after the fresh check") }
	t.Cleanup(func() { campaignClaimedHook = nil })
	tick(t, api, at("2026-06-22T00:00:30Z"), "fresh")
	require.Equal(t, 1, commandsFor(t, api, "a"))
	require.Equal(t, pages+1, r.pages, "fresh claims retain the check immediately before claiming")
}
