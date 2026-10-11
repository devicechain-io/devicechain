<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# @devicechain/widget-sdk

The typed contract for writing a DeviceChain dashboard widget as a React component, and the
sealed registry that makes it available to a host application. The built-in widgets are
declared through exactly this interface.

```bash
npm install @devicechain/widget-sdk @devicechain/dashboards react
```

`@devicechain/dashboards` and `react` are **peer dependencies**: the SDK defines the React
contexts a widget reads (selection, candidate lookup, time zone, theme root), and a context is
identified by the object that created it, so every widget package and the host must resolve
the same single copy of this package. Declare it as a peer in your own widget package too,
never as a dependency.

## Declare a widget

```tsx
import { defineWidget } from '@devicechain/widget-sdk';

export const tankLevelWidget = defineWidget({
  descriptor: {
    id: 'acme/tank-level',
    version: '1.0.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Tank level', category: 'Telemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: { window: { option: 'window', min: 1 } },
    options: {
      unit: { kind: 'string', doc: 'Unit shown beside the level.' },
      window: { kind: 'number', integer: true, min: 1, doc: 'Samples retained.' },
    },
    defaults: { unit: '%' },
    interactions: {},
  },
  implementation: {
    kind: 'trusted-react',
    // `data` is typed from the channel: a measurement widget receives measurement data.
    component: ({ data }) => <span>{Object.values(data.latest)[0]?.value}</span>,
  },
});
```

The descriptor is plain JSON-serializable data: no component, callback or URL. A descriptor
that disagrees with its channel, or whose defaults or bindings disagree with its option
schema, fails type checking, and `createWidgetRegistry` checks the same rules again at
runtime.

## Register it

```ts
import { createWidgetRegistry } from '@devicechain/widget-sdk';

const registry = createWidgetRegistry([tankLevelWidget]);
```

A registry is validated and sealed when it is created: duplicate ids, malformed descriptors,
an unsupported `sdkApiVersion` and invalid bindings or defaults are all reported together in
one `WidgetRegistryError`. There is no global registration; two registries on one page are
independent. Extension ids are namespaced (`scope/name`); the un-namespaced ids are reserved
for the built-in widgets.

Trusted widget code runs with the host page's privileges. The declarations in a descriptor
shape the API; they are not a security boundary.
