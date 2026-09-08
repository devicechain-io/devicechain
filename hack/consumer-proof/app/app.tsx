// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The consumer application. BYTE-IDENTICAL in both arms — the driver copies this
// file into each, and the ONLY thing that differs between them is `./runtime`, which
// is the map-runtime contract each bundler has to satisfy. That is deliberate: if the
// arms disagreed anywhere else, a difference in outcome would not be attributable to
// the bundler.

import { createRoot } from 'react-dom/client';
import { DashboardRenderer, MapRuntimeProvider } from '@devicechain/widgets';
import { SyntheticDataSource } from '@devicechain/dashboards';

import { BOARD } from './board';
import { mapRuntime } from './runtime';

const hub = new SyntheticDataSource();

createRoot(document.getElementById('root')!).render(
  <MapRuntimeProvider runtime={mapRuntime}>
    <div id="board" style={{ width: 800, height: 480 }}>
      <DashboardRenderer definition={BOARD} hub={hub} seedHistory={false} />
    </div>
  </MapRuntimeProvider>,
);

// Published for the driver: the URL THIS BUNDLER produced. The driver asserts the
// browser actually fetched it and got MapLibre's worker back — a claim that cannot be
// made from the build output alone, because the value is computed by bundler-emitted
// code at runtime.
declare global {
  interface Window {
    __dcMapRuntime?: { workerUrl: unknown };
  }
}
window.__dcMapRuntime = mapRuntime;
