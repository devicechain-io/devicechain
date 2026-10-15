// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The renderer root carries the density: the authored `canvas.density`, overridden by the
// viewer's, becomes the --dc-scale every size token multiplies by, and the alarm widgets
// colour from the SDK severity tokens rather than a palette of their own.

import { DashboardHub, type DashboardDefinition, type WidgetInstance } from '@devicechain/dashboards';
import { ALARM_LEVELS, SEVERITY_TOKENS } from '@devicechain/widget-sdk';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, subscribe: () => () => {}, gql: async () => ({ alarms: { results: [], pagination: { totalRecords: 0 } } }) };
});

import { DashboardRenderer } from './dashboard-renderer';

afterEach(cleanup);

const label: WidgetInstance = {
  id: 'l',
  type: 'label',
  layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 2, z: 0 } },
  options: { text: 'hello' },
};

const board = (density?: 'compact' | 'comfortable' | 'wall'): DashboardDefinition => ({
  schemaVersion: 1,
  title: 'Density',
  canvas: { grid: { columns: 12, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 }, ...(density ? { density } : {}) },
  widgets: [label],
});

const resolver = { devicesForAnchor: async () => [], deviceExists: async () => true };
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });

async function root(def: DashboardDefinition, density?: 'compact' | 'comfortable' | 'wall') {
  const hub = new DashboardHub({ resolver });
  const { container } = render(<DashboardRenderer definition={def} hub={hub} seedHistory={false} bindings={{}} density={density} />);
  await flush();
  hub.disposeAll();
  return container.firstElementChild as HTMLElement;
}

describe('renderer density', () => {
  it('defaults to comfortable (scale 1)', async () => {
    const el = await root(board());
    expect(el.getAttribute('data-density')).toBe('comfortable');
    expect(el.style.getPropertyValue('--dc-scale')).toBe('1');
  });

  it('applies the authored canvas density to every size token', async () => {
    const el = await root(board('wall'));
    expect(el.getAttribute('data-density')).toBe('wall');
    expect(el.style.getPropertyValue('--dc-scale')).toBe('1.5');
    expect(el.style.getPropertyValue('--font-md')).toBe('calc(13px * var(--dc-scale))');
    expect(el.style.getPropertyValue('--space-3')).toBe('calc(12px * var(--dc-scale))');
  });

  it('lets the viewer override win over the authored density', async () => {
    const el = await root(board('wall'), 'compact');
    expect(el.getAttribute('data-density')).toBe('compact');
    expect(el.style.getPropertyValue('--dc-scale')).toBe('0.9');
  });

  it('a label with no font option takes the type ramp, not a px literal', async () => {
    const hub = new DashboardHub({ resolver });
    const { container } = render(<DashboardRenderer definition={board()} hub={hub} seedHistory={false} bindings={{}} />);
    await flush();
    hub.disposeAll();
    const span = Array.from(container.querySelectorAll('span')).find((s) => s.textContent === 'hello') as HTMLElement;
    expect(span.style.fontSize).toBe('var(--font-lg, 16px)');
  });

  it('injects the severity tokens the alarm widgets read, from the SDK table', async () => {
    const el = await root(board());
    const css = el.querySelector('style[data-dc-tokens]')!.textContent!;
    for (const level of ALARM_LEVELS) expect(css).toContain(`--sev-${level}: ${SEVERITY_TOKENS[level].light};`);
  });
});
