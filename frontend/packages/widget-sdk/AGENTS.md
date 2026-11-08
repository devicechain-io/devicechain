<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# AGENTS.md: @devicechain/widget-sdk

Notes for an AI coding agent writing a dashboard widget, or changing this package.

## What it is

The typed contract for a dashboard widget written as a React component, plus the sealed
registry that makes it available to a host application. The built-in widgets are declared
through exactly this interface. Read `README.md` for the full list of exports.

## Rules that save time

- **One copy of this package.** It defines the React contexts a widget reads (selection,
  time range, theme root, density, freshness and others). A context is identified by the
  object that created it, so declare `@devicechain/widget-sdk` as a **peer dependency** in
  a widget package, never a dependency. Two copies means every context reads empty.
- **Peers, not bundles.** `react`, `echarts` and `@devicechain/dashboards` are peer
  dependencies. Do not import ECharts or the map library from the chart-free root entry;
  chart helpers live in their own entry so a DOM-only widget stays small.
- **Exports are public API.** A removal or rename breaks widget packages built elsewhere.
  `src/dist-shape.test.ts` guards the built shape: when it fails, treat that as a
  compatibility question before you update the expectation.
- **Colour comes from theme variables** (the `css` references), not hard-coded values, so a
  widget follows light and dark themes and tenant branding.
- Every source file begins with the two-line SPDX header used across the repository.

## Commands

Run from `frontend/packages/widget-sdk` (the test script builds the sibling packages first):

```bash
npm run typecheck
npm test
npm run build
```
