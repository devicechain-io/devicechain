// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The level, indicator and radial components (compass, progress ring), gathered into ONE module so they ship as one lazily
// loaded chunk. Reached only through the dynamic import in level-lazy.tsx; a static import of
// this module (or of any widget it re-exports) from the registry folds the whole family back
// into every host's initial download, and nothing about behaviour would change to reveal it.

export { Battery } from './battery';
export { Compass } from './compass';
export { LevelGauge } from './level-gauge';
export { LinearGauge } from './linear-gauge';
export { ProgressRing } from './progress-ring';
export { SignalBars } from './signal-bars';
export { StatusLight } from './status-light';
