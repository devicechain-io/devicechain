// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Bundle the package: ESM per entry. Declarations come from `tsc -p tsconfig.build.json`,
// run after this by the `build` script. The peers (React, the SDK, the runtime, ECharts)
// stay EXTERNAL: the host application owns the one copy of each, which is what makes the
// SDK's React contexts shared between host and widget.
import { rmSync } from 'node:fs';
import * as esbuild from 'esbuild';

rmSync('dist', { recursive: true, force: true });

await esbuild.build({
  entryPoints: { index: 'src/index.ts', card: 'src/card.ts' },
  outdir: 'dist',
  bundle: true,
  format: 'esm',
  platform: 'browser',
  target: 'es2020',
  jsx: 'automatic',
  // Every bare specifier is a dependency the host provides.
  packages: 'external',
  logLevel: 'info',
});
