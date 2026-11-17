// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
)

func TestTrackingDeviceCreatedRelationshipsAnswersReferenceViolation(t *testing.T) {
	ctx := conflictWireCtx(t)
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	if err := api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).AutoMigrate(&model.Device{}, &model.EntityRelationshipType{}, &model.EntityRelationship{}, &model.PendingOwnershipFact{}); err != nil {
		t.Fatal(err)
	}
	device := &model.Device{TokenReference: rdb.TokenReference{Token: "dev"}}
	typ := &model.EntityRelationshipType{TokenReference: rdb.TokenReference{Token: "custom"}}
	for _, row := range []any{device, typ} {
		if err := api.RDB.DB(ctx).Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	edge := &model.EntityRelationship{TokenReference: rdb.TokenReference{Token: model.DeviceAuthoredRelationshipToken("acme", "dev", "area", "bravo", "custom")}, SourceType: "device", SourceId: device.ID, TargetType: "area", TargetToken: "bravo", RelationshipTypeId: typ.ID}
	if err := api.RDB.DB(ctx).Create(edge).Error; err != nil {
		t.Fatal(err)
	}
	query := `mutation { updateEntityRelationshipType(token:"custom",request:{tracked:true}) { tracked } }`
	qe := onlyError(t, execServed(t, ctx, query, nil))
	if qe.Extensions["code"] != "REFERENCE_VIOLATION" || !strings.Contains(qe.Message, "device dev -> area bravo") || !strings.Contains(qe.Message, "delete those relationships") {
		t.Fatalf("unexpected refusal: %+v", qe)
	}
	if err := api.RDB.DB(ctx).Unscoped().Delete(edge).Error; err != nil {
		t.Fatal(err)
	}
	requireNoErrors(t, execServed(t, ctx, query, nil), "tracking after deletion")
}
