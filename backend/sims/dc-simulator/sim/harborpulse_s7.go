// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
)

const HarborpulseDefectIsolationCrossRead = "isolation-cross-read"
const HarborpulseDefectMCPWrongAnswer = "mcp-wrong-answer"
const HarborpulseTenantMarkerKey = "hp_tenant_marker"

// HarborpulseTenantMarker is an identity canary, not a simulator success counter.
// Its stored measurement distinguishes otherwise identical fleets across tenants.
func HarborpulseTenantMarker(tenant string) float64 {
	sum := sha256.Sum256([]byte(tenant))
	return float64(binary.BigEndian.Uint32(sum[:4]))
}

func HarborpulseTenantDescription(tenant string) string {
	return "Harborpulse isolation witness for tenant " + tenant
}

func (s *harborpulse) addS7Manifest(m *SimManifest) {
	for i := range m.Profiles {
		if m.Profiles[i].Token == HarborpulsePumpProfile {
			m.Profiles[i].Metrics = append(m.Profiles[i].Metrics, MetricSpec{
				Key: HarborpulseTenantMarkerKey, Name: "Tenant isolation witness", DataType: "DOUBLE",
			})
		}
	}
}

func (s *harborpulse) bootstrapS7(ctx context.Context, rt *Runtime) error {
	for _, d := range rt.deviceSnapshot() {
		var out any
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL,
			`mutation($token:String!,$description:String!){updateDevice(token:$token,request:{description:$description}){token}}`,
			map[string]any{"token": d.Token, "description": HarborpulseTenantDescription(rt.Tenant)}, &out); err != nil {
			return err
		}
	}
	return nil
}
