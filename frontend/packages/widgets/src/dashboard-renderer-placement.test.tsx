// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The renderer draws the placement modes and the frames a definition declares: square cells
// from the grid's own width, a relative widget inside its host's area above every grid widget,
// and each widget's frame (the canvas default, merged with its own).

import { DashboardHub, type DashboardDefinition } from '@devicechain/dashboards';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, subscribe: () => () => {}, gql: async () => ({}) };
});

import { DashboardRenderer } from './dashboard-renderer';

afterEach(cleanup);

const board: DashboardDefinition = {
  schemaVersion: 2,
  title: 'Placement',
  canvas: {
    grid: { columns: 12, minCellSize: 56, gutter: { x: 8, y: 'space-1' }, padding: 'space-4' },
    sizing: 'fill',
    breakpoints: { base: 0 },
    frame: { variant: 'flat' },
  },
  widgets: [
    { id: 'host', type: 'label', layout: { base: { mode: 'grid', col: 2, row: 1, cols: 4, rows: 3 } }, options: { text: 'host' } },
    {
      id: 'badge',
      type: 'label',
      layout: { base: { mode: 'relative', host: 'host', anchor: 'top-right', offset: { x: -0.25, y: 0.25 }, size: { cols: 1, rows: 1 } } },
      options: { text: '!' },
      frame: { variant: 'none' },
    },
    {
      id: 'caption',
      type: 'label',
      layout: { base: { mode: 'relative', host: 'badge', anchor: 'bottom', self: 'top', size: { cols: 2, rows: 1 } } },
      options: { text: 'caption' },
    },
  ],
};

const resolver = { devicesForAnchor: async () => [], deviceExists: async () => true };

async function draw(def: DashboardDefinition) {
  const hub = new DashboardHub({ resolver });
  const { container } = render(<DashboardRenderer definition={def} hub={hub} seedHistory={false} bindings={{}} />);
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
  hub.disposeAll();
  return container;
}

describe('renderer placement', () => {
  it('makes rows as tall as a cell is wide, from the grid width less its gutters', async () => {
    const c = await draw(board);
    const grid = c.querySelector('[data-grid]') as HTMLElement;
    expect(grid.style.gridTemplateColumns).toBe('repeat(12, minmax(0, 1fr))');
    expect(grid.style.gridAutoRows).toBe('var(--dc-cell)');
    expect(grid.style.getPropertyValue('--dc-cell')).toBe('calc((100cqw - 11 * calc(8px * var(--dc-scale, 1))) / 12)');
    expect(grid.style.rowGap).toBe('var(--dc-space-1)');
    // The cell is read inside a query container whose width is the grid's: the padding is outside it.
    expect((grid.parentElement as HTMLElement).style.containerType).toBe('inline-size');
    expect((grid.parentElement!.parentElement as HTMLElement).style.padding).toBe('var(--dc-space-4)');
  });

  it('places a grid widget on its footprint', async () => {
    const c = await draw(board);
    const host = c.querySelector('[data-widget="host"]') as HTMLElement;
    expect(host.style.gridColumn).toBe('3 / span 4');
    expect(host.style.gridRow).toBe('2 / span 3');
  });

  it('draws a relative widget in a layer over its host’s area, and one relative to IT inside its box', async () => {
    const c = await draw(board);
    const badge = c.querySelector('[data-widget="badge"]') as HTMLElement;
    expect(badge.dataset.host).toBe('host');
    const layer = badge.parentElement as HTMLElement;
    expect(layer.style.gridColumn).toBe('3 / span 4'); // the host's area
    expect(layer.style.pointerEvents).toBe('none'); // the host underneath stays usable
    expect(Number(badge.style.zIndex)).toBeGreaterThan(0); // above every grid widget
    const caption = c.querySelector('[data-widget="caption"]') as HTMLElement;
    expect(caption.parentElement).toBe(badge);
    expect(caption.style.top).toBe('calc(100% + 0 * (var(--dc-cell) + var(--dc-space-1)))');
    // Every widget in the definition is on the page, once.
    expect(c.querySelectorAll('[data-widget]')).toHaveLength(3);
  });

  it('frames each widget with the canvas default, merged with its own', async () => {
    const c = await draw(board);
    const frameOf = (id: string) => (c.querySelector(`[data-widget="${id}"]`) as HTMLElement).querySelector('[data-widget-body]')!
      .parentElement as HTMLElement;
    expect(frameOf('host').style.borderStyle).toBe('none'); // flat, from the canvas
    expect(frameOf('host').style.background).toBe('hsl(var(--card))');
    expect(frameOf('badge').style.background).toBe('transparent'); // its own `none`
  });
});
