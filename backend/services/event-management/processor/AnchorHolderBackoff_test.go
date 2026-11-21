// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	emmodel "github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type holderRepairFixture struct {
	api       *emmodel.Api
	sweep     *AnchorReconciliationSweep
	now       time.Time
	calls     atomic.Int32
	exists    atomic.Bool
	outage    atomic.Bool
	onlyOther atomic.Bool
}

func newHolderRepairFixture(t *testing.T) *holderRepairFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&emmodel.EntityDeletionWatermark{}, &emmodel.DeviceTokenHolder{}))
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX holders_key ON device_token_holders (tenant_id, device_token)`).Error)
	api := emmodel.NewApi(&rdb.RdbManager{Database: db})
	f := &holderRepairFixture{api: api, now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if f.outage.Load() {
			http.Error(w, "unreachable", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Variables struct {
				Refs []emmodel.AnchorRef `json:"refs"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Variables.Refs) > 500 {
			t.Errorf("unbounded request: %d refs", len(body.Variables.Refs))
		}
		out := []map[string]string{}
		if f.exists.Load() && (!f.onlyOther.Load() || r.Header.Get(auth.ServiceTenantHeader) == "other") {
			for _, ref := range body.Variables.Refs {
				out = append(out, map[string]string{"type": ref.Type, "token": ref.Token})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"existingEntityRefs": out}})
	}))
	t.Cleanup(dm.Close)
	f.sweep = &AnchorReconciliationSweep{Api: api, dmURL: dm.URL, now: func() time.Time { return f.now },
		client: svcclient.New(mintOnly(t), "shh", "event-management", []string{string(auth.DeviceRead)})}
	return f
}

func (f *holderRepairFixture) seed(t *testing.T, tenant string, count int) context.Context {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	rows := make([]emmodel.EntityDeletionWatermark, count)
	for i := range rows {
		rows[i] = emmodel.EntityDeletionWatermark{EntityType: "device", EntityToken: fmt.Sprintf("gone-%05d", i), DeletedTime: f.now.Add(-time.Hour)}
	}
	require.NoError(t, f.api.RDB.DB(ctx).CreateInBatches(&rows, 200).Error)
	return ctx
}

func TestHolderRepairDoesNotRepeatConfirmedAbsenceImmediately(t *testing.T) {
	f := newHolderRepairFixture(t)
	ctx := f.seed(t, "acme", 1)
	require.NoError(t, f.sweep.repairTenantHolders(ctx, ctx, "acme"))
	require.NoError(t, f.sweep.repairTenantHolders(ctx, ctx, "acme"))
	require.EqualValues(t, 1, f.calls.Load(), "confirmed absence must suppress another query until due")
}

func TestHolderRepairRecreationAfterBackoffIsCaughtOnNextDuePass(t *testing.T) {
	f := newHolderRepairFixture(t)
	ctx := f.seed(t, "acme", 1)
	// Accumulate the maximum backoff without a roster fact.
	for _, hours := range []int{1, 2, 4, 8, 16, 32, 64, 128, 168} {
		require.NoError(t, f.sweep.repairTenantHolders(ctx, ctx, "acme"))
		f.now = f.now.Add(time.Duration(hours) * time.Hour)
	}
	f.exists.Store(true)
	f.now = f.now.Add(-time.Minute)
	calls := f.calls.Load()
	require.NoError(t, f.sweep.repairTenantHolders(ctx, ctx, "acme"))
	require.Equal(t, calls, f.calls.Load(), "recreation does not bypass a not-yet-due schedule")
	f.now = f.now.Add(time.Minute)
	require.NoError(t, f.sweep.repairTenantHolders(ctx, ctx, "acme"))
	require.Equal(t, calls+1, f.calls.Load())
	var held emmodel.DeviceTokenHolder
	require.NoError(t, f.api.RDB.DB(ctx).Take(&held).Error)
	var w emmodel.EntityDeletionWatermark
	require.NoError(t, f.api.RDB.DB(ctx).Take(&w).Error)
	require.True(t, held.HeldSince.Equal(w.DeletedTime), "repair keeps the exact deletion bound")
	f.now = f.now.Add(7 * 24 * time.Hour)
	require.NoError(t, f.sweep.repairTenantHolders(ctx, ctx, "acme"))
	require.Equal(t, calls+1, f.calls.Load(), "held tokens leave the repair walk")
}

func TestHolderRepairBoundsQueriesAcrossManyDeletionsAndHourlyPasses(t *testing.T) {
	f := newHolderRepairFixture(t)
	ctx := f.seed(t, "acme", 1201)
	start := f.now
	for hour := 0; hour < 168; hour++ {
		f.now = start.Add(time.Duration(hour) * time.Hour)
		require.NoError(t, f.sweep.repairTenantHolders(ctx, ctx, "acme"))
	}
	// Eight due passes at hours 0,1,3,7,15,31,63,127, three pages each.
	require.EqualValues(t, 24, f.calls.Load(), "hourly re-querying would cost 504 requests")
}

func TestHolderRepairSchedulesEachTenantIndependently(t *testing.T) {
	f := newHolderRepairFixture(t)
	acme := f.seed(t, "acme", 1)
	other := f.seed(t, "other", 1)
	require.NoError(t, f.sweep.repairTenantHolders(acme, acme, "acme"))
	f.exists.Store(true)
	f.onlyOther.Store(true)
	require.NoError(t, f.sweep.repairHolders(context.Background()))
	require.EqualValues(t, 2, f.calls.Load(), "only the other tenant is due")
	var holders []emmodel.DeviceTokenHolder
	require.NoError(t, f.api.RDB.DB(acme).Find(&holders).Error)
	require.Empty(t, holders)
	require.NoError(t, f.api.RDB.DB(other).Find(&holders).Error)
	require.Len(t, holders, 1)
}

func TestHolderRepairOutageDoesNotBackOffUnansweredTokens(t *testing.T) {
	f := newHolderRepairFixture(t)
	ctx := f.seed(t, "acme", 1)
	f.outage.Store(true)
	require.Error(t, f.sweep.repairTenantHolders(ctx, ctx, "acme"))
	page, err := f.api.UnheldDeletedDeviceTokensAfter(ctx, "", f.now)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Nil(t, page[0].CheckAfter)
	f.outage.Store(false)
	require.NoError(t, f.sweep.repairTenantHolders(ctx, ctx, "acme"))
	require.EqualValues(t, 2, f.calls.Load())
}
