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
