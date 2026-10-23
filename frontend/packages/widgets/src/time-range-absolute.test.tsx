// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { rangeWindow, timeRangeLinkFromSearch } from '@devicechain/dashboards';
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { TimeRangeBar, absoluteLinkFor, rejectedLinkNote, useBoardTimeRange } from './time-range-bar';

const NOW = Date.parse('2026-10-22T12:00:00Z');
const FROM = '2026-10-22T02:00:00Z';
const TO = '2026-10-22T04:00:00Z';

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(NOW);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe('useBoardTimeRange with an absolute window', () => {
  const link = () => timeRangeLinkFromSearch(`?from=${FROM}&to=${TO}`, NOW);

  it('opens on the window, with the rolling projection a rolling-only widget reads', () => {
    const { result } = renderHook(() => useBoardTimeRange(undefined, link()));
    const state = result.current[0];
    expect(state.absolute).toEqual({ fromMs: Date.parse(FROM), toMs: Date.parse(TO) });
    // The projection: frozen at the window's end, span = its length. A widget that only knows the
    // rolling shape derives EXACTLY the absolute window from it, with its live tail off.
    expect(state).toMatchObject({ paused: true, pausedAt: Date.parse(TO), spanSeconds: 7200 });
    expect(rangeWindow(state, NOW + 999_999)).toEqual({
      startTime: '2026-10-22T02:00:00.000Z',
      endTime: '2026-10-22T04:00:00.000Z',
    });
  });

  it('a preset click or Back to live leaves the window for a live board', () => {
    const { result } = renderHook(() => useBoardTimeRange(undefined, link()));
    act(() => result.current[1].setSpan(86400));
    expect(result.current[0]).toEqual({ spanSeconds: 86400, paused: false });
    act(() => {
      result.current[1].setAbsolute(Date.parse(FROM), Date.parse(TO));
    });
    expect(result.current[0].absolute).toBeDefined();
    act(() => result.current[1].resume());
    expect(result.current[0]).toEqual({ spanSeconds: 7200, paused: false });
  });

  it('refuses a bad window and keeps the state', () => {
    const { result } = renderHook(() => useBoardTimeRange(undefined));
    let problem;
    act(() => {
      problem = result.current[1].setAbsolute(Date.parse(TO), Date.parse(FROM));
    });
    expect(problem).toBe('order');
    expect(result.current[0].absolute).toBeUndefined();
  });

  it('does not let a changed board default rewrite a chosen window', () => {
    const { result, rerender } = renderHook(({ d }: { d: number }) => useBoardTimeRange(d, link()), {
      initialProps: { d: 3600 },
    });
    rerender({ d: 21600 });
    expect(result.current[0].absolute).toBeDefined();
    expect(result.current[0].spanSeconds).toBe(7200);
  });
});

describe('<TimeRangeBar> absolute', () => {
  const controls = () => ({ setSpan: vi.fn(), pause: vi.fn(), resume: vi.fn(), setAbsolute: vi.fn() });
  const abs = { fromMs: Date.parse(FROM), toMs: Date.parse(TO) };
  const state = { spanSeconds: 7200, paused: true, pausedAt: abs.toMs, absolute: abs };

  it('says Fixed window, not Paused, highlights no preset even at a preset length, and offers Back to live', () => {
    const c = controls();
    render(<TimeRangeBar state={{ ...state, spanSeconds: 3600 }} controls={c} />);
    expect(document.querySelector('[data-time-range-absolute]')?.textContent).toContain('Fixed window');
    expect(document.querySelector('[data-time-range-paused]')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Pause' })).toBeNull();
    expect(screen.getByRole('button', { name: '1h' }).getAttribute('aria-pressed')).toBe('false');
    fireEvent.click(screen.getByRole('button', { name: 'Back to live' }));
    expect(c.resume).toHaveBeenCalledTimes(1);
  });

  it('shows the zone on the window and UTC in the tooltip', () => {
    render(<TimeRangeBar state={state} controls={controls()} />);
    const pill = document.querySelector('[data-time-range-absolute]') as HTMLElement;
    expect(pill.getAttribute('title')).toBe('2026-10-22T02:00:00.000Z \u2013 2026-10-22T04:00:00.000Z (UTC)');
    expect(pill.querySelector('[data-time-range-window]')?.textContent).toMatch(/[A-Z]{2,5}|GMT|UTC/);
  });

  it('loads the picker on demand, applies a window and shows the refusal in place', async () => {
    const c = controls();
    c.setAbsolute.mockReturnValueOnce('order').mockReturnValueOnce(undefined);
    render(<TimeRangeBar state={{ spanSeconds: 3600, paused: false }} controls={c} />);
    expect(document.querySelector('[data-absolute-picker]')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Custom range' }));
    await waitFor(() => expect(document.querySelector('[data-absolute-picker]')).not.toBeNull());
    expect(document.querySelector('[data-absolute-zone]')?.textContent).toMatch(
      /Times are in .* \(UTC[+-]\d\d:\d\d\)\. Shared links use UTC\./,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }));
    expect(document.querySelector('[data-absolute-problem="order"]')?.textContent).toContain('start must be before');
    expect(document.querySelector('[data-absolute-picker]')).not.toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }));
    expect(c.setAbsolute).toHaveBeenCalledTimes(2);
    expect(document.querySelector('[data-absolute-picker]')).toBeNull();
  });

  it('shows a dismissible note for an ignored link', () => {
    render(
      <TimeRangeBar state={{ spanSeconds: 3600, paused: false }} controls={controls()} notice="The link was ignored" />,
    );
    expect(screen.getByRole('status').textContent).toContain('The link was ignored');
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
    expect(document.querySelector('[data-time-range-notice]')).toBeNull();
  });
});

describe('link helpers', () => {
  it('builds a UTC link and drops the rolling parameters', () => {
    const href = absoluteLinkFor('https://x.test/dash/?dashboard=b&range=1h&paused=1', {
      fromMs: Date.parse(FROM),
      toMs: Date.parse(TO),
    });
    const q = new URL(href).searchParams;
    expect(q.get('from')).toBe('2026-10-22T02:00:00.000Z');
    expect(q.get('to')).toBe('2026-10-22T04:00:00.000Z');
    expect(q.get('dashboard')).toBe('b');
    expect(q.has('range')).toBe(false);
    expect(q.has('paused')).toBe(false);
  });

  it('words an ignored link, naming the values', () => {
    const note = rejectedLinkNote({ reason: 'order', from: TO, to: FROM });
    expect(note).toContain('ignored');
    expect(note).toContain('start must be before the end');
    expect(note).toContain(`from=${TO}`);
    expect(rejectedLinkNote({ reason: 'unparseable', from: '2026-10-22T02:00', to: TO })).toContain('with a zone');
  });
});
