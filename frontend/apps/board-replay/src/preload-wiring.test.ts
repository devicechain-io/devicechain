// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// loadReplay hands the board over only once the code of its lazily loaded widgets has arrived,
// so the replay's first frame is the whole board, not empty boxes filling in. The preload is held
// open here to see that loadReplay really waits on it, and is asked for the board's own types.

import { describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ preload: vi.fn() }));
vi.mock('@devicechain/widgets', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  preloadWidgets: (...a: unknown[]) => h.preload(...a),
}));

import { loadReplay } from './load';
import { BOARD_URL, ORIGIN, RECORDING_URL, boardBytes, fixtureRecordingText, stubFetch } from './testing/harness';

describe('loadReplay and the lazily loaded widgets', () => {
  it('waits for the widget code of the board it loads before resolving', async () => {
    let release!: () => void;
    h.preload.mockImplementation(() => new Promise<void>((r) => (release = r)));
    const stub = stubFetch({ [RECORDING_URL]: await fixtureRecordingText(), [BOARD_URL]: boardBytes() });
    let settled = false;
    const loading = loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: stub.fetchFn, origin: ORIGIN }).then((l) => {
      settled = true;
      return l;
    });
    await vi.waitFor(() => expect(h.preload).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 20));
    expect(settled).toBe(false);
    release();
    const loaded = await loading;
    expect(h.preload.mock.calls[0][0]).toBe(loaded.registry);
    expect(h.preload.mock.calls[0][1]).toEqual(loaded.definition.widgets.map((w) => w.type));
  });
});
