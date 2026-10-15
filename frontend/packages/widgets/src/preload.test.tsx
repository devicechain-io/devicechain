// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// preloadWidgets: a host that awaits it before rendering a board gets every lazily loaded widget
// on the first frame, not an empty box while its chunk is in flight. A chunk that fails is left
// for the widget itself to throw to its error boundary, so the preload never rejects.

import type { WidgetInstance } from '@devicechain/dashboards';
import { createWidgetRegistry } from '@devicechain/widget-sdk';
import { cleanup, render, screen } from '@testing-library/react';
import { Component, type ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

afterEach(() => {
  cleanup();
  vi.doUnmock('./widgets/level-widgets');
  vi.resetModules();
});

const widget: WidgetInstance = {
  id: 'w',
  type: 'battery',
  layout: { base: { mode: 'grid', col: 0, row: 0, cols: 3, rows: 2 } },
  datasource: { kind: 'device', deviceToken: 'd', measurements: ['m'] },
  options: {},
};
const data = {
  latest: { m: { id: 's', deviceToken: 'd', eventType: 0, occurredTime: '2026-07-02T10:00:00Z', name: 'm', value: 40, classifier: null } },
  samples: [],
  error: null,
};

class Boundary extends Component<{ children: ReactNode }, { error?: unknown }> {
  state: { error?: unknown } = {};
  static getDerivedStateFromError(error: unknown) {
    return { error };
  }
  render() {
    return this.state.error !== undefined ? <p>boundary: {String(this.state.error)}</p> : this.props.children;
  }
}

// Fresh modules per test: a chunk loaded by an earlier test would make every first frame full.
async function fresh() {
  vi.resetModules();
  const { builtinWidgetDefinitions } = await import('./builtin-registry');
  const { preloadWidgets } = await import('./preload');
  return { registry: createWidgetRegistry(builtinWidgetDefinitions), preloadWidgets };
}

describe('preloadWidgets', () => {
  // The control: without a preload, the first frame of a fresh lazy widget is empty. Without
  // this, the assertion below would pass for a widget that was never lazy at all.
  it('is needed: a fresh lazy widget draws nothing on its first frame', async () => {
    const { registry } = await fresh();
    const Lazy = registry.implementation('battery')!.component;
    const { container } = render(<Lazy widget={widget} data={data} />);
    expect(container.querySelector('[data-fill]')).toBeNull();
  });

  it('makes the first frame of a preloaded lazy widget complete', async () => {
    const { registry, preloadWidgets } = await fresh();
    await preloadWidgets(registry, ['battery']);
    const Lazy = registry.implementation('battery')!.component;
    const { container } = render(<Lazy widget={widget} data={data} />);
    expect(container.querySelector('[data-fill]')?.getAttribute('data-fill')).toBe('0.4');
  });

  it('skips eager built-ins and unknown types, and accepts duplicates', async () => {
    const { registry, preloadWidgets } = await fresh();
    await expect(preloadWidgets(registry, ['latest-card', 'acme/unknown', 'battery', 'battery'])).resolves.toBeUndefined();
    const Lazy = registry.implementation('battery')!.component;
    const { container } = render(<Lazy widget={widget} data={data} />);
    expect(container.querySelector('[data-fill]')).not.toBeNull();
  });

  it('does not reject when a chunk fails; the widget still throws it to its boundary', async () => {
    vi.doMock('./widgets/level-widgets', () => {
      throw new Error('chunk failed');
    });
    const { registry, preloadWidgets } = await fresh();
    await expect(preloadWidgets(registry, ['battery'])).resolves.toBeUndefined();
    const Lazy = registry.implementation('battery')!.component;
    vi.spyOn(console, 'error').mockImplementation(() => {});
    render(
      <Boundary>
        <Lazy widget={widget} data={data} />
      </Boundary>,
    );
    expect(await screen.findByText(/^boundary: .*Error/, {}, { timeout: 10_000 })).toBeTruthy();
  });
});
