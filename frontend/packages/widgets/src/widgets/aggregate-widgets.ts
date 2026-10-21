// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The aggregate and categorical components, gathered into ONE module so they ship as one lazily
// loaded chunk. Reached only through the dynamic import in aggregate-lazy.tsx; a static import of
// this module (or of any widget it re-exports) from the registry folds the family back into every
// host's initial download, and nothing about behaviour would change to reveal it.

export { BarChart } from './bar-chart';
export { FleetCount } from './fleet-count';
export { FleetTable } from './fleet-table';
export { PieChart } from './pie-chart';
export { StatCard } from './stat-card';
