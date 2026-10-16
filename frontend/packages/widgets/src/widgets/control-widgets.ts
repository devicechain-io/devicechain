// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The command-control components (button, slider, toggle), gathered into ONE module so they ship
// as one lazily loaded chunk, separate from the level family's. Reached only through the dynamic
// import in control-lazy.tsx; a static import of this module (or of any of its widgets) from the
// registry folds the controls, and the shared sending code, back into every host's initial
// download.

export { CommandButton } from './command-button';
export { CommandSlider } from './command-slider';
export { CommandToggle } from './command-toggle';
