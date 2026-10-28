// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pure half of the X.509 screens: courtesy checks on pasted PEM, the rules the anchor
// actions follow, and the mapping from a server refusal to the sentence an operator reads. No
// React and no network, so every rule here is unit-tested.
//
// 🔴 EVERYTHING HERE IS A HINT. The server validates a certificate authoritatively (key policy,
// CA bits, expiry, name constraints, the chain at every connect). These checks only save a round
// trip, and above all keep a pasted PRIVATE KEY from leaving the browser.

import { errorCode } from '@devicechain/client';
import type { TrustAnchor } from '@/lib/api/trust-anchors';

/** The device-derived token rule of a certificate profile: `pv-` plus the certificate's CN. */
export const DERIVED_TOKEN_PREFIX = 'pv-';

/** The most certificate credentials one device may hold (also the server's cap). */
export { MAX_X509_CREDENTIALS_PER_DEVICE } from '@/lib/api/credentials';

/** The server's cap on a pasted PEM, matched loosely: it is only here to refuse a mistaken paste. */
export const PEM_MAX_BYTES = 64 * 1024;

export type PemProblem = 'pemEmpty' | 'pemLooksLikeKey' | 'pemTooLarge' | 'pemNotCertificate' | 'pemNeedsOne';

const BEGIN_CERT = /-----BEGIN CERTIFICATE-----/g;

/** How many certificate blocks the text holds. */
export function certificateCount(pem: string): number {
  return (pem.match(BEGIN_CERT) ?? []).length;
}

/**
 * What is wrong with pasted PEM, as an i18n key in the `trustAnchors` namespace, or null.
 * `exactlyOne` is for an anchor (one CA certificate); a device credential may also carry the
 * intermediates that link its certificate to the anchor.
 */
export function checkPem(pem: string, opts: { exactlyOne: boolean }): PemProblem | null {
  if (pem.trim() === '') return 'pemEmpty';
  if (/PRIVATE KEY/i.test(pem)) return 'pemLooksLikeKey';
  if (new TextEncoder().encode(pem).length > PEM_MAX_BYTES) return 'pemTooLarge';
  const n = certificateCount(pem);
  if (n === 0) return 'pemNotCertificate';
  if (opts.exactlyOne && n !== 1) return 'pemNeedsOne';
  return null;
}

/** What still relies on an anchor, or null when nothing does. */
export interface AnchorBlockers {
  credentials: number;
  profiles: number;
}

/**
 * The delete guard: an anchor a credential or a provisioning profile still names cannot be
 * deleted, so the screen says why instead of offering a delete the server will refuse. The
 * count includes removed credentials the server still holds, as the foreign key does.
 */
export function deleteBlockers(anchor: Pick<TrustAnchor, 'credentialCount' | 'profileCount'>): AnchorBlockers | null {
  if (anchor.credentialCount + anchor.profileCount === 0) return null;
  return { credentials: anchor.credentialCount, profiles: anchor.profileCount };
}

/** True when the anchor's certificate has expired: chains to it no longer authenticate. */
export function isAnchorExpired(notAfter: string, now: number = Date.now()): boolean {
  const t = new Date(notAfter).getTime();
  return !Number.isNaN(t) && t <= now;
}

/** Days until `notAfter` (negative once past), or null when unreadable. */
export function daysUntil(notAfter: string | null | undefined, now: number = Date.now()): number | null {
  if (!notAfter) return null;
  const t = new Date(notAfter).getTime();
  if (Number.isNaN(t)) return null;
  return Math.floor((t - now) / 86_400_000);
}

/** The window inside which an expiry is called out as near. */
export const EXPIRY_WARN_DAYS = 30;

/** The token a device earmarked for a certificate profile takes from its certificate's CN. */
export function derivedDeviceToken(commonName: string): string {
  return `${DERIVED_TOKEN_PREFIX}${commonName.trim()}`;
}

/**
 * How a refusal from the X.509 operations is explained. The server's code decides the family
 * (never its text); `detail` carries the server's own sentence for the families where the
 * specifics (which field, which count) are the useful part.
 */
export type X509ErrorKind =
  | { key: 'errInUse' }
  | { key: 'errLimit'; detail: string }
  | { key: 'errInvalid'; detail: string }
  | { key: 'errConflict' }
  | { key: 'errNotFound' }
  | { key: 'errOther'; detail: string };

export function classifyX509Error(err: unknown, fallback: string): X509ErrorKind {
  switch (errorCode(err)) {
    case 'REFERENCE_VIOLATION':
      return { key: 'errInUse' };
    case 'LIMIT_EXCEEDED':
      return { key: 'errLimit', detail: fallback };
    case 'INVALID_VALUE':
      return { key: 'errInvalid', detail: fallback };
    case 'CONFLICT':
      return { key: 'errConflict' };
    case 'NOT_FOUND':
      return { key: 'errNotFound' };
    default:
      return { key: 'errOther', detail: fallback };
  }
}
