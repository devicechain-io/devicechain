// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The MapLibre runtime for this Vite app: the ready-made one from the widgets package's Vite
// entry. The worker is emitted as a same-origin file, which is what keeps the page's
// content-security policy and its no-third-party-requests promise intact. A recording with
// no positions answers the map with "Not in this recording", so MapLibre itself loads only
// when one that carries them (format version 2) is played.

import { viteMapRuntime } from '@devicechain/widgets/vite';

export const MAP_RUNTIME = viteMapRuntime;
