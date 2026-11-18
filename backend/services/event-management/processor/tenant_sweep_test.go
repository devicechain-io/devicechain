// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"reflect"
	"testing"

	emmodel "github.com/devicechain-io/dc-event-management/model"
	emtest "github.com/devicechain-io/dc-event-management/test"
	"github.com/devicechain-io/dc-microservice/core"
)

type isolatedAnchorAPI struct {
	emtest.MockApi
	visited []string
}

func (a *isolatedAnchorAPI) DistinctAnchorTargetsAfter(ctx context.Context, _, _ string) ([]emmodel.AnchorRef, error) {
	tenant, _ := core.TenantFromContext(ctx)
	a.visited = append(a.visited, tenant)
	if tenant == "A" {
		return nil, errors.New("tenant A read failed")
	}
	return nil, nil
}

func TestAnchorSweepIsolatesTenantFailure(t *testing.T) {
	api := &isolatedAnchorAPI{}
	api.Mock.On("DistinctAnchorTenants").Return([]string{"A", "B"}, nil)
	api.Mock.On("DistinctAnchorDeviceTokensAfter", "").Return([]emmodel.AnchorRef{}, nil).Once()
	s := &AnchorReconciliationSweep{Api: api}
	err := s.runOnce(context.Background())
	if !errors.Is(err, core.ErrPassPartial) || !reflect.DeepEqual(api.visited, []string{"A", "B"}) {
		t.Fatalf("visited %v, error %v; want both tenants and partial", api.visited, err)
	}
	api.Mock.AssertExpectations(t)
}
