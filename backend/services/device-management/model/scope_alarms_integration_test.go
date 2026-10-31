// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Alarms and devices under a scoped caller (ADR-081), on the real engine: the alarm stamp
// is a text[] compared with `&&`, which SQLite cannot run, and the migration that adds
// the column and its GIN index is exercised here as well, because the harness builds the
// schema through the real migration chain.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run Scope -v
package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type scopeAlarmRig struct {
	api      *Api
	tenant   string
	operator context.Context
	devices  map[string]uint
}

// newScopeAlarmRig seeds a fresh tenant (a unique name per run, so a server that ran this
// before needs no truncation) with acme-pump assigned to acme and globex-pump to globex.
func newScopeAlarmRig(t *testing.T) scopeAlarmRig {
	t.Helper()
	mgr := newPostgresRdbManager(t)
	api := NewApi(mgr)
	tenant := fmt.Sprintf("s3-%d", time.Now().UnixNano())
	r := scopeAlarmRig{api: api, tenant: tenant, devices: map[string]uint{},
		operator: auth.WithClaims(core.WithTenant(context.Background(), tenant),
			&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: tenant, Authorities: []string{"*"}})}
	ctx := r.operator
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "pump"})
	require.NoError(t, err)
	_, err = api.CreateCustomerType(ctx, &CustomerTypeCreateRequest{Token: "retail"})
	require.NoError(t, err)
	_, err = api.EnsureAssignmentType(ctx)
	require.NoError(t, err)
	for _, c := range []string{"acme", "globex"} {
		_, err := api.CreateCustomer(ctx, &CustomerCreateRequest{Token: c, CustomerTypeToken: "retail"})
		require.NoError(t, err)
		d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: c + "-pump", DeviceTypeToken: "pump"})
		require.NoError(t, err)
		r.devices[c+"-pump"] = d.ID
		r.assign(t, c+"-pump", c)
	}
	return r
}

func (r scopeAlarmRig) assign(t *testing.T, device, customer string) {
	t.Helper()
	_, err := r.api.CreateEntityRelationship(r.operator, &EntityRelationshipCreateRequest{
		Token: device + "-to-" + customer, SourceType: "device", Source: device,
		TargetType: "customer", Target: customer, RelationshipType: AssignmentRelationshipType})
	require.NoError(t, err)
}

func (r scopeAlarmRig) member(anchors ...string) context.Context {
	return auth.WithClaims(core.WithTenant(context.Background(), r.tenant), &auth.Claims{
		TokenType: auth.TokenTypeAccess, Tenant: r.tenant, ScopeAnchors: anchors,
		Authorities: []string{string(auth.AlarmRead), string(auth.AlarmWrite), string(auth.DeviceRead)}})
}

// raise drives the one alarm-raise path, as the raise-alarm consumer does: the tenant,
// and no caller.
func (r scopeAlarmRig) raise(t *testing.T, device, key string, at time.Time) {
	t.Helper()
	v := 99.0
	require.NoError(t, r.api.ApplyAlarmContributorEdge(core.WithTenant(context.Background(), r.tenant),
		r.devices[device], key, "temp", "rule-"+key, AlarmEdgeRaised, string(AlarmSeverityMajor), &v, at))
}

func (r scopeAlarmRig) resolve(t *testing.T, device, key string, at time.Time) {
	t.Helper()
	require.NoError(t, r.api.ApplyAlarmContributorEdge(core.WithTenant(context.Background(), r.tenant),
		r.devices[device], key, "temp", "rule-"+key, AlarmEdgeResolved, "", nil, at))
}

func (r scopeAlarmRig) alarmKeys(t *testing.T, ctx context.Context) []string {
	t.Helper()
	found, err := r.api.Alarms(ctx, AlarmSearchCriteria{})
	require.NoError(t, err)
	keys := []string{}
	for _, a := range found.Results {
		keys = append(keys, a.AlarmKey)
	}
	sort.Strings(keys)
	return keys
}

