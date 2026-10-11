// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A widget the console has never heard of, for the editor tests: a third-party registration
// composed into the registry the way a host would. Nothing in the console names it, and it
// ships no console translation key — every word on screen has to come from the descriptor.
//
// Its option keys are deliberately uninformative (`zq`, `vx`, …) and one is named after a
// built-in's command key without declaring the command control, so a control chosen by key
// name rather than by declaration would show up as the wrong field.

import { builtinWidgetDefinitions } from '@devicechain/widgets';
import { createWidgetRegistry, defineWidget } from '@devicechain/widget-sdk';

export const TANK_ID = 'acme/tank-level';

export const tankLevelWidget = defineWidget({
  descriptor: {
    id: TANK_ID,
    version: '1.2.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Tank level', category: 'Industrial', description: 'Fill level of a tank.' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: {},
    options: {
      title: { kind: 'string', doc: 'Heading.', label: 'Heading text', help: 'Shown above the tank.' },
      zq: { kind: 'number', min: 1, max: 10, integer: true, doc: 'Alarm band count.', label: 'Alarm bands' },
      vx: {
        kind: 'enum',
        values: ['rising', 'falling'],
        doc: 'Trend direction.',
        label: 'Trend',
        valueLabels: { rising: 'Going up', falling: 'Going down' },
        placeholder: 'Either way',
      },
      flim: { kind: 'boolean', doc: 'Show the rim.', label: 'Show rim' },
      commandName: { kind: 'string', doc: 'A name that happens to match a built-in key.', label: 'Tank name' },
      noLabelAtAll: { kind: 'string', doc: 'No label supplied.' },
    },
    defaults: { title: 'Tank level', zq: 3 },
    interactions: {},
  },
  implementation: { kind: 'trusted-react', component: () => null },
});

export function registryWithTank() {
  return createWidgetRegistry([...builtinWidgetDefinitions, tankLevelWidget]);
}
