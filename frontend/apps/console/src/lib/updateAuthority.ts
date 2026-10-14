// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The authorities that gate the update-management surfaces, named once.
//
// They are three, and kept apart on purpose: reading the catalogue, publishing to it,
// and deciding whose signature makes an artifact trusted are different trust decisions.
// In particular artifact:trust is NOT implied by artifact:write — whoever can upload an
// image must not be able to appoint the signer that vouches for it — so the signer
// registry's write controls test canTrustArtifacts, never canWriteArtifacts.
//
// These mirror the server's authority vocabulary (core/auth); the service enforces
// them on every call, so this is presentation only. The `*` super-authority passes all,
// via hasAuthority.
import { hasAuthority, type DecodedClaims } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { AREA, useAreaStatus, type AreaStatus } from '@/lib/capabilities';

export const ARTIFACT_AUTHORITY = {
  read: 'artifact:read',
  write: 'artifact:write',
  trust: 'artifact:trust',
} as const;

/** List and inspect artifacts, signers and update assignments. */
export const canReadArtifacts = (claims: DecodedClaims | null): boolean =>
  hasAuthority(claims, ARTIFACT_AUTHORITY.read);

/** Declare, retire and sign artifacts, and assign, cancel and abandon updates. */
export const canWriteArtifacts = (claims: DecodedClaims | null): boolean =>
  hasAuthority(claims, ARTIFACT_AUTHORITY.write);

/** Register and revoke artifact signers. Separate from write on purpose. */
export const canTrustArtifacts = (claims: DecodedClaims | null): boolean =>
  hasAuthority(claims, ARTIFACT_AUTHORITY.trust);

export interface UpdateAccess {
  /**
   * The raw discovery answer. `present` collapses 'unknown' into false, which is right
   * for a nav entry or a tab (hide until known) but WRONG for a routed page: on a cold
   * load or a deep link every page starts 'unknown', so a page that renders "not
   * deployed" or redirects on !present would do so for a moment on every instance that
   * DOES run the area — and forever if discovery failed. A page renders its "not
   * deployed" state only on status === 'absent', and otherwise lets the service answer.
   */
  status: AreaStatus;
  /** The area is known to be deployed. False while unknown: fail closed. */
  present: boolean;
  /** Show any update surface at all: the area is there AND the caller may read it. */
  canRead: boolean;
  /** Publish and sign artifacts, assign updates. Implies canRead's preconditions. */
  canWrite: boolean;
  /** Register and revoke signers. */
  canTrust: boolean;
}

/**
 * Everything an update surface asks before rendering. Write and trust are false
 * unless the area is present, so a control can test one flag. A routed page reads
 * `status` instead — see UpdateAccess.status.
 */
export function useUpdateAccess(): UpdateAccess {
  const { claims } = useAuth();
  const status = useAreaStatus(AREA.updateManagement);
  // The same predicate as useAreaPresent, derived from the one status read.
  const present = status === 'available';
  return {
    status,
    present,
    canRead: present && canReadArtifacts(claims),
    canWrite: present && canWriteArtifacts(claims),
    canTrust: present && canTrustArtifacts(claims),
  };
}
