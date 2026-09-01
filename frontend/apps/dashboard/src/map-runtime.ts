// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The /dash viewer's MapLibre runtime.
//
// 🔴 THIS APP IS THE REFERENCE EXTERNAL EMBEDDER, so this file is documentation as much as
// code: what /dash has to do to render a map widget is what a third-party host has to do.
// Keeping it to one import is the point. If this file ever grows, the consumer's job has
// grown with it.
//
// @devicechain/widgets is published to npm and its main entry writes no bundler dialect —
// that is what lets a webpack or Next consumer build it. Vite hosts do not pay for that
// portability: they import the ready-made runtime from the package's one Vite-only entry.
// A host on another bundler builds the runtime itself with its own worker-entry idiom; see
// the recipe in packages/widgets/src/map-runtime-context.tsx.

import { viteMapRuntime } from '@devicechain/widgets/vite';

/**
 * Installed once, where App mounts the DashboardRenderer.
 *
 * Re-exported under a local name rather than imported directly at the install site so there
 * is exactly one place to look for "what map runtime does this app use", and so swapping it
 * — for a test double, or for a hand-built runtime on another bundler — is a one-line edit
 * that the host-wiring test can see.
 */
export const MAP_RUNTIME = viteMapRuntime;
