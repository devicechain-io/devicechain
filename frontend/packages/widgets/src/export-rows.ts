// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tables the chart and the fleet table hand their frame to export (the frame's "Download CSV"):
// built from the state the widget is rendering, at the moment the viewer asks. The history table's own
// is in history-model.ts. Loaded on demand (the chart reaches this through a dynamic import), so none
// of it is part of a board's first download.
//
// 🔴 AN EXPORT IS WHAT THE WIDGET SHOWS. It holds the samples on screen, not a new question to the
// store, and its file name says which span they cover and what they are: "avg per 1m" for a point that
// is a stored bucket's average, "readings" for one that arrived live. The two are told apart by the
// sample itself (`bucketSeconds`), never guessed.

import { spanLabel, type FleetMember, type MeasurementSample } from '@devicechain/dashboards';
import { viewerLocale, type WidgetExportCell, type WidgetExportTable } from '@devicechain/widget-sdk';

import { cellFor, deviceLabel, sortMembers, type SortKey } from './fleet-model';
import { bucketLabel, roundTo, stamp } from './history-model';

interface ExportWords {
  time: string;
  measurement: string;
  series: string;
  value: string;
  basis: string;
  reading: string;
  average: (bucket: string) => string;
  device: string;
  deviceToken: string;
  readingTime: string;
  latest: string;
}

const WORDS: Record<'en' | 'es', ExportWords> = {
  en: {
    time: 'Time (UTC)',
    measurement: 'Measurement',
    series: 'Series',
    value: 'Value',
    basis: 'Basis',
    reading: 'reading',
    average: (bucket) => `avg per ${bucket}`,
    device: 'Device',
    deviceToken: 'Device token',
    readingTime: 'reading time (UTC)',
    latest: 'latest',
  },
  es: {
    time: 'Hora (UTC)',
    measurement: 'Medición',
    series: 'Serie',
    value: 'Valor',
    basis: 'Base',
    reading: 'lectura',
    average: (bucket) => `prom. por ${bucket}`,
    device: 'Dispositivo',
    deviceToken: 'Token del dispositivo',
    readingTime: 'hora de la lectura (UTC)',
    latest: 'última',
  },
};

function words(): ExportWords {
  return viewerLocale()?.toLowerCase().startsWith('es') ? WORDS.es : WORDS.en;
}

function isoOrEmpty(value: string | null): string {
  if (!value) return '';
  const at = Date.parse(value);
  return Number.isNaN(at) ? '' : new Date(at).toISOString();
}

export interface SamplesExportInputs {
  // What the file is called after: the widget's title, else its device.
  subject: string;
  samples: readonly MeasurementSample[];
  unit?: string;
  precision?: number;
  // The first heading of the series column: the measurement (one device) or the device (a compare).
  seriesIs: 'measurement' | 'device';
  // The span the widget draws, when it knows one (the board's range or its own pin).
  spanSeconds?: number;
}

// The samples a chart draws, long: one row per point, oldest first, `Time, Measurement|Series, Value
// (unit), Basis`. Long rather than wide because a chart's measurements do not share instants.
export function samplesExport(i: SamplesExportInputs): WidgetExportTable {
  const w = words();
  const points = i.samples
    .filter((s) => s.value !== null && s.occurredTime && !Number.isNaN(Date.parse(s.occurredTime)))
    .map((s) => ({ s, at: Date.parse(s.occurredTime as string) }))
    .sort((a, b) => a.at - b.at || (a.s.name < b.s.name ? -1 : a.s.name > b.s.name ? 1 : 0));
  const basis = (s: MeasurementSample): string => (s.bucketSeconds !== undefined ? w.average(bucketLabel(s.bucketSeconds)) : w.reading);
  // The file says what it holds: the buckets' widths and whether live readings are in it.
  const kinds = [...new Set(points.map(({ s }) => basis(s)))];
  const first = points[0]?.s.occurredTime;
  const last = points[points.length - 1]?.s.occurredTime;
  const parts = [i.subject, i.spanSeconds !== undefined ? spanLabel(i.spanSeconds) : undefined, first ? stamp(first) : undefined, last ? stamp(last) : undefined, ...kinds].filter(Boolean);
  return {
    name: parts.join('-'),
    columns: [w.time, i.seriesIs === 'device' ? w.series : w.measurement, `${w.value}${i.unit ? ` (${i.unit})` : ''}`, w.basis],
    rows: points.map(({ s, at }): WidgetExportCell[] => [new Date(at).toISOString(), s.name, roundTo(s.value as number, i.precision), basis(s)]),
  };
}

export interface FleetExportInputs {
  subject: string;
  members: readonly FleetMember[];
  total: number;
  // The measurement columns as the table shows them, in order, with their formats.
  columns: ReadonlyArray<{ name: string; heading: string; precision?: number }>;
  sort: SortKey;
}

// The fleet table, wide: a row per device in the order on screen (all of them, not the page), the
// device's name and token, then each measurement's latest reading and the instant it was taken. A
// cell with no reading is empty; the table says why on screen, and an empty cell is never a zero.
export function fleetExport(i: FleetExportInputs): WidgetExportTable {
  const w = words();
  const rows = sortMembers(i.members, i.sort).map((m): WidgetExportCell[] => [
    deviceLabel(m),
    m.token,
    ...i.columns.flatMap((c) => {
      const cell = cellFor(m, c.name);
      return cell.kind === 'value' ? [roundTo(cell.value, c.precision), isoOrEmpty(cell.sample.occurredTime)] : [null, null];
    }),
  ]);
  // A fleet past what the resolver read says so in the name, as the table does on screen.
  const partial = i.members.length < i.total ? `first-${i.members.length}-of-${i.total}` : undefined;
  return {
    name: [i.subject, w.latest, partial, stamp(new Date().toISOString())].filter(Boolean).join('-'),
    columns: [w.device, w.deviceToken, ...i.columns.flatMap((c) => [`${c.heading} (${w.latest})`, `${c.heading} ${w.readingTime}`])],
    rows,
  };
}
