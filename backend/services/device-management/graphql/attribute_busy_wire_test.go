// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func TestAttributeWriteBusyReachesAllInteractiveMutations(t *testing.T) {
	ctx := deviceConfigurationCtx(t)
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	value := "30"
	if _, err := api.SetEntityAttribute(ctx, &model.EntityAttributeSetRequest{EntityType: "device", Entity: "dev", Scope: "SHARED", AttrKey: "interval", ValueType: "LONG", Value: &value}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: "other", DeviceTypeToken: "sensor"}); err != nil {
		t.Fatal(err)
	}
	if err := api.RDB.Database.Callback().Query().Before("gorm:query").Register("force_attribute_row_busy", func(db *gorm.DB) {
		if db.Statement.Schema != nil && db.Statement.Schema.Name == "EntityAttribute" {
			db.AddError(&pgconn.PgError{Code: "55P03"})
		}
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`mutation { setEntityAttribute(request:{entityType:"device",entity:"dev",scope:"CLIENT",attrKey:"k",valueType:"STRING",value:"v"}) { attrKey } }`,
		`mutation { deleteEntityAttribute(entityType:"device",entity:"dev",scope:"CLIENT",attrKey:"k") }`,
		`mutation { restoreDeviceConfiguration(deviceToken:"dev",revision:1) { stale } }`,
		`mutation { copyDeviceConfiguration(fromDeviceToken:"dev",toDeviceToken:"other") { stale } }`,
	} {
		qe := onlyError(t, execServed(t, ctx, query, nil))
		if qe.Extensions["code"] != "ATTRIBUTE_WRITE_BUSY" {
			t.Errorf("%s: extensions=%v, message=%s", query, qe.Extensions, qe.Message)
		}
	}
}
