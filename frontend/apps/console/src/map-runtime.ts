// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The console's MapLibre runtime, and the single source of the worker URL for this app.
//
// @devicechain/widgets is published to npm and its main entry writes no bundler dialect —
// that is what lets a webpack or Next consumer build it. This app is built with Vite, so it
// takes the ready-made runtime from the package's one Vite-only entry rather than writing
// the dialect itself.
//
// 🔴 THE FENCE EDITOR READS THIS TOO, and that is deliberate. routes/geofences/FenceMap.tsx
// drives its own MapLibre instance (a fence is drawn, not rendered from a dashboard
// definition) and needs the same worker URL. It used to carry its own copy of the
// `?worker&url` import; two copies of a bundler incantation in one app is one copy too
// many, and the failure when they drift is a map that renders nothing.

import { viteMapRuntime } from '@devicechain/widgets/vite';

/**
 * Installed once, at the top of the tree, by TenantProvider — and reused by the fence
 * editor for its standalone MapLibre instance.
 */
export const MAP_RUNTIME = viteMapRuntime;
