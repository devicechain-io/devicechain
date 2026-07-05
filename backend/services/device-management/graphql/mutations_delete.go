// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// This file holds the registry delete mutations. They are uniform — each
// authorizes device:write (the data-plane RBAC pattern, ADR-008/ADR-033 phase 5),
// then delegates to the model delete API, which owns the hard-delete + edge-
// cascade + referential-integrity semantics (see model/api_delete.go). Each
// returns whether a row was removed; a referential refusal surfaces as a GraphQL
// error (model.ErrEntityInUse).

// Delete a device type (refused while devices reference it).
func (r *SchemaResolver) DeleteDeviceType(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteDeviceType(ctx, args.Token)
}

// Delete a device profile (refused while device types reference it, ADR-045).
func (r *SchemaResolver) DeleteDeviceProfile(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteDeviceProfile(ctx, args.Token)
}

// Delete a device, cascade-removing its credentials and relationship edges.
func (r *SchemaResolver) DeleteDevice(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteDevice(ctx, args.Token)
}

// Delete a device group and its relationship edges.
func (r *SchemaResolver) DeleteDeviceGroup(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteDeviceGroup(ctx, args.Token)
}

// Delete a single device credential.
func (r *SchemaResolver) DeleteDeviceCredential(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteDeviceCredential(ctx, args.Token)
}

// Delete an asset type (refused while assets reference it).
func (r *SchemaResolver) DeleteAssetType(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteAssetType(ctx, args.Token)
}

// Delete an asset and its relationship edges.
func (r *SchemaResolver) DeleteAsset(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteAsset(ctx, args.Token)
}

// Delete an asset group and its relationship edges.
func (r *SchemaResolver) DeleteAssetGroup(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteAssetGroup(ctx, args.Token)
}

// Delete an area type (refused while areas reference it).
func (r *SchemaResolver) DeleteAreaType(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteAreaType(ctx, args.Token)
}

// Delete an area and its relationship edges.
func (r *SchemaResolver) DeleteArea(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteArea(ctx, args.Token)
}

// Delete an area group and its relationship edges.
func (r *SchemaResolver) DeleteAreaGroup(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteAreaGroup(ctx, args.Token)
}

// Delete a customer type (refused while customers reference it).
func (r *SchemaResolver) DeleteCustomerType(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteCustomerType(ctx, args.Token)
}

// Delete a customer and its relationship edges.
func (r *SchemaResolver) DeleteCustomer(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteCustomer(ctx, args.Token)
}

// Delete a customer group and its relationship edges.
func (r *SchemaResolver) DeleteCustomerGroup(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteCustomerGroup(ctx, args.Token)
}

// Delete a relationship type (refused while edges still use it).
func (r *SchemaResolver) DeleteEntityRelationshipType(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteEntityRelationshipType(ctx, args.Token)
}

// Remove a single relationship edge — the explicit "unassign" operation (ADR-013).
func (r *SchemaResolver) RemoveEntityRelationship(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).RemoveEntityRelationship(ctx, args.Token)
}

// Remove multiple relationship edges by token (bulk "remove members" / "unassign").
func (r *SchemaResolver) RemoveEntityRelationships(ctx context.Context, args struct{ Tokens []string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).RemoveEntityRelationships(ctx, args.Tokens)
}

// Delete a single metric definition.
func (r *SchemaResolver) DeleteMetricDefinition(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteMetricDefinition(ctx, args.Token)
}

// Delete a single command definition.
func (r *SchemaResolver) DeleteCommandDefinition(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteCommandDefinition(ctx, args.Token)
}

// Delete a single alarm definition.
func (r *SchemaResolver) DeleteAlarmDefinition(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteAlarmDefinition(ctx, args.Token)
}

// Delete a single provisioning profile.
func (r *SchemaResolver) DeleteProvisioningProfile(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteProvisioningProfile(ctx, args.Token)
}
