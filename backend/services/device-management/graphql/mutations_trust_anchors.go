// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"

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
	// The revocation is saved even when its sessions could not be confirmed ended; that is the
	// result's sessionsEnded, not a failed mutation (an error would null the result and hide that
	// the revocation stands).
	ended, err := model.SessionsEndedOf(err)
	if err != nil {
		return nil, err
	}
	if revoked == nil {
		return nil, errors.New("the revocation returned no entry")
	}
	return &DeviceRevokedCertificateResolver{M: *revoked, S: r, C: ctx, Ended: &ended}, nil
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

// Store a CRL as the current list of its issuer under a trust anchor.
func (r *SchemaResolver) PutDeviceRevocationList(ctx context.Context, args struct {
	AnchorToken    string
	CrlPem         string
	IssuerChainPem *string
}) (*DeviceRevocationListResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	// The text bound is checked here as well as in the model: the argument is already in memory,
	// but nothing past this point should copy, parse or hold an oversized one.
	if bound := api.GraphqlCrlTextBound(); len(args.CrlPem) > bound {
		return nil, model.CrlTooLargeRefusal(bound)
	}
	chain := ""
	if args.IssuerChainPem != nil {
		chain = *args.IssuerChainPem
	}
	res, err := api.PutDeviceRevocationList(ctx, &model.RevocationListPutRequest{
		AnchorToken: args.AnchorToken, Crl: []byte(args.CrlPem), IssuerChainPem: chain,
		MaxBytes: api.GraphqlCrlTextBound(),
	})
	if err != nil {
		return nil, err
	}
	grace := api.RevocationLimits().DefaultStaleGrace
	if anchor, aerr := api.DeviceTrustAnchorsByToken(ctx, []string{args.AnchorToken}); aerr == nil && len(anchor) == 1 {
		if g, gerr := anchor[0].RevocationStaleGraceOf(grace); gerr == nil {
			grace = g
		}
	}
	return &DeviceRevocationListResultResolver{M: *res, Grace: grace}, nil
}

// Delete the current list of an issuer under a trust anchor.
func (r *SchemaResolver) DeleteDeviceRevocationList(ctx context.Context, args struct {
	AnchorToken string
	IssuerKey   string
	IssuerName  string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteDeviceRevocationList(ctx, args.AnchorToken, args.IssuerKey, args.IssuerName)
}
