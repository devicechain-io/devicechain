// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intentseam_test

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/internal/intentseam"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/intent"
	"github.com/devicechain-io/dc-microservice/ledger"
)

// The seam repeats a few stored spellings because the model imports it; they must agree.
func TestVocabularyAgreesWithTheModel(t *testing.T) {
	for name, pair := range map[string][2]string{
		"dynamic membership":  {intentseam.DynamicMode, string(model.MembershipDynamic)},
		"shared scope":        {intentseam.SharedScope, string(model.AttributeScopeShared)},
		"device member type":  {intentseam.DeviceMemberType, string(entity.TypeDevice)},
		"group not found":     {intentseam.CodeGroupNotFound, string(model.RejectGroupNotFound)},
		"not a device group":  {intentseam.CodeGroupNotADeviceGroup, string(model.RejectGroupNotADeviceGroup)},
		"version not found":   {intentseam.CodeGroupVersionNotFound, string(model.RejectGroupVersionNotFound)},
		"group not versioned": {intentseam.CodeGroupNotVersioned, string(model.RejectGroupNotVersioned)},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: the seam says %q, the model %q", name, pair[0], pair[1])
		}
	}
	if intentseam.MaxPageRows*200 > intentseam.PageByteBudget {
		t.Errorf("a full page of %d rows cannot fit the %d-byte budget", intentseam.MaxPageRows, intentseam.PageByteBudget)
	}
	if intent.MaxLinesPerReconcile != ledger.MaxLinesPerOperation {
		t.Errorf("an intent's chunk bound %d is not the planner's %d", intent.MaxLinesPerReconcile, ledger.MaxLinesPerOperation)
	}
}
