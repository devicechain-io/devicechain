// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Build ONE publishable frontend package: bundled ESM from esbuild, plus per-file
// declarations from the repo's pinned TypeScript. Run with the package directory as
// the working directory — `npm run build -w @devicechain/widgets` does exactly that.
// scripts/build-packages.mjs drives all of them in dependency order.
//
// 🔴 WHY TWO TOOLS RATHER THAN ONE. `typescript: ~7.0.2` is the native Go compiler.
// Its `lib/` ships `getExePath.js`, `tsc.js` and `version.cjs` — `createProgram`,
// `createCompilerHost` and `sys` are all `undefined`. Every JS tool that BUNDLES
// declarations (`tsup --dts`, `rollup-plugin-dts`, api-extractor, `vite-plugin-dts`)
// drives that API, so each one crashes against this repo's pin — and crashes AFTER
// the JS emit has already succeeded, which reads on the terminal as a partial
// success. The compiler's own `--emitDeclarationOnly` has no such problem. Bundled
// JS beside per-file `.d.ts` is a normal, valid arrangement: only the ENTRY
// declaration has to resolve for a consumer, and it does.
//
// 🔴 AND WHY NOT `tsc` FOR THE JS TOO. Sources use extensionless relative imports
// (`from './registry'`), which tsc emits verbatim — a `dist` no Node ESM loader can
// resolve. `rewriteRelativeImportExtensions` does not help: it only rewrites
// specifiers that already end in `.ts`, i.e. it requires the source rewrite it was
// meant to avoid.
//
// SCOPE, stated rather than left implicit: the emitted `.d.ts` carry extensionless
// specifiers, which is correct for `moduleResolution: bundler` (our consumers) and
// will NOT resolve under `node16`/`nodenext`. That is a documented limit of shipping
// ESM for bundlers, not an oversight.

import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import * as esbuild from 'esbuild';

const pkgDir = process.cwd();
const srcDir = path.join(pkgDir, 'src');
const distDir = path.join(pkgDir, 'dist');

// 🔴 A FAILED BUILD MUST LEAVE NO ARTIFACT. Every check below runs AFTER esbuild has
// already written its output, so an early exit would otherwise leave a `dist` that is
// complete enough to look real — the one that inlined a peer, or the one with no
// declarations because the JS emit succeeded and the type emit never ran. `npm pack`
// reads it happily. Found exactly that way: a deliberately broken build left a 1.4 MB
// bundle and zero `.d.ts` behind, and the tarball listing was the first thing to say so.
function fail(message) {
  rmSync(distDir, { recursive: true, force: true });
  console.error(`\n==> ${path.basename(pkgDir)}: ${message}\n`);
  process.exit(1);
}

const pkg = JSON.parse(readFileSync(path.join(pkgDir, 'package.json'), 'utf8'));

// ---------------------------------------------------------------------------
// Entry points come from `exports`, deliberately, rather than from a list kept
// beside it. A second list is a second thing to forget, and the failure it permits
// is an `exports` subpath resolving to a file the build never emitted — which npm
// packs happily and a consumer meets as a bare module-not-found. Deriving them here
// makes "every exported subpath is built" true by construction, and makes a subpath
// whose source moved or was deleted fail HERE rather than at someone's install.
// ---------------------------------------------------------------------------
const entries = [];
for (const [subpath, target] of Object.entries(pkg.exports ?? {})) {
  const js = typeof target === 'string' ? target : target?.import;
  const types = typeof target === 'string' ? undefined : target?.types;
  // `./package.json`, and anything else served straight from the tree, is not built.
  if (!js || !js.startsWith('./dist/')) continue;

  const stem = js.slice('./dist/'.length).replace(/\.js$/, '');
  const src = ['.ts', '.tsx']
    .map((ext) => path.join(srcDir, `${stem}${ext}`))
    .find((candidate) => existsSync(candidate));
  if (!src) {
    fail(`exports["${subpath}"] names ${js}, but there is no src/${stem}.ts or .tsx to build it from`);
  }
  if (!types) {
    fail(`exports["${subpath}"] has no "types" condition — a consumer on TypeScript would get \`any\``);
  }
  entries.push({ subpath, src, js, types });
}
if (entries.length === 0) {
  fail('no exports subpath points at ./dist/*.js — there is nothing to build');
}

