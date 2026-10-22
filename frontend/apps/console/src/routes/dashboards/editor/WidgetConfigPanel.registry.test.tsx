// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The panel for a widget the console has never heard of: every field comes from the
// descriptor, the control from the schema, the words from the package.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { KnownWidgetInstance, WidgetOptionSpecs } from '@devicechain/dashboards';
import { createWidgetRegistry, defineWidget } from '@devicechain/widget-sdk';

import { WidgetConfigPanel } from './WidgetConfigPanel';
import { TANK_ID, registryWithTank } from './testExtension';

vi.mock('./EntityPicker', () => ({
  EntityPicker: () => null,
  SharedDeviceTypeCatalogue: ({ children }: { children: unknown }) => children,
}));

afterEach(cleanup);

function tank(options: Record<string, unknown> = {}): KnownWidgetInstance {
  return {
    id: 'w1',
    // An extension id is not in the built-in union; the instance shape that carries one
    // arrives with the runtime work, so the test narrows it here.
    type: TANK_ID as KnownWidgetInstance['type'],
    layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 4, rows: 4 } },
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

// A specialized workflow is the descriptor's to ASK for, and the host's to grant only when
// the request is one it can honour. The basemap notice and the command picker both read
// fixed option keys; neither may fire on the keys alone, and the command picker may not
// fire on a declaration it cannot satisfy.
describe('a specialized workflow on a registered widget', () => {
  function registryWith(options: WidgetOptionSpecs) {
    const widget = defineWidget({
      descriptor: {
        id: 'acme/probe',
        version: '1.0.0',
        sdkApiVersion: 1,
        optionsVersion: 1,
        display: { title: 'Probe' },
        channel: 'none',
        datasource: { mode: 'none' },
        binding: {},
        options,
        defaults: {},
        interactions: {},
      },
      implementation: { kind: 'trusted-react', component: () => null },
    });
    return createWidgetRegistry([widget]);
  }

  function renderProbe(options: WidgetOptionSpecs, values: Record<string, unknown>) {
    const onChange = vi.fn();
    render(
      <WidgetConfigPanel
        widget={{ ...tank(values), type: 'acme/probe' as KnownWidgetInstance['type'] }}
        registry={registryWith(options)}
        datasource={undefined}
        onChange={onChange}
        onDatasource={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    return onChange;
  }

  const TILE = { kind: 'string', doc: 'Tiles.' } as const;
  const CREDIT = { kind: 'string', doc: 'Credit.' } as const;

  it('raises the uncredited-tiles notice only when both halves declare the basemap control', () => {
    renderProbe({ tileUrl: TILE, attribution: CREDIT }, { tileUrl: 'https://t/{z}/{x}/{y}.png' });
    expect(screen.queryByTestId('widget-tile-uncredited')).toBeNull();
    cleanup();
    renderProbe(
      { tileUrl: { ...TILE, control: 'basemap' }, attribution: { ...CREDIT, control: 'basemap' } },
      { tileUrl: 'https://t/{z}/{x}/{y}.png' },
    );
    expect(screen.getByTestId('widget-tile-uncredited')).toBeTruthy();
  });

  it('does not offer the command picker for a declaration it cannot honour', () => {
    const onChange = renderProbe({ cmd: { kind: 'string', doc: 'Command.', label: 'Command', control: 'command-vocabulary' } }, {});
    expect(screen.queryByTestId('widget-option-field-command')).toBeNull();
    fireEvent.change(screen.getByTestId('widget-option-field-cmd').querySelector('input')!, { target: { value: 'reboot' } });
    expect(lastOptions(onChange)).toEqual({ cmd: 'reboot' });
  });

  // The control vocabulary is open: a package may name a workflow this console does not
  // implement, and the option is then authored by the generic field rather than refused.
  it('renders the generic field for a control this host does not implement', () => {
    const onChange = renderProbe({ curve: { kind: 'string', doc: '.', label: 'Curve', control: 'acme/valve-curve' } }, {});
    fireEvent.change(screen.getByTestId('widget-option-field-curve').querySelector('input')!, { target: { value: 'linear' } });
    expect(lastOptions(onChange)).toEqual({ curve: 'linear' });
  });

  it('still gives a known control its specialized editor', () => {
    renderProbe({ target: { kind: 'string', doc: '.', control: 'slot-target' } }, {});
    expect(screen.getByTestId('widget-option-field-slot')).toBeTruthy();
    expect(screen.queryByTestId('widget-option-field-target')).toBeNull();
  });

  it('offers it when all three keys it writes are declared under it', () => {
    const cv = { kind: 'string', doc: '.', control: 'command-vocabulary' } as const;
    renderProbe({ commandName: cv, commandLabel: cv, parameterSchema: { ...cv, format: 'json' } }, {});
    expect(screen.getByTestId('widget-option-field-command')).toBeTruthy();
    expect(screen.queryByTestId('widget-option-field-commandName')).toBeNull();
  });
});
