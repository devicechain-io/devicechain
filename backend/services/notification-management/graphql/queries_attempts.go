// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-notification-management/model"
)

// NotificationAttempts searches the delivery ledger.
func (r *SchemaResolver) NotificationAttempts(ctx context.Context, args struct {
	Criteria model.NotificationAttemptSearchCriteria
}) (*NotificationAttemptSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).NotificationAttempts(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &NotificationAttemptSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
