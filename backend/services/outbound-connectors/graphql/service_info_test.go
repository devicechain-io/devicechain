// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

// outboundConnectorsInfo identifies the service answering: it returns the area the resolver
// was constructed with, and (as its schema documents) needs no specific authority, so it
// answers a context that carries neither claims nor a tenant nor an Api.
func TestOutboundConnectorsInfoOnTheWire(t *testing.T) {
	for _, area := range []string{"outbound-connectors", "another-area"} {
		schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{Area: area})
		res := schema.Exec(context.Background(), `{ outboundConnectorsInfo { area } }`, "", nil)
		if len(res.Errors) != 0 {
			t.Fatalf("outboundConnectorsInfo failed: %v", res.Errors)
		}
		var data struct {
			Info struct {
				Area string `json:"area"`
			} `json:"outboundConnectorsInfo"`
		}
		if err := json.Unmarshal(res.Data, &data); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if data.Info.Area != area {
			t.Fatalf("area is %q, want %q", data.Info.Area, area)
		}
	}
}
