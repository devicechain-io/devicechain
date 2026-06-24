// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
)

// ------------------
// Area type resolver
// ------------------

type EntityRelationshipResolver struct {
	M model.EntityRelationship
	S *SchemaResolver
	C context.Context
}

func (r *EntityRelationshipResolver) TargetDevice() *DeviceResolver {
	if r.M.TargetDevice != nil {
		return &DeviceResolver{
			M: *r.M.TargetDevice,
			S: r.S,
			C: r.C,
		}
	}
	return nil
}

func (r *EntityRelationshipResolver) TargetDeviceGroup() *DeviceGroupResolver {
	if r.M.TargetDeviceGroup != nil {
		return &DeviceGroupResolver{
			M: *r.M.TargetDeviceGroup,
			S: r.S,
			C: r.C,
		}
	}
	return nil
}

func (r *EntityRelationshipResolver) TargetAsset() *AssetResolver {
	if r.M.TargetAsset != nil {
		return &AssetResolver{
			M: *r.M.TargetAsset,
			S: r.S,
			C: r.C,
		}
	}
	return nil
}

func (r *EntityRelationshipResolver) TargetAssetGroup() *AssetGroupResolver {
	if r.M.TargetAssetGroup != nil {
		return &AssetGroupResolver{
			M: *r.M.TargetAssetGroup,
			S: r.S,
			C: r.C,
		}
	}
	return nil
}

func (r *EntityRelationshipResolver) TargetArea() *AreaResolver {
	if r.M.TargetArea != nil {
		return &AreaResolver{
			M: *r.M.TargetArea,
			S: r.S,
			C: r.C,
		}
	}
	return nil
}

func (r *EntityRelationshipResolver) TargetAreaGroup() *AreaGroupResolver {
	if r.M.TargetAreaGroup != nil {
		return &AreaGroupResolver{
			M: *r.M.TargetAreaGroup,
			S: r.S,
			C: r.C,
		}
	}
	return nil
}

func (r *EntityRelationshipResolver) TargetCustomer() *CustomerResolver {
	if r.M.TargetCustomer != nil {
		return &CustomerResolver{
			M: *r.M.TargetCustomer,
			S: r.S,
			C: r.C,
		}
	}
	return nil
}

func (r *EntityRelationshipResolver) TargetCustomerGroup() *CustomerGroupResolver {
	if r.M.TargetCustomerGroup != nil {
		return &CustomerGroupResolver{
			M: *r.M.TargetCustomerGroup,
			S: r.S,
			C: r.C,
		}
	}
	return nil
}
