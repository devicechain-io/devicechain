// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The chart engine (ECharts + zrender) is loaded on demand, as one chunk, and <EChart> stays a
// synchronous component that a third-party widget renders exactly as before:
//   - mounted before anyone loaded the engine, it fetches it and draws when it arrives;
//   - mounted after loadChartEngine, it draws in its first commit (no empty first frame);
//   - a failed fetch is THROWN to the error boundary, and a Reset fetches it again.
// echarts/core's init is stubbed (jsdom has no canvas); each test gets fresh modules, so the
// engine one test loaded does not make the next test's first frame full.
//
// A fresh module graph re-imports the real ECharts, which takes over a second on a busy machine,
// so every wait for the engine to ARRIVE carries ARRIVAL's timeout, not the 1 s default (that
// default failed this file under a full-workspace run). A wait for something that must NOT
// happen stays a fixed sleep.
const ARRIVAL = { timeout: 15_000 };

import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { Component, type ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

globalThis.ResizeObserver ??= class {
  observe() {}
  disconnect() {}
  unobserve() {}
} as unknown as typeof ResizeObserver;

const init = vi.fn();
const setOption = vi.fn();

// echarts/core with init stubbed; `fail` is how many times the engine chunk's import throws
// before it succeeds (the chunk itself is what a browser fails to fetch). `asked` counts the
// engine imports, so a test can see a retry -- and the absence of one.
function mockEcharts(fail = 0) {
  vi.doMock('echarts/core', async (importOriginal) => {
    const real = await importOriginal<typeof import('echarts/core')>();
    return {
      ...real,
      init: (el: HTMLElement) => {
        init(el);
        el.setAttribute('_echarts_instance_', 'stub');
        return { setOption, resize: () => {}, dispose: () => {} };
      },
    };
  });
  let asked = 0;
  vi.doMock('./echart-engine', async (importOriginal) => {
    if (asked++ < fail) throw new Error('Failed to fetch dynamically imported module');
    return importOriginal();
  });
  return () => asked;
}

async function fresh() {
  vi.resetModules();
  return import('./echart');
}

afterEach(() => {
  cleanup();
  init.mockClear();
  setOption.mockClear();
  vi.doUnmock('echarts/core');
  vi.doUnmock('./echart-engine');
  vi.doUnmock('./echart-markers');
  vi.resetModules();
});

const option = { series: [{ type: 'gauge', data: [{ value: 1 }] }] };

class Boundary extends Component<{ children: ReactNode }, { error?: unknown; key: number }> {
  state: { error?: unknown; key: number } = { key: 0 };
  static getDerivedStateFromError(error: unknown) {
    return { error };
  }
  render() {
    if (this.state.error !== undefined) {
      return (
        <div>
          <p>boundary: {String(this.state.error)}</p>
          <button onClick={() => this.setState((s) => ({ error: undefined, key: s.key + 1 }))}>Reset</button>
        </div>
      );
    }
    return <div key={this.state.key}>{this.props.children}</div>;
  }
}

describe('EChart and the on-demand chart engine', { timeout: 30_000 }, () => {
  it('importing the wrapper does not load the engine', async () => {
    const asked = mockEcharts();
    await fresh();
    expect(asked()).toBe(0);
  });

  // The control for the next test: without a preload, the first commit has not drawn.
  it('mounted before the engine is here, it draws once the engine arrives', async () => {
    mockEcharts();
    const { EChart } = await fresh();
    const { container } = render(<EChart option={option} />);
    const box = container.querySelector('[data-echart]')!;
    expect(box).not.toBeNull();
    expect(init).not.toHaveBeenCalled();
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
    await vi.waitFor(() => expect(init).toHaveBeenCalledWith(box), ARRIVAL);
    expect(setOption).toHaveBeenLastCalledWith(option, { notMerge: true });
    expect(box.hasAttribute('_echarts_instance_')).toBe(true);
  });

  it('after loadChartEngine, it draws in its first commit', async () => {
    mockEcharts();
    const { EChart, loadChartEngine } = await fresh();
    await loadChartEngine();
    const { container } = render(<EChart option={option} />);
    expect(init).toHaveBeenCalledTimes(1);
    expect(setOption).toHaveBeenCalledWith(option, { notMerge: true });
    expect(container.querySelector('[data-echart]')!.hasAttribute('_echarts_instance_')).toBe(true);
  });

  it('a failed load reaches the error boundary, and Reset fetches the engine again', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    const asked = mockEcharts(1);
    const { EChart } = await fresh();
    render(
      <Boundary>
        <EChart option={option} />
      </Boundary>,
    );
    expect(await screen.findByText(/^boundary: .*Error/, {}, ARRIVAL)).toBeTruthy();
    expect(init).not.toHaveBeenCalled();
    // Not retried on its own.
    await act(async () => void (await new Promise((r) => setTimeout(r, 50))));
    expect(asked()).toBe(1);

    await act(async () => void fireEvent.click(screen.getByText('Reset')));
    await vi.waitFor(() => expect(init).toHaveBeenCalledTimes(1), ARRIVAL);
    expect(screen.queryByText(/^boundary:/)).toBeNull();
    expect(asked()).toBe(2);
  });

  it('loadChartEngine rejects on a failed load, and a later call fetches again', async () => {
    const asked = mockEcharts(1);
    const { loadChartEngine } = await fresh();
    await expect(loadChartEngine()).rejects.toThrow();
    await expect(loadChartEngine()).resolves.toBeUndefined();
    expect(asked()).toBe(2);
  });

  // The threshold markers are memoized at two levels now (the wrapper's loadThresholdMarkers and
  // the engine's registerThresholdMarkers), so a failure remembered at EITHER one would leave every
  // later threshold chart of the page without its lines, with nothing retrying.
  it('a failed threshold-markers load is not remembered: the next call fetches again', async () => {
    mockEcharts();
    let markersAsked = 0;
    vi.doMock('./echart-markers', async (importOriginal) => {
      if (markersAsked++ < 1) throw new Error('Failed to fetch dynamically imported module');
      return importOriginal();
    });
    const { loadThresholdMarkers } = await fresh();
    await expect(loadThresholdMarkers()).rejects.toThrow();
    await expect(loadThresholdMarkers()).resolves.toBeUndefined();
    expect(markersAsked).toBe(2);
  });
});
