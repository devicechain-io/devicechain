// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm/clause"
)

// TenantsWith lists distinct tenant IDs having a row of model, in tenant order.
// This cross-tenant read bypasses isolation only to list IDs; callers must do
// each tenant's work under WithTenant. Models without a tenant field are refused.
func (rdb *RdbManager) TenantsWith(ctx context.Context, model any) ([]string, error) {
	var tenants []string
	query := rdb.DB(core.WithSystemContext(ctx)).Model(model)
	if err := query.Statement.Parse(model); err != nil {
		return nil, err
	}
	field := schemaTenantField(query.Statement.Schema)
	if field == nil {
		return nil, fmt.Errorf("TenantsWith: model %T has no tenant field", model)
	}
	err := query.Distinct().Order(clause.OrderByColumn{Column: clause.Column{Name: field.DBName}}).Pluck(field.DBName, &tenants).Error
	return tenants, err
}
