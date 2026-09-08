// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// ---- Test doubles still reach inside a BUILT DeviceChain package -----------
//
// 🔴 WHY THIS FILE EXISTS. This app used to compile `@devicechain/{client,dashboards,
// widgets}` from their sources through the workspace symlink — the one resolution path
// no consumer of the published packages ever takes. It now consumes their `dist`,
// which is what makes "the /dash app is just another embedder of the exact published
// artifacts" true instead of aspirational.
//
// That change quietly rests on a claim about vitest's module mocking. A dozen tests in
// this app partial-mock the package boundary — `vi.mock('@devicechain/client', async
// (importOriginal) => …)` — and every one of them is only meaningful if interception
// still reaches a bare `import '@devicechain/client'` written INSIDE a bundled
// `dist/index.js`. It should: internals stay external, so the specifier survives the
// bundle unchanged, and vitest intercepts by resolved id. But "it should" is exactly
// the kind of tooling claim this arc has been burned by twice, so it is proven here
// rather than assumed — and proven at both hops, in both packages' artifacts.
//
// Each assertion is written so it CANNOT pass without the mock in force: the doubles
// return answers the real implementations never would. That is the negative control,
// carried inside the positive case, rather than a separate run someone has to remember
// to do.
//
// The structural half of the claim — that these packages really do import their
// siblings by bare specifier in `dist`, instead of having bundled them in — is
// asserted in packages/widgets/src/dist-shape.test.ts. Both halves are needed: mocking
// a specifier that no longer appears in the artifact would prove nothing.

import { describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

// Hop 1: this app's mock -> @devicechain/dashboards' dist -> `gql`.
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

// Hop 2: this app's mock -> @devicechain/widgets' dist -> `isTerminalCommandStatus`.
//
// The double is deliberately INVERTED rather than merely different: it calls a
// nonsense status terminal and a real terminal status not. Either answer is
// unreachable from the real implementation, so neither assertion below can pass by
// accident if the interception stops at this app's own module graph.
const NOT_A_STATUS = 'definitely-not-a-command-status';
vi.mock('@devicechain/dashboards', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/dashboards')>();
  return {
    ...actual,
    isTerminalCommandStatus: (status: string) => status === NOT_A_STATUS,
  };
});

import { createEntityLister } from '@devicechain/dashboards';
import { isTerminalStatus } from '@devicechain/widgets';

describe('mocking a package boundary reaches inside the built package', () => {
  it('intercepts the SDK call that @devicechain/dashboards makes from its dist', async () => {
    gqlMock.mockResolvedValueOnce({
      devices: { results: [{ token: 'sentinel-device', name: 'From the double' }] },
    });

    const candidates = await createEntityLister()('device');

    // The double answered, so the built runtime's own `import { gql }` resolved to it.
    // The real one would have attempted a fetch against a relative URL under jsdom.
    expect(candidates).toEqual([{ token: 'sentinel-device', name: 'From the double' }]);
    expect(gqlMock).toHaveBeenCalledTimes(1);
    expect(gqlMock.mock.calls[0][0]).toBe('device-management');
  });

  it('intercepts the call @devicechain/widgets makes from its dist into dashboards', () => {
    // widgets' `isTerminalStatus` is a thin alias over dashboards'
    // `isTerminalCommandStatus`, so this is that cross-package call and nothing else.
    expect(isTerminalStatus(NOT_A_STATUS)).toBe(true);
    expect(isTerminalStatus('SUCCESSFUL')).toBe(false);
  });
});
