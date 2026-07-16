// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest';
import { AI_PROVIDERS_BASE, aiProviderPath } from './paths';

describe('AI provider routes', () => {
  // The regression this file exists for. When the screens moved from the tenant tree
  // to /admin (ADR-065), all three of their internal navigations kept pointing at the
  // deleted /ai-providers route. Nothing caught it: the move renders as a rename, so
  // the unchanged strings never appeared in the diff, and a route is a string the
  // typechecker has no opinion about. The result was that every provider link ejected
  // the operator to the tenant login page.
  it('lives under /admin, because a provider is instance config', () => {
    expect(AI_PROVIDERS_BASE.startsWith('/admin/')).toBe(true);
  });

  it('builds a detail route under the same base', () => {
    expect(aiProviderPath('claude-prod')).toBe('/admin/ai-providers/claude-prod');
  });

  it('encodes the token so the detail page can decode it back', () => {
    // The list encodes and the detail page decodes; if only one side does it, a token
    // needing escaping resolves to the wrong provider or to no route at all. Today's
    // token grammar makes this unlikely, not impossible — and the pair is free.
    expect(aiProviderPath('a b/c')).toBe('/admin/ai-providers/a%20b%2Fc');
    expect(decodeURIComponent(aiProviderPath('a b/c').slice(AI_PROVIDERS_BASE.length + 1))).toBe(
      'a b/c',
    );
  });
});
