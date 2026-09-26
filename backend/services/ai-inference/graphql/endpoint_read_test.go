// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"testing"

	"github.com/devicechain-io/dc-ai-inference/model"
)

// A provider with no endpoint override reads back `endpoint: null` — whether the column
// holds NULL, or the empty string a pod on an earlier release wrote during a rolling
// upgrade. The counterweight: a real override reads back exactly.
func TestALegacyEmptyEndpointReadsAsNull(t *testing.T) {
	for name, stored := range map[string]sql.NullString{
		"NULL":         {},
		"empty string": {String: "", Valid: true},
	} {
		r := &AIProviderResolver{M: model.AIProvider{Endpoint: stored}}
		if got := r.Endpoint(); got != nil {
			t.Errorf("%s: endpoint read back %q, want null", name, *got)
		}
	}
	r := &AIProviderResolver{M: model.AIProvider{Endpoint: sql.NullString{String: "https://proxy.example.invalid", Valid: true}}}
	if got := r.Endpoint(); got == nil || *got != "https://proxy.example.invalid" {
		t.Errorf("an override read back %v, want it exactly", got)
	}
}