// ---------------------------------------------------------------------------
// Externals are DERIVED from the manifest, never hand-listed. A hand-listed set
// rots in the one direction that produces a green build: drop `maplibre-gl` from it
// and esbuild resolves the library out of node_modules and inlines it — succeeding,
// silently, shipping a second copy of a peer to every consumer. Deriving them means
// the manifest and the bundle cannot disagree, and the reachability check below
// turns any remaining disagreement into a failure.
// ---------------------------------------------------------------------------
const declared = [
  ...Object.keys(pkg.dependencies ?? {}),
  ...Object.keys(pkg.peerDependencies ?? {}),
];
// Both forms. MEASURED, because the obvious reason to write it is the wrong one:
// esbuild ALREADY externalizes a named package's subpaths, so `echarts` alone keeps
// `echarts/core` out of the bundle. The `/*` entry is redundant today and kept only so
// the rule does not silently depend on that behaviour — and if it ever stopped being
// true, the foreign-input check below would fail the build rather than ship the inline.
const external = declared.flatMap((name) => [name, `${name}/*`]);

rmSync(distDir, { recursive: true, force: true });

const result = await esbuild.build({
  entryPoints: entries.map((entry) => entry.src),
  outdir: distDir,
  outbase: srcDir,
  bundle: true,
  // 🔴 NOT A TUNING KNOB. widgets/src/widgets/map-geometry.ts dynamically imports
  // ~156 kB of Natural Earth geometry (44 kB on the wire) precisely so a viewer
  // looking at a provider's
  // tiles does not also download a world map. esbuild without `splitting` INLINES
  // dynamic imports of internal modules — folding that payload into the main chunk,
  // and (now that the apps consume `dist`) into their entry graphs too. Every guard
  // protecting that boundary scans `src`, so all of them keep passing while the
  // artifact violates it. packages/widgets/src/dist-shape.test.ts asserts the
  // outcome on the artifact rather than trusting this flag.
  splitting: true,
  format: 'esm',
  platform: 'browser',
  target: 'es2020',
  // Shipped, with sources inlined, so a consumer debugging a stack trace lands in
  // readable code rather than in a bundle. It roughly triples the tarball (widgets:
  // 207 kB packed) and gives away nothing — the sources are Apache-2.0 and public.
  sourcemap: true,
  metafile: true,
  chunkNames: 'chunks/[name]-[hash]',
  jsx: 'automatic',
  external,
  logLevel: 'warning',
});

// ---------------------------------------------------------------------------
// The counterweight to deriving externals: assert that nothing OUTSIDE this
// package's own `src` was bundled in. An externals list that has rotted, a
// dependency nobody declared, a stray reach into another workspace package's
// internals — all three produce a build that succeeds and an artifact that is
// wrong, and all three land here as a foreign input.
// ---------------------------------------------------------------------------
const foreign = Object.keys(result.metafile.inputs).filter(
  (input) => !path.resolve(pkgDir, input).startsWith(srcDir + path.sep),
);
if (foreign.length > 0) {
  fail(
    'these files were bundled in from outside src/, which means something is not external:\n' +
      foreign.map((f) => `  ${f}`).join('\n') +
      '\n\nEverything in "dependencies" and "peerDependencies" is externalized automatically,' +
      '\nso a foreign input means the import is of something package.json declares nowhere.',
  );
}
// A reach control for the check above: an absence claim over an empty input set
// would read exactly like a clean build. A bundle always has at least its entries.
if (Object.keys(result.metafile.inputs).length < entries.length) {
  fail('esbuild reported fewer inputs than entry points — the metafile scan is not seeing this build');
}

// ---------------------------------------------------------------------------
// Declarations, from the pinned compiler. See the header for why this is a
// separate step rather than `--dts` on the bundler.
// ---------------------------------------------------------------------------
function resolveTsc() {
  let dir = pkgDir;
  for (;;) {
    const candidate = path.join(dir, 'node_modules', '.bin', 'tsc');
    if (existsSync(candidate)) return candidate;
    const parent = path.dirname(dir);
    if (parent === dir) return null;
    dir = parent;
  }
}

const tsc = resolveTsc();
if (!tsc) fail('could not find node_modules/.bin/tsc — run `npm ci` in frontend/');

const tscRun = spawnSync(tsc, ['-p', 'tsconfig.build.json'], { stdio: 'inherit', cwd: pkgDir });
if (tscRun.status !== 0) {
  fail(`declaration emit failed (tsc exited ${tscRun.status})`);
}

// 🔴 Asserted rather than assumed. This compiler will report an error and emit in
// the SAME run when `rootDir` is wrong — measured — so a non-zero status is not the
// only way for declarations to be missing, and a zero status is not proof they are
// there. Check for the files a consumer will actually resolve.
for (const entry of entries) {
  const emitted = path.join(pkgDir, entry.types.replace(/^\.\//, ''));
  if (!existsSync(emitted)) {
    fail(
      `exports["${entry.subpath}"].types names ${entry.types}, which the declaration emit did not produce`,
    );
  }
}

const outputs = Object.keys(result.metafile.outputs).length;
console.log(
  `  ${pkg.name}: ${entries.length} entr${entries.length === 1 ? 'y' : 'ies'} -> ${outputs} JS outputs`,
);
