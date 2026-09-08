// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The copy recipe, executed exactly as a consumer following the package README would.
//
// 🔴 BOTH FILES, SIDE BY SIDE, UNDER THEIR OWN NAMES. `maplibre-gl-worker.mjs` is 19 KB
// and its first line is `import … from "./maplibre-gl-shared.mjs"`; MapLibre loads it as
// a MODULE worker, so that import is resolved by the browser, relative to wherever the
// file is served from. Copy the worker alone and it resolves to a 404 — and the failure
// is silent: the worker URL itself answers 200, the map builds, markers appear, tiles
// load, and nothing reaches the console. Renaming or hashing either file breaks it the
// same way, which is why they are copied verbatim.

import { copyFileSync, mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const from = path.join(here, 'node_modules', 'maplibre-gl', 'dist');
const to = path.join(here, 'dist', 'vendor');

mkdirSync(to, { recursive: true });
for (const name of ['maplibre-gl-worker.mjs', 'maplibre-gl-shared.mjs']) {
  copyFileSync(path.join(from, name), path.join(to, name));
  console.log(`copied ${name}`);
}
