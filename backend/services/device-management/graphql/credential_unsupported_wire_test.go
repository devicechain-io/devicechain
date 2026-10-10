// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
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

const wireCreateCredential = `mutation ($request: DeviceCredentialCreateRequest) {
  createDeviceCredential(request: $request) { token } }`

// A credential of a type that is not accepted is answered with extensions.code
// UNSUPPORTED on the wire, through the served schema.
func TestCreatingAnX509CredentialAnswersUnsupported(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Device{}, &model.DeviceType{}, &model.DeviceProfile{},
		&model.DeviceProfileVersion{}, &model.DeviceCredential{}); err != nil {
		t.Fatal(err)
	}
	api := model.NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "dt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: "dev", DeviceTypeToken: "dt"}); err != nil {
		t.Fatal(err)
	}
	ctx = withAuthorities(ctx, auth.DeviceWrite, auth.DeviceRead)
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)

	qe := onlyError(t, execServed(t, ctx, wireCreateCredential, map[string]any{"request": map[string]any{
		"token": "c1", "deviceToken": "dev", "credentialType": "X509_CERTIFICATE",
		"credentialId": "AA:BB", "enabled": true}}))
	if got := qe.Extensions["code"]; got != "UNSUPPORTED" {
		t.Fatalf("extensions.code = %v, want UNSUPPORTED (message %q)", got, qe.Message)
	}
}
