// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The webpack arm's map runtime — the bundler-native recipe.
//
// The worker is built by a second webpack entry to a known filename (see
// webpack.config.mjs, where the two alternatives and why they fail are recorded), so
// naming it here is the whole of the host's side of the contract.

// `loadStyles` is deliberately NOT supplied: this arm imports MapLibre's stylesheet
// itself, which is the ordinary thing every map library asks a host to do, and it keeps
// the optional field genuinely exercised as optional.
import 'maplibre-gl/dist/maplibre-gl.css';

import type { MapRuntime } from '@devicechain/widgets';

export const mapRuntime: MapRuntime = { workerUrl: '/maplibre-worker.js' };
