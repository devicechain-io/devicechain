// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Client-side decode of the DeviceChain access token (ADR-008 JWT). This reads
// the payload for display + routing only; it does NOT verify the signature —
// every protected operation is still enforced server-side by the token's
// authorities. Never trust these claims for authorization decisions beyond what
// the UI shows.

export interface DecodedClaims {
  /** Tenant the subject acts within (authoritative server-side). */
  tenant: string;
  /** Human-readable subject identifier. */
  username: string;
  /** Assigned role names — display/audit only; enforcement is on authorities. */
  roles: string[];
  /** Effective capabilities (e.g. "device:write", or "*" for super-authority). */
  authorities: string[];
  /** "access" | "refresh". */
  typ: string;
  /** Expiry, epoch seconds (from the standard `exp` claim). */
  exp?: number;
  /** Issue time, epoch seconds (from the standard `iat` claim). */
  iat?: number;
  /**
   * The membership's scope, from the `scp` claim: anchors such as `customer:acme` that
   * limit the subject to part of the tenant. Empty when the subject sees the whole tenant.
   *
   * Deliberately NOT named `scope`: an OAuth access token carries its own `scope` claim (a
   * space-separated grant string), and the two mean unrelated things. Optional so a
   * hand-built claims object (a test fixture, an older caller) still type-checks; an
   * absent value means the same as an empty one.
   */
  scopeAnchors?: string[];
}

/** One anchor of a scope, split into its kind and token. */
export interface ScopeAnchorRef {
  /** `customer` or `group`. */
  kind: string;
  token: string;
}

function base64UrlDecode(segment: string): string {
  const padded = segment.replace(/-/g, '+').replace(/_/g, '/');
  const json = atob(padded);
  // Handle UTF-8 payloads.
  return decodeURIComponent(
    Array.prototype.map
      .call(json, (c: string) => '%' + ('00' + c.charCodeAt(0).toString(16)).slice(-2))
      .join(''),
  );
}

/** Decode a JWT's payload, or null if it is malformed. */
export function decodeToken(token: string): DecodedClaims | null {
  const parts = token.split('.');
  if (parts.length !== 3) return null;
  try {
    const payload = JSON.parse(base64UrlDecode(parts[1]));
    return {
      tenant: payload.tenant ?? '',
      username: payload.username ?? '',
      roles: payload.roles ?? [],
      authorities: payload.authorities ?? [],
      typ: payload.typ ?? '',
      exp: payload.exp,
      iat: payload.iat,
      scopeAnchors: Array.isArray(payload.scp) ? payload.scp.filter((a: unknown) => typeof a === 'string') : [],
    };
  } catch {
    return null;
  }
}

/** True if the token is missing, malformed, or within `skewSeconds` of expiry. */
export function isExpired(token: string, skewSeconds = 30): boolean {
  const claims = decodeToken(token);
  if (!claims || claims.exp === undefined) return true;
  const nowSeconds = Date.now() / 1000;
  return claims.exp - skewSeconds <= nowSeconds;
}

/** True if the claims grant the authority (the super-authority "*" passes all). */
export function hasAuthority(claims: DecodedClaims | null, required: string): boolean {
  if (!claims) return false;
  return claims.authorities.some((a) => a === '*' || a === required);
}

/**
 * True when the claims limit the subject to part of the tenant (a non-empty `scp`). The
 * server enforces the scope on every request; this only lets a client stop offering what
 * the server would refuse.
 */
export function isScoped(claims: DecodedClaims | null): boolean {
  return (claims?.scopeAnchors?.length ?? 0) > 0;
}

/**
 * The claims' scope anchors split into kind and token (`customer:acme` → customer/acme).
 * An anchor with no `:` is dropped rather than guessed at.
 */
export function scopeAnchorRefs(claims: DecodedClaims | null): ScopeAnchorRef[] {
  return (claims?.scopeAnchors ?? []).flatMap((a) => {
    const i = a.indexOf(':');
    return i > 0 ? [{ kind: a.slice(0, i), token: a.slice(i + 1) }] : [];
  });
}
