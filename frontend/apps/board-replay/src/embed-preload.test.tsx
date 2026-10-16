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

import ReplayApp from './App';
import { DEFAULT_CANVAS } from './embed';
import { BOARD_URL, ORIGIN, RECORDING_URL, boardBytes, fixtureRecordingText, manualTicker, stubFetch } from './testing/harness';

afterEach(() => {
  cleanup();
});

describe('the embed and the lazily loaded widgets', () => {
  it('shows no canvas until the widget code of its board has arrived', async () => {
    let release!: () => void;
    h.preload.mockImplementation(() => new Promise<void>((r) => (release = r)));
    const stub = stubFetch({ [RECORDING_URL]: await fixtureRecordingText(), [BOARD_URL]: boardBytes() });
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
