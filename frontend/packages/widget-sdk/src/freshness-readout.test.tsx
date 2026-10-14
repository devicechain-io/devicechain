// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { WidgetFreshnessProvider } from './context';
import { Freshness } from './freshness-readout';
import { WidgetFrame } from './frame';

const T0 = Date.parse('2026-10-14T12:00:00Z');

beforeEach(() => {
  document.documentElement.lang = 'en';
  vi.useFakeTimers();
  vi.setSystemTime(T0);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const tick = (ms: number) => act(() => void vi.advanceTimersByTime(ms));

describe('<Freshness>', () => {
  it('shows the age and ticks as time passes', () => {
    render(<Freshness at={T0 - 12_000} />);
    expect(screen.getByText(/12 sec/)).toBeTruthy();
    tick(5_000);
    expect(screen.getByText(/17 sec/)).toBeTruthy();
    tick(60_000);
    expect(screen.getByText(/1 min/)).toBeTruthy();
  });

  it('renders nothing before any sample', () => {
    const { container } = render(<Freshness at={undefined} />);
    expect(container.innerHTML).toBe('');
  });

  it('localises the age and the badge word', () => {
    document.documentElement.lang = 'es';
    render(<Freshness at={T0 - 12_000} staleAfterSeconds={5} />);
    expect(screen.getByText(/hace 12/)).toBeTruthy();
    expect(screen.getByText('obsoleto')).toBeTruthy();
  });

  it('goes stale when the threshold passes, and not before', () => {
    render(<Freshness at={T0 - 55_000} staleAfterSeconds={60} />);
    expect(screen.queryByText('stale')).toBeNull();
    tick(6_000);
    expect(screen.getByText('stale')).toBeTruthy();
  });

  it('ages a sample stamped AHEAD of the viewer from when it arrived, not "now" forever', () => {
    // A device clock an hour fast: clamping alone would read "now" for the next hour.
    render(<Freshness at={T0 + 3_600_000} staleAfterSeconds={30} />);
    expect(screen.getByText('now')).toBeTruthy();
    tick(45_000);
    expect(screen.getByText(/45 sec/)).toBeTruthy();
    expect(screen.getByText('stale')).toBeTruthy();
  });

  it('restarts the arrival clock for a new future-stamped sample', () => {
    const { rerender } = render(<Freshness at={T0 + 3_600_000} />);
    tick(20_000);
    expect(screen.getByText(/20 sec/)).toBeTruthy();
    rerender(<Freshness at={T0 + 3_620_000} />);
    expect(screen.getByText('now')).toBeTruthy();
  });

  it('drives every readout on a board from ONE interval', () => {
    const spy = vi.spyOn(globalThis, 'setInterval');
    render(
      <>
        <Freshness at={T0 - 1_000} />
        <Freshness at={T0 - 2_000} />
        <Freshness at={T0 - 3_000} />
      </>,
    );
    expect(spy.mock.calls.filter(([, ms]) => ms === 1000)).toHaveLength(1);
    tick(1_000);
    expect(screen.getByText(/4 sec/)).toBeTruthy();
    spy.mockRestore();
  });

  it('never goes stale without a threshold', () => {
    render(<Freshness at={T0 - 3 * 86400_000} />);
    tick(3_600_000);
    expect(screen.queryByText('stale')).toBeNull();
  });
});

describe('WidgetFrame freshness', () => {
  const framed = (freshness: { at: number | undefined; staleAfterSeconds: number | undefined } | undefined) =>
    render(
      <WidgetFreshnessProvider freshness={freshness}>
        <WidgetFrame title="t">value</WidgetFrame>
      </WidgetFreshnessProvider>,
    );

  it('draws no readout for a widget that did not opt in', () => {
    const { container } = framed(undefined);
    expect(container.querySelector('[data-widget-freshness]')).toBeNull();
  });

  it('shows the age but does not grey the value when no threshold is known', () => {
    const { container } = framed({ at: T0 - 7 * 86400_000, staleAfterSeconds: undefined });
    expect(container.querySelector('[data-widget-freshness]')).not.toBeNull();
    const body = container.querySelector('[data-widget-body]') as HTMLElement;
    expect(body.style.filter).toBe('');
    expect(screen.queryByText('stale')).toBeNull();
  });

  it('greys the value and badges it once past a known threshold', () => {
    const { container } = framed({ at: T0 - 120_000, staleAfterSeconds: 60 });
    const body = container.querySelector('[data-widget-body]') as HTMLElement;
    expect(body.style.filter).toBe('grayscale(1)');
    expect(screen.getByText('stale')).toBeTruthy();
  });

  it('ages the sample the widget shows when it passes one, not the newest the host holds', () => {
    render(
      <WidgetFreshnessProvider freshness={{ at: T0 - 2_000, staleAfterSeconds: 60 }}>
        <WidgetFrame title="t" sampleTime={new Date(T0 - 300_000).toISOString()}>
          value
        </WidgetFrame>
      </WidgetFreshnessProvider>,
    );
    expect(screen.getByText(/5 min/)).toBeTruthy();
    expect(screen.getByText('stale')).toBeTruthy();
  });

  it('shows no age for a shown value that has no sample yet', () => {
    const { container } = render(
      <WidgetFreshnessProvider freshness={{ at: T0 - 2_000, staleAfterSeconds: 60 }}>
        <WidgetFrame title="t" sampleTime={null}>
          value
        </WidgetFrame>
      </WidgetFreshnessProvider>,
    );
    expect(container.querySelector('[data-widget-freshness]')).toBeNull();
  });

  it('keeps the value normal while within the threshold', () => {
    const { container } = framed({ at: T0 - 10_000, staleAfterSeconds: 60 });
    expect((container.querySelector('[data-widget-body]') as HTMLElement).style.filter).toBe('');
  });
});

describe('WidgetFrame freshness on a small widget', () => {
  const originalObserver = globalThis.ResizeObserver;
  afterEach(() => {
    globalThis.ResizeObserver = originalObserver;
  });

  function measured(width: number, height: number) {
    globalThis.ResizeObserver = class {
      constructor(private cb: ResizeObserverCallback) {}
      observe() {
        this.cb([{ contentRect: { width, height } } as ResizeObserverEntry], this as unknown as ResizeObserver);
      }
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
  }

  const framed = (staleAfterSeconds: number | undefined) =>
    render(
      <WidgetFreshnessProvider freshness={{ at: T0 - 120_000, staleAfterSeconds }}>
        <WidgetFrame title="t">value</WidgetFrame>
      </WidgetFreshnessProvider>,
    );

  it('drops the age readout so the value keeps its room', () => {
    measured(100, 60);
    const { container } = framed(undefined);
    expect(container.querySelector('[data-widget-freshness]')).toBeNull();
  });

  it('still badges a stale small widget, in the corner', () => {
    measured(100, 60);
    const { container } = framed(60);
    expect(container.querySelector('[data-widget-freshness]')).toBeNull();
    expect(screen.getByText('stale')).toBeTruthy();
  });

  it('keeps the readout on a roomy widget', () => {
    measured(400, 300);
    const { container } = framed(undefined);
    expect(container.querySelector('[data-widget-freshness]')).not.toBeNull();
  });
});
