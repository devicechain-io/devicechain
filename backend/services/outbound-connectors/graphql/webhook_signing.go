// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-outbound-connectors/model"
)

// GenerateWebhookSigningSecret mints a signing key for a handle and reveals it once.
func (r *SchemaResolver) GenerateWebhookSigningSecret(ctx context.Context, args struct {
	Handle string
}) (*IssuedWebhookSigningSecretResolver, error) {
	if err := auth.Authorize(ctx, auth.ConnectorWrite); err != nil {
		return nil, err
	}
	issued, err := r.GetApi(ctx).GenerateWebhookSigningSecret(ctx, args.Handle)
	if err != nil {
		return nil, err
	}
	return &IssuedWebhookSigningSecretResolver{M: *issued}, nil
}

// RotateWebhookSigningSecret installs a new key and reveals it once; the old one keeps signing.
func (r *SchemaResolver) RotateWebhookSigningSecret(ctx context.Context, args struct {
	Handle string
}) (*IssuedWebhookSigningSecretResolver, error) {
	if err := auth.Authorize(ctx, auth.ConnectorWrite); err != nil {
		return nil, err
	}
	issued, err := r.GetApi(ctx).RotateWebhookSigningSecret(ctx, args.Handle)
	if err != nil {
		return nil, err
	}
	return &IssuedWebhookSigningSecretResolver{M: *issued}, nil
}

// EndWebhookSigningRotation drops the previous key.
func (r *SchemaResolver) EndWebhookSigningRotation(ctx context.Context, args struct {
	Handle string
}) (*WebhookSigningKeyResolver, error) {
	if err := auth.Authorize(ctx, auth.ConnectorWrite); err != nil {
		return nil, err
	}
	status, err := r.GetApi(ctx).EndWebhookSigningRotation(ctx, args.Handle)
	if err != nil {
		return nil, err
	}
	return &WebhookSigningKeyResolver{M: *status}, nil
}

// DeleteWebhookSigningSecret removes a handle's keys.
func (r *SchemaResolver) DeleteWebhookSigningSecret(ctx context.Context, args struct {
	Handle string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.ConnectorWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteWebhookSigningSecret(ctx, args.Handle)
}

// WebhookSigningSecret reports a handle's status; null when it has no key.
func (r *SchemaResolver) WebhookSigningSecret(ctx context.Context, args struct {
	Handle string
}) (*WebhookSigningKeyResolver, error) {
	if err := auth.Authorize(ctx, auth.ConnectorRead); err != nil {
		return nil, err
	}
	status, err := r.GetApi(ctx).WebhookSigningSecretStatus(ctx, args.Handle)
	if err != nil || status == nil {
		return nil, err
	}
	return &WebhookSigningKeyResolver{M: *status}, nil
}

// WebhookSigningKeyResolver resolves WebhookSigningKey.
type WebhookSigningKeyResolver struct{ M model.WebhookSigningStatus }

func (r *WebhookSigningKeyResolver) Handle() string { return r.M.Handle }
func (r *WebhookSigningKeyResolver) Rotating() bool { return r.M.Rotating }

// IssuedWebhookSigningSecretResolver resolves IssuedWebhookSigningSecret.
type IssuedWebhookSigningSecretResolver struct {
	M model.IssuedWebhookSigningSecret
}

func (r *IssuedWebhookSigningSecretResolver) Handle() string { return r.M.Handle }
func (r *IssuedWebhookSigningSecretResolver) Secret() string { return r.M.Secret }
func (r *IssuedWebhookSigningSecretResolver) Rotating() bool { return r.M.Rotating }
