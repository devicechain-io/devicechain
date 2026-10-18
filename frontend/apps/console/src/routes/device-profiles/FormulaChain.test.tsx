// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs; only the GraphQL transport is faked.
//
// What this file pins: a formula that reads another calculated metric shows the chain it builds
// on, asks the sample for the MEASURED metrics the whole chain reads (not for the calculated
// metric, which the server computes), and the history panel sends a calculated input to
// calculatedEvents with the type the profile declares for it.
import i18n from '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
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

function install() {
  const historyCalls: Record<string, unknown>[] = [];
  gqlMock.mockImplementation((_svc: string, doc: string, vars: Record<string, unknown>) => {
    const d = String(doc);
    if (d.includes('query TestMetricFormula')) {
      return Promise.resolve({
        testMetricFormula: {
          compileError: null,
          value: '4.6',
          skippedReason: null,
          inputs: ['kva'],
          chain: ['kva', 'kw'],
          measuredInputs: ['voltage', 'current'],
          constants: [],
          warnings: [],
          estimatedCost: 5,
        },
      });
    }
    if (d.includes('query MetricDefinitions')) {
      return Promise.resolve({
        metricDefinitions: {
          results: [
            { metricKey: 'voltage', dataType: 'DOUBLE', calculated: false, expression: null },
            { metricKey: 'current', dataType: 'DOUBLE', calculated: false, expression: null },
            { metricKey: 'kva', dataType: 'DOUBLE', calculated: true, expression: 'voltage * current / 1000' },
          ],
          pagination: {},
        },
      });
    }
    if (d.includes('query CalculatedEvents')) {
      historyCalls.push(vars);
      return Promise.resolve({ calculatedEvents: { samples: [], computed: 0, skipped: [], inputDecimalPlaces: 8 } });
    }
    return Promise.resolve({});
  });
  return { historyCalls };
}

function renderEditor() {
  render(
    <FormulaEditor
      profileToken="meter"
      metricKey="kw"
      dataType="DOUBLE"
      minValue=""
      maxValue=""
      expression="kva * 2.0"
      onChange={vi.fn()}
    />,
  );
}

describe('FormulaEditor: chained calculated metrics', () => {
  it('shows the chain and asks the sample for the measured metrics under it', async () => {
    install();
    renderEditor();
    expect((await screen.findByTestId('formula-chain')).textContent).toContain('kva → kw → kw');
    expect(screen.getByLabelText('voltage')).toBeTruthy();
    expect(screen.getByLabelText('current')).toBeTruthy();
    expect(screen.queryByLabelText('kva')).toBeNull();
    expect(screen.getByTestId('formula-outcome').textContent).toContain('4.6');
  });

  it('sends a calculated input to the history preview with its declared type', async () => {
    const { historyCalls } = install();
    renderEditor();
    fireEvent.change(await screen.findByLabelText('Device token'), { target: { value: 'meter-001' } });
    fireEvent.click(screen.getByRole('button', { name: 'Run on history' }));
    await waitFor(() => expect(historyCalls).toHaveLength(1));
    expect(historyCalls[0].inputs).toEqual([{ key: 'kva', dataType: 'DOUBLE' }]);
  });
});
