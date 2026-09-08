// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Verify the four publishable packages AS TARBALLS — the bytes `npm publish` uploads —
// rather than as sources, as a workspace, or as a `dist` directory.
//
//   node scripts/verify-packages.mjs                 # expects the committed placeholder
//   node scripts/verify-packages.mjs --version 0.14.0
//
// 🔴 WHY THE TARBALL AND NOT `dist/`. Everything between `dist` and what a consumer
// downloads is decided by fields nothing else in this repo reads: `files`, `exports`,
// `publishConfig`. Each of them fails in the same direction — quietly, with a green
// build behind it:
//
//   `files` naming a path that does not exist       npm ships nothing and does not error
//   an `exports` subpath whose target is not packed a bare module-not-found at install
//   a missing `publishConfig.access`                a scoped publish that is not public
//   a stale internal peer range                     ERESOLVE for every consumer, forever
//
// The last one is the reason this exists at all. An npm version is IMMUTABLE: a
// `widgets@0.14.0` whose `@devicechain/client` peer still says `0.0.0-dev` is
// uninstallable at that number for as long as the registry exists. There is no fix
// after the fact, only a burnt version. So the check has to run BEFORE the publish,
// on the artifact, and it does — the release job runs it between the version stamp
// and the first `npm publish`.
//
// 🔴 THE SPECIFIER SCAN ASKS A PARSER, NOT A REGEX. esbuild does not strip comments,
// so these packages' own source comments — several of which QUOTE the forbidden
// `?worker&url` specifier while explaining the rule — survive into `dist`. A text scan
// flags those, and the first fix for that is to start stripping comments by hand,
// which is how the source-level version of this gate acquired three separate evasions.
// `metafile.inputs[*].imports` is every specifier the real parser saw.
//
// 🔴 AND THEN IT LOADS THEM, under bare Node ESM, out of a node_modules built ONLY
// from what each manifest declares. That arm is what turns the scan from an absence
// claim into a positive one: a broken relative specifier, an unemitted chunk, an
// undeclared dependency and a bundler-dialect import are all resolution failures, and
// a module that loads has demonstrated it has none of them. The work directory is
// outside the repository on purpose — Node resolves by walking parent directories, so
// an extraction under `frontend/` would see `frontend/node_modules` and resolve
// specifiers the tarball never declared.
//
// The overlap with packages/widgets/src/dist-shape.test.ts is deliberate and is
// documented there: that file reads the workspace `dist` and owns the assertions
// specific to the widgets' own shape (the lazy geometry boundary, the chunk it lives
// in, the portable entry never reaching the /vite one). This one reads the TARBALL,
// across all four packages, and so also sees what `files` left out.
//
// 🔴 SCOPE, so it is not overclaimed. This proves the artifact RESOLVES and LOADS in
// Node. It does not prove it renders, and it exercises no bundler. That is
// hack/consumer-proof.sh's job — a real browser, three bundlers, out of tree — and it
// is not a merge gate because it needs Chrome. The two are complementary: this one
// runs on every pull request and every release.

import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, rmSync, symlinkSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';
import { DEPENDENCY_FIELDS, PackageError, SCOPE, resolvePackages } from './packages.mjs';

const PLACEHOLDER = '0.0.0-dev';

// ---------------------------------------------------------------------------
// The bundler-dialect carve-out, written down rather than inferred.
//
// A query-string specifier or a stylesheet-as-a-module is legal in exactly one place:
// `@devicechain/widgets/vite`, a subpath a consumer reaches BY NAME, having chosen
// Vite. Everywhere else it is the arc's defining defect — a specifier only one bundler
// resolves, in a package that claims to be bundler-neutral, which no build anywhere
// reports because the library it points into is external and the specifier is
// therefore only ever copied, never resolved.
//
// Keyed by `<package name><exports subpath>`, and asserted as an EXACT set: a third
// specifier appearing under this key is a change to the contract that entry documents,
// and should have to be written here to ship.
//
// An entry with a carve-out is also excluded from the Node load arm below — a `?query`
// specifier is, by construction, one Node cannot resolve. That exclusion is the same
// fact stated twice, which is why both come from this one map.
// ---------------------------------------------------------------------------
export const DIALECT_CARVE_OUTS = new Map([
  [
    '@devicechain/widgets./vite',
    ['maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url', 'maplibre-gl/dist/maplibre-gl.css'],
  ],
]);

