// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Assertions for hack/widget-package-proof.sh. Usage: node check.mjs <builtCopyDir> <sourceExampleDir>
//
// Run with the built copy as the working directory, so `esbuild` resolves the INSTALLED
// tarballs. esbuild itself is loaded from the copy's own node_modules for the same reason.
import { existsSync, readFileSync, readdirSync, realpathSync, statSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';

const [copyDir, sourceDir] = process.argv.slice(2);
if (!copyDir || !sourceDir) {
  console.error('usage: node check.mjs <builtCopyDir> <sourceExampleDir>');
  process.exit(2);
}

const copyRequire = createRequire(path.join(copyDir, 'package.json'));
const esbuild = copyRequire('esbuild');
const failures = [];
const expect = (ok, message) => {
  if (!ok) failures.push(message);
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${message}`);
};

// ---- 1. no private / monorepo reach in the example ---------------------------------------

const ALLOWED_BARE = new Set([
  'react',
  'react/jsx-runtime',
  '@devicechain/widget-sdk',
  '@devicechain/widget-sdk/echarts',
  '@devicechain/dashboards',
]);

function walk(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const full = path.join(dir, name);
    if (statSync(full).isDirectory()) out.push(...walk(full));
    else out.push(full);
  }
  return out;
}

const SPECIFIER = /(?:from\s+|import\s*\(\s*|import\s+|require\s*\(\s*)['"]([^'"]+)['"]/g;
const sources = walk(path.join(sourceDir, 'src')).filter((f) => /\.(ts|tsx)$/.test(f));
expect(sources.length >= 4, `scanned ${sources.length} example source files`);
for (const file of sources) {
  const text = readFileSync(file, 'utf8');
  for (const [, spec] of text.matchAll(SPECIFIER)) {
    const rel = path.relative(sourceDir, file);
    if (spec.startsWith('./')) continue;
    expect(ALLOWED_BARE.has(spec), `${rel} imports only allowed specifiers (${spec})`);
  }
}

const pkg = JSON.parse(readFileSync(path.join(sourceDir, 'package.json'), 'utf8'));
for (const field of ['dependencies', 'devDependencies', 'peerDependencies']) {
  for (const [name, range] of Object.entries(pkg[field] ?? {})) {
    expect(
      !/^(workspace:|file:|link:|\.|\/|git)/.test(range),
      `${field}.${name} is a registry range, not a local path (${range})`,
    );
  }
}
expect(
  !Object.keys(pkg.dependencies ?? {}).some((n) => n.startsWith('@devicechain/')),
  '@devicechain/* are peers, never dependencies',
);
expect(!('@devicechain/widgets' in { ...pkg.peerDependencies, ...pkg.devDependencies }), 'does not depend on @devicechain/widgets');

const tsconfig = readFileSync(path.join(sourceDir, 'tsconfig.json'), 'utf8');
expect(!/"paths"|"baseUrl"|\.\.\//.test(tsconfig), 'tsconfig has no paths, baseUrl or parent-directory reach');

// ---- 1b. the peers resolve from the installed tarballs, and the SDK exactly once ----------

// A symlink or a parent node_modules would resolve outside the copy; realpath sees through both.
const ownModules = path.join(realpathSync(copyDir), 'node_modules') + path.sep;
for (const name of ['@devicechain/widget-sdk', '@devicechain/dashboards', 'react']) {
  const resolved = realpathSync(copyRequire.resolve(`${name}/package.json`));
  expect(resolved.startsWith(ownModules), `${name} resolves inside the copy's node_modules (${resolved})`);
}

// The SDK's React contexts are identified by the object that created them, so a second,
// nested copy would give a widget a selector that silently does nothing.
function installedCopies(root, scope, name) {
  let n = 0;
  const stack = [path.join(root, 'node_modules')];
  while (stack.length > 0) {
    const dir = stack.pop();
    if (!existsSync(dir)) continue;
    if (existsSync(path.join(dir, scope, name, 'package.json'))) n++;
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (!entry.isDirectory()) continue;
      if (entry.name.startsWith('@')) {
        for (const inner of readdirSync(path.join(dir, entry.name), { withFileTypes: true })) {
          if (inner.isDirectory()) stack.push(path.join(dir, entry.name, inner.name, 'node_modules'));
        }
      } else {
        stack.push(path.join(dir, entry.name, 'node_modules'));
      }
    }
  }
  return n;
}
const sdkCopies = installedCopies(copyDir, '@devicechain', 'widget-sdk');
expect(sdkCopies === 1, `exactly one copy of @devicechain/widget-sdk is installed (found ${sdkCopies})`);

