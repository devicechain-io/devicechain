// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// DeviceReplacements lists the device-replacement journal (ADR-074), newest first,
// optionally narrowed to one device.
//
// Gated on device:READ, not device:write, and the difference from the credential
// queries next door is the point: those return credential ids, which for an
// ACCESS_TOKEN are the device's bearer, so they had to be raised to device:write.
// A replacement record names credentials only by their ENTITY TOKENS — see
// DeviceReplacement.RetiredCredentialTokens and NewCredentialToken — so there is no
// bearer in it to protect, and a maintenance history an operator cannot read is a
// history that answers nobody's question.
func (r *SchemaResolver) DeviceReplacements(ctx context.Context, args struct {
	Criteria model.DeviceReplacementSearchCriteria
}) (*DeviceReplacementSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DeviceReplacements(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &DeviceReplacementSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
