// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

func keyList(n int) []string {
	k := make([]string, n)
	for i := range k {
		k[i] = fmt.Sprintf("k-%d", i)
	}
	return k
}

// Every request-path batch-key lookup answers a key list at rdb.MaxLookupKeys and
// refuses one over it with the typed limit refusal (served as LIMIT_EXCEEDED), rather
// than handing an unbounded caller-supplied list to the database's IN predicate.
func TestKeyLookupsAreBounded(t *testing.T) {
	api := newPartialUpdateApi(t,
		&Alarm{}, &AreaType{}, &Area{}, &AssetType{}, &Asset{}, &CommandDefinition{},
		&DeviceProfile{}, &EntityRelationshipType{}, &EntityRelationship{}, &DeviceCredential{},
		&Device{}, &DeviceType{}, &CustomerType{}, &Customer{}, &DetectionRule{}, &GeoFence{},
		&EntityGroup{}, &MetricDefinition{}, &ProvisioningProfile{}, &EntityAttribute{})
	ctx := core.WithTenant(context.Background(), "acme")

	discard := func(_ any, err error) error { return err }
	lookups := map[string]func([]string) error{
		"AlarmsByToken":                  func(k []string) error { return discard(api.AlarmsByToken(ctx, k)) },
		"AreaTypesByToken":               func(k []string) error { return discard(api.AreaTypesByToken(ctx, k)) },
		"AreasByToken":                   func(k []string) error { return discard(api.AreasByToken(ctx, k)) },
		"AssetTypesByToken":              func(k []string) error { return discard(api.AssetTypesByToken(ctx, k)) },
		"AssetsByToken":                  func(k []string) error { return discard(api.AssetsByToken(ctx, k)) },
		"CommandDefinitionsByToken":      func(k []string) error { return discard(api.CommandDefinitionsByToken(ctx, k)) },
		"EntityRelationshipTypesByToken": func(k []string) error { return discard(api.EntityRelationshipTypesByToken(ctx, k)) },
		"EntityRelationshipsByToken":     func(k []string) error { return discard(api.EntityRelationshipsByToken(ctx, k)) },
		"DeviceCredentialsByToken":       func(k []string) error { return discard(api.DeviceCredentialsByToken(ctx, k)) },
		"CustomerTypesByToken":           func(k []string) error { return discard(api.CustomerTypesByToken(ctx, k)) },
		"CustomersByToken":               func(k []string) error { return discard(api.CustomersByToken(ctx, k)) },
		"DetectionRulesByToken":          func(k []string) error { return discard(api.DetectionRulesByToken(ctx, k)) },
		"DeviceTypesByToken":             func(k []string) error { return discard(api.DeviceTypesByToken(ctx, k)) },
		"DevicesByToken":                 func(k []string) error { return discard(api.DevicesByToken(ctx, k)) },
		"DevicesByExternalId":            func(k []string) error { return discard(api.DevicesByExternalId(ctx, k)) },
		"GeoFencesByToken":               func(k []string) error { return discard(api.GeoFencesByToken(ctx, k)) },
		"EntityGroupsByToken":            func(k []string) error { return discard(api.EntityGroupsByToken(ctx, k)) },
		"MetricDefinitionsByToken":       func(k []string) error { return discard(api.MetricDefinitionsByToken(ctx, k)) },
		"DeviceProfilesByToken":          func(k []string) error { return discard(api.DeviceProfilesByToken(ctx, k)) },
		"ProvisioningProfilesByToken":    func(k []string) error { return discard(api.ProvisioningProfilesByToken(ctx, k)) },
		"RemoveEntityRelationships":      func(k []string) error { return discard(api.RemoveEntityRelationships(ctx, k)) },
		"EntityAttributes.AttrKeys": func(k []string) error {
			return discard(api.EntityAttributes(ctx, EntityAttributeSearchCriteria{
				AttrKeys: &k, Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
			}))
		},
	}
	for name, call := range lookups {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, call(keyList(rdb.MaxLookupKeys)), "a list at the bound must be answered")

			err := call(keyList(rdb.MaxLookupKeys + 1))
			le, ok := limit.As(err)
			require.True(t, ok, "a list over the bound must be refused with the limit refusal, got %v", err)
			require.Equal(t, rdb.MaxLookupKeys+1, le.Got)
			require.Equal(t, rdb.MaxLookupKeys, le.Max)
		})
	}
}

// Below the bound a token lookup returns the same rows in the same order as before: the
// matching rows only, in storage (creation) order regardless of the key order, with
// unknown keys contributing nothing, and the preloaded association still populated.
func TestDevicesByTokenRowsAndOrder(t *testing.T) {
	api := newBulkDeviceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedType(t, api, ctx, "tracker", "tracker-profile")
	_, err := api.CreateDevicesFromTemplate(ctx, &DeviceBulkCreateRequest{
		DeviceTypeToken: "tracker", Count: 3, TokenTemplate: "fleet-{n:04d}",
	})
	require.NoError(t, err)

	found, err := api.DevicesByToken(ctx, []string{"fleet-0003", "zz", "fleet-0001"})
	require.NoError(t, err)
	got := make([]string, 0, len(found))
	for _, d := range found {
		got = append(got, d.Token)
		require.Equal(t, "tracker", d.DeviceType.Token, "the DeviceType preload must still apply")
	}
	require.Equal(t, []string{"fleet-0001", "fleet-0003"}, got)
}

// DevicesByExternalId matches on the external_id column, not the token: a device whose
// external id differs from its token is found by the former and not by the latter, in
// storage order, with the DeviceType preload still applied.
func TestDevicesByExternalIdRowsAndOrder(t *testing.T) {
	api := newBulkDeviceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedType(t, api, ctx, "tracker", "tracker-profile")
	_, err := api.CreateDevicesFromTemplate(ctx, &DeviceBulkCreateRequest{
		DeviceTypeToken: "tracker", Count: 3, TokenTemplate: "fleet-{n:04d}",
		ExternalIdTemplate: strp("vin-{n:04d}"),
	})
	require.NoError(t, err)

	found, err := api.DevicesByExternalId(ctx, []string{"vin-0003", "zz", "fleet-0002", "vin-0001"})
	require.NoError(t, err)
	got := make([]string, 0, len(found))
	for _, d := range found {
		got = append(got, d.Token+"="+d.ExternalId.String)
		require.Equal(t, "tracker", d.DeviceType.Token, "the DeviceType preload must still apply")
	}
	require.Equal(t, []string{"fleet-0001=vin-0001", "fleet-0003=vin-0003"}, got,
		"a token must not match the external_id lookup")
}