// ---- 2. the built DOM-only entry ---------------------------------------------------------

function importsOf(file) {
  const result = esbuild.buildSync({
    entryPoints: [file],
    bundle: true,
    write: false,
    metafile: true,
    format: 'esm',
    platform: 'browser',
    packages: 'external',
    logLevel: 'silent',
  });
  return [...new Set(Object.values(result.metafile.inputs).flatMap((i) => i.imports.map((x) => x.path)))].sort();
}

const cardImports = importsOf(path.join(copyDir, 'dist/card.js'));
console.log(`     dist/card.js imports: ${cardImports.join(', ')}`);
expect(cardImports.includes('@devicechain/widget-sdk'), 'dist/card.js imports the SDK root');
for (const spec of cardImports) {
  expect(
    ['react', 'react/jsx-runtime', '@devicechain/widget-sdk', '@devicechain/dashboards'].includes(spec),
    `dist/card.js imports only the root-level peers (${spec})`,
  );
}

const indexImports = importsOf(path.join(copyDir, 'dist/index.js'));
expect(indexImports.includes('@devicechain/widget-sdk/echarts'), 'dist/index.js imports the SDK echarts entry (reach control)');

// ---- 3. what a HOST bundle of each entry would contain -----------------------------------

const HEAVY = [/node_modules[\\/]echarts[\\/]/, /node_modules[\\/]zrender[\\/]/, /node_modules[\\/]maplibre-gl[\\/]/, /@devicechain[\\/]widgets[\\/]/];

function hostBundle(entry) {
  const result = esbuild.buildSync({
    entryPoints: [path.join(copyDir, entry)],
    bundle: true,
    write: false,
    metafile: true,
    format: 'esm',
    platform: 'browser',
    // The way a host app bundles: everything resolved, from the installed tarballs.
    absWorkingDir: copyDir,
    logLevel: 'silent',
    jsx: 'automatic',
  });
  const inputs = Object.keys(result.metafile.inputs);
  const bytes = result.outputFiles.reduce((n, f) => n + f.contents.length, 0);
  const text = result.outputFiles.map((f) => f.text).join('\n');
  return { inputs, bytes, text };
}

const card = hostBundle('dist/card.js');
const chart = hostBundle('dist/index.js');
console.log(`     host bundle: card ${card.bytes} bytes (${card.inputs.length} modules), index ${chart.bytes} bytes (${chart.inputs.length} modules)`);

// Reach control FIRST: an absence claim from a detector that cannot see the thing is vacuous.
expect(
  chart.inputs.some((i) => HEAVY[0].test(i)) && chart.inputs.some((i) => HEAVY[1].test(i)),
  'reach control: the chart entry bundle DOES contain echarts and zrender',
);
for (const re of HEAVY) {
  expect(!card.inputs.some((i) => re.test(i)), `DOM-only host bundle contains no module matching ${re}`);
}
expect(!/maplibre/i.test(card.text), 'DOM-only host bundle text never mentions maplibre');
expect(card.bytes * 4 < chart.bytes, 'DOM-only host bundle is a fraction of the chart bundle');

if (failures.length > 0) {
  console.error(`\n${failures.length} check(s) failed`);
  process.exit(1);
}
