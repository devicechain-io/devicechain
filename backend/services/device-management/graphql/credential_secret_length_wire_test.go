// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const wireUpdateCredential = `mutation ($token: String!, $request: DeviceCredentialUpdateRequest!) {
  updateDeviceCredential(token: $token, request: $request) { token } }`

// A too-short MQTT_BASIC secret is answered with extensions.code INVALID_VALUE on the wire,
// on create and on rotation, through the served schema, and the message names the floor
// and never the secret.
func TestAShortMqttPasswordAnswersInvalidValueOnTheWire(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Device{}, &model.DeviceType{}, &model.DeviceProfile{},
		&model.DeviceProfileVersion{}, &model.DeviceProfileActivation{}, &model.DeviceCredential{}); err != nil {
		t.Fatal(err)
	}
	api := model.NewApi(&rdb.RdbManager{Database: db})
	api.DeviceSecretKey = dmtest.DeviceSecretKey()
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "dt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: "dev", DeviceTypeToken: "dt"}); err != nil {
		t.Fatal(err)
	}
	// An ACCESS_TOKEN credential: the floor is the password's, not every credential's.
	if _, err := api.CreateDeviceCredential(ctx, &model.DeviceCredentialCreateRequest{Token: "tok", DeviceToken: "dev",
		CredentialType: string(model.CredentialAccessToken), CredentialId: "tok-id", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ctx = withAuthorities(ctx, auth.DeviceWrite, auth.DeviceRead)
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)

	const short = "fifteen-bytes-x"
	qe := onlyError(t, execServed(t, ctx, wireCreateCredential, map[string]any{"request": map[string]any{
		"token": "c1", "deviceToken": "dev", "credentialType": "MQTT_BASIC",
		"credentialId": "u1", "credentialValue": short, "enabled": true}}))
	if got := qe.Extensions["code"]; got != "INVALID_VALUE" {
		t.Fatalf("create: extensions.code = %v, want INVALID_VALUE (message %q)", got, qe.Message)
	}
	if !strings.Contains(qe.Message, "16") || strings.Contains(qe.Message, short) {
		t.Fatalf("create: message %q must name the floor and never the secret", qe.Message)
	}

	qe = onlyError(t, execServed(t, ctx, wireUpdateCredential, map[string]any{"token": "tok", "request": map[string]any{
		"credentialType": "MQTT_BASIC", "credentialValue": short}}))
	if got := qe.Extensions["code"]; got != "INVALID_VALUE" {
		t.Fatalf("rotate: extensions.code = %v, want INVALID_VALUE (message %q)", got, qe.Message)
	}
}
