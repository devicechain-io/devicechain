// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs; only the GraphQL transport is faked.
//
// What this file pins: the formula editor's history panel sends the draft formula, its bounds
// and the measured inputs it reads (typed from the profile's draft metrics) to calculatedEvents;
// it shows the stored-inputs caveat on every result, the tally by reason and the newest samples;
// a range the server refuses as too large says to narrow it; and the hand-typed sample stays.
import i18n from '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { GraphQLRequestError } from '@devicechain/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

import { FormulaEditor } from './FormulaEditor';

afterEach(cleanup);
beforeEach(() => {
  void i18n.changeLanguage('en');
  gqlMock.mockReset();
});

const history = {
  calculatedEvents: {
    samples: [
      { occurredTime: '2026-10-16T12:00:00Z', eventId: 'aa', inputs: [{ key: 'voltage', value: '230' }, { key: 'current', value: '10' }], value: '2.3', skippedReason: null },
      { occurredTime: '2026-10-16T12:01:00Z', eventId: 'bb', inputs: [{ key: 'voltage', value: '231' }], value: null, skippedReason: 'missing_input' },
    ],
    computed: 1,
    skipped: [{ reason: 'missing_input', count: 1 }],
    inputDecimalPlaces: 8,
  },
};

function install(answer: () => unknown = () => Promise.resolve(history)) {
  const calls: Record<string, unknown>[] = [];
  gqlMock.mockImplementation((svc: string, doc: string, vars: Record<string, unknown>) => {
    const d = String(doc);
    if (d.includes('query TestMetricFormula')) {
      return Promise.resolve({
        testMetricFormula: { compileError: null, value: null, skippedReason: 'missing_input', inputs: ['voltage', 'current'], warnings: [], estimatedCost: 7 },
      });
    }
    if (d.includes('query MetricDefinitions')) {
      return Promise.resolve({
        metricDefinitions: {
          results: [
            { metricKey: 'voltage', dataType: 'DOUBLE', calculated: false, expression: null },
            { metricKey: 'current', dataType: 'DOUBLE', calculated: false, expression: null },
            { metricKey: 'power_kw', dataType: 'DOUBLE', calculated: true, expression: 'voltage * current / 1000' },
          ],
          pagination: {},
        },
      });
    }
    if (d.includes('query CalculatedEvents')) {
      calls.push({ svc, ...vars });
      return answer();
    }
    return Promise.resolve({});
  });
  return calls;
}

function renderEditor() {
  render(
    <FormulaEditor
      profileToken="meter"
      metricKey="power_kw"
      dataType="DOUBLE"
      minValue=""
      maxValue="100"
      expression="voltage * current / 1000"
      onChange={vi.fn()}
    />,
  );
}

async function runOnHistory() {
  fireEvent.change(await screen.findByLabelText('Device token'), { target: { value: ' meter-001 ' } });
  fireEvent.click(screen.getByRole('button', { name: 'Run on history' }));
}

describe('FormulaEditor: recent history', () => {
  it('evaluates the draft over a device’s stored readings and labels the stored-input caveat', async () => {
    const calls = install();
    renderEditor();
    // The hand-typed sample is still there beside the history panel.
    expect(await screen.findByLabelText('voltage')).toBeTruthy();
    await runOnHistory();

    await waitFor(() => expect(calls).toHaveLength(1));
    const sent = calls[0];
    expect(sent.svc).toBe('event-management');
    expect(sent.deviceToken).toBe('meter-001');
    expect(sent.expression).toBe('voltage * current / 1000');
    expect(sent.dataType).toBe('DOUBLE');
    expect(sent.maxValue).toBe(100);
    expect(sent.minValue).toBeNull();
    expect(sent.inputs).toEqual([
      { key: 'voltage', dataType: 'DOUBLE' },
      { key: 'current', dataType: 'DOUBLE' },
    ]);
    const span = Date.parse(sent.to as string) - Date.parse(sent.from as string);
    expect(span).toBe(24 * 3_600_000);

    expect(await screen.findByTestId('formula-history-caveat')).toHaveProperty(
      'textContent',
      expect.stringContaining('Previewed from stored 8-decimal inputs'),
    );
    const tally = screen.getByTestId('formula-history-tally').textContent ?? '';
    expect(tally).toContain('2 samples, 1 with a value');
    expect(tally).toContain('1 skipped: an input or device constant the formula reads is missing');
    expect(screen.getByText(/voltage=230 current=10/)).toBeTruthy();
    expect(screen.getByText(/→ 2\.3/)).toBeTruthy();
  });

  it('says to narrow the window when the server refuses it as too large', async () => {
    install(() =>
      Promise.reject(
        new GraphQLRequestError('GraphQL error', 200, [
          { message: 'limit exceeded for samples', extensions: { code: 'LIMIT_EXCEEDED' } },
        ]),
      ),
    );
    renderEditor();
    await runOnHistory();
    expect(await screen.findByText(/too many readings to preview \(at most 50,000 samples\)\. Choose a shorter window/)).toBeTruthy();
    expect(screen.queryByTestId('formula-history-result')).toBeNull();
  });

  it('is not runnable without a device', async () => {
    install();
    renderEditor();
    const button = await screen.findByRole('button', { name: 'Run on history' });
    expect((button as HTMLButtonElement).disabled).toBe(true);
  });
});
