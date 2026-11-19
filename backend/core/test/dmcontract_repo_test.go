// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import "testing"

// sanctionedDMModelImporters is every production file outside device-management that still
// imports its model package, with the exact model names it uses. It only shrinks.
var sanctionedDMModelImporters = map[string]DMModelSanction{
	"backend/services/event-processing/graphql/resolvers_preview_rule.go": {
		Names: []string{"CheckDraftCalculatedMetrics", "MetricDefinition", "NewProfileResolution", "ProfileResolution", "ProfileScope"},
		Reason: "replay preview builds a DRAFT profile resolution with device-management's own calculated-metric " +
			"compiler, so a preview computes what the resolver will once published; that code is model's domain, " +
			"not a wire shape, and leaves with the profile carve",
	},
	"backend/services/event-processing/internal/preview/preview.go": {
		Names:  []string{"ProfileResolution"},
		Reason: "replays stored events through the draft resolution above (ProfileResolution.Recalculate)",
	},
}

// The device-management wire-contract boundary (see dmcontract.go). It reads files in other
// modules, so -count=1 is load-bearing: Go's test cache does not track them.
func TestDeviceManagementModelStaysInsideDeviceManagement(t *testing.T) {
	scan, err := scanDMContractBoundary(workspaceRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range dmContractBoundaryErrors(scan, sanctionedDMModelImporters) {
		t.Error(issue)
	}
}
