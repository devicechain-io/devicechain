// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { MeasurementSample, WidgetInstance } from '@devicechain/dashboards';
import { WidgetTimeZoneProvider, type MeasurementStreamState } from '@devicechain/widget-sdk';
import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { Table } from './widgets/table';

let originalLang: string | null;
beforeEach(() => {
  originalLang = document.documentElement.getAttribute('lang');
});
afterEach(() => {
  cleanup();
  if (originalLang === null) document.documentElement.removeAttribute('lang');
  else document.documentElement.setAttribute('lang', originalLang);
});

const sample: MeasurementSample = {
  id: 'sample-1',
  deviceToken: 'pump-3',
  eventType: 0,
  occurredTime: '2026-10-22T14:05:42Z',
  name: 'Pump 3 temperature',
  value: 20.456,
  classifier: null,
};
const data: MeasurementStreamState = {
  latest: { [sample.name]: sample },
  samples: [sample],
  error: null,
};
const widget: WidgetInstance = {
  id: 'table-locale',
  type: 'table',
  layout: { base: { mode: 'grid', col: 0, row: 0, cols: 4, rows: 3 } },
  datasource: { kind: 'device', deviceToken: 'pump-3', measurements: [sample.name] },
  options: { precision: 2 },
};

describe('telemetry table headers', () => {
  it.each([
    { locale: 'es', headers: ['Medición', 'Valor', 'Hora'] },
    { locale: 'es-MX', headers: ['Medición', 'Valor', 'Hora'] },
    { locale: 'en', headers: ['Measurement', 'Value', 'Time'] },
    { locale: 'fr', headers: ['Measurement', 'Value', 'Time'] },
  ])('uses the viewer locale $locale without changing the reading', ({ locale, headers }) => {
    document.documentElement.lang = locale;
    render(
      <WidgetTimeZoneProvider timeZone="UTC">
        <Table widget={widget} data={data} />
      </WidgetTimeZoneProvider>,
    );

    expect(screen.getAllByRole('columnheader').map((header) => header.textContent)).toEqual(headers);
    const cells = within(screen.getAllByRole('row')[1]).getAllByRole('cell');
    expect(cells.map((cell) => cell.textContent)).toEqual([
      'Pump 3 temperature',
      '20.46',
      new Date('2026-10-22T14:05:42Z').toLocaleTimeString(undefined, { timeZone: 'UTC', timeZoneName: 'short' }),
    ]);
  });
});
