// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-dashboard-management/model"
	"github.com/devicechain-io/dc-dashboard-management/sharecheck"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type existenceLookup struct {
	existing    map[string]bool
	err         error
	calls       int
	failAt      int
	afterLookup func()
}

func (l *existenceLookup) Missing(_ context.Context, _, _ string, tokens []string) ([]string, error) {
	l.calls++
	var missing []string
	for _, token := range tokens {
		if !l.existing[token] {
			missing = append(missing, token)
		}
	}
	if l.err != nil || l.calls == l.failAt {
		return missing, errors.New("lookup unavailable")
	}
	if l.afterLookup != nil {
		l.afterLookup()
	}
	return missing, nil
}

func TestShareSweepDeletesMissingAnchorsAndKeepsExisting(t *testing.T) {
	for _, kind := range []string{"customer", "group"} {
		for _, tc := range []struct {
			name           string
			existing, fail bool
			deleted        int64
		}{
			{name: "missing", deleted: 1},
			{name: "existing", existing: true},
			{name: "lookup error with partial answer", fail: true},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				api, db := sharesApi(t)
				ctx := core.WithTenant(context.Background(), "t1")
				at := time.Now().UTC().Add(-time.Hour)
				share(t, db, "t1", 1, kind, "acme", at)
				share(t, db, "t2", 1, kind, "acme", at)
				lookup := &existenceLookup{existing: map[string]bool{"acme": tc.existing}}
				if tc.fail {
					lookup.err = errors.New("outage")
				}
				api.ShareTargets = lookup
				deleted, err := (&ShareSweep{API: api}).sweepTenant(ctx, "t1")
				if tc.fail {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, tc.deleted, deleted)
				var count int64
				require.NoError(t, db.WithContext(ctx).Model(&model.DashboardShare{}).Count(&count).Error)
				require.EqualValues(t, 1-tc.deleted, count)
				require.NoError(t, db.WithContext(core.WithTenant(context.Background(), "t2")).Model(&model.DashboardShare{}).Count(&count).Error)
				require.EqualValues(t, 1, count)
			})
		}
	}
}

func TestShareSweepLooksUpAllPagesBeforeChangingATenant(t *testing.T) {
	api, db := sharesApi(t)
	ctx := core.WithTenant(context.Background(), "t1")
	for i := 0; i < rdb.MaxLookupKeys+1; i++ {
		share(t, db, "t1", uint(i+1), "customer", fmt.Sprintf("c%05d", i), time.Now())
	}
	lookup := &existenceLookup{failAt: 2}
	api.ShareTargets = lookup
	_, err := (&ShareSweep{API: api}).sweepTenant(ctx, "t1")
	require.Error(t, err)
	require.Equal(t, 2, lookup.calls)
	var count int64
	require.NoError(t, db.WithContext(ctx).Model(&model.DashboardShare{}).Count(&count).Error)
	require.EqualValues(t, rdb.MaxLookupKeys+1, count)
}

func TestShareSweepEnumeratesTenantsAndCountsRepairs(t *testing.T) {
	api, db := sharesApi(t)
	at := time.Now().UTC().Add(-24 * time.Hour)
	share(t, db, "t1", 1, "customer", "acme", at.Add(-time.Hour))
	share(t, db, "t2", 1, "group", "acme", at.Add(time.Hour))
	api.ShareTargets = &existenceLookup{}
	ms := &core.Microservice{FunctionalArea: "dashboard-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	changes := ms.NewCounterVec("test_share_sweep_changes_total", "Test sweep repairs", []string{"action"})
	s := &ShareSweep{API: api, Changes: changes}
	require.NoError(t, s.sweep(context.Background()))
	require.EqualValues(t, 2, testutil.ToFloat64(changes.WithLabelValues("deleted")))
	// Repair is idempotent; later passes cannot add more changes.
	require.NoError(t, s.sweep(context.Background()))
	require.EqualValues(t, 2, testutil.ToFloat64(changes.WithLabelValues("deleted")))
}

func TestShareSweepPreservesSharesInsertedAfterLookup(t *testing.T) {
	for _, kind := range []string{"customer", "group"} {
		t.Run(kind, func(t *testing.T) {
			api, db := sharesApi(t)
			ctx := core.WithTenant(context.Background(), "t1")
			at := time.Now().UTC().Add(-24 * time.Hour)
			share(t, db, "t1", 1, kind, "acme", at)
			lookup := &existenceLookup{}
			var inserted []model.DashboardShare
			lookup.afterLookup = func() {
				// A share created after the lookup must be left for a later pass.
				inserted = []model.DashboardShare{
					{DashboardID: 2, AnchorKind: kind, AnchorToken: "acme", CreatedAt: time.Now().UTC()},
				}
				require.NoError(t, db.WithContext(ctx).Create(&inserted).Error)
			}
			api.ShareTargets = lookup
			deleted, err := (&ShareSweep{API: api}).sweepTenant(ctx, "t1")
			require.NoError(t, err)
			require.EqualValues(t, 1, deleted)
			var got []model.DashboardShare
			require.NoError(t, db.WithContext(ctx).Order("dashboard_id").Find(&got).Error)
			require.Equal(t, inserted, got)
		})
	}
}

type sweepAnswer struct{ answer string }

func (q sweepAnswer) Query(_ context.Context, _, _, _ string, _ map[string]any, out any) error {
	return json.Unmarshal([]byte(q.answer), out)
}

func TestShareSweepDoesNotMutateMalformedTokenAnswers(t *testing.T) {
	for _, kind := range []string{"customer", "group"} {
		list := "customersByToken"
		if kind == "group" {
			list = "entityGroupsByToken"
		}
		for _, tokens := range [][]string{{""}, {"unrequested"}, {"acme", "acme"}} {
			t.Run(kind+fmt.Sprint(tokens), func(t *testing.T) {
				api, db := sharesApi(t)
				ctx := core.WithTenant(context.Background(), "t1")
				at := time.Now().UTC().Add(-24 * time.Hour)
				// An empty answer would delete these rows; malformed answers must preserve both.
				share(t, db, "t1", 1, kind, "acme", at.Add(-time.Hour))
				share(t, db, "t1", 2, kind, "acme", at.Add(time.Hour))
				var before []model.DashboardShare
				require.NoError(t, db.WithContext(ctx).Order("dashboard_id").Find(&before).Error)
				entities := []map[string]any{}
				for _, token := range tokens {
					entities = append(entities, map[string]any{"token": token, "accessScope": false})
				}
				answer, err := json.Marshal(map[string]any{list: entities})
				require.NoError(t, err)
				api.ShareTargets = sharecheck.New(sweepAnswer{answer: string(answer)}, "u")
				deleted, err := (&ShareSweep{API: api}).sweepTenant(ctx, "t1")
				require.Error(t, err)
				require.Zero(t, deleted)
				var after []model.DashboardShare
				require.NoError(t, db.WithContext(ctx).Order("dashboard_id").Find(&after).Error)
				require.Equal(t, before, after)
			})
		}
	}
}