func (r scopeAlarmRig) alarm(t *testing.T, key string) *Alarm {
	t.Helper()
	var a Alarm
	require.NoError(t, r.api.RDB.DB(r.operator).Where("alarm_key = ?", key).First(&a).Error)
	return &a
}

// TestAlarmRaisedCarriesDeviceAnchors: an alarm is stamped with its device's owners when
// it is raised, and a scoped member sees, acknowledges and clears exactly its own.
func TestAlarmRaisedCarriesDeviceAnchors(t *testing.T) {
	r := newScopeAlarmRig(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	r.raise(t, "acme-pump", "acme-hot", t0)
	r.raise(t, "globex-pump", "globex-hot", t0)

	require.Equal(t, []string{"customer:acme"}, []string(r.alarm(t, "acme-hot").ScopeAnchors))
	require.Equal(t, []string{"customer:globex"}, []string(r.alarm(t, "globex-hot").ScopeAnchors))

	require.Equal(t, []string{"acme-hot"}, r.alarmKeys(t, r.member("customer:acme")))
	require.Equal(t, []string{"globex-hot"}, r.alarmKeys(t, r.member("customer:globex")))
	require.Equal(t, []string{"acme-hot", "globex-hot"}, r.alarmKeys(t, r.member("customer:acme", "customer:globex")))
	require.Empty(t, r.alarmKeys(t, r.member("group:acme")), "an anchor of another kind matched")
	// Negative control: the operator sees both.
	require.Equal(t, []string{"acme-hot", "globex-hot"}, r.alarmKeys(t, r.operator))

	// Filtering by an originator outside the scope finds nothing, as a missing one does.
	dt, other := "device", "globex-pump"
	found, err := r.api.Alarms(r.member("customer:acme"), AlarmSearchCriteria{OriginatorType: &dt, OriginatorToken: &other})
	require.NoError(t, err)
	require.Empty(t, found.Results)

	// Acknowledge and clear: their own, yes; another customer's is the store's miss.
	acme := r.member("customer:acme")
	by := "acme-operator"
	_, err = r.api.AcknowledgeAlarm(acme, r.alarm(t, "acme-hot").Token, &by)
	require.NoError(t, err)
	require.True(t, r.alarm(t, "acme-hot").Acknowledged)
	_, err = r.api.AcknowledgeAlarm(acme, r.alarm(t, "globex-hot").Token, &by)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound), "acknowledging globex's alarm: %v", err)
	require.False(t, r.alarm(t, "globex-hot").Acknowledged)
	bulk, err := r.api.AcknowledgeAlarms(acme, []string{r.alarm(t, "globex-hot").Token}, &by)
	require.NoError(t, err)
	require.Len(t, bulk.Refusals, 1, "a bulk acknowledge reached globex's alarm")
	require.False(t, r.alarm(t, "globex-hot").Acknowledged)
	_, err = r.api.ClearAlarm(acme, r.alarm(t, "globex-hot").Token)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound), "clearing globex's alarm: %v", err)
	_, err = r.api.ClearAlarm(acme, r.alarm(t, "acme-hot").Token)
	require.NoError(t, err)

	// A scoped write can never move the stamp, even on its own alarm.
	err = r.api.RDB.DB(acme).Model(r.alarm(t, "acme-hot")).
		Updates(map[string]any{"scope_anchors": []string{"customer:globex"}}).Error
	require.True(t, errors.Is(err, auth.ErrScopeUnsupported), "a scoped caller rewrote the stamp: %v", err)
}

