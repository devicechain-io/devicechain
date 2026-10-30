// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The widget library, as far as loading a replay needs it, behind ONE dynamic import (load.ts).
//
// Why a module of its own: a dynamic `import('@devicechain/widgets')` keeps the library's WHOLE
// namespace, so every built-in widget it re-exports is bundled into the one chunk and the lazily
// loaded per-widget chunks stop being lazy. Naming just the two functions here lets the bundler
// shake the rest, and the library stays out of the first download.
export { hostWidgetRegistry } from '@devicechain/host-widgets';
export { preloadWidgets } from '@devicechain/widgets';
