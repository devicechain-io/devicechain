// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// Create a trust anchor from a CA certificate.
func (r *SchemaResolver) CreateDeviceTrustAnchor(ctx context.Context, args struct {
	Request model.DeviceTrustAnchorCreateRequest
}) (*DeviceTrustAnchorResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	created, err := r.GetApi(ctx).CreateDeviceTrustAnchor(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &DeviceTrustAnchorResolver{M: *created, S: r, C: ctx}, nil
}

// Update a trust anchor. Disabling one ends the live sessions of the devices that rely on it.
func (r *SchemaResolver) UpdateDeviceTrustAnchor(ctx context.Context, args struct {
	Token   string
	Request model.DeviceTrustAnchorUpdateRequest
}) (*DeviceTrustAnchorResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	updated, err := r.GetApi(ctx).UpdateDeviceTrustAnchor(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &DeviceTrustAnchorResolver{M: *updated, S: r, C: ctx}, nil
}

// Delete a trust anchor nothing relies on.
func (r *SchemaResolver) DeleteDeviceTrustAnchor(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteDeviceTrustAnchor(ctx, args.Token)
}

// Revoke a certificate under a trust anchor.
func (r *SchemaResolver) RevokeDeviceCertificate(ctx context.Context, args struct {
	AnchorToken       string
	CertificatePem    *string
	FingerprintSha256 *string
	Reason            *string
}) (*DeviceRevokedCertificateResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	revoked, err := r.GetApi(ctx).RevokeDeviceCertificate(ctx, &model.DeviceCertificateRevokeRequest{
		AnchorToken: args.AnchorToken, CertificatePem: args.CertificatePem,
		FingerprintSha256: args.FingerprintSha256, Reason: args.Reason,
	})
	if err != nil {
		return nil, err
	}
	return &DeviceRevokedCertificateResolver{M: *revoked, S: r, C: ctx}, nil
}

// Remove a manual revocation.
func (r *SchemaResolver) UnrevokeDeviceCertificate(ctx context.Context, args struct {
	AnchorToken       string
	FingerprintSha256 string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).UnrevokeDeviceCertificate(ctx, args.AnchorToken, args.FingerprintSha256)
}
