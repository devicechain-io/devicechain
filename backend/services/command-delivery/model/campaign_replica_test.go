// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var campaignReplicaDue = at("2026-06-22T00:00:30Z")

func prepareReplicaCampaign(t *testing.T, api *Api) []string {
	t.Helper()
	members := make([]string, 60)
	for i := range members {
		members[i] = fmt.Sprintf("lamp-%02d", i)
	}
	api.GroupTargetResolver = &pagingResolver{members: members}
	setClock(api, at("2026-06-21T22:00:00Z"))
	req := cronRequest("replicas", "0 0 22 6 *")
	req.Trigger = `{"kind":"CRON","zone":"UTC","cron":"0 0 22 6 *"}`
	req.Steps[0].Name = strp("replica-on")
	_, err := api.CreateCampaign(tenantCtx(), req)
	require.NoError(t, err)
	return members
}

func assertReplicaDispatch(t *testing.T, api *Api, members []string, want int) {
	t.Helper()
	for _, d := range members {
		require.Equal(t, want, commandsFor(t, api, d), d)
	}
	var cycles []CampaignCycle
	require.NoError(t, api.RDB.DB(tenantCtx()).Find(&cycles).Error)
	require.Len(t, cycles, 1)
	var batches []CommandBatch
	require.NoError(t, api.RDB.DB(tenantCtx()).Find(&batches).Error)
	require.Len(t, batches, want, "one batch for the single cycle/step element set")
	var elements []CampaignElement
	require.NoError(t, api.RDB.DB(tenantCtx()).Find(&elements).Error)
	require.Len(t, elements, len(members))
	if want == 1 {
		for _, e := range elements {
			require.Equal(t, string(ElementDispatched), e.Status)
			require.Equal(t, batches[0].Token, e.BatchToken.String)
		}
	}
}

// A second pass finishes after the first reads PENDING ids, before its claim UPDATE.
// This deterministic SQLite control exercises claim ownership, not Postgres locking.
func TestCampaignReplicaCannotReclaimAnotherPassesDispatchedElements(t *testing.T) {
	api := newCampaignTestApi(t)
	members := prepareReplicaCampaign(t, api)
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	// Replay planning as if a replica had read the campaign before planned_through
	// was advanced. Cycle identity must survive that stale planning window.
	require.NoError(t, api.RDB.DB(tenantCtx()).Model(&Campaign{}).
		Where("token = ?", "replicas").Update("planned_through", nil).Error)
	tick(t, api, at("2026-06-21T22:00:00Z"), "replan")
	campaignClaimHook = func() {
		campaignClaimHook = nil
		require.NoError(t, api.TickCampaigns(context.Background(), campaignReplicaDue, "winner"))
	}
	t.Cleanup(func() { campaignClaimHook = nil })
	tick(t, api, campaignReplicaDue, "loser")
	assertReplicaDispatch(t, api, members, 1)
	var elements []CampaignElement
	require.NoError(t, api.RDB.DB(tenantCtx()).Find(&elements).Error)
	for _, e := range elements {
		require.Equal(t, "winner", e.ClaimedBy.String, "a completed claim was stolen")
	}
}

func stopReplicaCampaign(api *Api, operation string) error {
	if operation == "delete" {
		return api.DeleteCampaign(tenantCtx(), "replicas")
	}
	_, err := api.SetCampaignEnabled(tenantCtx(), "replicas", false)
	return err
}

func TestCampaignReplicaRechecksStoppedCampaignBeforeCreatingBatch(t *testing.T) {
	for _, operation := range []string{"live", "delete", "disable"} {
		t.Run(operation, func(t *testing.T) {
			api := newCampaignTestApi(t)
			members := prepareReplicaCampaign(t, api)
			tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
			campaignDispatchHook = func() {
				if operation != "live" {
					require.NoError(t, stopReplicaCampaign(api, operation))
				}
			}
			t.Cleanup(func() { campaignDispatchHook = nil })
			tick(t, api, campaignReplicaDue, "claimed")
			want := 0
			if operation == "live" {
				want = 1
			}
			assertReplicaDispatch(t, api, members, want)
		})
	}
}

type campaignPostgresName struct{ gorm.Dialector }

func (campaignPostgresName) Name() string { return "postgres" }

// Execute the Postgres lock branch on SQLite but fail its SQL before execution.
// This proves all three entry points propagate lock failure and write nothing;
// the integration suite below is what proves mutual exclusion across sessions.
func TestCampaignReplicaLockFailureAbortsDispatchDeleteAndDisable(t *testing.T) {
	for _, operation := range []string{"dispatch", "delete", "disable"} {
		t.Run(operation, func(t *testing.T) {
			api := newCampaignTestApi(t)
			members := prepareReplicaCampaign(t, api)
			tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
			db := api.RDB.Database
			db.Dialector = campaignPostgresName{db.Dialector}
			lockErr := errors.New("campaign lock unavailable")
			calls := 0
			require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("test:campaign-lock-error", func(tx *gorm.DB) {
				if strings.Contains(tx.Statement.SQL.String(), "pg_advisory_xact_lock") {
					calls++
					tx.AddError(lockErr)
				}
			}))
			var err error
			if operation == "dispatch" {
				setClock(api, campaignReplicaDue)
				err = api.TickCampaigns(context.Background(), campaignReplicaDue, "locked")
			} else {
				err = stopReplicaCampaign(api, operation)
			}
			require.ErrorIs(t, err, lockErr)
			require.Positive(t, calls)
			assertReplicaDispatch(t, api, members, 0)
			c, err := api.campaignByToken(tenantCtx(), "replicas")
			require.NoError(t, err)
			require.True(t, c.Enabled)
		})
	}
}
