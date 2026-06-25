// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// --------------------------
// Device claim resolver
// --------------------------

type DeviceClaimResolver struct {
	M model.DeviceClaim
	S *SchemaResolver
	C context.Context
}

func (r *DeviceClaimResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceClaimResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceClaimResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceClaimResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceClaimResolver) Status() string {
	return r.M.Status
}

func (r *DeviceClaimResolver) ExpiresAt() *string {
	if r.M.ExpiresAt.Valid {
		return util.FormatTime(r.M.ExpiresAt.Time)
	}
	return nil
}

func (r *DeviceClaimResolver) ClaimedTime() *string {
	if r.M.ClaimedTime.Valid {
		return util.FormatTime(r.M.ClaimedTime.Time)
	}
	return nil
}

func (r *DeviceClaimResolver) Device() *DeviceResolver {
	if r.M.Device != nil {
		return &DeviceResolver{
			M: *r.M.Device,
			S: r.S,
			C: r.C,
		}
	}
	ids := []string{fmt.Sprintf("%d", r.M.DeviceId)}
	rez, err := r.S.DevicesById(r.C, struct{ Ids []string }{Ids: ids})
	if err != nil || len(rez) == 0 {
		return nil
	}
	return rez[0]
}

// ClaimedByCustomer returns the customer the claim was redeemed by, or nil for an
// unredeemed (open or canceled) claim.
func (r *DeviceClaimResolver) ClaimedByCustomer() *CustomerResolver {
	if !r.M.ClaimedByCustomerId.Valid {
		return nil
	}
	ids := []string{fmt.Sprintf("%d", r.M.ClaimedByCustomerId.Int64)}
	rez, err := r.S.CustomersById(r.C, struct{ Ids []string }{Ids: ids})
	if err != nil || len(rez) == 0 {
		return nil
	}
	return rez[0]
}
