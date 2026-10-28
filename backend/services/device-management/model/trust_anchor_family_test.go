// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// ─── DeviceTrustAnchor ─────────────────────────────────────────────────────

func trustAnchorFamily() putest.Family[*Api] {
	const (
		nameSeed        = "Original name"
		descriptionSeed = "Original description"
		metadataSeed    = `{"fleet":"north"}`
	)
	return putest.Family[*Api]{
		Name:    "deviceTrustAnchor",
		Token:   "ta-1",
		Migrate: []any{&DeviceTrustAnchor{}, &DeviceCredential{}, &PendingSessionKick{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			_, err := api.CreateDeviceTrustAnchor(ctx, &DeviceTrustAnchorCreateRequest{
				Token: "ta-1", Name: strp(nameSeed), Description: strp(descriptionSeed),
				CertificatePem: newRootCA(t, "Seed Root").pem(), Metadata: strp(metadataSeed),
			})
			if err != nil {
				t.Fatalf("seed trust anchor: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.DeviceTrustAnchorsByToken(ctx, []string{"ta-1"})
			e := requireOne(t, "trust anchor", rows, err)
			return map[string]string{
				"name":        nullStr(e.Name),
				"description": nullStr(e.Description),
				"enabled":     boolStr(e.Enabled),
				"metadata":    jsonStr(e.Metadata),
			}
		},
		NewRequest: func() any { return new(DeviceTrustAnchorUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateDeviceTrustAnchor(ctx, token, req.(*DeviceTrustAnchorUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", nameSeed, "Renamed",
				func(r *DeviceTrustAnchorUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", descriptionSeed, "Rewritten",
				func(r *DeviceTrustAnchorUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			putest.RequiredBoolField("enabled", true,
				func(r *DeviceTrustAnchorUpdateRequest) *dcgraphql.OptionalBool { return &r.Enabled }),
			putest.OptionalStringField("metadata", metadataSeed, `{"fleet":"south"}`,
				func(r *DeviceTrustAnchorUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata }),
		},
	}
}
