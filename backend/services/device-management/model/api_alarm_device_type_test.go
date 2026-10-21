// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The device-type facet of the alarm search and the per-type alarm count. The two classify by
// the same device read, so for every type the count must equal the filtered search's total:
// a tab badge and the tab's rows are these two reads.

type alarmTypeRig struct {
	api     *Api
	db      *gorm.DB
	devices map[string]uint
	n       int
}

func newAlarmTypeRig(t *testing.T) *alarmTypeRig {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&Alarm{}, &Device{}, &DeviceType{}))
	return &alarmTypeRig{api: NewApi(&rdb.RdbManager{Database: db}), db: db, devices: map[string]uint{}}
}

func tenantCtx(tenant string) context.Context { return core.WithTenant(context.Background(), tenant) }

// device seeds a device of the named type (created on first use) in tenant.
func (r *alarmTypeRig) device(t *testing.T, tenant, typeToken, token string) {
	t.Helper()
	ctx := tenantCtx(tenant)
	var dt DeviceType
	if err := r.db.WithContext(ctx).Where("token = ?", typeToken).First(&dt).Error; err != nil {
		dt = DeviceType{TokenReference: rdb.TokenReference{Token: typeToken}}
		require.NoError(t, r.db.WithContext(ctx).Create(&dt).Error)
	}
	d := Device{TokenReference: rdb.TokenReference{Token: token}, DeviceTypeId: dt.ID}
	require.NoError(t, r.db.WithContext(ctx).Create(&d).Error)
	r.devices[tenant+"/"+token] = d.ID
}

func (r *alarmTypeRig) alarm(t *testing.T, tenant, device, severity string, state AlarmState) {
	t.Helper()
	r.n++
	a := Alarm{
		TokenReference: rdb.TokenReference{Token: fmt.Sprintf("alarm-%d", r.n)},
		OriginatorType: string(entity.TypeDevice), OriginatorId: r.devices[tenant+"/"+device],
		AlarmKey: fmt.Sprintf("k%d", r.n), MetricKey: "temp", State: string(state), Severity: severity,
		RaisedTime: time.Unix(int64(1000+r.n), 0).UTC(),
	}
	require.NoError(t, r.db.WithContext(tenantCtx(tenant)).Create(&a).Error)
}

func (r *alarmTypeRig) counts(t *testing.T, ctx context.Context, c AlarmCountCriteria) []string {
	t.Helper()
	found, err := r.api.AlarmCountsByDeviceType(ctx, c)
	require.NoError(t, err)
	out := []string{}
	for _, f := range found {
		out = append(out, fmt.Sprintf("%s/%s=%d", f.DeviceType.Token, f.Severity, f.Count))
	}
	sort.Strings(out)
	return out
}

func (r *alarmTypeRig) search(t *testing.T, ctx context.Context, deviceType string, state *string) (int64, []string) {
	t.Helper()
	found, err := r.api.Alarms(ctx, AlarmSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100}, DeviceType: &deviceType, State: state})
	require.NoError(t, err)
	keys := []string{}
	for _, a := range found.Results {
		keys = append(keys, a.AlarmKey)
	}
	sort.Strings(keys)
	return int64(found.Pagination.TotalRecords), keys
}

