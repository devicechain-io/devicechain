// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A TEST-ONLY external widget package, standing in for what a third party ships: it lives
// outside @devicechain/widgets, imports nothing but the public widget SDK, and declares its
// option text itself (no host message id anywhere), so a host that has never heard of it can
// still author and render it. Nothing in a production build imports this directory.

import { defineWidget, pickSample, primaryMeasurementName } from '@devicechain/widget-sdk';

export const ACME_TANK_ID = 'acme/tank-level';

export const acmeTankWidget = defineWidget({
  descriptor: {
    id: ACME_TANK_ID,
    version: '1.2.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Tank level', category: 'Industrial', description: 'Fill level of a storage tank.' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: {},
    options: {
      heading: { kind: 'string', doc: 'Heading shown above the gauge.', label: 'Tank heading', help: 'Shown above the level.' },
      unit: { kind: 'string', doc: 'Unit suffix.', label: 'Level unit' },
      capacity: {
        kind: 'number',
        min: 1,
        max: 100000,
        integer: true,
        doc: 'Tank capacity.',
        label: 'Capacity (litres)',
      },
      orientation: {
        kind: 'enum',
        values: ['vertical', 'horizontal'],
        doc: 'Drawing orientation.',
        label: 'Orientation',
        valueLabels: { vertical: 'Standing', horizontal: 'Lying down' },
      },
    },
    defaults: { heading: 'Tank level', unit: '%' },
    interactions: {},
  },
  implementation: {
    kind: 'trusted-react',
    component: ({ widget, data }) => {
      const sample = pickSample(data.latest, primaryMeasurementName(widget));
      const options = widget.options ?? {};
      return (
        <div data-testid={`acme-tank-${widget.id}`}>
          <h3>{typeof options.heading === 'string' ? options.heading : ''}</h3>
          <span data-testid={`acme-tank-value-${widget.id}`}>
            {sample && sample.value !== null ? `${String(sample.value)}${typeof options.unit === 'string' ? options.unit : ''}` : 'no reading'}
          </span>
        </div>
      );
    },
  },
});

// The widget as a console writes it after an author adds it from the Add menu, sets its
// settings and binds it to a device through a slot named `tank`. The console's end-to-end test
// asserts the authoring flow produces exactly this (after the generated id and slot name are
// normalized), and the /dash viewer's test loads it -- so the two ends of "authored in the
// console, viewed in /dash" are held to ONE document rather than two that merely look alike.
export const ACME_TANK_WIDGET_JSON = {
  id: 'tank-1',
  type: ACME_TANK_ID,
  widget: { version: '1.2.0', optionsVersion: 1, sdkApiVersion: 1 },
  layout: { base: { col: 0, colSpan: 8, row: 0, rowSpan: 4, z: 1 } },
  datasource: { kind: 'slot', slot: 'tank', measurements: ['level'] },
  options: { heading: 'Main tank', unit: '%', capacity: 5000 },
} as const;
