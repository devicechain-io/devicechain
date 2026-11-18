// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
)

func TestRuleCountBoundAgreesWithThePublishBound(t *testing.T) {
	if MaxRulesPerValidation != dmmodel.MaxEnabledDetectionRulesPerPublish {
		t.Fatalf("MaxRulesPerValidation=%d and dmmodel.MaxEnabledDetectionRulesPerPublish=%d must agree: "+
			"a publish bound above the gate's bound allows a publish that the gate refuses, "+
			"surfacing as the fail-closed \"validation unavailable\"; below it, the author's "+
			"LIMIT_EXCEEDED names the wrong number", MaxRulesPerValidation, dmmodel.MaxEnabledDetectionRulesPerPublish)
	}
}
