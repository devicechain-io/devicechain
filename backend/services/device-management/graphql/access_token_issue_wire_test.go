// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A client that sets its own access token on a device — the round trip a presentation
// client makes instead of reading another credential's token, which a read can no longer
// return — relies on four things the wire must do, and this test asks the served schema
// for each, with the documents such a client sends:
//
//   - createDeviceCredential answers with the token it was given, at
//     data.createDeviceCredential.credentialId;
//   - updateDeviceCredential with a new credentialId rotates, answering with the new
//     token at data.updateDeviceCredential.credentialId, and the old token stops
//     authenticating at once;
//   - every later read of the credential answers with a fingerprint, never the token;
//   - none of it touches another credential on the same device.
const (
	issueExistsQuery = `query Creds($tokens: [String!]!) { deviceCredentialsByToken(tokens: $tokens) { token credentialType credentialId enabled device { token } } }`
	issueCreate      = `mutation CreateCred($r: DeviceCredentialCreateRequest) { createDeviceCredential(request: $r) { token credentialType credentialId enabled device { token } } }`
	issueRotate      = `mutation RotateCred($t: String!, $r: DeviceCredentialUpdateRequest!) { updateDeviceCredential(token: $t, request: $r) { token credentialType credentialId enabled device { token } } }`
)

type issuedRow struct {
	Token          string `json:"token"`
	CredentialType string `json:"credentialType"`
	CredentialID   string `json:"credentialId"`
	Enabled        bool   `json:"enabled"`
	Device         struct {
		Token string `json:"token"`
	} `json:"device"`
}

func TestAClientSetAccessTokenIsEchoedOnceAndRotatesAlone(t *testing.T) {
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
	// the credential another component (a simulator) presents for the same device
	const otherToken = "the-other-components-token-0001"
	if _, err := api.CreateDeviceCredential(ctx, &model.DeviceCredentialCreateRequest{Token: "dev-cred", DeviceToken: "dev",
		CredentialType: string(model.CredentialAccessToken), CredentialId: otherToken, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ctx = withAuthorities(ctx, auth.DeviceWrite, auth.DeviceRead)
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})

	exec := func(doc, field string, vars map[string]any) json.RawMessage {
		t.Helper()
		resp := schema.Exec(ctx, doc, "", vars)
		if len(resp.Errors) > 0 {
			t.Fatalf("%s: %v", field, resp.Errors)
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			t.Fatal(err)
		}
		return data[field]
	}
	readAll := func(tokens ...string) map[string]issuedRow {
		t.Helper()
		var rows []issuedRow
		if err := json.Unmarshal(exec(issueExistsQuery, "deviceCredentialsByToken", map[string]any{"tokens": toAny(tokens)}), &rows); err != nil {
			t.Fatal(err)
		}
		out := map[string]issuedRow{}
		for _, r := range rows {
			out[r.Token] = r
		}
		return out
	}
	authenticates := func(token string) bool {
		_, err := api.AuthenticateDevice(ctx, &model.PresentedCredential{
			CredentialType: string(model.CredentialAccessToken), CredentialId: token}, time.Now())
		return err == nil
	}
	issued := func(raw json.RawMessage) issuedRow {
		t.Helper()
		var r issuedRow
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}

	if got := readAll("dev-client"); len(got) != 0 {
		t.Fatalf("before create, the exists query answered %v", got)
	}
	otherBefore := readAll("dev-cred")["dev-cred"].CredentialID

	// create: the token is answered once, at data.createDeviceCredential.credentialId
	const first = "Zp0x9r3Qk2LmN8vB7cX6zA5sD4fG3hJ2kL1qW0eR9tY"
	created := issued(exec(issueCreate, "createDeviceCredential", map[string]any{"r": map[string]any{
		"token": "dev-client", "deviceToken": "dev", "credentialType": "ACCESS_TOKEN",
		"credentialId": first, "enabled": true}}))
	if created.CredentialID != first || created.Device.Token != "dev" || !created.Enabled || created.CredentialType != "ACCESS_TOKEN" {
		t.Fatalf("create answered %+v, want the token it was given on an enabled ACCESS_TOKEN of dev", created)
	}
	read := readAll("dev-client")["dev-client"]
	if read.CredentialID == first || !credential.IsAccessTokenDigest(read.CredentialID) {
		t.Fatalf("a read after create answered credentialId %q, want a fingerprint", read.CredentialID)
	}
	if !authenticates(first) {
		t.Fatal("the created token does not authenticate")
	}

	// rotate: the new token is answered at data.updateDeviceCredential.credentialId
	const second = "aB1cD2eF3gH4iJ5kL6mN7oP8qR9sT0uV1wX2yZ3-_00"
	rotated := issued(exec(issueRotate, "updateDeviceCredential", map[string]any{
		"t": "dev-client", "r": map[string]any{"credentialId": second, "enabled": true}}))
	if rotated.CredentialID != second || rotated.Device.Token != "dev" || !rotated.Enabled {
		t.Fatalf("rotate answered %+v, want the new token", rotated)
	}
	after := readAll("dev-client", "dev-cred")
	if got := after["dev-client"].CredentialID; got == second || !credential.IsAccessTokenDigest(got) || got == read.CredentialID {
		t.Fatalf("a read after rotate answered credentialId %q, want a NEW fingerprint", got)
	}
	if authenticates(first) {
		t.Fatal("the rotated-out token still authenticates")
	}
	if !authenticates(second) {
		t.Fatal("the rotated-in token does not authenticate")
	}

	// the other credential on the device is untouched
	if !authenticates(otherToken) {
		t.Fatal("rotating dev-client broke dev-cred")
	}
	if got := after["dev-cred"].CredentialID; got != otherBefore || !after["dev-cred"].Enabled {
		t.Fatalf("dev-cred changed: fingerprint %q -> %q, enabled %v", otherBefore, got, after["dev-cred"].Enabled)
	}
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
