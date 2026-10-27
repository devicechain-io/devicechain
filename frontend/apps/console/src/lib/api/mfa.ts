// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Two-factor authentication (an authenticator app, with recovery codes) against the
// user-management service.
//
// The identity-token operations (enrol, confirm, regenerate, disable, status) take the
// identity token as an ARGUMENT and no Authorization header, exactly like login: they work
// before any tenant is selected, which is what lets a member whose tenant requires a second
// factor set one up from the sign-in screen. The password is re-checked by every one of the
// mutations, because identity tokens travel.
//
// 🔴 NOTHING SECRET HERE IS CACHED. The enrolment secret (beginTotpEnrollment) and the
// recovery codes (confirmTotpEnrollment, regenerateRecoveryCodes) are returned ONCE by the
// server: they are handed straight to the caller, which keeps them in component state only
// and drops them on dismissal. They are never routed through useQuery, the cached resource
// store, storage, a URL or a log. The status read carries no secret and is the only thing
// here a screen may refetch.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/user-management';
import type { MfaStatusQuery, TenantMfaQuery } from '@/gql/user-management/graphql';
import type { IdentityAuth } from '@/lib/api/user-management';

export type MfaStatus = MfaStatusQuery['mfaStatus'];
export type TenantMfa = TenantMfaQuery['tenant'];

// The codes and the challenge reader live in lib/mfaChallenge.ts, which has no GraphQL
// documents, so the sign-in screen can use them without loading every document in this file.
export { MFA_CODE, mfaChallengeOf, type MfaChallenge } from '@/lib/mfaChallenge';

// ── Enrolment ─────────────────────────────────────────────────────────────

const BEGIN_TOTP = graphql(`
  mutation BeginTotpEnrollment($identityToken: String!, $password: String!) {
    beginTotpEnrollment(identityToken: $identityToken, password: $password) {
      otpauthUri
      secret
      expiresAt
    }
  }
`);

/** What beginTotpEnrollment hands back, once. Hold it in component state only. */
export interface TotpEnrollment {
  otpauthUri: string;
  secret: string;
  expiresAt: string;
}

export async function beginTotpEnrollment(identityToken: string, password: string): Promise<TotpEnrollment> {
  const data = await gql('user-management', BEGIN_TOTP, { identityToken, password }, { anonymous: true });
  return data.beginTotpEnrollment;
}

const CONFIRM_TOTP = graphql(`
  mutation ConfirmTotpEnrollment($identityToken: String!, $password: String!, $code: String!) {
    confirmTotpEnrollment(identityToken: $identityToken, password: $password, code: $code) {
      codes
      auth {
        identityToken
        expiresAt
        superuser
        memberships {
          tenant
          roles
          ssoRequired
        }
      }
    }
  }
`);

/**
 * The ten recovery codes, shown once, and the caller's replacement sign-in. Turning the
 * factor on ends every session the account had, including the one that enrolled, so the
 * caller must swap `auth` in.
 */
export interface TotpEnrollmentResult {
  codes: string[];
  auth: IdentityAuth;
}

export async function confirmTotpEnrollment(
  identityToken: string,
  password: string,
  code: string,
): Promise<TotpEnrollmentResult> {
  const data = await gql(
    'user-management',
    CONFIRM_TOTP,
    { identityToken, password, code },
    { anonymous: true },
  );
  return data.confirmTotpEnrollment;
}

// ── Maintenance ───────────────────────────────────────────────────────────

const REGENERATE_CODES = graphql(`
  mutation RegenerateRecoveryCodes($identityToken: String!, $password: String!, $code: String!) {
    regenerateRecoveryCodes(identityToken: $identityToken, password: $password, code: $code) {
      codes
    }
  }
`);

/** A fresh set of ten recovery codes, once. The password AND a code are needed. */
export async function regenerateRecoveryCodes(
  identityToken: string,
  password: string,
  code: string,
): Promise<string[]> {
  const data = await gql(
    'user-management',
    REGENERATE_CODES,
    { identityToken, password, code },
    { anonymous: true },
  );
  return data.regenerateRecoveryCodes.codes;
}

const DISABLE_TOTP = graphql(`
  mutation DisableTotp($identityToken: String!, $password: String!, $code: String!) {
    disableTotp(identityToken: $identityToken, password: $password, code: $code)
  }
`);

/** Turns the factor off and ends EVERY session the account has, this one included. */
export async function disableTotp(identityToken: string, password: string, code: string): Promise<boolean> {
  const data = await gql('user-management', DISABLE_TOTP, { identityToken, password, code }, { anonymous: true });
  return data.disableTotp;
}

const MFA_STATUS = graphql(`
  query MfaStatus($identityToken: String!) {
    mfaStatus(identityToken: $identityToken) {
      totpEnabled
      enrolledAt
      recoveryCodesRemaining
    }
  }
`);

export async function getMfaStatus(identityToken: string): Promise<MfaStatus> {
  const data = await gql('user-management', MFA_STATUS, { identityToken }, { anonymous: true });
  return data.mfaStatus;
}

// ── The tenant's own requirement (tenant plane) ───────────────────────────

const TENANT_MFA = graphql(`
  query TenantMfa {
    tenant {
      token
      mfaRequired
      mfaRequiredByTier
    }
  }
`);

export async function getTenantMfa(): Promise<TenantMfa> {
  const data = await gql('user-management', TENANT_MFA);
  return data.tenant;
}

const SET_TENANT_MFA_REQUIRED = graphql(`
  mutation SetOwnTenantMfaRequired($required: Boolean!) {
    setTenantMfaRequired(required: $required) {
      token
      mfaRequired
      mfaRequiredByTier
    }
  }
`);

export async function setTenantMfaRequired(required: boolean): Promise<TenantMfa> {
  const data = await gql('user-management', SET_TENANT_MFA_REQUIRED, { required });
  return data.setTenantMfaRequired;
}
