// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// A recorded run is pinned to its board by hash, so a board from a newer release can match the
// recording exactly and still be one this player cannot draw. It is refused in words, not played.

import { CURRENT_SCHEMA_VERSION } from '@devicechain/dashboards';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import ReplayApp from './App';
import { setReplayLocale } from './i18n/config';
import { ReplayLoadError, loadReplay, sha256Hex } from './load';
import {
  BOARD_URL,
  ORIGIN,
  RECORDING_URL,
  bufferToArrayBuffer,
  boardBytes,
  fixtureRecordingText,
  stubFetch,
} from './testing/harness';

async function newerBoard(version = CURRENT_SCHEMA_VERSION + 1) {
  const doc = JSON.parse(boardBytes().toString('utf8'));
  doc.schemaVersion = version;
  const bytes = Buffer.from(JSON.stringify(doc));
  const text = await fixtureRecordingText(async () => {});
  const rec = JSON.parse(text);
  rec.board.sha256 = await sha256Hex(bufferToArrayBuffer(bytes));
  return { bytes, recording: JSON.stringify(rec) };
}

beforeEach(async () => {
  await setReplayLocale('en');
});
afterEach(cleanup);

describe('board-replay with a board from a newer version', () => {
  it('refuses to load it, with its own code', async () => {
    const { bytes, recording } = await newerBoard();
    const stub = stubFetch({ [RECORDING_URL]: recording, [BOARD_URL]: bytes });
    const err = await loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: stub.fetchFn, origin: ORIGIN }).then(
      () => null,
      (e: unknown) => e,
    );
    expect(err).toBeInstanceOf(ReplayLoadError);
    expect((err as ReplayLoadError).code).toBe('boardTooNew');
  });

  it('shows the refusal message instead of a board', async () => {
    const { bytes, recording } = await newerBoard();
    const stub = stubFetch({ [RECORDING_URL]: recording, [BOARD_URL]: bytes });
    render(<ReplayApp recordingUrl={RECORDING_URL} boardUrl={BOARD_URL} fetchFn={stub.fetchFn} origin={ORIGIN} />);
    const alert = await screen.findByRole('alert');
    await waitFor(() => expect(alert.textContent).toMatch(/saved by a newer version of DeviceChain\. Upgrade this player/));
    expect(screen.getByTestId('replay-body').getAttribute('data-state')).toBe('error');
  });

  it('still loads the same board at the current version', async () => {
    const { bytes, recording } = await newerBoard(CURRENT_SCHEMA_VERSION);
    const stub = stubFetch({ [RECORDING_URL]: recording, [BOARD_URL]: bytes });
    const loaded = await loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: stub.fetchFn, origin: ORIGIN });
    expect(loaded.definition.schemaVersion).toBe(CURRENT_SCHEMA_VERSION);
  });
});
