// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs; only the GraphQL transport is faked.
//
// What this file pins: the metric form offers STRING; choosing it hides the unit, bounds and
// formula (a text reading has none) and clears what they held, switching back restores them, and
// the create request carries no unit, bounds or formula for a STRING metric.
import i18n from '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

vi.mock('@/components/ui/token-field', () => ({
  TokenField: (p: { id: string; value: string; onChange: (v: string) => void }) => (
    <input id={p.id} value={p.value} onChange={(e) => p.onChange(e.target.value)} />
  ),
}));

import { MetricDefinitionForm } from './DefinitionForms';

afterEach(cleanup);
beforeEach(() => {
  void i18n.changeLanguage('en');
  gqlMock.mockReset();
});

function install() {
  const created: Record<string, unknown>[] = [];
  gqlMock.mockImplementation((_svc: string, doc: string, vars: Record<string, unknown>) => {
    if (String(doc).includes('mutation CreateMetricDefinition')) {
      created.push(vars.request as Record<string, unknown>);
      return Promise.resolve({ createMetricDefinition: { id: '1', token: 't' } });
    }
    return Promise.resolve({});
  });
  return created;
}

async function chooseType(label: string) {
  fireEvent.click(screen.getByLabelText('Data type'));
  fireEvent.click(await screen.findByText(label));
}

describe('MetricDefinitionForm: STRING', () => {
  it('hides the numeric fields for STRING and restores them when switching back', async () => {
    install();
    render(<MetricDefinitionForm profileToken="p" onDone={vi.fn()} />);
    expect(screen.getByLabelText('Min')).toBeTruthy();
    await chooseType('String');
    expect(screen.queryByLabelText('Min')).toBeNull();
    expect(screen.queryByLabelText('Max')).toBeNull();
    expect(screen.queryByLabelText('Unit')).toBeNull();
    expect(screen.getByText(/A text reading/)).toBeTruthy();
    expect((screen.getByRole('radio', { name: 'Calculated' }) as HTMLButtonElement).disabled).toBe(true);

    await chooseType('Double');
    expect(screen.getByLabelText('Min')).toBeTruthy();
    expect(screen.getByLabelText('Max')).toBeTruthy();
    expect(screen.getByLabelText('Unit')).toBeTruthy();
  });

  it('creates a STRING metric with no unit, bounds or formula, even after they were typed', async () => {
    const created = install();
    render(<MetricDefinitionForm profileToken="p" onDone={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Metric key'), { target: { value: 'mode' } });
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'mode' } });
    fireEvent.change(screen.getByLabelText('Unit'), { target: { value: 'Cel' } });
    fireEvent.change(screen.getByLabelText('Min'), { target: { value: '1' } });
    fireEvent.change(screen.getByLabelText('Max'), { target: { value: '9' } });
    fireEvent.click(screen.getByRole('radio', { name: 'Calculated' }));
    await chooseType('String');

    fireEvent.click(screen.getByRole('button', { name: 'Create metric' }));
    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0]).toMatchObject({
      dataType: 'STRING',
      unit: null,
      minValue: null,
      maxValue: null,
      expression: null,
    });
  });
});