/** A specifier only one bundler understands: a query string, or a stylesheet as a module. */
export const isDialect = (specifier) => specifier.includes('?') || specifier.endsWith('.css');

// ---------------------------------------------------------------------------
// Paths a published tarball must not contain. Each of these has actually shipped from
// this workspace: before the `files` allowlist landed, a pack of these three packages
// carried 36 test files and the widgetlab fixtures.
//
// Sourcemaps are deliberately NOT here. They are shipped, with sources inlined, so a
// consumer debugging a stack trace lands in readable code; the sources are Apache-2.0
// and public.
// ---------------------------------------------------------------------------
const FORBIDDEN = [
  { re: /(^|\/)src\//, why: 'sources — a consumer resolves `dist`, and shipping `src` doubles the tarball' },
  { re: /\.test\.[cm]?[jt]sx?$/, why: 'a test file' },
  { re: /(^|\/)node_modules\//, why: 'a nested node_modules' },
  { re: /(^|\/)tsconfig[^/]*\.json$/, why: 'a tsconfig — build configuration, not artifact' },
  { re: /\.tsbuildinfo$/, why: 'a TypeScript build cache' },
];

// ---------------------------------------------------------------------------
// Every `exports` target that must be IN the tarball, as tarball-relative paths.
//
// Read off `exports` rather than listed beside it, for the reason build-package.mjs
// derives its entries the same way: a second list is a second thing to forget, and
// what it permits is an `exports` subpath resolving to a file that was never packed —
// which npm accepts and a consumer meets as a bare module-not-found.
//
// Returns both the paths and, separately, the JS entries with their subpaths, since
// the scan and load arms below run per ENTRY and need to know which subpath each came
// from to look up its carve-out.
// ---------------------------------------------------------------------------
export function exportTargets(pkg) {
  const files = new Set();
  const entries = [];

  // Conditions NEST — `{ ".": { node: { import: "./x.js" } } }` is legal, and none of
  // these packages writes one today. Walking the tree rather than reading one level
  // deep is four extra lines and removes a silent skip: a nested target this did not
  // descend into would be a file nothing checked was packed, inside a gate whose whole
  // job is to notice that.
  const walk = (subpath, node, underTypes) => {
    if (typeof node === 'string') {
      if (!node.startsWith('./')) return; // a bare specifier: another package's problem
      const rel = node.slice(2);
      files.add(rel);
      // Only a JS target is a module graph to scan and load; a `.d.ts` or a `.json` is
      // checked for presence and nothing more.
      if (!underTypes && /\.[cm]?js$/.test(rel)) entries.push({ subpath, rel });
      return;
    }
    if (node === null || typeof node !== 'object') return;
    for (const [condition, value] of Object.entries(node)) {
      walk(subpath, value, underTypes || condition === 'types');
    }
  };
  for (const [subpath, target] of Object.entries(pkg.exports ?? {})) walk(subpath, target, false);
  // `main` and `types` are legacy fallbacks, but a tool that reads them and finds
  // nothing there is a bug report we would rather not receive.
  for (const legacy of [pkg.main, pkg.types]) {
    if (typeof legacy === 'string' && legacy.startsWith('./')) files.add(legacy.slice(2));
  }
  return { files: [...files], entries };
}

// ---------------------------------------------------------------------------
// Everything that can be decided from the packed manifest and the packed file list.
// Split out from the disk work so it can be driven directly by the tests, both ways:
// a manifest that should pass, and one deliberately broken in each direction.
//
// Returns a list of problems. An empty list means every check ran and none fired —
// which is only true because `checked` is returned alongside and asserted by the
// caller: an `exports` map that produced no targets, or a file list that came back
// empty, would otherwise satisfy every rule here by having nothing to test.
// ---------------------------------------------------------------------------
export function checkPackedManifest(pkg, filePaths, expectedVersion) {
  const problems = [];
  const name = pkg.name ?? '<unnamed>';
  const packed = new Set(filePaths);

  if (typeof pkg.name !== 'string' || !pkg.name.startsWith(SCOPE)) {
    problems.push(`${name}: name is not under ${SCOPE}`);
  }
  if (pkg.version !== expectedVersion) {
    problems.push(`${name}: version is ${pkg.version}, expected ${expectedVersion}`);
  }
  for (const field of DEPENDENCY_FIELDS) {
    for (const [dep, range] of Object.entries(pkg[field] ?? {})) {
      if (dep.startsWith(SCOPE) && range !== expectedVersion) {
        problems.push(
          `${name}: ${field}.${dep} is "${range}", expected "${expectedVersion}" — an exact-pinned ` +
            'internal peer that does not match makes the package uninstallable, permanently',
        );
      }
    }
  }
  // Only meaningful once a real version is expected: when the placeholder IS the
  // expectation, finding it is the pass condition, not the failure.
  if (expectedVersion !== PLACEHOLDER && JSON.stringify(pkg).includes(`"${PLACEHOLDER}"`)) {
    problems.push(`${name}: the placeholder ${PLACEHOLDER} survived somewhere in the packed manifest`);
  }

  // Provenance is generated from the manifest's `repository`, and its ABSENCE fails
  // the attestation rather than silently skipping it — so the release would die after
  // the publish it cannot take back.
  if (!pkg.repository?.url) {
    problems.push(`${name}: no repository.url — npm provenance attestation requires it and fails without it`);
  }
  if (!pkg.license) problems.push(`${name}: no license field`);
  // A scoped package defaults to RESTRICTED. Without this, a publish either 402s
  // (no paid plan) or succeeds privately, which reads as success.
  if (pkg.publishConfig?.access !== 'public') {
    problems.push(`${name}: publishConfig.access is not "public" — a scoped package defaults to restricted`);
  }

  const { files: targets } = exportTargets(pkg);
  for (const target of targets) {
    if (!packed.has(target)) {
      problems.push(
        `${name}: exports/main names ${target}, which is NOT in the tarball — check the "files" allowlist`,
      );
    }
  }
  for (const required of ['README.md', 'LICENSE']) {
    if (!packed.has(required)) {
      problems.push(`${name}: ${required} is not in the tarball ("files" naming a missing path ships nothing, silently)`);
    }
  }
  for (const file of filePaths) {
    const hit = FORBIDDEN.find(({ re }) => re.test(file));
    if (hit) problems.push(`${name}: ships ${file} — ${hit.why}`);
  }

  return { problems, checked: { targets: targets.length, files: filePaths.length } };
}

// ---------------------------------------------------------------------------
// Pack, extract, and build the hermetic node_modules. Returns the extracted package
// directory.
// ---------------------------------------------------------------------------
function packAndExtract(name, dir, work, extractedByName) {
  const tarballs = path.join(work, 'tarballs');
  mkdirSync(tarballs, { recursive: true });

  // `--json` for the file list, so the inventory comes from npm's own packer rather
  // than from a second reading of `files` — the point is to see what npm decided.
  let report;
  try {
    const stdout = execFileSync('npm', ['pack', '--json', '--pack-destination', tarballs], {
      cwd: dir,
      encoding: 'utf8',
      // npm writes its notices to stderr; anything it puts on stdout that is not the
      // JSON would break the parse, so slice from the array rather than trusting it.
      stdio: ['ignore', 'pipe', 'inherit'],
    });
    report = JSON.parse(stdout.slice(stdout.indexOf('[')))[0];
  } catch (err) {
    // npm's own error is already on stderr, inherited above. This says which package it
    // was about, which the raw stack over an execFileSync internal does not.
    throw new PackageError(`${name}: \`npm pack\` failed (${err.message.split('\n')[0]})`);
  }

  const root = path.join(work, 'extract', name);
  mkdirSync(root, { recursive: true });
  execFileSync('tar', ['-xzf', path.join(tarballs, report.filename), '-C', root]);
  const pkgDir = path.join(root, 'package');
  if (!existsSync(path.join(pkgDir, 'package.json'))) {
    throw new PackageError(`${name}: the tarball did not extract to package/package.json`);
  }

  // The ONLY resolution source for the load arm. Built from what the manifest
  // declares, so an import of something declared nowhere fails to resolve — which is
  // the whole reason not to extract inside the workspace.
  const pkg = JSON.parse(readFileSync(path.join(pkgDir, 'package.json'), 'utf8'));
  const nm = path.join(root, 'node_modules');
  for (const dep of [
    ...Object.keys(pkg.dependencies ?? {}),
    ...Object.keys(pkg.peerDependencies ?? {}),
  ]) {
    // An internal peer points at the EXTRACTED sibling, not the workspace link, so the
    // whole set is checked as tarballs. Leaf-first order guarantees it is already there.
    const target = dep.startsWith(SCOPE)
      ? extractedByName.get(dep)
      : path.join(defaultFrontendDir(), 'node_modules', dep);
    if (!target || !existsSync(target)) {
      throw new PackageError(
        `${name}: declares "${dep}" but there is nothing to link it to at ${target ?? '<unknown>'} — ` +
          'run `npm ci` in frontend/',
      );
    }
    const link = path.join(nm, dep);
    mkdirSync(path.dirname(link), { recursive: true });
    if (!existsSync(link)) symlinkSync(target, link, 'dir');
  }

  return { pkgDir, pkg, files: report.files.map((f) => f.path), report };
}

export function defaultFrontendDir() {
  return path.dirname(path.dirname(fileURLToPath(import.meta.url)));
}

// ---------------------------------------------------------------------------
// The artifact arms: what the parser saw, and whether Node can load it.
// ---------------------------------------------------------------------------
async function scanAndLoad(name, pkgDir, pkg) {
  const esbuild = await import('esbuild');
  const problems = [];
  const { entries } = exportTargets(pkg);
  let specifiersSeen = 0;
  let loaded = 0;

  for (const { subpath, rel } of entries) {
    const entryPath = path.join(pkgDir, rel);
    let result;
    try {
      result = await esbuild.build({
        entryPoints: [entryPath],
        bundle: true,
        write: false,
        metafile: true,
        format: 'esm',
        platform: 'browser',
        // Keep node_modules out — this is THIS package's own graph — while still
        // recording every bare specifier it points at.
        packages: 'external',
        logLevel: 'silent',
      });
    } catch (err) {
      // 🔴 Converted to a problem rather than thrown. An entry whose own graph does not
      // resolve — a chunk `splitting` emitted and `files` then failed to pack, a
      // relative specifier pointing at nothing — makes esbuild throw here, BEFORE the
      // load arm below ever runs. Left unhandled it exits non-zero with a Node stack
      // trace over an esbuild internal, which is a true failure reported as a crash;
      // the reader has to work out that it was their package and not this script.
      problems.push(
        `${name}: exports["${subpath}"] (${rel}) does not resolve as a module graph: ` +
          `${(err.errors ?? []).map((e) => e.text).join('; ') || err.message}`,
      );
      continue;
    }
    const imports = Object.values(result.metafile.inputs).flatMap((input) =>
      input.imports.map((i) => i.path),
    );
    specifiersSeen += imports.length;

    const key = `${pkg.name}${subpath}`;
    const allowed = DIALECT_CARVE_OUTS.get(key) ?? [];
    const found = imports.filter(isDialect);
    // Exact set, both directions: an unexpected dialect specifier is the defect, and a
    // carve-out that no longer appears means this map is now describing a contract the
    // artifact does not have.
    const sameSet = found.length === allowed.length && allowed.every((s) => found.includes(s));
    if (!sameSet) {
      problems.push(
        `${name}: exports["${subpath}"] carries bundler-dialect specifiers [${found.join(', ')}], ` +
          `expected exactly [${allowed.join(', ')}]. A query-string or stylesheet specifier is ` +
          'unresolvable for a webpack or Rollup consumer, and no build anywhere reports it.',
      );
    }

    if (allowed.length > 0) continue; // by construction not Node-loadable; see DIALECT_CARVE_OUTS

    try {
      const namespace = await import(pathToFileURL(entryPath).href);
      const exported = Object.keys(namespace).length;
      if (exported === 0) {
        problems.push(`${name}: exports["${subpath}"] loaded but exports nothing`);
      }
      loaded += 1;
      console.log(`    ${subpath} -> ${rel}: ${imports.length} specifiers, ${exported} exports`);
    } catch (err) {
      problems.push(
        `${name}: exports["${subpath}"] does not load under bare Node ESM: ${err.message}\n` +
          '      (a broken relative specifier, an unemitted chunk, an undeclared dependency, or a ' +
          'bundler-dialect import — all four land here)',
      );
    }
  }

  return { problems, entries: entries.length, specifiersSeen, loaded };
}

// ---------------------------------------------------------------------------
export function parseArgs(argv) {
  let version = PLACEHOLDER;
  let keep = false;
  for (let i = 0; i < argv.length; i += 1) {
    if (argv[i] === '--version') {
      version = (argv[i + 1] ?? '').replace(/^v/, '');
      i += 1;
      if (!version) throw new PackageError('--version needs a value');
    } else if (argv[i] === '--keep') {
      keep = true;
    } else {
      throw new PackageError(`unknown argument '${argv[i]}'. Usage: verify-packages.mjs [--version X] [--keep]`);
    }
  }
  return { version, keep };
}

export async function main(argv, packagesDir = path.join(defaultFrontendDir(), 'packages')) {
  const { version, keep } = parseArgs(argv);
  const packages = resolvePackages(packagesDir);
  const work = path.join(os.tmpdir(), `dc-verify-packages-${process.pid}`);
  rmSync(work, { recursive: true, force: true });

  const problems = [];
  const extractedByName = new Map();
  // Reach controls, all four of them. Every check above is at heart an assertion over
  // a collection, and each collection has an empty state that satisfies it.
  let totals = { targets: 0, files: 0, entries: 0, specifiers: 0, loaded: 0 };

  try {
    for (const { name, dir } of packages) {
      console.log(`  ${name}`);
      const { pkgDir, pkg, files, report } = packAndExtract(name, dir, work, extractedByName);
      extractedByName.set(pkg.name, pkgDir);
      console.log(`    ${report.filename}: ${files.length} files, ${report.unpackedSize} bytes unpacked`);

      const manifest = checkPackedManifest(pkg, files, version);
      problems.push(...manifest.problems);
      totals.targets += manifest.checked.targets;
      totals.files += manifest.checked.files;

      const artifact = await scanAndLoad(name, pkgDir, pkg);
      problems.push(...artifact.problems);
      totals.entries += artifact.entries;
      totals.specifiers += artifact.specifiersSeen;
      totals.loaded += artifact.loaded;
    }
  } finally {
    if (keep) console.log(`\nwork directory kept at ${work}`);
    else rmSync(work, { recursive: true, force: true });
  }

  // 🔴 THE REACH CONTROLS. Nearly everything above is either an absence claim or a
  // per-item loop, and both read as clean over an empty collection: a tarball that
  // packed nothing has no forbidden files in it, an `exports` map the parser did not
  // understand yields no entries to scan, and a scan that opened no files reports no
  // dialect. Each number below is the specific emptiness that would make the check
  // above it vacuous. They are floors, not exact counts — an exact count is a second
  // thing to update for every legitimate edit, and gets relaxed the first time it is
  // in the way.
  if (totals.files < packages.length * 3) {
    problems.push(`only ${totals.files} packed files across ${packages.length} packages — the pack inventory is not being read`);
  }
  if (totals.targets < packages.length) {
    problems.push(`only ${totals.targets} exports targets resolved — the exports maps are not being read`);
  }
  if (totals.entries === 0) problems.push('no JS entry was scanned at all — the dialect scan checked nothing');
  if (totals.specifiers === 0) problems.push('the parser reported no import specifiers — the scan is not seeing these bundles');
  if (totals.loaded === 0) problems.push('no entry was loaded under Node — the load arm ran over nothing');

  if (problems.length > 0) {
    throw new PackageError(`the packed artifacts are wrong:\n${problems.map((p) => `  ${p}`).join('\n')}`);
  }

  console.log(
    `\nVerified ${packages.length} tarballs at ${version}: ${totals.files} packed files, ` +
      `${totals.targets} exports targets, ${totals.entries} entries scanned ` +
      `(${totals.specifiers} specifiers), ${totals.loaded} loaded under Node.`,
  );
}

if (fileURLToPath(import.meta.url) === path.resolve(process.argv[1] ?? '')) {
  try {
    await main(process.argv.slice(2));
  } catch (err) {
    if (!(err instanceof PackageError)) throw err;
    console.error(`\n==> verify-packages: ${err.message}\n`);
    process.exit(1);
  }
}
