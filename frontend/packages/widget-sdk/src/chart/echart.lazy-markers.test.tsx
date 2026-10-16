// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The <EChart> half of the on-demand threshold markers: an option that asks for a markLine
// is applied at once (without it), and applied AGAIN once the markers have registered -- the
// second setOption is what makes the thresholds appear. Without it the first threshold chart
// of a page would never draw its lines. echarts/core's init is stubbed (jsdom has no canvas);
// `use` stays real so the registration itself is the real one. The chart engine is loaded first,
// as preloadWidgets does for a board: what is under test here is the markers, not the engine.

import { act, render } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

const setOption = vi.fn();
vi.mock('echarts/core', async (importOriginal) => {
  const real = await importOriginal<typeof import('echarts/core')>();
  return {
    ...real,
    init: () => ({ setOption, resize: () => {}, dispose: () => {} }),
  };
});

globalThis.ResizeObserver ??= class {
  observe() {}
  disconnect() {}
  unobserve() {}
} as unknown as typeof ResizeObserver;

const { EChart, loadChartEngine, loadThresholdMarkers } = await import('./echart');
await loadChartEngine();

describe('EChart with threshold markers', () => {
  it('applies the option once without the markers, then again after they register', async () => {
    const option = { series: [{ type: 'line', data: [], markLine: { data: [{ yAxis: 1 }] } }] };
    render(<EChart option={option} />);
    expect(setOption).toHaveBeenCalledTimes(1);
    await act(async () => {
      await loadThresholdMarkers();
    });
    expect(setOption).toHaveBeenCalledTimes(2);
    expect(setOption.mock.calls[1][0]).toBe(option);
  });

  it('a chart without markers is applied once and does not wait on them', () => {
    setOption.mockClear();
    render(<EChart option={{ series: [{ type: 'line', data: [] }] }} />);
    expect(setOption).toHaveBeenCalledTimes(1);
  });
});
