// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/payloaddecode/plancost"
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
	// Cost is what EstimatedCost is made of, so that ingest prices an uplink by its actual size
	// (payloaddecode.Plan.CostFor) instead of charging the largest parse every time. The zero
	// value for a decoder this build cannot run.
	Cost plancost.Shape
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
		Cost:                dec.CostShape(),
		ProfileVersionToken: res.Scope().ProfileVersionToken,
	}, nil
}

// BrokerAssertedDeviceDecoder answers the same question as DeviceDecoder for a device whose identity
// the platform's own MQTT broker has already proven: the device authenticated at CONNECT and was
// confined by its grant to its own raw-payload topic, so there is no credential to present.
//
// 🔴 ONLY A SERVICE WITH device:read MAY ASK (the resolver enforces it), and the asker is event-sources
// reading the capture stream, which the broker alone writes a device's publish into. The answer is
// null for a device that does not exist, so the ingest path records the payload as undeliverable
// rather than carrying bytes for a device nobody registered. It reads through
// ProfileResolutionByDeviceType exactly as DeviceDecoder does, so what is answered is what the
// resolver will run.
func BrokerAssertedDeviceDecoder(ctx context.Context, api DeviceManagementApi, deviceToken string) (*IntegrationDecoderAnswer, error) {
	devices, err := api.DevicesByToken(ctx, []string{deviceToken})
	if err != nil {
		return nil, err
	}
	if len(devices) != 1 || devices[0] == nil {
		return nil, nil
	}
	return decoderAnswerForType(ctx, api, devices[0].DeviceTypeId)
}

// decoderAnswerForType is the plan of the active published profile version of a device type.
func decoderAnswerForType(ctx context.Context, api DeviceManagementApi, deviceTypeId uint) (*IntegrationDecoderAnswer, error) {
	res, err := api.ProfileResolutionByDeviceType(ctx, deviceTypeId)
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
		Cost:                dec.CostShape(),
		ProfileVersionToken: res.Scope().ProfileVersionToken,
	}, nil
}

// DeviceDecoder answers whether the type of the device with this token has a payload decoder, for
// a device that publishes its own payload, AFTER authenticating the credential the device presented.
//
// 🔴 THE CREDENTIAL IS CHECKED BEFORE ANYTHING ABOUT THE DEVICE IS SAID. It returns (nil, nil) for
// every failure to authenticate, whatever the cause (nothing presented, an unknown or disabled or
// expired credential, a wrong secret, a credential that belongs to another device or tenant, a
// device that does not exist), so the answer discloses neither whether a decoder is attached nor
// whether the device exists to a caller who does not hold the device's credential. The ingest route
// turns that one answer into one response.
//
// It authenticates whatever deviceAuthMode the instance runs: this wire has no unauthenticated
// form, because the answer selects a decoder and spends the profile's budgets on the device's
// behalf.
//
// It reads through ProfileResolutionByDeviceType like IntegrationDecoder, so what is answered is
// what the resolver will run.
func DeviceDecoder(ctx context.Context, api DeviceManagementApi, deviceToken string, presented *PresentedCredential) (*IntegrationDecoderAnswer, error) {
	device, err := api.AuthenticateDevice(ctx, presented, time.Now())
	switch {
	case err == nil:
	case IsCredentialRefusal(err), errors.Is(err, ErrCredentialMisconfigured):
		return nil, nil
	default:
		return nil, err
	}
	if device == nil || device.Token != deviceToken {
		return nil, nil
	}
	return decoderAnswerForType(ctx, api, device.DeviceTypeId)
}
