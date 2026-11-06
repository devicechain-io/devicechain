// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// ─── DeviceTrustAnchor ─────────────────────────────────────────────────────

func trustAnchorFamily() putest.Family[*Api] {
	const (
		nameSeed        = "Original name"
		descriptionSeed = "Original description"
		metadataSeed    = `{"fleet":"north"}`
		graceSeed       = "12h0m0s"
	)
	return putest.Family[*Api]{
		Name:  "deviceTrustAnchor",
		Token: "ta-1",
		Migrate: []any{&DeviceTrustAnchor{}, &DeviceCredential{}, &PendingSessionKick{},
			&DeviceRevocationIssuer{}, &DeviceRevocationList{}, &DeviceRevokedCertificate{}, &DeviceCredentialChainLink{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			ca := newRootCA(t, "Seed Root")
			_, err := api.CreateDeviceTrustAnchor(ctx, &DeviceTrustAnchorCreateRequest{
				Token: "ta-1", Name: strp(nameSeed), Description: strp(descriptionSeed),
				CertificatePem: ca.pem(), Metadata: strp(metadataSeed),
			})
			if err != nil {
				t.Fatalf("seed trust anchor: %v", err)
			}
			// A current list, so that switching the mode to REQUIRE_CURRENT (which the family does)
			// is a switch the guard allows, and a grace the anchor sets.
			if _, err := api.PutDeviceRevocationList(ctx, &RevocationListPutRequest{AnchorToken: "ta-1", Crl: ca.crl(t, crlSpec{})}); err != nil {
				t.Fatalf("seed revocation list: %v", err)
			}
			if _, err := api.UpdateDeviceTrustAnchor(ctx, "ta-1", &DeviceTrustAnchorUpdateRequest{
				RevocationStaleGrace: dcgraphql.OptionalStringOf(graceSeed)}); err != nil {
				t.Fatalf("seed grace: %v", err)
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
				"revocationMode": func() string {
					m, err := e.RevocationModeOf()
					if err != nil {
						t.Fatalf("revocation mode: %v", err)
					}
					return string(m)
				}(),
				"revocationStaleGrace": func() string {
					if !e.RevocationStaleGrace.Valid {
						return nullMarker
					}
					return (time.Duration(e.RevocationStaleGrace.Int64) * time.Second).String()
				}(),
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
			{
				Name: "revocationMode", Seeded: "ENTRIES", Replace: "REQUIRE_CURRENT", Kind: putest.RequiredValue,
				Set: func(req any, v string) {
					req.(*DeviceTrustAnchorUpdateRequest).RevocationMode = OptionalRevocationMode{Set: true, Value: &v}
				},
				SetNull: func(req any) {
					req.(*DeviceTrustAnchorUpdateRequest).RevocationMode = OptionalRevocationMode{Set: true}
				},
			},
			putest.OptionalStringField("revocationStaleGrace", graceSeed, "6h0m0s",
				func(r *DeviceTrustAnchorUpdateRequest) *dcgraphql.OptionalString { return &r.RevocationStaleGrace }),
		},
	}
}
