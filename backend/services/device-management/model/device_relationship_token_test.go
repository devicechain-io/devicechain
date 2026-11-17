// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	gql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/google/uuid"
)

func TestDeviceAuthoredRelationshipTokenPin(t *testing.T) {
	// Computed with the resolver's original UUIDv5 namespace and NUL-joined fields.
	if got := DeviceAuthoredRelationshipToken("acme", "dev", "customer", "bravo", "member"); got != "e87cb07e-ce08-5ed6-8d47-a570f01343c4" {
		t.Fatal(got)
	}
}

func trackingRequest(value bool) *EntityRelationshipTypeUpdateRequest {
	return &EntityRelationshipTypeUpdateRequest{Tracked: gql.OptionalBool{Set: true, Value: &value}}
}

func seedTrackingFixture(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	deviceType := &DeviceType{TokenReference: rdb.TokenReference{Token: "sensor"}}
	areaType := &AreaType{TokenReference: rdb.TokenReference{Token: "region"}}
	for _, row := range []any{deviceType, areaType} {
		if err := api.RDB.DB(ctx).Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []any{
		&Device{TokenReference: rdb.TokenReference{Token: "dev"}, DeviceTypeId: deviceType.ID},
		&Area{TokenReference: rdb.TokenReference{Token: "bravo"}, AreaTypeId: areaType.ID},
	} {
		if err := api.RDB.DB(ctx).Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}

	if _, err := api.CreateEntityRelationshipType(ctx, &EntityRelationshipTypeCreateRequest{Token: "custom"}); err != nil {
		t.Fatal(err)
	}
}

func trackingEdge(tenant string, authored bool, target string) *EntityRelationshipCreateRequest {
	token := uuid.NewString()
	if authored {
		token = DeviceAuthoredRelationshipToken(tenant, "dev", "area", target, "custom")
	}
	return &EntityRelationshipCreateRequest{DeviceAuthored: authored, Token: token, SourceType: "device", Source: "dev", TargetType: "area", Target: target, RelationshipType: "custom"}
}

func TestDeviceAuthoredEdgesBlockTracking(t *testing.T) {
	for _, n := range []int{1, deviceAuthoredEdgePageSize + 7} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			api := newPartialUpdateApi(t, &DeviceType{}, &Device{}, &AreaType{}, &Area{}, &EntityRelationshipType{}, &EntityRelationship{}, &PendingOwnershipFact{})
			ctx := core.WithTenant(context.Background(), "acme")
			seedTrackingFixture(t, api, ctx)
			for i := 0; i < n; i++ {
				target := "bravo"
				if i > 0 {
					target = fmt.Sprintf("area-%03d", i)
					if err := api.RDB.DB(ctx).Create(&Area{TokenReference: rdb.TokenReference{Token: target}, AreaTypeId: 1}).Error; err != nil {
						t.Fatal(err)
					}
				}
				if _, err := api.CreateEntityRelationship(ctx, trackingEdge("acme", true, target)); err != nil {
					t.Fatal(err)
				}
			}
			request := trackingRequest(true)
			request.Name = gql.OptionalString{Set: true, Value: new("must roll back")}
			_, err := api.UpdateEntityRelationshipType(ctx, "custom", request)
			var refusal *ErrDeviceAuthoredEdgesBlockTracking
			if !errors.As(err, &refusal) || refusal.Count != n {
				t.Fatalf("want %d blocked edges, got %v", n, err)
			}
			if len(refusal.Edges) > 5 || !strings.Contains(err.Error(), "device dev -> area bravo") || !strings.Contains(err.Error(), "delete those relationships, then mark the type tracked") {
				t.Fatal(err)
			}
			types, err := api.EntityRelationshipTypesByToken(ctx, []string{"custom"})
			if err != nil || types[0].Tracked || types[0].Name.Valid {
				t.Fatalf("refused update changed type: %v %v", types, err)
			}
			// Rename remains legal despite device-created edges.
			if _, err := api.UpdateEntityRelationshipType(ctx, "custom", &EntityRelationshipTypeUpdateRequest{Name: gql.OptionalString{Set: true, Value: new("renamed")}}); err != nil {
				t.Fatal(err)
			}
			if err := api.RDB.DB(ctx).Unscoped().Where("relationship_type_id = ?", types[0].ID).Delete(&EntityRelationship{}).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := api.UpdateEntityRelationshipType(ctx, "custom", trackingRequest(true)); err != nil {
				t.Fatal(err)
			}
			if _, err := api.UpdateEntityRelationshipType(ctx, "custom", trackingRequest(false)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdminDeviceSourceEdgeDoesNotBlockTracking(t *testing.T) {
	api := newPartialUpdateApi(t, &DeviceType{}, &Device{}, &AreaType{}, &Area{}, &EntityRelationshipType{}, &EntityRelationship{}, &PendingOwnershipFact{})
	ctx := core.WithTenant(context.Background(), "acme")
	seedTrackingFixture(t, api, ctx)
	if _, err := api.CreateEntityRelationship(ctx, trackingEdge("acme", false, "bravo")); err != nil {
		t.Fatal(err)
	}
	// A matching device-authored edge in another tenant must not block this tenant.
	other := core.WithTenant(context.Background(), "other")
	seedTrackingFixture(t, api, other)
	if _, err := api.CreateEntityRelationship(other, trackingEdge("other", true, "bravo")); err != nil {
		t.Fatal(err)
	}
	typ, err := api.UpdateEntityRelationshipType(ctx, "custom", trackingRequest(true))
	if err != nil || !typ.Tracked {
		t.Fatalf("admin edge blocked: %v", err)
	}
}
