// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The panel for a widget the console has never heard of: every field comes from the
// descriptor, the control from the schema, the words from the package.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { KnownWidgetInstance } from '@devicechain/dashboards';

import { WidgetConfigPanel } from './WidgetConfigPanel';
import { TANK_ID, registryWithTank } from './testExtension';

vi.mock('./EntityPicker', () => ({ EntityPicker: () => null }));

afterEach(cleanup);

function tank(options: Record<string, unknown> = {}): KnownWidgetInstance {
  return {
    id: 'w1',
    // An extension id is not in the built-in union; the instance shape that carries one
    // arrives with the runtime work, so the test narrows it here.
    type: TANK_ID as KnownWidgetInstance['type'],
    layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 4, z: 0 } },
    options,
  };
}

function renderPanel(options: Record<string, unknown> = {}) {
  const onChange = vi.fn();
  render(
    <WidgetConfigPanel
      widget={tank(options)}
      registry={registryWithTank()}
      datasource={undefined}
      onChange={onChange}
      onDatasource={vi.fn()}
      onClose={vi.fn()}
    />,
  );
  return onChange;
}

const lastOptions = (onChange: ReturnType<typeof vi.fn>) => onChange.mock.calls[onChange.mock.calls.length - 1][0].options;

describe('a settings panel for a widget only the registry knows', () => {
  it('words every field with the package text, with no console translation key', () => {
    renderPanel();
    expect(screen.getByText('Tank level')).toBeTruthy(); // panel heading: the descriptor's title
    expect(screen.getByText('Heading text')).toBeTruthy();
    expect(screen.getByText('Shown above the tank.')).toBeTruthy();
    expect(screen.getByText('Alarm bands')).toBeTruthy();
    expect(screen.getByText('Trend')).toBeTruthy();
    expect(screen.getByText('Show rim')).toBeTruthy();
  });

  it('humanizes the key when the package supplied no label at all', () => {
    renderPanel();
    expect(screen.getByText('No label at all')).toBeTruthy();
  });

  // zq / vx / flim say nothing about their kind: the control can only have come from the
  // schema. A number is bounded by ITS declared bounds, and the bounds hold on commit.
  it('renders a number field bounded by the schema, whatever the key is called', () => {
    const onChange = renderPanel({ zq: 3 });
    const input = screen.getByDisplayValue('3') as HTMLInputElement;
    expect(input.type).toBe('number');
    expect(input.min).toBe('1');
    expect(input.max).toBe('10');
    fireEvent.change(input, { target: { value: '99' } });
    fireEvent.blur(input);
    expect(lastOptions(onChange).zq).toBe(10);
    fireEvent.change(input, { target: { value: '0' } });
    fireEvent.blur(input);
    expect(lastOptions(onChange).zq).toBe(1);
  });

  it('offers an enum its declared values, worded by the package', () => {
    const onChange = renderPanel();
    expect(screen.getByText('Either way')).toBeTruthy(); // the package's unset text
    fireEvent.click(screen.getByText('Either way'));
    fireEvent.click(screen.getByText('Going down'));
    expect(lastOptions(onChange).vx).toBe('falling');
  });

  it('renders a boolean as a checkbox that writes true', () => {
    const onChange = renderPanel();
    fireEvent.click(screen.getByRole('checkbox'));
    expect(lastOptions(onChange).flim).toBe(true);
  });

  // An option named like a built-in's command key is just a string unless the descriptor
  // DECLARES the command control.
  it('does not mistake an option for a specialized workflow by its name', () => {
    const onChange = renderPanel();
    expect(screen.queryByTestId('widget-option-field-command')).toBeNull();
    const field = screen.getByTestId('widget-option-field-commandName');
    fireEvent.change(field.querySelector('input')!, { target: { value: 'Tank A' } });
    expect(lastOptions(onChange).commandName).toBe('Tank A');
  });

  // The datasource block follows the descriptor's policy and channel.
  it('offers a data source with measurements for a measurement widget that requires one', () => {
    renderPanel();
    expect(screen.getByText('Data source')).toBeTruthy();
  });
});
