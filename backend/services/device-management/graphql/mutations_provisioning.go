// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/rs/zerolog/log"
)

// Create a new provisioning profile.
func (r *SchemaResolver) CreateProvisioningProfile(ctx context.Context, args struct {
	Request *model.ProvisioningProfileCreateRequest
}) (*ProvisioningProfileResolver, error) {
	api := r.GetApi(ctx)
	created, err := api.CreateProvisioningProfile(ctx, args.Request)
	if err != nil {
		return nil, err
	}
	return &ProvisioningProfileResolver{M: *created, S: r, C: ctx}, nil
}

// Update an existing provisioning profile.
func (r *SchemaResolver) UpdateProvisioningProfile(ctx context.Context, args struct {
	Token   string
	Request *model.ProvisioningProfileCreateRequest
}) (*ProvisioningProfileResolver, error) {
	api := r.GetApi(ctx)
	updated, err := api.UpdateProvisioningProfile(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}
	return &ProvisioningProfileResolver{M: *updated, S: r, C: ctx}, nil
}

// provisioningRejected is the single generic error a provisioning caller sees.
// The model returns distinct sentinels for operator-facing logs, but the
// device-facing response must not let a caller distinguish "no such key" from
// "wrong secret" from "disabled" from "not pre-provisioned" — any of which would
// let an attacker enumerate valid provision keys or device tokens. The specific
// reason is logged server-side.
var provisioningRejected = errors.New("provisioning request rejected")

// sanitizeProvisioningError collapses every provisioning policy sentinel to one
// generic error (logging the real reason), and passes any other error (e.g. an
// unexpected datastore failure, which is not an enumeration vector) through
// unchanged.
func sanitizeProvisioningError(err error) error {
	switch {
	case errors.Is(err, model.ErrProvisioningKeyNotResolved),
		errors.Is(err, model.ErrProvisioningDisabled),
		errors.Is(err, model.ErrProvisioningExpired),
		errors.Is(err, model.ErrProvisioningSecretMismatch),
		errors.Is(err, model.ErrProvisioningStrategyInvalid),
		errors.Is(err, model.ErrProvisioningDeviceNotPreProvisioned):
		log.Warn().Err(err).Msg("device provisioning request rejected")
		return provisioningRejected
	default:
		return err
	}
}

// Self-register a device against a provisioning profile (ADR-012). This is an
// unauthenticated entry point: a credential-less device presents only its
// provision key+secret, the tenant is resolved from the globally-unique key, and
// the key+secret is the gate. Every policy failure is collapsed to one generic
// error so the response cannot be used to enumerate keys.
func (r *SchemaResolver) ProvisionDevice(ctx context.Context, args struct {
	Request *model.ProvisionDeviceRequest
}) (*ProvisionDeviceResultResolver, error) {
	api := r.GetApi(ctx)
	result, err := api.ProvisionDeviceBootstrap(ctx, args.Request, time.Now())
	if err != nil {
		return nil, sanitizeProvisioningError(err)
	}
	return &ProvisionDeviceResultResolver{M: *result, S: r, C: ctx}, nil
}
