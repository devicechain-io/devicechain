// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/entity"
)

func uptr(v uint) *uint { return &v }

// resolvedAnchor must collapse whichever single Target* is set into the matching
// (anchor_type, anchor_id) pair, and yield nil/nil when none is set.
func TestResolvedAnchor(t *testing.T) {
	cases := []struct {
		name     string
		event    dmmodel.ResolvedEvent
		wantType *entity.Type
		wantId   *uint
	}{
		{"device", dmmodel.ResolvedEvent{TargetDeviceId: uptr(7)}, ptrType(entity.TypeDevice), uptr(7)},
		{"devicegroup", dmmodel.ResolvedEvent{TargetDeviceGroupId: uptr(8)}, ptrType(entity.TypeDeviceGroup), uptr(8)},
		{"asset", dmmodel.ResolvedEvent{TargetAssetId: uptr(9)}, ptrType(entity.TypeAsset), uptr(9)},
		{"assetgroup", dmmodel.ResolvedEvent{TargetAssetGroupId: uptr(10)}, ptrType(entity.TypeAssetGroup), uptr(10)},
		{"customer", dmmodel.ResolvedEvent{TargetCustomerId: uptr(11)}, ptrType(entity.TypeCustomer), uptr(11)},
		{"customergroup", dmmodel.ResolvedEvent{TargetCustomerGroupId: uptr(12)}, ptrType(entity.TypeCustomerGroup), uptr(12)},
		{"area", dmmodel.ResolvedEvent{TargetAreaId: uptr(13)}, ptrType(entity.TypeArea), uptr(13)},
		{"areagroup", dmmodel.ResolvedEvent{TargetAreaGroupId: uptr(14)}, ptrType(entity.TypeAreaGroup), uptr(14)},
		{"none", dmmodel.ResolvedEvent{}, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotType, gotId := resolvedAnchor(tc.event)
			if tc.wantType == nil {
				if gotType != nil || gotId != nil {
					t.Fatalf("expected nil anchor, got type=%v id=%v", gotType, gotId)
				}
				return
			}
			if gotType == nil || *gotType != string(*tc.wantType) {
				t.Fatalf("anchor type = %v, want %q", gotType, *tc.wantType)
			}
			if gotId == nil || *gotId != *tc.wantId {
				t.Fatalf("anchor id = %v, want %d", gotId, *tc.wantId)
			}
		})
	}
}

func ptrType(t entity.Type) *entity.Type { return &t }
