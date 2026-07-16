// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The AI-provider screens' own routes, in one place.
//
// These live under /admin because a provider is instance config an operator owns
// (ADR-065). That prefix is not cosmetic: a link that drops it matches no route, so
// React Router's catch-all sends the operator to "/", the tenant tree bounces them
// for having no tenant session, and they land on the tenant LOGIN page — an
// unrecoverable-looking exit from the admin console, off one wrong string.
//
// The screens moved here from the tenant tree, and a move renders as a rename in
// review: the navigation strings inside them are unchanged lines, so nothing in a
// diff, a typecheck, or a compiler points at them. Naming the base once is what
// makes the prefix a fact of the module rather than something three call sites each
// have to remember.
export const AI_PROVIDERS_BASE = '/admin/ai-providers';

// aiProviderPath builds the route to one provider's editor. It encodes the token
// because the detail page decodes it — the two must agree, and they only do if both
// go through this pair.
export function aiProviderPath(token: string): string {
  return `${AI_PROVIDERS_BASE}/${encodeURIComponent(token)}`;
}
