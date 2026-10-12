// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { act, cleanup, render, renderHook, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { FlashValue, flashTextStyle, useFlashOnChange } from './flash';

afterEach(cleanup);

// Longer than the hook's internal hold window — advancing this always clears a held tint.
const PAST_HOLD = 5_000;

describe('useFlashOnChange', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('does not flash on mount', () => {
    const { result } = renderHook(({ v }) => useFlashOnChange(v, true), {
      initialProps: { v: 21 as number | null | undefined },
    });
    expect(result.current).toBeNull();
  });

  it('flashes up on a rise and down on a fall, then clears', () => {
    const { result, rerender } = renderHook(({ v }) => useFlashOnChange(v, true), {
      initialProps: { v: 21 as number | null | undefined },
    });

    act(() => rerender({ v: 22 }));
    expect(result.current).toBe('up');

    act(() => rerender({ v: 20 }));
    expect(result.current).toBe('down');

    // The held direction auto-clears after the hold window.
    act(() => vi.advanceTimersByTime(PAST_HOLD));
    expect(result.current).toBeNull();
  });

  it('does not flash when the value is unchanged', () => {
    const { result, rerender } = renderHook(({ v }) => useFlashOnChange(v, true), {
      initialProps: { v: 21 as number | null | undefined },
    });
    act(() => rerender({ v: 21 }));
    expect(result.current).toBeNull();
  });

  it('never flashes while disabled, and enabling later does not flash a stale change', () => {
    const { result, rerender } = renderHook(({ v, on }) => useFlashOnChange(v, on), {
      initialProps: { v: 21 as number | null | undefined, on: false },
    });
    // A change while disabled produces no flash...
    act(() => rerender({ v: 25, on: false }));
    expect(result.current).toBeNull();
    // ...and turning it on (no value change) does not flash the value it never saw rise.
    act(() => rerender({ v: 25, on: true }));
    expect(result.current).toBeNull();
    // A subsequent real change now flashes.
    act(() => rerender({ v: 26, on: true }));
    expect(result.current).toBe('up');
  });

  it('ignores non-numeric and non-finite transitions', () => {
    const { result, rerender } = renderHook(({ v }) => useFlashOnChange(v, true), {
      initialProps: { v: 21 as number | null | undefined },
    });
    act(() => rerender({ v: null }));
    expect(result.current).toBeNull();
    act(() => rerender({ v: 21 }));
    expect(result.current).toBeNull(); // coming out of null: `before` is non-numeric, not a rise
    act(() => rerender({ v: 25 }));
    expect(result.current).toBe('up'); // now both finite → a real rise flashes
    act(() => rerender({ v: NaN }));
    expect(result.current).toBeNull(); // NaN is non-finite, not a fall
  });

  // Regression: a dep change that takes the early-return branch mid-hold must not strand the
  // tint (the timer is cancelled by cleanup, so the branch has to clear `direction` itself).
  it('clears a held tint when the value goes non-numeric mid-hold', () => {
    const { result, rerender } = renderHook(({ v }) => useFlashOnChange(v, true), {
      initialProps: { v: 21 as number | null | undefined },
    });
    act(() => rerender({ v: 22 }));
    expect(result.current).toBe('up');
    // Device drops offline before the hold elapses — the value clears, so must the tint.
    act(() => rerender({ v: null }));
    expect(result.current).toBeNull();
    act(() => vi.advanceTimersByTime(PAST_HOLD));
    expect(result.current).toBeNull();
  });

  it('clears a held tint when disabled mid-hold and does not resurrect it on re-enable', () => {
    const { result, rerender } = renderHook(({ v, on }) => useFlashOnChange(v, on), {
      initialProps: { v: 21 as number | null | undefined, on: true },
    });
    act(() => rerender({ v: 22, on: true }));
    expect(result.current).toBe('up');
    act(() => rerender({ v: 22, on: false }));
    expect(result.current).toBeNull();
    // Re-enabling with the same value must not bring back the stale 'up' (no timer exists).
    act(() => rerender({ v: 22, on: true }));
    expect(result.current).toBeNull();
  });

  // Regression: an anchor's one measurement slot interleaves values across member devices;
  // a change that coincides with an identity switch is a different entity, not a rise/fall.
  it('does not flash when identity changes, then flashes a real same-entity change', () => {
    const { result, rerender } = renderHook(({ v, id }) => useFlashOnChange(v, true, id), {
      initialProps: { v: 21 as number | null | undefined, id: 'dev-a' },
    });
    // Same value slot, different device, different value → adopt, do not flash.
    act(() => rerender({ v: 30, id: 'dev-b' }));
    expect(result.current).toBeNull();
    // A real change within the same device now flashes.
    act(() => rerender({ v: 31, id: 'dev-b' }));
    expect(result.current).toBe('up');
  });
});

describe('flashTextStyle', () => {
  it('snaps to a color while flashing and fades when cleared', () => {
    const up = flashTextStyle('up');
    expect(up.transition).toBe('none');
    expect(String(up.color)).toContain('flash-up');

    const down = flashTextStyle('down');
    expect(String(down.color)).toContain('flash-down');

    const cleared = flashTextStyle(null);
    expect(cleared.color).toBeUndefined();
    expect(String(cleared.transition)).toContain('color');
  });
});

describe('FlashValue', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('renders the formatted value and snaps the transition off on a rise', () => {
    const { rerender } = render(<FlashValue value={21.239} precision={1} enabled />);
    const span = screen.getByText('21.2');
    // No flash on mount: base color, no snap.
    expect(span.style.transition).not.toBe('none');

    act(() => rerender(<FlashValue value={22.5} precision={1} enabled />));
    // The snap (transition:none) is the reliable flashing signal — jsdom's CSSOM may not
    // retain a var()-based `color`, so the exact tint is asserted on flashTextStyle directly.
    const risen = screen.getByText('22.5');
    expect(risen.style.transition).toBe('none');
  });

  it('does not snap when disabled', () => {
    const { rerender } = render(<FlashValue value={21} enabled={false} />);
    act(() => rerender(<FlashValue value={99} enabled={false} />));
    const span = screen.getByText('99');
    expect(span.style.transition).not.toBe('none');
  });
});
