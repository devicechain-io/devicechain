// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { WIDGET_TYPES } from '@devicechain/dashboards';
import { createBuiltinWidgetRegistry } from '@devicechain/widgets';

import { AddWidgetMenu } from './AddWidgetMenu';
import { TANK_ID, registryWithTank } from './testExtension';

afterEach(cleanup);

// Radix menus open on pointerdown, not click.
function open() {
  fireEvent.pointerDown(screen.getByRole('button', { name: /add widget/i }), {
    button: 0,
    ctrlKey: false,
    pointerType: 'mouse',
  });
}

describe('the add-widget menu', () => {
  it('lists every descriptor the registry holds, by title rather than by id', async () => {
    const registry = createBuiltinWidgetRegistry();
    render(<AddWidgetMenu registry={registry} onAdd={vi.fn()} />);
    open();
    expect(registry.list().map((d) => d.descriptor.id)).toEqual([...WIDGET_TYPES]);
    for (const { descriptor } of registry.list()) {
      const item = await screen.findByTestId(`add-widget-${descriptor.id}`);
      // The console's English catalog words every built-in title; it equals the descriptor's
      // own text, which is also what a locale without the id would show.
      expect(within(item).getByText(descriptor.display.title)).toBeTruthy();
    }
  });

  it('shows a registration composed in, with its category, and which release it adds', async () => {
    const registry = registryWithTank();
    const onAdd = vi.fn();
    render(<AddWidgetMenu registry={registry} onAdd={onAdd} />);
    open();
    const item = await screen.findByTestId(`add-widget-${TANK_ID}`);
    // The extension ships no console message id: its words are the descriptor's own.
    expect(within(item).getByText('Tank level')).toBeTruthy();
    expect(within(item).getByText(`${TANK_ID} · v1.2.0`)).toBeTruthy();
    expect(screen.getByText('Industrial')).toBeTruthy();

    fireEvent.click(item);
    expect(onAdd).toHaveBeenCalledTimes(1);
    expect(onAdd.mock.calls[0][0].id).toBe(TANK_ID);
  });

  // The control: the extra entry is there because the registry holds it, not because the
  // menu was handed a fixed list.
  it('does not show the extension when the registry does not hold it', async () => {
    render(<AddWidgetMenu registry={createBuiltinWidgetRegistry()} onAdd={vi.fn()} />);
    open();
    await screen.findByTestId('add-widget-gauge');
    expect(screen.queryByTestId(`add-widget-${TANK_ID}`)).toBeNull();
  });
});
