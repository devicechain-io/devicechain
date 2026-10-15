// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The renderer measures its CONTAINER and reflows: the authored layout while its cells fit, the
// column count that fits otherwise, the base layout poured into it in reading order, and a
// relative widget following its host. jsdom has no layout, so the container's width is supplied.

import { DashboardHub, type DashboardDefinition } from '@devicechain/dashboards';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, subscribe: () => () => {}, gql: async () => ({}) };
});

import { DashboardRenderer } from './dashboard-renderer';

afterEach(() => {
  cleanup();
  delete (HTMLElement.prototype as { clientWidth?: number }).clientWidth;
});

const grid = (col: number, row: number, cols: number, rows: number) => ({ mode: 'grid' as const, col, row, cols, rows });

const board: DashboardDefinition = {
  schemaVersion: 2,
  title: 'Reflow',
  canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
  widgets: [
    { id: 'a', type: 'label', layout: { base: grid(0, 0, 6, 2) }, options: { text: 'a' } },
    { id: 'b', type: 'label', layout: { base: grid(6, 0, 6, 2) }, options: { text: 'b' } },
    {
      id: 'badge',
      type: 'label',
      layout: { base: { mode: 'relative', host: 'b', anchor: 'top-right', size: { cols: 1, rows: 1 } } },
      options: { text: '!' },
    },
  ],
};

const resolver = { devicesForAnchor: async () => [], deviceExists: async () => true };

async function draw(width: number | undefined) {
  if (width !== undefined) {
    Object.defineProperty(HTMLElement.prototype, 'clientWidth', { configurable: true, get: () => width });
  }
  const hub = new DashboardHub({ resolver });
  const { container } = render(<DashboardRenderer definition={board} hub={hub} seedHistory={false} bindings={{}} />);
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
  hub.disposeAll();
  return container;
}

const at = (c: HTMLElement, id: string) => {
  const el = c.querySelector(`[data-widget="${id}"]`) as HTMLElement;
  return `${el.style.gridColumn} | ${el.style.gridRow}`;
};

describe('renderer reflow', () => {
  it('draws the authored layout at 960 px, where 12 cells are 72 px', async () => {
    const c = await draw(960);
    expect((c.firstElementChild as HTMLElement).dataset.columns).toBe('12');
    expect((c.querySelector('[data-grid]') as HTMLElement).style.gridTemplateColumns).toBe('repeat(12, minmax(0, 1fr))');
    expect(at(c, 'a')).toBe('1 / span 6 | 1 / span 2');
    expect(at(c, 'b')).toBe('7 / span 6 | 1 / span 2');
  });

  it('draws the authored layout when the container has no measured width', async () => {
    const c = await draw(undefined);
    expect(at(c, 'b')).toBe('7 / span 6 | 1 / span 2');
  });

  it('pours the board into 6 columns at 390 px, b under a, and the cell follows the live columns', async () => {
    const c = await draw(390);
    expect((c.firstElementChild as HTMLElement).dataset.columns).toBe('6');
    const g = c.querySelector('[data-grid]') as HTMLElement;
    expect(g.style.gridTemplateColumns).toBe('repeat(6, minmax(0, 1fr))');
    expect(g.style.getPropertyValue('--dc-cell')).toBe('calc((100cqw - 5 * calc(8px * var(--dc-scale, 1))) / 6)');
    expect(at(c, 'a')).toBe('1 / span 6 | 1 / span 2');
    expect(at(c, 'b')).toBe('1 / span 6 | 3 / span 2');
  });

  it('moves a relative widget with its host', async () => {
    const c = await draw(390);
    const layer = (c.querySelector('[data-widget="badge"]') as HTMLElement).parentElement as HTMLElement;
    expect(`${layer.style.gridColumn} | ${layer.style.gridRow}`).toBe('1 / span 6 | 3 / span 2');
  });
});
