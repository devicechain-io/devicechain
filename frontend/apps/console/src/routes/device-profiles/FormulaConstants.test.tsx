// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs; only the GraphQL transport is faked.
//
// What this file pins: a formula that reads per-device constants (`device.name`) shows one
// input per constant beside the hand-typed sample and sends their values to testMetricFormula;
// and the history panel runs it with the typed device's CURRENT metadata, read by the console
// (the event service holds no device records): a number is sent as its text, a name the
// metadata holds as anything else as blank text (present, not a number), a name it lacks is
// left out, and the panel says whose constants those are.
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

const history = {
  calculatedEvents: { samples: [], computed: 0, skipped: [], inputDecimalPlaces: 8 },
};

function install(metadata: string | null) {
  const formulaCalls: Record<string, unknown>[] = [];
  const historyCalls: Record<string, unknown>[] = [];
  gqlMock.mockImplementation((_svc: string, doc: string, vars: Record<string, unknown>) => {
    const d = String(doc);
    if (d.includes('query TestMetricFormula')) {
      formulaCalls.push(vars);
      return Promise.resolve({
        testMetricFormula: {
          compileError: null,
          value: null,
          skippedReason: 'missing_input',
          inputs: ['voltage'],
          constants: ['calibration', 'gain'],
          warnings: [],
          estimatedCost: 9,
        },
      });
    }
    if (d.includes('query MetricDefinitions')) {
      return Promise.resolve({
        metricDefinitions: {
          results: [{ metricKey: 'voltage', dataType: 'DOUBLE', calculated: false, expression: null }],
          pagination: {},
        },
      });
    }
    if (d.includes('query DeviceByToken')) {
      return Promise.resolve({
        devicesByToken: metadata === null ? [] : [{ id: '1', token: 'meter-001', metadata }],
      });
    }
    if (d.includes('query CalculatedEvents')) {
      historyCalls.push(vars);
      return Promise.resolve(history);
    }
    return Promise.resolve({});
  });
  return { formulaCalls, historyCalls };
}

function renderEditor() {
  render(
    <FormulaEditor
      profileToken="meter"
      metricKey="power_cal"
      dataType="DOUBLE"
      minValue=""
      maxValue=""
      expression="voltage * device.calibration * device.gain"
      onChange={vi.fn()}
    />,
  );
}

describe('FormulaEditor: device constants', () => {
  it('shows an input per constant and sends what is typed', async () => {
    const { formulaCalls } = install('{}');
    renderEditor();
    fireEvent.change(await screen.findByLabelText('device.calibration'), { target: { value: ' 1.02 ' } });
    expect(screen.getByLabelText('device.gain')).toBeTruthy();
    expect(screen.getByTestId('formula-constants').textContent).toContain('JSON number');

    await waitFor(() => {
      const last = formulaCalls[formulaCalls.length - 1];
      expect(last.constants).toEqual([{ key: 'calibration', value: '1.02' }]);
    });
  });

  it('runs the history with the typed device’s current metadata', async () => {
    const { historyCalls } = install('{"calibration": 1.25, "gain": "high", "site": "north"}');
    renderEditor();
    fireEvent.change(await screen.findByLabelText('Device token'), { target: { value: 'meter-001' } });
    fireEvent.click(screen.getByRole('button', { name: 'Run on history' }));

    await waitFor(() => expect(historyCalls).toHaveLength(1));
    expect(historyCalls[0].constants).toEqual([
      { key: 'calibration', value: '1.25' },
      { key: 'gain', value: '' }, // present and not a number: the server reads it as invalid_input
    ]);
    expect(await screen.findByTestId('formula-history-constants')).toHaveProperty(
      'textContent',
      expect.stringContaining('device.calibration, device.gain'),
    );
  });

  it('leaves out a constant the device does not hold, and sends none for an unknown device', async () => {
    const present = install('{"calibration": 3}');
    renderEditor();
    fireEvent.change(await screen.findByLabelText('Device token'), { target: { value: 'meter-001' } });
    fireEvent.click(screen.getByRole('button', { name: 'Run on history' }));
    await waitFor(() => expect(present.historyCalls).toHaveLength(1));
    expect(present.historyCalls[0].constants).toEqual([{ key: 'calibration', value: '3' }]);

    cleanup();
    const unknown = install(null);
    renderEditor();
    fireEvent.change(await screen.findByLabelText('Device token'), { target: { value: 'nobody' } });
    fireEvent.click(screen.getByRole('button', { name: 'Run on history' }));
    await waitFor(() => expect(unknown.historyCalls).toHaveLength(1));
    expect(unknown.historyCalls[0].constants).toEqual([]);
  });
});
