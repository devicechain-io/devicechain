// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { SelectionCandidate, SelectionTarget, WidgetInstance } from '@devicechain/dashboards';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { WidgetCandidatesProvider, WidgetSelectProvider } from '../frame';
import { EntitySelector } from './entity-selector';

afterEach(cleanup);

const widget = (options: Record<string, unknown>): WidgetInstance => ({
  id: 'sel',
  type: 'entity-selector',
  layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 2, z: 0 } },
  options,
});

const members: SelectionCandidate[] = [
  { binding: { kind: 'device', deviceToken: 'therm-a' }, label: 'therm-a', selected: false },
  { binding: { kind: 'device', deviceToken: 'therm-b' }, label: 'therm-b', selected: true },
];

function renderSelector({
  options,
  select,
  candidates,
}: {
  options: Record<string, unknown>;
  select?: (t: SelectionTarget) => void;
  candidates?: (slot: string) => Promise<SelectionCandidate[]>;
}) {
  return render(
    <WidgetSelectProvider select={select}>
      <WidgetCandidatesProvider candidates={candidates}>
        <EntitySelector widget={widget(options)} data={null} />
      </WidgetCandidatesProvider>
    </WidgetSelectProvider>,
  );
}

describe('EntitySelector', () => {
  it('renders the target slot’s candidates, highlights the current pick, and fires select on change', async () => {
    const select = vi.fn();
    const candidates = vi.fn(async () => members);
    renderSelector({ options: { selectionTarget: 'therm' }, select, candidates });

    // Options arrive async through the provider.
    await screen.findByRole('option', { name: 'therm-a' });
    const combo = screen.getByRole('combobox') as HTMLSelectElement;
    expect(combo.value).toBe('therm-b'); // the selected candidate's token is the option value
    expect(candidates).toHaveBeenCalledWith('therm');

    fireEvent.change(combo, { target: { value: 'therm-a' } });
    expect(select).toHaveBeenCalledWith({
      slot: 'therm',
      binding: { kind: 'device', deviceToken: 'therm-a' },
    });
  });

  it('shows the prompt (no pick) when no candidate is currently selected', async () => {
    const select = vi.fn();
    const candidates = vi.fn(async () => [
      { binding: { kind: 'device' as const, deviceToken: 'x' }, label: 'x', selected: false },
    ]);
    renderSelector({ options: { selectionTarget: 'therm' }, select, candidates });
    await screen.findByRole('option', { name: 'x' });
    expect((screen.getByRole('combobox') as HTMLSelectElement).value).toBe('');
  });

  it('is inert (no picker) when the select callback is not wired', () => {
    renderSelector({ options: { selectionTarget: 'therm' }, select: undefined, candidates: undefined });
    expect(screen.queryByRole('combobox')).toBeNull();
    expect(screen.getByText(/available on the live dashboard/i)).toBeTruthy();
  });

  it('is inert when select is wired but no candidate provider is (feature-detect)', () => {
    const select = vi.fn();
    renderSelector({ options: { selectionTarget: 'therm' }, select, candidates: undefined });
    expect(screen.queryByRole('combobox')).toBeNull();
    expect(screen.getByText(/isn’t available here/i)).toBeTruthy();
  });

  it('is inert when no target slot is configured', () => {
    const select = vi.fn();
    const candidates = vi.fn(async () => members);
    renderSelector({ options: {}, select, candidates });
    expect(screen.queryByRole('combobox')).toBeNull();
    expect(screen.getByText(/no target slot/i)).toBeTruthy();
    expect(candidates).not.toHaveBeenCalled();
  });

  it('shows a no-options prompt when the target slot yields no candidates', async () => {
    const select = vi.fn();
    const candidates = vi.fn(async () => []);
    renderSelector({ options: { selectionTarget: 'therm' }, select, candidates });
    await waitFor(() => expect(candidates).toHaveBeenCalled());
    const combo = screen.getByRole('combobox') as HTMLSelectElement;
    expect(combo.disabled).toBe(true);
    await screen.findByRole('option', { name: 'No options' });
  });
});
