// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The level family's lazy registration: the component arrives from its own chunk on first
// mount, and a chunk that fails to load reaches the widget's error boundary rather than
// leaving an empty box that reads as a widget with nothing to say.

import type { WidgetInstance } from '@devicechain/dashboards';
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

describe('the lazy level registrations', () => {
  it('render nothing until the chunk arrives, then the real component', async () => {
    vi.resetModules();
    const { batteryWidget } = await import('./widgets/level-lazy');
    const Lazy = batteryWidget.implementation.component;
    const { container } = render(<Lazy widget={widget} data={data} />);
    // The first frame is empty: the module was fresh, so nothing was loaded yet.
    expect(container.querySelector('[data-fill]')).toBeNull();
    expect(await screen.findByText('40', {}, { timeout: 10_000 })).toBeTruthy();
    expect(container.querySelector('[data-fill]')?.getAttribute('data-fill')).toBe('0.4');
  });

  it('render synchronously once the chunk has loaded', async () => {
    vi.resetModules();
    const { batteryWidget, loadLevelWidgets } = await import('./widgets/level-lazy');
    await loadLevelWidgets();
    const Lazy = batteryWidget.implementation.component;
    const { container } = render(<Lazy widget={widget} data={data} />);
    expect(container.querySelector('[data-fill]')?.getAttribute('data-fill')).toBe('0.4');
  });

  it('throw a failed chunk load to the error boundary', async () => {
    vi.resetModules();
    vi.doMock('./widgets/level-widgets', () => {
      throw new Error('chunk failed');
    });
    const { batteryWidget } = await import('./widgets/level-lazy');
    const Lazy = batteryWidget.implementation.component;
    vi.spyOn(console, 'error').mockImplementation(() => {});
    render(
      <Boundary>
        <Lazy widget={widget} data={data} />
      </Boundary>,
    );
    // vitest rewraps the factory's throw in its own Error; what matters is that it arrived.
    expect(await screen.findByText(/^boundary: .*Error/, {}, { timeout: 10_000 })).toBeTruthy();
    expect(document.querySelector('[data-fill]')).toBeNull();
  });
});
