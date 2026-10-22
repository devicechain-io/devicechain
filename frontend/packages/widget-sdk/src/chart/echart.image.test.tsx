// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// <EChart> offers its canvas to the frame it is drawn in, and the frame's "Download image" reads it
// through getDataURL: a PNG at twice the on-screen resolution, on an opaque background (the canvas is
// transparent). echarts/core's init is stubbed (jsdom has no canvas); the engine chunk is the real one.

const ARRIVAL = { timeout: 15_000 };

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

globalThis.ResizeObserver ??= class {
  observe() {}
  disconnect() {}
  unobserve() {}
} as unknown as typeof ResizeObserver;

const getDataURL = vi.fn((_options: unknown) => 'data:image/png;base64,CHART');

vi.mock('echarts/core', async (importOriginal) => {
  const real = await importOriginal<typeof import('echarts/core')>();
  return {
    ...real,
    init: (el: HTMLElement) => {
      el.setAttribute('_echarts_instance_', 'stub');
      return { setOption: () => {}, resize: () => {}, dispose: () => {}, getDataURL };
    },
  };
});

import { WidgetFrame } from '../frame';
import { EChart } from './echart';

let clicked: Array<{ href: string; download: string }>;

beforeEach(() => {
  document.documentElement.lang = 'en';
  document.body.style.background = 'rgb(250, 250, 250)';
  clicked = [];
  getDataURL.mockClear();
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    clicked.push({ href: this.href, download: this.download });
  });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  document.body.style.background = '';
});

describe('a chart inside a frame', () => {
  it('is saved as a PNG at twice the resolution on the page’s background, once the chart has drawn', async () => {
    render(
      <WidgetFrame title="Temperature" flush>
        <EChart option={{ series: [] }} />
      </WidgetFrame>,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Export data' }, ARRIVAL));
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Download image (PNG)' }));
    await waitFor(() => expect(clicked).toHaveLength(1));
    expect(getDataURL).toHaveBeenCalledWith({ type: 'png', pixelRatio: 2, backgroundColor: 'rgb(250, 250, 250)' });
    expect(clicked[0].href).toBe('data:image/png;base64,CHART');
    expect(clicked[0].download).toMatch(/^Temperature-\d{8}T\d{4}Z\.png$/);
  });

  it('offers no export outside a frame, where there is no menu to put it in', () => {
    render(<EChart option={{ series: [] }} />);
    expect(screen.queryByRole('button', { name: 'Export data' })).toBeNull();
  });
});
