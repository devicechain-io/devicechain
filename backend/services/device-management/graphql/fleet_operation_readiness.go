// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/ledger"
)

type FleetOperationReadinessResolver struct {
	protocolVersion int32
	enforcement     string
	executableKinds []string
	orphans         func() (int64, error)

	// The count is computed once per resolver: GraphQL aliases may select the field many times
	// in one request, and each selection must not run another scan.
	once       sync.Once
	orphanRows int64
	orphanErr  error
}

func (r *FleetOperationReadinessResolver) ProtocolVersion() int32    { return r.protocolVersion }
func (r *FleetOperationReadinessResolver) Enforcement() string       { return r.enforcement }
func (r *FleetOperationReadinessResolver) ExecutableKinds() []string { return r.executableKinds }

// OrphanedAttributeRows is read at its own field so a count that cannot be read becomes a null
// with an error on this field alone, never a response that hides the other facts.
func (r *FleetOperationReadinessResolver) OrphanedAttributeRows() (*int32, error) {
	r.once.Do(func() { r.orphanRows, r.orphanErr = r.orphans() })
	orphans, err := r.orphanRows, r.orphanErr
	if err != nil {
		return nil, err
	}
	if orphans > math.MaxInt32 || orphans < 0 {
		return nil, fmt.Errorf("orphaned attribute row count exceeds GraphQL Int range")
	}
	n := int32(orphans)
	return &n, nil
}

func (r *SchemaResolver) FleetOperationReadiness(ctx context.Context) (*FleetOperationReadinessResolver, error) {
	return r.BulkChangeReadiness(ctx)
}

func (r *SchemaResolver) BulkChangeReadiness(ctx context.Context) (*FleetOperationReadinessResolver, error) {
	// Authorize also refuses scoped members, using this service's existing rule.
	if err := auth.Authorize(ctx, auth.FleetOperate); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	enforcement := api.FleetOperationEnforcement(ctx)
	return &FleetOperationReadinessResolver{
		protocolVersion: model.FleetOperationWriterProtocolVersion,
		enforcement:     enforcement,
		executableKinds: ledger.ExecutableKinds(),
		orphans:         func() (int64, error) { return api.CountOrphanedDeviceAttributes(ctx) },
	}, nil
}
