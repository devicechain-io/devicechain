// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

// eventProcessingInfo is the identity and liveness probe: it answers through the served
// schema with the functional area, and (as its schema documents) reads no tenant data and
// needs no authority, so it answers a context that carries neither claims nor a tenant.
func TestEventProcessingInfoOnTheWire(t *testing.T) {
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(context.Background(), `{ eventProcessingInfo { functionalArea } }`, "", nil)
	if len(res.Errors) != 0 {
		t.Fatalf("eventProcessingInfo failed: %v", res.Errors)
	}
	var data struct {
		Info struct {
			FunctionalArea string `json:"functionalArea"`
		} `json:"eventProcessingInfo"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if data.Info.FunctionalArea != "event-processing" {
		t.Fatalf("functionalArea is %q, want %q", data.Info.FunctionalArea, "event-processing")
	}
	if string(res.Data) != `{"eventProcessingInfo":{"functionalArea":"event-processing"}}` {
		t.Fatalf("unexpected shape: %s", res.Data)
	}
}
