// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A trap for the recorded source's "no network" promise: while installed, any use of
// fetch, WebSocket, EventSource or XMLHttpRequest THROWS at the call site (so the code
// under test fails where it dialled) and is also counted, so a test that swallowed the
// exception still fails when it asserts `calls` is empty.

import { vi } from 'vitest';

const GLOBALS = ['fetch', 'WebSocket', 'EventSource', 'XMLHttpRequest'] as const;

export interface NetworkTrap {
  calls: string[];
  restore(): void;
}

export function installNetworkTrap(): NetworkTrap {
  const calls: string[] = [];
  for (const name of GLOBALS) {
    vi.stubGlobal(name, function trapped() {
      calls.push(name);
      throw new Error(`network call attempted: ${name}`);
    });
  }
  return { calls, restore: () => vi.unstubAllGlobals() };
}
