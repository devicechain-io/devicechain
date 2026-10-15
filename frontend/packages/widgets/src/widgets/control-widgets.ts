// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The command-control components, gathered into ONE module so they ship as one lazily loaded
// chunk, separate from the level family's. Reached only through the dynamic import in
// control-lazy.tsx; a static import of this module (or of either widget) from the registry folds
// both controls, and the shared sending code, back into every host's initial download.

export { CommandSlider } from './command-slider';
export { CommandToggle } from './command-toggle';