// An alarm cycle carries the owners its device had when the cycle began: after a sale, the
// old owner keeps the cleared cycle it saw, and the new cycle is the new owner's.
func TestAReRaisedAlarmIsStampedWithTheNewOwner(t *testing.T) {
	r := newScopeAlarmRig(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	r.raise(t, "acme-pump", "hot", t0)
	r.resolve(t, "acme-pump", "hot", t0.Add(time.Minute))

	// Sold: acme's edge removed, globex's created.
	_, err := r.api.RemoveEntityRelationship(r.operator, "acme-pump-to-acme")
	require.NoError(t, err)
	r.assign(t, "acme-pump", "globex")

	r.raise(t, "acme-pump", "hot", t0.Add(2*time.Minute))
	require.Equal(t, []string{"customer:globex"}, []string(r.alarm(t, "hot").ScopeAnchors))
	require.Equal(t, []string{"hot"}, r.alarmKeys(t, r.member("customer:globex")))
	require.Empty(t, r.alarmKeys(t, r.member("customer:acme")))
	// The device itself moved too: current ownership, at once.
	d, err := r.api.DevicesByToken(r.member("customer:acme"), []string{"acme-pump"})
	require.NoError(t, err)
	require.Empty(t, d)
	d, err = r.api.DevicesByToken(r.member("customer:globex"), []string{"acme-pump"})
	require.NoError(t, err)
	require.Len(t, d, 1)
}

// The device rule on the real engine, beside the tenant predicate: a paged list and its
// count, by token, and an untracked edge that is not ownership.
func TestScopedDevicesOnPostgres(t *testing.T) {
	r := newScopeAlarmRig(t)
	_, err := r.api.CreateEntityRelationshipType(r.operator, &EntityRelationshipTypeCreateRequest{Token: "watches"})
	require.NoError(t, err)
	_, err = r.api.CreateEntityRelationship(r.operator, &EntityRelationshipCreateRequest{
		Token: "globex-watched-by-acme", SourceType: "device", Source: "globex-pump",
		TargetType: "customer", Target: "acme", RelationshipType: "watches"})
	require.NoError(t, err)

	found, err := r.api.Devices(r.member("customer:acme"), DeviceSearchCriteria{})
	require.NoError(t, err)
	require.Len(t, found.Results, 1)
	require.Equal(t, "acme-pump", found.Results[0].Token)
	require.EqualValues(t, 1, found.Pagination.TotalRecords, "the count saw past the scope")

	all, err := r.api.Devices(r.operator, DeviceSearchCriteria{})
	require.NoError(t, err)
	require.Len(t, all.Results, 2, "the unscoped control")
}

// TestScopedAlarmCountsByDeviceType: a scoped member's per-type count and type-filtered search
// cover exactly the alarms it can see, and agree with each other; the operator sees both.
func TestScopedAlarmCountsByDeviceType(t *testing.T) {
	r := newScopeAlarmRig(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	r.raise(t, "acme-pump", "acme-hot", t0)
	r.raise(t, "globex-pump", "globex-hot", t0)

	pump := "pump"
	count := func(ctx context.Context) int {
		found, err := r.api.AlarmCountsByDeviceType(ctx, AlarmCountCriteria{})
		require.NoError(t, err)
		n := 0
		for _, c := range found {
			require.Equal(t, "pump", c.DeviceType.Token)
			n += c.Count
		}
		return n
	}
	search := func(ctx context.Context) []string {
		found, err := r.api.Alarms(ctx, AlarmSearchCriteria{DeviceType: &pump})
		require.NoError(t, err)
		keys := []string{}
		for _, a := range found.Results {
			keys = append(keys, a.AlarmKey)
		}
		sort.Strings(keys)
		return keys
	}
	acme := r.member("customer:acme")
	require.Equal(t, 1, count(acme))
	require.Equal(t, []string{"acme-hot"}, search(acme))
	require.Equal(t, 0, count(r.member("group:acme")))
	require.Empty(t, search(r.member("group:acme")))
	// Negative control: the operator counts and finds both.
	require.Equal(t, 2, count(r.operator))
	require.Equal(t, []string{"acme-hot", "globex-hot"}, search(r.operator))
}

// TestScopedAlarmCountsBySeverity: a scoped member's per-severity counts cover exactly the alarms
// its search returns -- by the alarm's own stamp, whatever the originator -- and the type facet
// narrows them as the type-filtered search does.
func TestScopedAlarmCountsBySeverity(t *testing.T) {
	r := newScopeAlarmRig(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	r.raise(t, "acme-pump", "acme-hot", t0)
	r.raise(t, "globex-pump", "globex-hot", t0)

	pump := "pump"
	count := func(ctx context.Context, deviceType *string) int {
		found, err := r.api.AlarmCountsBySeverity(ctx, AlarmCountCriteria{}, deviceType)
		require.NoError(t, err)
		n := 0
		for _, c := range found {
			require.Equal(t, string(AlarmSeverityMajor), c.Severity)
			n += c.Count
		}
		return n
	}
	total := func(ctx context.Context) int {
		found, err := r.api.Alarms(ctx, AlarmSearchCriteria{})
		require.NoError(t, err)
		return int(found.Pagination.TotalRecords)
	}
	acme := r.member("customer:acme")
	require.Equal(t, 1, count(acme, nil))
	require.Equal(t, total(acme), count(acme, nil))
	require.Equal(t, 1, count(acme, &pump))
	require.Equal(t, 0, count(r.member("group:acme"), nil))
	// Negative control: the operator counts both.
	require.Equal(t, 2, count(r.operator, nil))
	require.Equal(t, 2, count(r.operator, &pump))
	require.Equal(t, total(r.operator), count(r.operator, nil))
}

// TestScopedAlarmTimeRangeOnPostgres: the alarm search's time range keeps the alarms whose span
// overlaps it, on the real engine and through the real raise and resolve path, and a scoped
// member's ranged search still finds only its own customer's alarms.
func TestScopedAlarmTimeRangeOnPostgres(t *testing.T) {
	r := newScopeAlarmRig(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	// acme: raised 3h ago, cleared 2h ago. globex: raised 3h ago, still active.
	r.raise(t, "acme-pump", "acme-hot", t0.Add(-3*time.Hour))
	r.resolve(t, "acme-pump", "acme-hot", t0.Add(-2*time.Hour))
	r.raise(t, "globex-pump", "globex-hot", t0.Add(-3*time.Hour))

	ranged := func(ctx context.Context, from, to time.Time) []string {
		t.Helper()
		f, e := from.Format(time.RFC3339), to.Format(time.RFC3339)
		found, err := r.api.Alarms(ctx, AlarmSearchCriteria{StartTime: &f, EndTime: &e})
		require.NoError(t, err)
		require.EqualValues(t, len(found.Results), found.Pagination.TotalRecords)
		keys := []string{}
		for _, a := range found.Results {
			keys = append(keys, a.AlarmKey)
		}
		sort.Strings(keys)
		return keys
	}
	// The last hour: acme's alarm was over, globex's raised before it is still active.
	require.Equal(t, []string{"globex-hot"}, ranged(r.operator, t0.Add(-time.Hour), t0))
	// 2.5h ago to 1h ago: acme's cleared inside the range, so it overlaps too.
	require.Equal(t, []string{"acme-hot", "globex-hot"}, ranged(r.operator, t0.Add(-150*time.Minute), t0.Add(-time.Hour)))
	// Before either was raised: nothing.
	require.Empty(t, ranged(r.operator, t0.Add(-5*time.Hour), t0.Add(-4*time.Hour)))

	// A scoped member's range is still its scope's.
	acme := r.member("customer:acme")
	require.Equal(t, []string{"acme-hot"}, ranged(acme, t0.Add(-150*time.Minute), t0.Add(-time.Hour)))
	require.Empty(t, ranged(acme, t0.Add(-time.Hour), t0))
	require.Equal(t, []string{"globex-hot"}, ranged(r.member("customer:globex"), t0.Add(-150*time.Minute), t0))

	// A re-raise reuses the row and nulls cleared_time: the alarm is active again from its new
	// raise, and the earlier cycle is no row any range can find.
	r.raise(t, "acme-pump", "acme-hot", t0.Add(-30*time.Minute))
	require.Equal(t, []string{"acme-hot", "globex-hot"}, ranged(r.operator, t0.Add(-10*time.Minute), t0))
	require.Equal(t, []string{"globex-hot"}, ranged(r.operator, t0.Add(-150*time.Minute), t0.Add(-time.Hour)))
}
