// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
)

// IntegrationDecoderAnswer is what the ingest path learns about a device type's payload decoder.
type IntegrationDecoderAnswer struct {
	// Present is true when the active published version of the type's profile declares a decoder,
	// USABLE OR NOT: an uplink for a type whose decoder this build cannot run must still reach
	// the resolver, where it is recorded as a decode failure the tenant can see, rather than be
	// flattened here as though there were no decoder.
	Present bool
	// Input is the input kind an uplink must carry (FRAME, JSON or DECODED).
	Input string
	// MaxReadings is the decoder's static output count; 0 for a decoder this build cannot run.
	MaxReadings int
	// EstimatedCost is its static worst-case cost, the term of the ingest charge that prices work
	// that emits little or nothing; 0 for a decoder this build cannot run.
	EstimatedCost uint64
	// ProfileVersionToken names the version the answer is for ("{profile}@{version}").
	ProfileVersionToken string
}

// IntegrationDecoder answers whether a device type, as seen by an authenticated integration, has
// a payload decoder. It is the ingest path's one question about decoders, asked once per
// (tenant, device type) per cache window rather than once per uplink.
//
// It returns (nil, nil) for every failure to authenticate (no such integration, a wrong token, a
// switched-off integration), like the credential check beside it, so the answer says nothing
// about which. A device type outside the integration's scope, or one that does not exist, is
// "no decoder": the caller resolved the device through the same scope check, and a secret holder
// learns nothing about a type it may not reach.
//
// 🔴 IT READS THROUGH ProfileResolutionByDeviceType, the resolver's own cached read, so what is
// answered is what the resolver will run and the question costs a memory hit once warm. The api
// is a parameter so the service hands in the cached one and a test can hand in the plain one.
func IntegrationDecoder(ctx context.Context, api DeviceManagementApi, integrationToken, presented, deviceTypeToken string) (*IntegrationDecoderAnswer, error) {
	integration, err := api.AuthenticateIntegration(ctx, integrationToken, presented)
	switch {
	case err == nil:
	case errors.Is(err, ErrIntegrationAuthFailed), errors.Is(err, ErrIntegrationDisabled):
		return nil, nil
	default:
		return nil, err
	}
	types, err := api.DeviceTypesByToken(ctx, []string{deviceTypeToken})
	if err != nil {
		return nil, err
	}
	if len(types) != 1 || !integration.ScopeHas(types[0].ID) {
		return &IntegrationDecoderAnswer{}, nil
	}
	res, err := api.ProfileResolutionByDeviceType(ctx, types[0].ID)
	if err != nil {
		return nil, err
	}
	dec := res.Decoder()
	if !dec.Declared() {
		return &IntegrationDecoderAnswer{}, nil
	}
	return &IntegrationDecoderAnswer{
		Present:             true,
		Input:               dec.Input(),
		MaxReadings:         dec.MaxReadings(),
		EstimatedCost:       dec.EstimatedCost(),
		ProfileVersionToken: res.Scope().ProfileVersionToken,
	}, nil
}
