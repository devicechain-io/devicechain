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
import { gql, GraphQLRequestError } from '@devicechain/client';
import { graphql } from '@/gql/user-management';
import type { MfaStatusQuery, TenantMfaQuery } from '@/gql/user-management/graphql';
import { serverErrorCode } from '@/i18n/signInError';
import type { IdentityAuth } from '@/lib/api/user-management';

export type MfaStatus = MfaStatusQuery['mfaStatus'];
export type TenantMfa = TenantMfaQuery['tenant'];

/** The error codes this feature adds, matched on `extensions.code` and never on the prose. */
export const MFA_CODE = {
  /** login held the sign-in for a code: ask for it. */
  challenge: 'MFA_CHALLENGE',
  /** The challenge lapsed or was used up: sign in again. */
  challengeExpired: 'MFA_CHALLENGE_EXPIRED',
  /** The tenant needs a second factor and this sign-in had none, but the account has one. */
  required: 'MFA_REQUIRED',
  /** The tenant needs a second factor and the account has none: send them to set one up. */
  enrollmentRequired: 'MFA_ENROLLMENT_REQUIRED',
  alreadyEnrolled: 'MFA_ALREADY_ENROLLED',
  notEnrolled: 'MFA_NOT_ENROLLED',
  /** Resetting a factor with failures on record needs a new password with it. */
  resetNeedsPassword: 'MFA_RESET_NEEDS_PASSWORD',
} as const;

/** What login's MFA_CHALLENGE error carries in its extensions. */
export interface MfaChallenge {
  challenge: string;
  /** RFC 3339; the challenge accepts attempts until then. */
  expiresAt: string;
  /** `TOTP` and `RECOVERY_CODE`. */
  methods: string[];
}

/**
 * The challenge login held the sign-in for, or null when the error is anything else. The
 * handle is read from the error's extensions and nowhere else, and it never goes to a URL
 * or to storage.
 */
export function mfaChallengeOf(err: unknown): MfaChallenge | null {
  if (serverErrorCode(err) !== MFA_CODE.challenge) return null;
  const ext = (err as GraphQLRequestError).errors?.[0]?.extensions as Record<string, unknown> | undefined;
  const challenge = ext?.challenge;
  const expiresAt = ext?.expiresAt;
  if (typeof challenge !== 'string' || challenge === '' || typeof expiresAt !== 'string') return null;
  const methods = Array.isArray(ext?.methods)
    ? (ext.methods as unknown[]).filter((m): m is string => typeof m === 'string')
    : [];
  return { challenge, expiresAt, methods };
}

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
