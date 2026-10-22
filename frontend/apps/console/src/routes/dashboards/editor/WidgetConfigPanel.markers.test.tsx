// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The timeseries chart's event markers in the settings panel: two toggles, worded in the console's
// catalogs (English and Spanish), that write the options the chart reads, and survive the
// definition's serialize/parse round trip.

import '@/i18n/config';
import i18n from '@/i18n/config';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { parseDashboardDefinition, serializeDefinition, type KnownWidgetInstance, type ResolvedWidgetInstance } from '@devicechain/dashboards';

import { WidgetConfigPanel } from './WidgetConfigPanel';

vi.mock('./EntityPicker', () => ({
  EntityPicker: () => null,
  SharedDeviceTypeCatalogue: ({ children }: { children: unknown }) => children,
}));

afterEach(async () => {
  cleanup();
  await i18n.changeLanguage('en');
});

const last = (fn: ReturnType<typeof vi.fn>) => fn.mock.calls[fn.mock.calls.length - 1][0];

const chart = (options: Record<string, unknown> = {}): KnownWidgetInstance => ({
  id: 'w1',
  type: 'timeseries-chart',
  layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 6, rows: 4 } },
  options,
});

// Holds the widget the way the editor does, so a toggle's patch is applied before the next click.
function Harness({ onChange }: { onChange: (w: ResolvedWidgetInstance) => void }) {
  const [widget, setWidget] = useState<ResolvedWidgetInstance>(chart());
  return (
    <WidgetConfigPanel
      widget={widget}
      datasource={undefined}
      onChange={(next) => {
        setWidget(next);
        onChange(next);
      }}
      onDatasource={vi.fn()}
      onScope={vi.fn()}
      onClose={vi.fn()}
    />
  );
}

describe('the event-marker options of a timeseries chart', () => {
  it('are two checkboxes, off until asked, that write markAlarms and markCommands', () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    const field = (name: string) => screen.getByTestId(`widget-option-field-${name}`);
    expect(field('markAlarms').textContent).toContain('Mark alarms');
    expect(field('markCommands').textContent).toContain('Mark commands');
    const alarms = within(field('markAlarms')).getByRole('checkbox');
    const commands = within(field('markCommands')).getByRole('checkbox');
    expect(alarms.getAttribute('aria-checked')).toBe('false');
    expect(commands.getAttribute('aria-checked')).toBe('false');

    fireEvent.click(alarms);
    expect(last(onChange).options).toMatchObject({ markAlarms: true });
    fireEvent.click(commands);
    expect(last(onChange).options).toMatchObject({ markAlarms: true, markCommands: true });
    fireEvent.click(alarms);
    const options = last(onChange).options as Record<string, unknown>;
    expect(options.markAlarms).not.toBe(true);
    expect(options.markCommands).toBe(true);
  });

  it('each read their own key: with one set, the other shows unchecked', () => {
    for (const [set, other] of [['markAlarms', 'markCommands'], ['markCommands', 'markAlarms']] as const) {
      const { unmount } = render(
        <WidgetConfigPanel widget={chart({ [set]: true })} datasource={undefined} onChange={vi.fn()} onDatasource={vi.fn()} onScope={vi.fn()} onClose={vi.fn()} />,
      );
      const box = (name: string) => within(screen.getByTestId(`widget-option-field-${name}`)).getByRole('checkbox');
      expect(box(set).getAttribute('aria-checked')).toBe('true');
      expect(box(other).getAttribute('aria-checked')).toBe('false');
      unmount();
    }
  });

  it('are worded in Spanish when the console is', async () => {
    await i18n.changeLanguage('es');
    render(<Harness onChange={vi.fn()} />);
    expect(screen.getByTestId('widget-option-field-markAlarms').textContent).toContain('Marcar alarmas');
    expect(screen.getByTestId('widget-option-field-markCommands').textContent).toContain('Marcar comandos');
  });

  it('round-trip through a saved definition', () => {
    const empty = parseDashboardDefinition({ schemaVersion: 2, widgets: [] });
    const saved = serializeDefinition({ ...empty, widgets: [chart({ markAlarms: true, markCommands: true })] });
    const back = parseDashboardDefinition(JSON.parse(saved));
    expect(back.widgets[0]).toMatchObject({ type: 'timeseries-chart', options: { markAlarms: true, markCommands: true } });
  });
});
