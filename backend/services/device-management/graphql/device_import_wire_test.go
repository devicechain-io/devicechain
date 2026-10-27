// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const wireImportDevices = `mutation ($rows: [DeviceImportRow!]!, $mode: DeviceImportMode!, $dryRun: Boolean!) {
  importDevices(rows: $rows, mode: $mode, dryRun: $dryRun) {
    dryRun created exists conflict invalid refused
    rows { row token outcome code message }
  } }`

func importWireCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Device{}, &model.DeviceAlias{}, &model.DeviceType{}, &model.DeviceProfile{},
		&model.DeviceProfileVersion{}, &model.DeviceProfileActivation{}, &model.DeviceCredential{}, &model.ProvisioningProfile{}); err != nil {
		t.Fatal(err)
	}
	api := keyedApi(db)
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "sensor"}); err != nil {
		t.Fatal(err)
	}
	return context.WithValue(ctx, gqlcore.ContextApiKey, api)
}

func execImport(t *testing.T, ctx context.Context, vars map[string]any) (json.RawMessage, string) {
	t.Helper()
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, wireImportDevices, "", vars)
	if len(res.Errors) > 0 {
		return nil, res.Errors[0].Message
	}
	return res.Data, ""
}

func TestImportDevicesOverTheWire(t *testing.T) {
	const secret = "wire-secret-0123456789"
	ctx := withAuthorities(importWireCtx(t), auth.DeviceWrite)
	vars := map[string]any{"mode": "FACTORY_CREDENTIALS", "dryRun": false, "rows": []any{
		map[string]any{"token": "d1", "deviceTypeToken": "sensor", "credentialType": "MQTT_BASIC",
			"credentialId": "u1", "credentialValue": secret},
		map[string]any{"token": "d2", "deviceTypeToken": "missing", "credentialType": "ACCESS_TOKEN", "credentialId": "t2"},
	}}
	data, msg := execImport(t, ctx, vars)
	if msg != "" {
		t.Fatalf("importDevices: %s", msg)
	}
	var out struct {
		ImportDevices struct {
			Created, Invalid int
			Rows             []struct {
				Row            int
				Token, Outcome string
				Code           *string
			}
		}
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	got := out.ImportDevices
	if got.Created != 1 || got.Invalid != 1 || got.Rows[0].Outcome != "CREATED" || got.Rows[1].Outcome != "INVALID" ||
		got.Rows[1].Code == nil || *got.Rows[1].Code != "NOT_FOUND" {
		t.Fatalf("unexpected result: %s", data)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("the response carries the secret: %s", data)
	}

	// A re-run over the wire is all EXISTS for the row that landed.
	data, msg = execImport(t, ctx, vars)
	if msg != "" || !strings.Contains(string(data), `"exists":1`) {
		t.Fatalf("re-run: %q %s", msg, data)
	}
}

// A misnamed row field is a request error, not a row quietly missing a column.
func TestImportDevicesRejectsAnUnknownRowField(t *testing.T) {
	ctx := withAuthorities(importWireCtx(t), auth.DeviceWrite)
	_, msg := execImport(t, ctx, map[string]any{"mode": "NONE", "dryRun": true, "rows": []any{
		map[string]any{"token": "d1", "deviceTypeToken": "sensor", "deviceType": "sensor"}}})
	if msg == "" {
		t.Fatal("an unknown input field was accepted")
	}
}

func TestImportDevicesNeedsDeviceWrite(t *testing.T) {
	vars := map[string]any{"mode": "NONE", "dryRun": true, "rows": []any{
		map[string]any{"token": "d1", "deviceTypeToken": "sensor"}}}

	_, msg := execImport(t, withAuthorities(importWireCtx(t), viewerBaseline...), vars)
	if msg == "" || strings.Contains(msg, "created") {
		t.Fatalf("a viewer was admitted: %q", msg)
	}
	if _, msg = execImport(t, importWireCtx(t), vars); msg == "" {
		t.Fatal("an anonymous caller was admitted")
	}
}

func TestImportDevicesRefusesMoreThanTheCapOnTheWire(t *testing.T) {
	ctx := withAuthorities(importWireCtx(t), auth.DeviceWrite)
	rows := make([]any, 1001)
	for i := range rows {
		rows[i] = map[string]any{"token": "d", "deviceTypeToken": "sensor"}
	}
	_, msg := execImport(t, ctx, map[string]any{"mode": "NONE", "dryRun": true, "rows": rows})
	if !strings.Contains(msg, "1000") {
		t.Fatalf("1001 rows: %q", msg)
	}
}