func TestAlarmsFilterAndCountByDeviceType(t *testing.T) {
	r := newAlarmTypeRig(t)
	r.device(t, "A", "pump", "p1")
	r.device(t, "A", "pump", "p2")
	r.device(t, "A", "valve", "v1")
	r.device(t, "A", "meter", "m1") // a type with no alarm
	r.device(t, "B", "pump", "p1")  // the same tokens in another tenant

	r.alarm(t, "A", "p1", "MAJOR", AlarmStateActive)    // k1
	r.alarm(t, "A", "p1", "CRITICAL", AlarmStateActive) // k2
	r.alarm(t, "A", "p2", "MAJOR", AlarmStateActive)    // k3
	r.alarm(t, "A", "v1", "MINOR", AlarmStateCleared)   // k4
	r.alarm(t, "A", "v1", "WARNING", AlarmStateActive)  // k5
	r.alarm(t, "B", "p1", "CRITICAL", AlarmStateActive) // k6

	a := tenantCtx("A")
	active := string(AlarmStateActive)

	require.Equal(t, []string{"pump/CRITICAL=1", "pump/MAJOR=2", "valve/WARNING=1"},
		r.counts(t, a, AlarmCountCriteria{State: &active}))
	require.Equal(t, []string{"pump/CRITICAL=1", "pump/MAJOR=2", "valve/MINOR=1", "valve/WARNING=1"},
		r.counts(t, a, AlarmCountCriteria{}), "with no state facet the cleared alarm counts too")
	major := "MAJOR"
	require.Equal(t, []string{"pump/MAJOR=2"}, r.counts(t, a, AlarmCountCriteria{Severity: &major}))

	total, keys := r.search(t, a, "pump", &active)
	require.Equal(t, int64(3), total)
	require.Equal(t, []string{"k1", "k2", "k3"}, keys)
	total, keys = r.search(t, a, "valve", nil)
	require.Equal(t, int64(2), total)
	require.Equal(t, []string{"k4", "k5"}, keys)
	total, keys = r.search(t, a, "meter", nil)
	require.Zero(t, total, "a type with no alarm")
	require.Empty(t, keys)
	total, keys = r.search(t, a, "no-such-type", nil)
	require.Zero(t, total, "an unknown type finds nothing")
	require.Empty(t, keys)

	// The other tenant: its own pump alarm only, by both reads.
	b := tenantCtx("B")
	require.Equal(t, []string{"pump/CRITICAL=1"}, r.counts(t, b, AlarmCountCriteria{}))
	total, keys = r.search(t, b, "pump", nil)
	require.Equal(t, int64(1), total)
	require.Equal(t, []string{"k6"}, keys)

	// The invariant the tab strip relies on: per type, count == filtered total.
	for _, tenant := range []string{"A", "B"} {
		ctx := tenantCtx(tenant)
		found, err := r.api.AlarmCountsByDeviceType(ctx, AlarmCountCriteria{})
		require.NoError(t, err)
		sums := map[string]int64{}
		for _, f := range found {
			sums[f.DeviceType.Token] += int64(f.Count)
		}
		for token, sum := range sums {
			got, _ := r.search(t, ctx, token, nil)
			require.Equal(t, sum, got, "tenant %s type %s: count and filtered total disagree", tenant, token)
		}
	}

	// Negative control: with no type facet the search sees every alarm of the tenant.
	all, err := r.api.Alarms(a, AlarmSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100}})
	require.NoError(t, err)
	require.Equal(t, int32(5), all.Pagination.TotalRecords)
}

// An alarm whose originator is not a device belongs to no type: neither read classifies it.
func TestAlarmsByDeviceTypeIgnoreNonDeviceOriginators(t *testing.T) {
	r := newAlarmTypeRig(t)
	r.device(t, "A", "pump", "p1")
	r.alarm(t, "A", "p1", "MAJOR", AlarmStateActive)
	// An asset alarm whose originator id collides with the device's id.
	asset := Alarm{TokenReference: rdb.TokenReference{Token: "asset-alarm"}, OriginatorType: "asset",
		OriginatorId: r.devices["A/p1"], AlarmKey: "asset-k", MetricKey: "m", State: string(AlarmStateActive),
		Severity: "CRITICAL", RaisedTime: time.Unix(5000, 0).UTC()}
	require.NoError(t, r.db.WithContext(tenantCtx("A")).Create(&asset).Error)

	require.Equal(t, []string{"pump/MAJOR=1"}, r.counts(t, tenantCtx("A"), AlarmCountCriteria{}))
	total, keys := r.search(t, tenantCtx("A"), "pump", nil)
	require.Equal(t, int64(1), total)
	require.Equal(t, []string{"k1"}, keys)
}
