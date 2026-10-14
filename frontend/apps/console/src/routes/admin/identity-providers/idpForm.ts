// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The create-connection editor's state, its client-side checks and the request it
// builds. A plain module (not a component) so every rule is testable on its own.
//
// The checks are a courtesy that says WHY before a round trip. The server re-validates
// everything authoritatively and its refusals are shown verbatim in the form, so nothing
// here may be stricter than the server in a way that blocks a legal request.

import type { IdpClaim, IdpConnectionCreateRequest } from '@/lib/api/identity-providers';

export type IdpKind = 'tenant' | 'instance';

export interface IdpCreateState {
  token: string;
  name: string;
  kind: IdpKind;
  issuer: string;
  clientId: string;
  // Write-only. A create form always starts empty; nothing ever fills it back in.
  clientSecret: string;
  // Comma or whitespace separated; empty keeps the server default.
  scopes: string;
  emailClaim: string;
  claims: IdpClaim[];
  // Comma or whitespace separated authentication method references (amr).
  requiredAmr: string;
  // The operator's statement that the provider itself enforces multi-factor sign-in.
  mfaEnforcedByIdp: boolean;
  // PEM of private CA certificates to trust for this provider, beside the system roots.
  caBundle: string;
}

export function newCreateState(): IdpCreateState {
  return {
    token: '',
    name: '',
    kind: 'tenant',
    issuer: '',
    clientId: '',
    clientSecret: '',
    scopes: '',
    emailClaim: '',
    claims: [],
    requiredAmr: '',
    mfaEnforcedByIdp: false,
    caBundle: '',
  };
}

// "letters, digits and hyphens" — the connection token appears in its callback address.
const TOKEN_RE = /^[A-Za-z0-9][A-Za-z0-9-]*$/;

// Splits an operator-typed list on commas and whitespace, dropping blanks and repeats.
export function splitList(text: string): string[] {
  const seen = new Set<string>();
  for (const part of text.split(/[\s,]+/)) {
    if (part !== '') seen.add(part);
  }
  return [...seen];
}

// The i18n keys (identityProviders namespace) of the client-side refusals.
export type IdpValidationKey =
  | 'tokenRequiredError'
  | 'tokenInvalidError'
  | 'nameRequiredError'
  | 'issuerRequiredError'
  | 'issuerNotHttpsError'
  | 'clientIdRequiredError'
  | 'claimIncompleteError'
  | 'instanceNeedsMfaError'
  | 'caBundleLooksLikeKeyError'
  | 'caBundleNotPemError'
  | 'caBundleTooLargeError';

// The server's cap on a CA bundle.
export const CA_BUNDLE_MAX_BYTES = 64 * 1024;

// checkCaBundle is a courtesy that says why BEFORE the text leaves the browser — above all
// that a private key was pasted, which should not be sent anywhere. The server re-validates
// authoritatively. Empty is fine (no private CA).
export function checkCaBundle(text: string): IdpValidationKey | null {
  if (text.trim() === '') return null;
  if (/PRIVATE KEY/i.test(text)) return 'caBundleLooksLikeKeyError';
  if (new TextEncoder().encode(text).length > CA_BUNDLE_MAX_BYTES) return 'caBundleTooLargeError';
  if (!/-----BEGIN CERTIFICATE-----/.test(text)) return 'caBundleNotPemError';
  return null;
}

// validateCreate returns the i18n key of the first problem, or null when the form is
// shaped well enough to send.
export function validateCreate(s: IdpCreateState): IdpValidationKey | null {
  if (s.token.trim() === '') return 'tokenRequiredError';
  if (!TOKEN_RE.test(s.token.trim())) return 'tokenInvalidError';
  if (s.name.trim() === '') return 'nameRequiredError';
  if (s.issuer.trim() === '') return 'issuerRequiredError';
  if (!/^https:\/\//i.test(s.issuer.trim())) return 'issuerNotHttpsError';
  if (s.clientId.trim() === '') return 'clientIdRequiredError';
  if (s.claims.some((c) => c.name.trim() === '' || c.value.trim() === '')) {
    return 'claimIncompleteError';
  }
  const caProblem = checkCaBundle(s.caBundle);
  if (caProblem) return caProblem;
  // An instance connection can reach a superuser, so it must either demand a
  // multi-factor method of the provider or carry the operator's recorded statement that
  // the provider enforces MFA itself. The API refuses neither-nor; saying so here saves
  // the round trip.
  if (s.kind === 'instance' && splitList(s.requiredAmr).length === 0 && !s.mfaEnforcedByIdp) {
    return 'instanceNeedsMfaError';
  }
  return null;
}

// createRequest builds the mutation input. Optional fields are OMITTED when blank so the
// server default applies; the secret is omitted when blank (a public client).
export function createRequest(s: IdpCreateState): IdpConnectionCreateRequest {
  const scopes = splitList(s.scopes);
  const amr = splitList(s.requiredAmr);
  const claims = s.claims.map((c) => ({ name: c.name.trim(), value: c.value.trim() }));
  const req: IdpConnectionCreateRequest = {
    token: s.token.trim(),
    name: s.name.trim(),
    kind: s.kind,
    issuer: s.issuer.trim(),
    clientId: s.clientId.trim(),
  };
  if (s.clientSecret !== '') req.clientSecret = s.clientSecret;
  if (scopes.length > 0) req.scopes = scopes;
  if (s.emailClaim.trim() !== '') req.emailClaim = s.emailClaim.trim();
  if (claims.length > 0) req.requiredClaims = claims;
  if (amr.length > 0) req.requiredAmr = amr;
  if (s.caBundle.trim() !== '') req.caBundle = s.caBundle.trim();
  // The acknowledgement only means anything for an instance connection with no required
  // methods; never send a stale tick left over from a kind the operator switched away from.
  if (s.kind === 'instance' && amr.length === 0 && s.mfaEnforcedByIdp) {
    req.mfaEnforcedByIdp = true;
  }
  return req;
}

// A connection that is switched on but whose snapshot is unapproved: the provider's
// endpoints changed (a refresh withdraws the approval and leaves the switch on), so
// sign-ins are being refused until a person looks again. A brand-new connection is also
// unapproved but is created disabled, so it gets the first-approval wording instead.
export function needsReapproval(c: { enabled: boolean; discovery: { approved: boolean } }): boolean {
  return c.enabled && !c.discovery.approved;
}
