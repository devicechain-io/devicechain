// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/identity"
)

// The second factor's data-plane surface. Every mutation here takes the IDENTITY token as an
// argument, like login's neighbours, and re-checks the password in the identity layer; none
// reads the Authorization header.

// VerifyMfa completes a sign-in that login held for its second factor. The source address keys
// the per-source limiter and lock; the credential budget (one verification per request) is
// installed by the schema.
func (r *SchemaResolver) VerifyMfa(ctx context.Context, args struct {
	Challenge string
	Code      string
}) (*IdentityAuthResolver, error) {
	res, err := r.getIdentityManager(ctx).VerifyMfa(ctx, args.Challenge, args.Code, gqlcore.SourceAddr(ctx))
	if err != nil {
		return nil, err
	}
	return &IdentityAuthResolver{auth: res}, nil
}

// TotpEnrollmentResolver resolves TotpEnrollment.
type TotpEnrollmentResolver struct{ e *identity.TotpEnrollment }

func (r *TotpEnrollmentResolver) OtpauthUri() string { return r.e.OtpauthURI }
func (r *TotpEnrollmentResolver) Secret() string     { return r.e.Secret }
func (r *TotpEnrollmentResolver) ExpiresAt() string  { return r.e.ExpiresAt.UTC().Format(time.RFC3339) }

// RecoveryCodesResolver resolves RecoveryCodes.
type RecoveryCodesResolver struct{ codes []string }

func (r *RecoveryCodesResolver) Codes() []string { return r.codes }

// MfaStatusResolver resolves MfaStatus.
type MfaStatusResolver struct{ s *identity.MfaStatus }

func (r *MfaStatusResolver) TotpEnabled() bool { return r.s.TotpEnabled }
func (r *MfaStatusResolver) EnrolledAt() *string {
	if r.s.EnrolledAt == nil {
		return nil
	}
	t := r.s.EnrolledAt.UTC().Format(time.RFC3339)
	return &t
}
func (r *MfaStatusResolver) RecoveryCodesRemaining() int32 { return int32(r.s.RecoveryCodesRemaining) }

// BeginTotpEnrollment starts enrolling an authenticator.
func (r *SchemaResolver) BeginTotpEnrollment(ctx context.Context, args struct {
	IdentityToken string
	Password      string
}) (*TotpEnrollmentResolver, error) {
	e, err := r.getIdentityManager(ctx).BeginTotpEnrollment(ctx, args.IdentityToken, args.Password)
	if err != nil {
		return nil, err
	}
	return &TotpEnrollmentResolver{e: e}, nil
}

// ConfirmTotpEnrollment activates the authenticator and returns the recovery codes once.
func (r *SchemaResolver) ConfirmTotpEnrollment(ctx context.Context, args struct {
	IdentityToken string
	Password      string
	Code          string
}) (*RecoveryCodesResolver, error) {
	codes, err := r.getIdentityManager(ctx).ConfirmTotpEnrollment(ctx, args.IdentityToken, args.Password, args.Code)
	if err != nil {
		return nil, err
	}
	return &RecoveryCodesResolver{codes: codes}, nil
}

// RegenerateRecoveryCodes replaces the recovery codes (password and a code).
func (r *SchemaResolver) RegenerateRecoveryCodes(ctx context.Context, args struct {
	IdentityToken string
	Password      string
	Code          string
}) (*RecoveryCodesResolver, error) {
	codes, err := r.getIdentityManager(ctx).RegenerateRecoveryCodes(ctx, args.IdentityToken, args.Password,
		args.Code, gqlcore.SourceAddr(ctx))
	if err != nil {
		return nil, err
	}
	return &RecoveryCodesResolver{codes: codes}, nil
}

// DisableTotp turns the second factor off (password and a code).
func (r *SchemaResolver) DisableTotp(ctx context.Context, args struct {
	IdentityToken string
	Password      string
	Code          string
}) (bool, error) {
	if err := r.getIdentityManager(ctx).DisableTotp(ctx, args.IdentityToken, args.Password,
		args.Code, gqlcore.SourceAddr(ctx)); err != nil {
		return false, err
	}
	return true, nil
}

// MfaStatus reads the identity's own second-factor status.
func (r *SchemaResolver) MfaStatus(ctx context.Context, args struct{ IdentityToken string }) (*MfaStatusResolver, error) {
	s, err := r.getIdentityManager(ctx).MfaStatusFor(ctx, args.IdentityToken)
	if err != nil {
		return nil, err
	}
	return &MfaStatusResolver{s: s}, nil
}

// MfaRequired is the tenant's effective requirement; MfaRequiredByTier the part of it no tenant
// setting can turn off.
func (r *TenantResolver) MfaRequired() bool       { return r.t.MfaEffectivelyRequired() }
func (r *TenantResolver) MfaRequiredByTier() bool { return r.t.MfaRequiredByTier() }

// SetTenantMfaRequired requires or stops requiring a second factor of the caller's own tenant
// (requires member:write, and an unscoped caller). It cannot turn off the tier's floor.
func (r *SchemaResolver) SetTenantMfaRequired(ctx context.Context, args struct{ Required bool }) (*TenantResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberWrite); err != nil {
		return nil, err
	}
	t, err := r.getMembersService(ctx).SetOwnTenantMfaRequired(ctx, args.Required)
	if err != nil {
		return nil, err
	}
	return &TenantResolver{t: t, svc: r}, nil
}

// MfaEnabled resolves whether the member has two-factor authentication on.
func (r *MemberResolver) MfaEnabled() bool { return r.M.MfaEnabled }

// ---- admin plane ---------------------------------------------------------------------------

// MfaEnabled resolves whether the identity has an active second factor.
func (r *AdminIdentityResolver) MfaEnabled(ctx context.Context) (bool, error) {
	return ctx.Value(ContextAdminSsoKey).(*identity.Manager).MfaEnabled(ctx, &r.M)
}

// MfaRequired and MfaRequiredByTier resolve a tenant's own setting and its tier's floor.
func (r *AdminTenantResolver) MfaRequired() bool       { return r.M.MfaRequired }
func (r *AdminTenantResolver) MfaRequiredByTier() bool { return r.M.MfaRequiredByTier() }

// SetTenantMfaRequired requires or stops requiring a second factor of a tenant (requires
// user:write).
func (r *AdminResolver) SetTenantMfaRequired(ctx context.Context, args struct {
	Tenant   string
	Required bool
}) (*AdminTenantResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	t, err := r.getAdminService(ctx).SetTenantMfaRequired(ctx, args.Tenant, args.Required)
	if err != nil {
		return nil, err
	}
	return &AdminTenantResolver{M: *t}, nil
}

// ResetIdentityMfa removes an identity's second factor (requires user:write).
func (r *AdminResolver) ResetIdentityMfa(ctx context.Context, args struct {
	Email       string
	NewPassword *string
}) (*AdminIdentityResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	newPassword := strOrEmpty(args.NewPassword)
	if newPassword != "" {
		if err := checkPassword(newPassword); err != nil {
			return nil, err
		}
	}
	id, err := r.sso(ctx).ResetIdentityMfa(ctx, args.Email, newPassword)
	if err != nil {
		return nil, err
	}
	return &AdminIdentityResolver{M: *id}, nil
}
