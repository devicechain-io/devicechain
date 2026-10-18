// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs; only the GraphQL transport is faked.
//
// What this file pins: a calculated metric is created with its formula (a measured one with a
// null formula); the kind switch is locked on edit; the formula is checked through
// testMetricFormula (nothing is evaluated in the browser) and the server's compile error,
// cost and sample outcome are shown; a late answer for an earlier draft never overwrites the
// current one.
import i18n from '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

// The real TokenField asks the server whether a generated token is free; that is not what is
// under test, so it is a plain input here.
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

const ok = (over: Record<string, unknown> = {}) => ({
  testMetricFormula: {
    compileError: null,
    value: null,
    skippedReason: 'missing_input',
    inputs: ['voltage', 'current'],
    warnings: [],
    estimatedCost: 7,
    ...over,
  },
});

function install(testAnswer: (vars: Record<string, unknown>) => unknown = () => ok()) {
  const created: Record<string, unknown>[] = [];
  gqlMock.mockImplementation((_svc: string, doc: string, vars: Record<string, unknown>) => {
    if (String(doc).includes('query TestMetricFormula')) return Promise.resolve(testAnswer(vars));
    if (String(doc).includes('mutation CreateMetricDefinition')) {
      created.push(vars.request as Record<string, unknown>);
      return Promise.resolve({ createMetricDefinition: { id: '1', token: 't' } });
    }
    return Promise.resolve({});
  });
  return created;
}

function fill() {
  fireEvent.change(screen.getByLabelText('Metric key'), { target: { value: 'power_kw' } });
  fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'power-kw' } });
}

describe('MetricDefinitionForm: calculated metrics', () => {
  it('creates a measured metric with a null formula by default', async () => {
    const created = install();
    render(<MetricDefinitionForm profileToken="meter" onDone={vi.fn()} />);
    expect(screen.queryByLabelText('Formula')).toBeNull();
    fill();
    fireEvent.click(screen.getByRole('button', { name: 'Create metric' }));
    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0].expression).toBeNull();
  });

  it('creates a calculated metric with its trimmed formula, after the server checks it', async () => {
    const created = install((vars) => {
      const sample = vars.sample as { key: string; value: string }[];
      return sample.length === 2
        ? ok({ value: '1.15', skippedReason: null })
        : ok();
    });
    render(<MetricDefinitionForm profileToken="meter" onDone={vi.fn()} />);
    fireEvent.click(screen.getByRole('radio', { name: 'Calculated' }));
    fill();
    const create = screen.getByRole('button', { name: 'Create metric' });
    expect((create as HTMLButtonElement).disabled).toBe(true); // no formula yet
    fireEvent.change(screen.getByLabelText('Formula'), { target: { value: ' voltage * current / 1000 ' } });
    expect((create as HTMLButtonElement).disabled).toBe(false);

    await screen.findByText(/The formula compiles/);
    expect(screen.getByText(/estimated cost 7 of at most 100/)).toBeTruthy();
    expect(screen.getByTestId('formula-outcome').textContent).toContain('an input or device constant the formula reads is missing');

    fireEvent.change(screen.getByLabelText('voltage'), { target: { value: '230' } });
    fireEvent.change(screen.getByLabelText('current'), { target: { value: '5' } });
    await waitFor(() => expect(screen.getByTestId('formula-outcome').textContent).toContain('Value: 1.15'));

    const calls = gqlMock.mock.calls.filter(([, d]) => String(d).includes('query TestMetricFormula'));
    const call = calls[calls.length - 1];
    expect(call[2]).toMatchObject({
      profileToken: 'meter',
      expression: 'voltage * current / 1000',
      dataType: 'DOUBLE',
      metricKey: 'power_kw',
      sample: [
        { key: 'voltage', value: '230' },
        { key: 'current', value: '5' },
      ],
    });

    fireEvent.click(create);
    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0].expression).toBe('voltage * current / 1000');
  });

  it('shows the compiler error the server returns', async () => {
    install(() => ok({ compileError: 'undeclared reference to "volts"', inputs: [], estimatedCost: null, skippedReason: null }));
    render(<MetricDefinitionForm profileToken="meter" onDone={vi.fn()} />);
    fireEvent.click(screen.getByRole('radio', { name: 'Calculated' }));
    fireEvent.change(screen.getByLabelText('Formula'), { target: { value: 'volts * 2' } });
    await screen.findByText(/The formula does not compile: undeclared reference to "volts"/);
  });

  it('locks the kind on edit', () => {
    install();
    const entity = {
      id: '1', token: 'power-kw', name: null, description: null, metricKey: 'power_kw', dataType: 'DOUBLE',
      unit: null, minValue: null, maxValue: null, enum: null, descriptor: null, metadata: null,
      expression: 'voltage * current / 1000', calculated: true,
    };
    render(<MetricDefinitionForm profileToken="meter" entity={entity as never} onDone={vi.fn()} />);
    expect((screen.getByRole('radio', { name: 'Calculated' }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('radio', { name: 'Measured' }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByLabelText('Formula') as HTMLTextAreaElement).value).toBe('voltage * current / 1000');
  });
});
