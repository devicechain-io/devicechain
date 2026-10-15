// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The fixed-canvas embed: its options, the canvas fit, and the embed player (autoplay from the
// first chapter, loop, re-timed views).

import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import ReplayApp from './App';
import { DEFAULT_CANVAS, fitCanvas, parseEmbedOptions, parseTheme } from './embed';
import { setReplayLocale } from './i18n/config';
import { BOARD_URL, ORIGIN, RECORDING_URL, boardBytes, fixtureRecordingText, manualTicker, stubFetch } from './testing/harness';

// Every data source the app builds, so a test can ask each what time it says it is.
const built = vi.hoisted(() => ({ sources: [] as Array<{ now(): number; timeShiftMs: number }> }));
vi.mock('@devicechain/dashboards', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/dashboards')>();
  class RecordingSource extends actual.RecordedDataSource {
    constructor(...args: ConstructorParameters<typeof actual.RecordedDataSource>) {
      super(...args);
      built.sources.push(this);
    }
  }
  return { ...actual, RecordedDataSource: RecordingSource };
});

beforeEach(async () => {
  await setReplayLocale('en');
  built.sources.length = 0;
});
afterEach(() => {
  cleanup();
});

describe('embed options', () => {
  const quiet = () => {};

  it('is off unless asked for, and defaults to a playing, re-timed 960x1080 canvas', () => {
    expect(parseEmbedOptions({}, '', quiet)).toBeNull();
    expect(parseEmbedOptions({ embed: '1' }, '', quiet)).toEqual({ canvas: DEFAULT_CANVAS, autoplay: true, retime: true });
    expect(DEFAULT_CANVAS).toEqual({ width: 960, height: 1080 });
  });

  it('reads the mount element first and the page URL second', () => {
    expect(parseEmbedOptions({}, '?embed=1&canvas=1280x720&retime=0', quiet)).toEqual({
      canvas: { width: 1280, height: 720 },
      autoplay: true,
      retime: false,
    });
    expect(parseEmbedOptions({ embed: '0' }, '?embed=1', quiet)).toBeNull();
    expect(parseEmbedOptions({ embed: '1', canvas: '800x600' }, '?canvas=1280x720', quiet)?.canvas).toEqual({ width: 800, height: 600 });
  });

  it('ignores an invalid value with a warning, keeping the default', () => {
    const warn = vi.fn();
    expect(parseEmbedOptions({ embed: '1', canvas: '9x9' }, '', warn)?.canvas).toEqual(DEFAULT_CANVAS);
    expect(parseEmbedOptions({ embed: '1', canvas: '960 by 1080' }, '', warn)?.canvas).toEqual(DEFAULT_CANVAS);
    expect(parseEmbedOptions({ embed: '1', autoplay: 'yes' }, '', warn)?.autoplay).toBe(true);
    expect(parseTheme({ theme: 'sepia' }, '', warn)).toBe('dark');
    expect(warn).toHaveBeenCalledTimes(4);
    expect(parseTheme({}, '?theme=light', warn)).toBe('light');
  });
});

describe('fitting the canvas', () => {
  it('scales the whole canvas to the largest size that fits, centred', () => {
    expect(fitCanvas({ width: 1280, height: 720 }, DEFAULT_CANVAS)).toEqual({ scale: 2 / 3, left: 320, top: 0 });
    expect(fitCanvas({ width: 640, height: 360 }, DEFAULT_CANVAS)).toEqual({ scale: 1 / 3, left: 160, top: 0 });
    expect(fitCanvas({ width: 960, height: 1080 }, DEFAULT_CANVAS)).toEqual({ scale: 1, left: 0, top: 0 });
    expect(fitCanvas({ width: 960, height: 2080 }, DEFAULT_CANVAS)).toEqual({ scale: 1, left: 0, top: 500 });
  });
});

async function mountEmbed(options = { canvas: DEFAULT_CANVAS, autoplay: true, retime: true }) {
  const stub = stubFetch({ [RECORDING_URL]: await fixtureRecordingText(), [BOARD_URL]: boardBytes() });
  const ticker = manualTicker();
  render(<ReplayApp recordingUrl={RECORDING_URL} boardUrl={BOARD_URL} fetchFn={stub.fetchFn} origin={ORIGIN} ticker={ticker} embed={options} />);
  await screen.findByTestId('embed-canvas');
  await act(async () => {
    await screen.findByTestId('replay-body').then(() => new Promise((r) => setTimeout(r, 50)));
  });
  return ticker;
}

describe('the embed', () => {
  it('draws the board on the fixed canvas with the replay label and no control chrome', async () => {
    await mountEmbed();
    const canvas = screen.getByTestId('embed-canvas');
    expect(canvas.style.width).toBe('960px');
    expect(canvas.style.height).toBe('1080px');
    expect(screen.getByTestId('replay-label').textContent).toBe('Replay of a recorded run · times moved to now');
    expect(canvas.contains(screen.getByTestId('replay-label'))).toBe(true);
    expect(screen.queryByTestId('transport-bar')).toBeNull();
    expect(screen.queryByRole('button', { name: 'About this recording' })).toBeNull();
    await waitFor(() => expect(canvas.querySelectorAll('[data-widget]').length).toBeGreaterThan(0));
  });

  it('says only that it is a replay when it is not re-timed', async () => {
    await mountEmbed({ canvas: DEFAULT_CANVAS, autoplay: false, retime: false });
    expect(screen.getByTestId('replay-label').textContent).toBe('Replay of a recorded run');
    expect(screen.getByTestId('replay-body').getAttribute('data-state')).toBe('paused');
    expect(built.sources.every((s) => s.timeShiftMs === 0)).toBe(true);
  });

  it('plays from the first chapter, re-timed so the cursor reads as now', async () => {
    const before = Date.now();
    await mountEmbed();
    expect(screen.getByTestId('replay-body').getAttribute('data-state')).toBe('playing');
    const source = built.sources[built.sources.length - 1];
    // The fixture's first chapter is 20 s in; the view built there says it is now.
    expect(source.now()).toBeGreaterThanOrEqual(before - 5);
    expect(source.now()).toBeLessThanOrEqual(Date.now() + 5);
    expect(source.timeShiftMs).not.toBe(0);
  });

  it('starts over from the first chapter at the end, with a fresh view, and keeps playing', async () => {
    const ticker = await mountEmbed();
    const views = built.sources.length;
    await act(async () => {
      ticker.frame(60_000); // past the fixture's 60 s end
      await new Promise((r) => setTimeout(r, 20));
    });
    expect(built.sources.length).toBeGreaterThan(views);
    expect(screen.getByTestId('replay-body').getAttribute('data-state')).toBe('playing');
    const latest = built.sources[built.sources.length - 1];
    // Built at the loop: its cursor (the first chapter again) reads as the moment it was built.
    expect(Math.abs(latest.now() - Date.now())).toBeLessThan(1_000);
  });
});
