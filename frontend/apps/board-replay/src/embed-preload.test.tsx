// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The embed shows its board only once the code of the board's lazily loaded widgets has arrived,
// the same as the full replay page: its first frame is the whole board, not boxes filling in.
// The preload is held open here to see that the embed's canvas really waits on it.

import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ preload: vi.fn() }));
vi.mock('@devicechain/widgets', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  preloadWidgets: (...a: unknown[]) => h.preload(...a),
}));

import ReplayApp, { loadEmbed } from './App';
import { DEFAULT_CANVAS } from './embed';
import { BOARD_URL, ORIGIN, RECORDING_URL, boardBytes, fixtureRecordingText, manualTicker, stubFetch } from './testing/harness';

afterEach(() => {
  cleanup();
});

// The case draws the whole board once its preload is released: the first full-board render in a
// file, which is all CPU and outlasted the 5 s default on a loaded machine (every workspace's
// suite at once). Nothing is left to load ahead, so the budget is sized to that render.
describe('the embed and the lazily loaded widgets', { timeout: 15_000 }, () => {
  it('shows no canvas until the widget code of its board has arrived', async () => {
    let release!: () => void;
    h.preload.mockImplementation(() => new Promise<void>((r) => (release = r)));
    const stub = stubFetch({ [RECORDING_URL]: await fixtureRecordingText(), [BOARD_URL]: boardBytes() });
    // As mountBoardReplay does: the embed's code is fetched before the first render, so what the
    // canvas waits on below is the widgets' preload alone.
    await loadEmbed();
    render(
      <ReplayApp
        recordingUrl={RECORDING_URL}
        boardUrl={BOARD_URL}
        fetchFn={stub.fetchFn}
        origin={ORIGIN}
        ticker={manualTicker()}
        embed={{ canvas: DEFAULT_CANVAS, autoplay: true, retime: true }}
      />,
    );
    await vi.waitFor(() => expect(h.preload).toHaveBeenCalledTimes(1));
    await act(() => new Promise((r) => setTimeout(r, 20)));
    expect(screen.queryByTestId('embed-canvas')).toBeNull();
    await act(async () => release());
    await screen.findByTestId('embed-canvas');
  });
});
