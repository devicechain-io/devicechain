// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The one-way conversion of a definition stored before schemaVersion 3 onto the time span.
//
// Before 3, a `timeseries-chart` kept a rolling SAMPLE COUNT (`options.window`, default 300) and
// every chart seeded a fixed hour of history whatever that count was. Now it draws a SPAN
// (`options.windowSeconds`, or the board's time range when it names none).
//
// 🔴 There is no honest mapping from a count to a span. N samples cover N x (the sampling
// interval) seconds, and the interval is a property of the device, not of the dashboard: 300
// samples is five minutes from a device reporting every second and a day from one reporting every
// five minutes. Any factor chosen here would be a guess painted as a conversion. So the rule is:
//
//   - `options.window` is DROPPED from a time-series chart, and the chart follows the board's
//     time range (one hour unless the board names another -- which is exactly the window every
//     chart already seeded, so what the chart showed on load does not change);
//   - each such widget is FLAGGED (by position in the widget list), and the parser names them so an
//     editor can tell the author which charts to look at. A chart that was deliberately short or
//     long gets its span pinned by hand.
//
// The widget's stored options generation (`widget.optionsVersion`) is moved from 1 to 2 on a
// time-series chart, because the options changed meaning; without it the chart would load as a
// "settings version differs" placeholder. Other widgets keep their `window`: it is a memory bound
// on their rolling buffer that only the chart ever drew, and is unchanged.
//
// Like the grid conversion it works on the RAW document and does not modify its input. It is
// idempotent: its own output carries no `window` and no version-1 reference, so converting it
// again changes nothing and flags nothing.

import { CURRENT_SCHEMA_VERSION } from './types';
import { isRecord } from './is-record';

const TIME_SERIES = 'timeseries-chart';

export interface WindowConversion {
  doc: Record<string, unknown>;
  // Indices into `doc.widgets` of the charts whose sample-count window was dropped.
  flagged: number[];
}

export function convertSampleWindows(raw: Record<string, unknown>): WindowConversion {
  const widgetsIn = Array.isArray(raw.widgets) ? raw.widgets : [];
  const flagged: number[] = [];
  const widgets = widgetsIn.map((w, i) => {
    if (!isRecord(w) || w.type !== TIME_SERIES) return w;
    let out: Record<string, unknown> = w;
    if (isRecord(w.options) && Object.prototype.hasOwnProperty.call(w.options, 'window')) {
      const { window: _count, ...options } = w.options;
      out = { ...out, options };
      flagged.push(i);
    }
    if (isRecord(w.widget) && w.widget.optionsVersion === 1) {
      out = { ...out, widget: { ...w.widget, optionsVersion: 2 } };
    }
    return out;
  });
  return { doc: { ...raw, schemaVersion: CURRENT_SCHEMA_VERSION, widgets }, flagged };
}
