// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The identity-provider screens' routes, in one place. They live under /admin because a
// connection is instance config an operator owns; a link that drops the prefix matches no
// route and bounces the operator to the tenant login page.
export const IDENTITY_PROVIDERS_BASE = '/admin/identity-providers';

// Encodes the token because the detail page decodes it.
export function identityProviderPath(token: string): string {
  return `${IDENTITY_PROVIDERS_BASE}/${encodeURIComponent(token)}`;
}
