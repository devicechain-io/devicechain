// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Resolves a tenant branding logo value (ADR-038 / ADR-058) to a src usable in an
// <img>. A Tier-0 value — an https URL or a data: URI — is used as-is. An
// object-store (Tier-1) logo instead arrives as an AUTHORIZING proxy path
// (/branding/logo?v=…): because an <img> element cannot carry a bearer token, we
// fetch the bytes with the caller's access token and hand back a blob: object URL,
// revoking it when the logo changes or the component unmounts so it never leaks.

import { useEffect, useState } from 'react';
import { resolveAuthToken } from '@devicechain/client';

// The object-store read proxy is served by user-management; a resolved Tier-1 logo
// value is the service-relative path, which we address under the area's api prefix.
const PROXY_PATH_PREFIX = '/branding/logo';
const USER_MANAGEMENT_API_BASE = '/api/user-management';

function directSrc(logo: string | null | undefined): string | null {
  return logo && !logo.startsWith(PROXY_PATH_PREFIX) ? logo : null;
}

export function useBrandingLogoSrc(logo: string | null | undefined): string | null {
  // Seed synchronously for a Tier-0 value so there is no flash; a proxy-path value
  // resolves asynchronously in the effect.
  const [src, setSrc] = useState<string | null>(() => directSrc(logo));

  useEffect(() => {
    if (!logo) {
      setSrc(null);
      return;
    }
    if (!logo.startsWith(PROXY_PATH_PREFIX)) {
      setSrc(logo);
      return;
    }
    // Clear while (re)fetching so a previously-revoked object URL is never shown.
    setSrc(null);
    let cancelled = false;
    let objectUrl: string | null = null;
    (async () => {
      try {
        const token = await resolveAuthToken();
        const res = await fetch(USER_MANAGEMENT_API_BASE + logo, {
          headers: token ? { Authorization: `Bearer ${token}` } : {},
        });
        if (!res.ok) throw new Error(String(res.status));
        const bytes = await res.blob();
        if (cancelled) return;
        objectUrl = URL.createObjectURL(bytes);
        setSrc(objectUrl);
      } catch {
        if (!cancelled) setSrc(null);
      }
    })();
    return () => {
      cancelled = true;
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [logo]);

  return src;
}
