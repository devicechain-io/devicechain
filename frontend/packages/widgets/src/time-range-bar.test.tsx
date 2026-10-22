// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { act, cleanup, fireEvent, render, renderHook, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { TimeRangeBar, useBoardTimeRange } from './time-range-bar';
import { useHeld } from './use-held';

const NOW = Date.parse('2026-10-14T12:00:00Z');

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe('useBoardTimeRange', () => {
  it('starts at the board default, live', () => {
    const { result } = renderHook(() => useBoardTimeRange(21600));
    expect(result.current[0]).toEqual({ spanSeconds: 21600, paused: false });
  });

  it('pauses at the clock, keeps the instant through a span change, and resumes', () => {
    const { result } = renderHook(() => useBoardTimeRange(undefined));
    act(() => result.current[1].pause());
    expect(result.current[0]).toEqual({ spanSeconds: 3600, paused: true, pausedAt: NOW });
    vi.setSystemTime(NOW + 120_000);
    act(() => result.current[1].setSpan(86400));
    expect(result.current[0]).toEqual({ spanSeconds: 86400, paused: true, pausedAt: NOW });
    act(() => result.current[1].resume());
    expect(result.current[0]).toEqual({ spanSeconds: 86400, paused: false });
  });

  it('follows the author changing the board default, but not an ordinary re-render', () => {
    const { result, rerender } = renderHook(({ d }: { d: number | undefined }) => useBoardTimeRange(d), {
      initialProps: { d: 3600 as number | undefined },
    });
    act(() => result.current[1].setSpan(900)); // the viewer picks 15m
    rerender({ d: 3600 });
    expect(result.current[0].spanSeconds).toBe(900);
    rerender({ d: 21600 }); // the author moves the default
    expect(result.current[0].spanSeconds).toBe(21600);
    rerender({ d: undefined }); // ...and clears it
    expect(result.current[0].spanSeconds).toBe(3600);
  });

  it('ignores an unusable span from the controls', () => {
    const { result } = renderHook(() => useBoardTimeRange(undefined));
    act(() => result.current[1].setSpan(5));
    expect(result.current[0].spanSeconds).toBe(3600);
  });
});

describe('<TimeRangeBar>', () => {
  const controls = () => ({ setSpan: vi.fn(), pause: vi.fn(), resume: vi.fn(), setAbsolute: vi.fn() });

  it('marks the active preset and sets a span on click', () => {
    const c = controls();
    render(<TimeRangeBar state={{ spanSeconds: 21600, paused: false }} controls={c} />);
    expect(screen.getByRole('button', { name: '6h' }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByRole('button', { name: '1h' }).getAttribute('aria-pressed')).toBe('false');
    fireEvent.click(screen.getByRole('button', { name: '7d' }));
    expect(c.setSpan).toHaveBeenCalledWith(604800);
  });

  it('shows a board default that is none of the presets under its own label, as the selected one', () => {
    render(<TimeRangeBar state={{ spanSeconds: 7200, paused: false }} controls={controls()} />);
    expect(screen.getByRole('button', { name: '2h' }).getAttribute('aria-pressed')).toBe('true');
    for (const id of ['15m', '1h', '6h', '24h', '7d']) {
      expect(screen.getByRole('button', { name: id }).getAttribute('aria-pressed'), id).toBe('false');
    }
  });

  it('is unmistakable when paused: a Paused indicator with the instant, and the button offers Resume', () => {
    const c = controls();
    render(<TimeRangeBar state={{ spanSeconds: 3600, paused: true, pausedAt: NOW }} controls={c} />);
    const pill = document.querySelector('[data-time-range-paused]') as HTMLElement;
    expect(pill.textContent).toContain('Paused');
    expect(pill.textContent).toContain(new Date(NOW).toLocaleTimeString());
    expect(pill.getAttribute('role')).toBe('status');
    // A pause freezes what the board shows, not what it does: the pill says commands still go out.
    expect(pill.querySelector('[data-time-range-actions-live]')?.textContent).toContain('commands still send');
    fireEvent.click(screen.getByRole('button', { name: 'Resume' }));
    expect(c.resume).toHaveBeenCalledTimes(1);
    expect(c.pause).not.toHaveBeenCalled();
  });

  it('live: no indicator, and the button offers Pause', () => {
    const c = controls();
    render(<TimeRangeBar state={{ spanSeconds: 3600, paused: false }} controls={c} />);
    expect(document.querySelector('[data-time-range-paused]')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Pause' }));
    expect(c.pause).toHaveBeenCalledTimes(1);
  });

  it('takes its words from the host', () => {
    render(
      <TimeRangeBar
        state={{ spanSeconds: 3600, paused: true, pausedAt: NOW }}
        controls={controls()}
        labels={{ range: 'Rango', pause: 'Pausar', resume: 'Reanudar', paused: 'En pausa', pausedAt: (t) => `desde ${t}`, actionsLive: 'los comandos se siguen enviando' }}
      />,
    );
    expect(screen.getByRole('button', { name: 'Reanudar' })).toBeTruthy();
    expect(screen.getByRole('group', { name: 'Rango' })).toBeTruthy();
    expect(document.querySelector('[data-time-range-paused]')?.textContent).toContain('En pausa');
    expect(document.querySelector('[data-time-range-actions-live]')?.textContent).toContain('los comandos se siguen enviando');
  });
});

describe('useHeld', () => {
  const ready = (v: { loading: boolean }) => !v.loading;

  it('passes the value through while not held', () => {
    const { result, rerender } = renderHook(({ v }) => useHeld(v, false, ready), { initialProps: { v: { loading: false, n: 1 } } });
    rerender({ v: { loading: false, n: 2 } });
    expect(result.current.n).toBe(2);
  });

  it('holds the value it had when the hold began, then releases to the live one', () => {
    const { result, rerender } = renderHook(({ v, hold }) => useHeld(v, hold, ready), {
      initialProps: { v: { loading: false, n: 1 }, hold: false },
    });
    rerender({ v: { loading: false, n: 2 }, hold: true });
    rerender({ v: { loading: false, n: 3 }, hold: true });
    expect(result.current.n).toBe(2);
    rerender({ v: { loading: false, n: 4 }, hold: false });
    expect(result.current.n).toBe(4);
  });

  it('holding from the start waits for the first READY value and keeps that one', () => {
    const { result, rerender } = renderHook(({ v }) => useHeld(v, true, ready), { initialProps: { v: { loading: true, n: 0 } } });
    expect(result.current.n).toBe(0);
    rerender({ v: { loading: false, n: 5 } });
    rerender({ v: { loading: false, n: 6 } });
    expect(result.current.n).toBe(5);
  });
});
