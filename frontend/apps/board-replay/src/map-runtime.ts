// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The MapLibre runtime for this Vite app: the ready-made one from the widgets package's Vite
// entry. The worker is emitted as a same-origin file, which is what keeps the page's
// content-security policy and its no-third-party-requests promise intact. The recorded
// source answers the map with "Not in this recording" (the format carries no positions), so
// MapLibre itself loads only if a recording that does carry them is ever played.

import { viteMapRuntime } from '@devicechain/widgets/vite';

export const MAP_RUNTIME = viteMapRuntime;
