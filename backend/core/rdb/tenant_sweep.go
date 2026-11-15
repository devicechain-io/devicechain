// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// TenantsWith lists distinct tenant IDs having a row of model, in tenant order.
// This cross-tenant read bypasses isolation only to list IDs; callers must do
// each tenant's work under WithTenant. extra narrows the listing.
func (rdb *RdbManager) TenantsWith(ctx context.Context, model any, extra ...func(*gorm.DB) *gorm.DB) ([]string, error) {
	var tenants []string
	query := rdb.DB(core.WithSystemContext(ctx)).Model(model)
	for _, filter := range extra {
		query = filter(query)
	}
	err := query.Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error
	return tenants, err
}
