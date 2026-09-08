// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The copy arm's map runtime: a plain path to files this app serves itself.
//
// This is the bundler-agnostic form of the contract, and the reason the contract is a
// URL at all — a host with no worker-entry story still has a static directory.

import 'maplibre-gl/dist/maplibre-gl.css';

import type { MapRuntime } from '@devicechain/widgets';

export const mapRuntime: MapRuntime = { workerUrl: '/vendor/maplibre-gl-worker.mjs' };
