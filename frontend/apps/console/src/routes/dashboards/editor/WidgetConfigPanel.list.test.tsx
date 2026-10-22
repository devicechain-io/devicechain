// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The list control (thresholds) end to end, against a panel that KEEPS ITS STATE. The
// exhaustiveness gate drives a controlled panel one edit at a time; a list also has to add a
// row, edit each cell of it, and remove rows, which is a sequence. Every step asserts what
// reached the stored options, because that is what the renderer reads.

import '@/i18n/config';
import { cleanup, fireEvent, render, waitFor } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { KnownWidgetInstance, ResolvedWidgetInstance } from '@devicechain/dashboards';

import { WidgetConfigPanel } from './WidgetConfigPanel';

vi.mock('./EntityPicker', () => ({
  EntityPicker: () => null,
  SharedDeviceTypeCatalogue: ({ children }: { children: unknown }) => children,
}));

afterEach(cleanup);

// An object, not a bare variable: the Harness assigns it from a callback TypeScript cannot see.
const store: { options: Record<string, unknown> | undefined } = { options: undefined };

// Read through a function so TypeScript does not narrow it to the value last assigned in the test.
const stored = () => store.options;

function Harness({ initial }: { initial?: Record<string, unknown> }) {
  const [widget, setWidget] = useState<KnownWidgetInstance>({
    id: 'g1',
    type: 'gauge',
    layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 4, rows: 4 } },
    options: initial ?? {},
  });
  return (
    <WidgetConfigPanel
      widget={widget}
      datasource={{ kind: 'device', deviceToken: 'dev-1', measurements: ['temp'] }}
      slots={{}}
      onChange={(next: ResolvedWidgetInstance) => {
        setWidget(next as KnownWidgetInstance);
        store.options = next.options as Record<string, unknown> | undefined;
      }}
      onDatasource={vi.fn()}
      onScope={vi.fn()}
      onClose={vi.fn()}
    />
  );
}

const cell = (root: HTMLElement, row: number, field: string) =>
  root.querySelector<HTMLElement>(`[data-testid="list-cell-thresholds-${row}-${field}"]`)!;
const add = (root: HTMLElement) => fireEvent.click(root.querySelector('[data-testid="list-add-thresholds"]')!);

async function pickLevel(root: HTMLElement, row: number, label: string) {
  fireEvent.click(cell(root, row, 'level').querySelector('button')!);
  const choice = await waitFor(() => {
    const el = [...document.querySelectorAll<HTMLElement>('[role="dialog"] button')].find(
      (b) => (b.textContent ?? '').trim() === label,
    );
    expect(el, `no "${label}" choice`).toBeTruthy();
    return el!;
  });
  fireEvent.click(choice);
}

describe('the thresholds list control', () => {
  it('adds a valid row, edits every cell, and removes rows (the last removal unsets the option)', async () => {
    store.options = undefined;
    const { container } = render(<Harness />);

    add(container);
    expect(stored()?.thresholds).toEqual([{ value: 0, level: 'normal' }]);

    const value = cell(container, 0, 'value').querySelector('input')!;
    fireEvent.change(value, { target: { value: '15' } });
    fireEvent.blur(value);
    expect(stored()?.thresholds).toEqual([{ value: 15, level: 'normal' }]);

    await pickLevel(container, 0, 'Critical');
    expect(stored()?.thresholds).toEqual([{ value: 15, level: 'critical' }]);

    fireEvent.change(cell(container, 0, 'label').querySelector('input')!, { target: { value: 'Trip' } });
    expect(stored()?.thresholds).toEqual([{ value: 15, level: 'critical', label: 'Trip' }]);

    // Clearing the optional label drops the key; the row stays valid.
    fireEvent.change(cell(container, 0, 'label').querySelector('input')!, { target: { value: '' } });
    expect(stored()?.thresholds).toEqual([{ value: 15, level: 'critical' }]);

    add(container);
    expect(stored()?.thresholds).toEqual([
      { value: 15, level: 'critical' },
      { value: 0, level: 'normal' },
    ]);

    fireEvent.click(container.querySelector('button[aria-label="Remove row 1"]')!);
    expect(stored()?.thresholds).toEqual([{ value: 0, level: 'normal' }]);

    fireEvent.click(container.querySelector('button[aria-label="Remove row 1"]')!);
    expect((() => { const o = stored(); return o !== undefined && 'thresholds' in o; })(), 'removing the last row must unset the option').toBe(false);
  });

  it('keeps the last good value when a required cell is cleared (the row would be invalid)', () => {
    store.options = undefined;
    const { container } = render(<Harness initial={{ thresholds: [{ value: 7, level: 'major' }] }} />);
    const value = cell(container, 0, 'value').querySelector('input')!;
    fireEvent.change(value, { target: { value: '' } });
    fireEvent.blur(value);
    expect(stored(), 'a cleared required cell must not write').toBeUndefined();
    // ...and must not even TRY: a refused write would show the editor-failure banner.
    expect(container.textContent).not.toContain('cannot hold');
  });

  it('shows the stored rows', () => {
    const { container } = render(
      <Harness
        initial={{
          thresholds: [
            { value: 5, level: 'warning', label: 'Low' },
            { value: 9, level: 'minor' },
          ],
        }}
      />,
    );
    expect(cell(container, 0, 'value').querySelector('input')!.value).toBe('5');
    expect(cell(container, 0, 'label').querySelector('input')!.value).toBe('Low');
    expect(cell(container, 1, 'value').querySelector('input')!.value).toBe('9');
    expect(container.querySelector('[data-testid="list-row-thresholds-2"]')).toBeNull();
    // Each column is named once, visibly, above the rows.
    expect(container.querySelector('[data-testid="list-head-thresholds"]')!.textContent).toBe('Starts atSeverityLabel');
  });

  it('shows no column headings when there are no rows', () => {
    store.options = undefined;
    const { container } = render(<Harness />);
    expect(container.querySelector('[data-testid="list-head-thresholds"]')).toBeNull();
  });

  it('stops offering Add at the declared maximum', () => {
    const rows = Array.from({ length: 12 }, (_, i) => ({ value: i, level: 'normal' }));
    const { container } = render(<Harness initial={{ thresholds: rows }} />);
    expect((container.querySelector('[data-testid="list-add-thresholds"]') as HTMLButtonElement).disabled).toBe(true);
  });
});
