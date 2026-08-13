// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

// TestValidateCommandEnqueueExposesTheCodeOnTheWire drives the enqueue gate through
// the REAL schema, because the code's only purpose is to cross a service boundary.
//
// 🔴 A MODEL TEST CANNOT COVER THIS, and the gap is not theoretical: a resolver method
// that compiled, satisfied the schema parse, and returned null every time would leave
// the model's code perfectly correct and unreachable — command-delivery would relay an
// empty code, REACT would classify every rejection as unrecognized, and every
// permanently-invalid command would go back to being retried to the poison cap. The
// whole chain would be dead with every model assertion still green.
func TestValidateCommandEnqueueExposesTheCodeOnTheWire(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.Device{}, &model.DeviceType{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	ctx = withAuthorities(ctx, auth.DeviceRead)
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))

	// A variable, not a query literal — the path every real caller uses, including
	// command-delivery's validator.
	const query = `query($deviceToken: String!, $commandKey: String!, $payload: String) {
	  validateCommandEnqueue(deviceToken: $deviceToken, commandKey: $commandKey, payload: $payload) {
	    allowed
	    code
	    reason
	  }
	}`
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, query, "", map[string]any{
		"deviceToken": "ghost", "commandKey": "reboot", "payload": nil,
	})
	if len(res.Errors) > 0 {
		t.Fatalf("graphql errors: %v", res.Errors)
	}

	var out struct {
		Verdict struct {
			Allowed bool    `json:"allowed"`
			Code    *string `json:"code"`
			Reason  *string `json:"reason"`
		} `json:"validateCommandEnqueue"`
	}
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("decode verdict: %v", err)
	}
	if out.Verdict.Allowed {
		t.Fatal("a command to a nonexistent device must be rejected")
	}
	if out.Verdict.Code == nil {
		t.Fatal("the rejection reached the wire with a NULL code; a caller cannot classify it, " +
			"so every rejection degrades to 'retry until the poison cap gives up'")
	}
	// The literal, not the constant: this string is what crosses the boundary as JSON,
	// so renaming the constant must fail here rather than be absorbed on both sides.
	if *out.Verdict.Code != "DEVICE_NOT_FOUND" {
		t.Fatalf("wire code = %q, want DEVICE_NOT_FOUND", *out.Verdict.Code)
	}
	if out.Verdict.Reason == nil || *out.Verdict.Reason == "" {
		t.Fatal("the human reason must survive alongside the code; the code alone cannot tell " +
			"an operator WHICH device or command was refused")
	}
}
