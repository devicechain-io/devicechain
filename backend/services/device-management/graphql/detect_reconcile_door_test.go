// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The detection reconcile doors hand out a tenant's rules, devices and thresholds, so they take
// the authority every other device read takes and see only the caller's tenant.

// reconcileDoorApi seeds tenant "acme" with one published profile carrying a rule, one device on
// a type adopting it, and one threshold attribute, and returns the api.
func reconcileDoorApi(t *testing.T) *model.Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.Device{}, &model.DeviceType{}, &model.DeviceProfile{},
		&model.DeviceProfileVersion{}, &model.MetricDefinition{}, &model.CommandDefinition{},
		&model.DetectionRule{}, &model.DetectionRuleScopeRef{}, &model.EntityAttribute{},
		&model.EntityGroupMembership{}, &model.EntityGroupFacetRef{}))
	api := model.NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "acme")

	_, err = api.CreateDeviceProfile(ctx, &model.DeviceProfileCreateRequest{Token: "prof"})
	require.NoError(t, err)
	profiles, err := api.DeviceProfilesByToken(ctx, []string{"prof"})
	require.NoError(t, err)
	rule := &model.DetectionRule{DeviceProfileId: profiles[0].ID, Enabled: true,
		Definition: datatypes.JSON(`{"name":"hot","type":"threshold"}`)}
	rule.Token = "hot"
	require.NoError(t, api.RDB.DB(ctx).Create(rule).Error)
	_, err = api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	profileToken := "prof"
	_, err = api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "sensor", ProfileToken: &profileToken})
	require.NoError(t, err)
	_, err = api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: "d1", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	v := "50"
	_, err = api.SetEntityAttribute(ctx, &model.EntityAttributeSetRequest{EntityType: entity.TypeDevice.String(),
		Entity: "d1", Scope: string(model.AttributeScopeShared), AttrKey: "limit",
		ValueType: string(model.AttributeValueDouble), Value: &v})
	require.NoError(t, err)
	return api
}

func reconcileCtx(api *model.Api, tenant string) context.Context {
	return context.WithValue(core.WithTenant(context.Background(), tenant), gqlcore.ContextApiKey, api)
}

// callReconcileDoors calls every door and reports how many entries each returned (or its error).
func callReconcileDoors(ctx context.Context) (map[string]int, map[string]error) {
	r := &SchemaResolver{}
	counts, errs := map[string]int{}, map[string]error{}
	args := struct {
		AfterId *string
		Limit   int32
	}{Limit: 10}
	if page, err := r.ActiveProfileRules(ctx, args); err != nil {
		errs["activeProfileRules"] = err
	} else {
		counts["activeProfileRules"] = len(page.Entries())
	}
	if page, err := r.DeviceRosterPage(ctx, args); err != nil {
		errs["deviceRosterPage"] = err
	} else {
		counts["deviceRosterPage"] = len(page.Entries())
	}
	if page, err := r.DeviceThresholdAttributePage(ctx, args); err != nil {
		errs["deviceThresholdAttributePage"] = err
	} else {
		counts["deviceThresholdAttributePage"] = len(page.Entries())
	}
	return counts, errs
}

// No claims, no answer — from every door.
func TestReconcileDoorsRefuseUnauthenticated(t *testing.T) {
	_, errs := callReconcileDoors(reconcileCtx(reconcileDoorApi(t), "acme"))
	assert.Len(t, errs, 3, "every reconcile door must refuse a caller with no claims")
}

// device:read opens every door onto the caller's own tenant — with real rows behind it, so the
// admitted half is not passing on an empty answer — and a caller in another tenant sees none of
// them.
func TestReconcileDoorsAnswerOnlyTheCallersTenant(t *testing.T) {
	api := reconcileDoorApi(t)

	counts, errs := callReconcileDoors(withAuthorities(reconcileCtx(api, "acme"), auth.DeviceRead))
	require.Empty(t, errs)
	assert.Equal(t, map[string]int{"activeProfileRules": 1, "deviceRosterPage": 1, "deviceThresholdAttributePage": 1}, counts)

	counts, errs = callReconcileDoors(withAuthorities(reconcileCtx(api, "other"), auth.DeviceRead))
	require.Empty(t, errs)
	assert.Equal(t, map[string]int{"activeProfileRules": 0, "deviceRosterPage": 0, "deviceThresholdAttributePage": 0}, counts)
}

// A malformed cursor is refused, never read as "start over".
func TestReconcileDoorsRefuseAMalformedCursor(t *testing.T) {
	ctx := withAuthorities(reconcileCtx(reconcileDoorApi(t), "acme"), auth.DeviceRead)
	bad := "12abc"
	_, err := (&SchemaResolver{}).DeviceRosterPage(ctx, struct {
		AfterId *string
		Limit   int32
	}{AfterId: &bad, Limit: 10})
	assert.Error(t, err)
}
