// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Tests for the packed-artifact gate's decision logic.
//
//   node --test scripts/verify-packages.test.mjs
//
// 🔴 WHAT THIS FILE COVERS, AND WHAT IT DELIBERATELY DOES NOT. The gate has two
// halves. This file drives the half that DECIDES — given a packed manifest and a packed
// file list, what is wrong with them — because that half is pure, and because every
// rule in it is an assertion that can only be trusted once it has been seen to fail.
// So nearly every test below is a negative control: break one thing, assert the message
// names it, and assert the same input passes untouched.
//
// The other half PACKS, EXTRACTS, PARSES and LOADS. It is not driven from here: it
// needs `npm pack`, a built `dist` and esbuild, none of which exist when this file runs
// — CI runs `npm run test:scripts` BEFORE `npm ci`, deliberately, so that the release
// plumbing fails in the first seconds of the job rather than behind a two-minute
// install. That half is gated by running the real thing (`npm run verify:packages`,
// after the build), and its own negative controls were run against a planted defect of
// each class: a dialect specifier leaking into the portable entry, a carve-out
// specifier disappearing, a missing LICENSE, a drifted internal peer range, a chunk
// missing from the tarball, `src/` shipped, `publishConfig` dropped, `repository`
// dropped, and an `exports` subpath the build never emitted.

import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, it } from 'node:test';

import { ORDER, SCOPE } from './packages.mjs';
import {
  DIALECT_CARVE_OUTS,
  checkPackedManifest,
  exportTargets,
  isDialect,
  parseArgs,
} from './verify-packages.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const frontend = path.dirname(here);

// A manifest that must pass every rule, written out in full rather than derived from a
// real one — the real manifests are the subject of the LAST describe block, and using
// one here would make "it passes" a statement about that package rather than about the
// rules. Every negative control below is this object with exactly one thing changed.
const GOOD = {
  name: '@devicechain/example',
  version: '1.2.3',
  license: 'Apache-2.0',
  repository: { type: 'git', url: 'git+https://github.com/devicechain-io/devicechain.git' },
  publishConfig: { access: 'public' },
  exports: {
    '.': { types: './dist/index.d.ts', import: './dist/index.js' },
    './package.json': './package.json',
  },
  peerDependencies: { '@devicechain/client': '1.2.3', react: '^19.2.8' },
};
const GOOD_FILES = ['LICENSE', 'README.md', 'package.json', 'dist/index.js', 'dist/index.d.ts'];

/** The manifest with one field replaced, so each control differs from the pass case by one thing. */
const withChange = (change) => ({ ...structuredClone(GOOD), ...change });

function problemsFor(pkg, files = GOOD_FILES, version = '1.2.3') {
  return checkPackedManifest(pkg, files, version).problems;
}

describe('checkPackedManifest', () => {
  // 🔴 First, because every negative control below is worth nothing without it: the
  // rules must accept a correct manifest. A check that rejects everything "catches"
  // every planted defect and blocks every release.
  it('passes a correct manifest with nothing to say', () => {
    assert.deepEqual(problemsFor(GOOD), []);
  });

  it('reports the version, and says what it expected', () => {
    const problems = problemsFor(withChange({ version: '1.2.2' }));
    assert.equal(problems.length, 1);
    assert.match(problems[0], /version is 1\.2\.2, expected 1\.2\.3/);
  });

  // The one that cannot be fixed after the fact. An npm version is immutable, so a
  // package published with an internal peer pointing at a version that does not exist
  // is uninstallable at that number forever.
  it('reports an internal peer range that did not move with the version', () => {
    // The realistic value is the committed placeholder — a package the stamper skipped
    // — so this trips the range rule AND the stray-placeholder backstop. Both are
    // asserted, because a change that quietly dropped one would still leave the other
    // making this test pass.
    const problems = problemsFor(
      withChange({ peerDependencies: { '@devicechain/client': '0.0.0-dev', react: '^19.2.8' } }),
    );
    assert.equal(problems.length, 2);
    assert.match(problems[0], /peerDependencies\.@devicechain\/client is "0\.0\.0-dev"/);
    assert.match(problems[0], /uninstallable/);
    assert.match(problems[1], /placeholder 0\.0\.0-dev survived/);

    // And a range that is merely wrong, with no placeholder in it, is one problem.
    const drifted = problemsFor(
      withChange({ peerDependencies: { '@devicechain/client': '^1.2.3', react: '^19.2.8' } }),
    );
    assert.equal(drifted.length, 1);
    assert.match(drifted[0], /is "\^1\.2\.3", expected "1\.2\.3"/);
  });

  it('leaves EXTERNAL ranges alone — only the internal scope is pinned to the version', () => {
    assert.deepEqual(problemsFor(withChange({ peerDependencies: { react: '^19.2.8' } })), []);
    assert.deepEqual(
      problemsFor(withChange({ dependencies: { echarts: '^6.0.0' } })),
      [],
    );
  });

  it('checks every dependency field, not just peerDependencies', () => {
    for (const field of ['dependencies', 'devDependencies', 'optionalDependencies']) {
      const problems = problemsFor(withChange({ [field]: { '@devicechain/brand': '9.9.9' } }));
      assert.equal(problems.length, 1, `${field} was not checked`);
      assert.match(problems[0], new RegExp(`${field}\\.@devicechain/brand`));
    }
  });

  // The placeholder scan is a backstop for a field the loops above do not know about.
  // It is suppressed when the placeholder IS the expectation, which is the state every
  // pull request is in — otherwise the gate would fail on every PR.
  it('finds a stray placeholder anywhere in the manifest — but not when it is what was asked for', () => {
    const stray = withChange({ overrides: { something: '0.0.0-dev' } });
    assert.match(problemsFor(stray).join('\n'), /placeholder 0\.0\.0-dev survived/);

    const committed = withChange({
      version: '0.0.0-dev',
      peerDependencies: { '@devicechain/client': '0.0.0-dev', react: '^19.2.8' },
    });
    assert.deepEqual(problemsFor(committed, GOOD_FILES, '0.0.0-dev'), []);
  });

  it('requires repository.url, because provenance FAILS without it rather than skipping', () => {
    const problems = problemsFor(withChange({ repository: undefined }));
    assert.equal(problems.length, 1);
    assert.match(problems[0], /provenance/);
  });

  it('requires publishConfig.access=public, because a scoped package defaults to restricted', () => {
    assert.match(problemsFor(withChange({ publishConfig: undefined })).join('\n'), /defaults to restricted/);
    assert.match(
      problemsFor(withChange({ publishConfig: { access: 'restricted' } })).join('\n'),
      /defaults to restricted/,
    );
  });

  it('requires a license and the scope', () => {
    assert.match(problemsFor(withChange({ license: undefined })).join('\n'), /no license field/);
    assert.match(problemsFor(withChange({ name: 'devicechain-example' })).join('\n'), new RegExp(`not under ${SCOPE}`));
  });

  // 🔴 `files` naming a path that does not exist ships NOTHING and npm does not error.
  // That is how three of these packages had no LICENSE at all while every manifest
  // named one.
  it('reports an exports target, a README or a LICENSE that is not in the tarball', () => {
    assert.match(
      problemsFor(GOOD, GOOD_FILES.filter((f) => f !== 'dist/index.js')).join('\n'),
      /exports\/main names dist\/index\.js, which is NOT in the tarball/,
    );
    assert.match(
      problemsFor(GOOD, GOOD_FILES.filter((f) => f !== 'dist/index.d.ts')).join('\n'),
      /names dist\/index\.d\.ts, which is NOT in the tarball/,
    );
    for (const required of ['README.md', 'LICENSE']) {
      assert.match(
        problemsFor(GOOD, GOOD_FILES.filter((f) => f !== required)).join('\n'),
        new RegExp(`${required} is not in the tarball`),
      );
    }
  });

  it('reports the legacy main/types fields too, which tools still read', () => {
    const problems = problemsFor(withChange({ main: './dist/legacy.js' }));
    assert.equal(problems.length, 1);
    assert.match(problems[0], /dist\/legacy\.js/);
  });

  it('reports files that must never ship, naming the file and why', () => {
    const cases = [
      ['src/index.ts', /sources/],
      ['dist/thing.test.js', /a test file/],
      ['dist/thing.test.tsx', /a test file/],
      ['node_modules/left-pad/index.js', /nested node_modules/],
      ['tsconfig.build.json', /build configuration/],
      ['dist/tsconfig.tsbuildinfo', /build cache/],
    ];
    for (const [file, why] of cases) {
      const problems = problemsFor(GOOD, [...GOOD_FILES, file]);
      assert.equal(problems.length, 1, `${file} was not rejected`);
      assert.match(problems[0], why);
    }
  });

  // Sourcemaps are shipped deliberately — with sources inlined, so a consumer debugging
  // a stack trace lands in readable code. A rule that swept them up would be found by
  // whoever added it and quietly relaxed, so it is asserted instead.
  it('does NOT reject the sourcemaps, which are shipped on purpose', () => {
    assert.deepEqual(problemsFor(GOOD, [...GOOD_FILES, 'dist/index.js.map']), []);
  });

  it('counts what it checked, so the caller can tell an empty run from a clean one', () => {
    const { checked } = checkPackedManifest(GOOD, GOOD_FILES, '1.2.3');
    assert.equal(checked.targets, 3); // dist/index.js, dist/index.d.ts, package.json
    assert.equal(checked.files, GOOD_FILES.length);
  });
});

describe('exportTargets', () => {
  it('reads both the string form and the conditions form', () => {
    const { files, entries } = exportTargets({
      exports: {
        '.': { types: './dist/index.d.ts', import: './dist/index.js' },
        './vite': { types: './dist/vite.d.ts', import: './dist/vite.js' },
        './package.json': './package.json',
        './tokens': './tokens.generated.json',
      },
    });
    assert.deepEqual(files.sort(), [
      'dist/index.d.ts',
      'dist/index.js',
      'dist/vite.d.ts',
      'dist/vite.js',
      'package.json',
      'tokens.generated.json',
    ]);
    // Only the JS entries are scannable and loadable — a .d.ts is not a module graph,
    // and a .json is not one either.
    assert.deepEqual(entries, [
      { subpath: '.', rel: 'dist/index.js' },
      { subpath: './vite', rel: 'dist/vite.js' },
    ]);
  });

  it('ignores a target that is not a relative path, so a bare specifier is not read as a file', () => {
    const { files } = exportTargets({ exports: { '.': { import: 'some-other-package' } } });
    assert.deepEqual(files, []);
  });

  // No package writes a nested condition today, which is exactly why this is pinned:
  // the version that read one level deep passed over one silently, and a target nothing
  // checked was packed is the failure this whole gate exists to catch.
  it('descends into NESTED conditions rather than skipping them', () => {
    const { files, entries } = exportTargets({
      exports: {
        '.': {
          node: { types: './dist/node.d.ts', import: './dist/node.js' },
          default: './dist/index.js',
        },
      },
    });
    assert.deepEqual(files.sort(), ['dist/index.js', 'dist/node.d.ts', 'dist/node.js']);
    // ...and a `.d.ts` reached through a nested `types` is still not a loadable entry.
    assert.deepEqual(entries.map((e) => e.rel).sort(), ['dist/index.js', 'dist/node.js']);
  });
});

describe('the dialect rule', () => {
  it('calls a query string or a stylesheet a dialect, and nothing else', () => {
    for (const yes of [
      'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url',
      './thing.css',
      'maplibre-gl/dist/maplibre-gl.css',
      './styles?inline',
      './asset?url',
    ]) {
      assert.ok(isDialect(yes), `${yes} should be a dialect`);
    }
    for (const no of ['react', 'echarts/core', './registry', '@devicechain/client', './chunks/a-B7.js']) {
      assert.ok(!isDialect(no), `${no} should NOT be a dialect`);
    }
  });

  // The carve-out is the contract `widgets/src/vite.ts` documents. Pinned here so
  // widening it is an edit somebody has to make on purpose, in a file with a test
  // beside it, rather than a specifier that appears and is never noticed.
  it('carves out exactly one entry, with exactly two specifiers', () => {
    assert.deepEqual([...DIALECT_CARVE_OUTS.keys()], ['@devicechain/widgets./vite']);
    assert.deepEqual(DIALECT_CARVE_OUTS.get('@devicechain/widgets./vite'), [
      'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url',
      'maplibre-gl/dist/maplibre-gl.css',
    ]);
  });
});

describe('parseArgs', () => {
  it('defaults to the committed placeholder, which is what a pull request must be at', () => {
    assert.deepEqual(parseArgs([]), { version: '0.0.0-dev', keep: false });
  });

  it('takes the tag verbatim and strips the leading v, like the stamper', () => {
    assert.equal(parseArgs(['--version', 'v0.14.0']).version, '0.14.0');
    assert.equal(parseArgs(['--version', '0.14.0']).version, '0.14.0');
  });

  it('refuses an argument it does not understand rather than ignoring it', () => {
    // A typo'd flag silently ignored is a gate that ran with the wrong expectation.
    assert.throws(() => parseArgs(['--verson', '0.14.0']), /unknown argument/);
    assert.throws(() => parseArgs(['--version']), /needs a value/);
  });
});

// ---------------------------------------------------------------------------
// 🔴 The reach controls. Everything above runs over an object written twenty lines
// earlier, so all of it would keep passing if the real packages stopped having
// `exports` at all — and then the real gate would check nothing and report success.
// These read the tree.
// ---------------------------------------------------------------------------
describe('the real manifests give the gate something to check', () => {
  const manifests = ORDER.map((name) => ({
    name,
    pkg: JSON.parse(readFileSync(path.join(frontend, 'packages', name, 'package.json'), 'utf8')),
  }));

  it('every publishable package has exports the gate can resolve', () => {
    for (const { name, pkg } of manifests) {
      const { files } = exportTargets(pkg);
      assert.ok(files.length > 0, `${name} yields no exports targets — the gate would check nothing`);
    }
  });

  it('at least one JS entry exists to scan and load — otherwise both artifact arms are vacuous', () => {
    const entries = manifests.flatMap(({ pkg }) => exportTargets(pkg).entries);
    assert.ok(entries.length >= 3, `only ${entries.length} JS entries across the workspace`);
  });

  it('the carve-out names a subpath that actually exists', () => {
    for (const key of DIALECT_CARVE_OUTS.keys()) {
      const found = manifests.some(({ pkg }) =>
        Object.keys(pkg.exports ?? {}).some((subpath) => `${pkg.name}${subpath}` === key),
      );
      assert.ok(found, `the carve-out ${key} names an exports subpath no package has`);
    }
  });

  // The gate is only ever run by CI and by the release job. A rename surfaces as a
  // release that skipped its last check before an immutable publish, so it is pinned.
  it('stays wired to both workflows that run it', () => {
    // `includes` rather than `assert.match`: a failed regex assertion prints the entire
    // workflow as the "actual" value and buries the one line that matters.
    // Each file is pinned to what it actually writes. ci.yml goes through the npm
    // script (no arguments, and a maintainer looking for "how do I check this" finds it
    // in package.json); release.yml calls the file directly because it passes the tag,
    // matching the stamper on the line above it.
    const workflows = path.join(frontend, '..', '.github', 'workflows');
    for (const [file, call] of [
      ['ci.yml', 'npm run verify:packages'],
      ['release.yml', 'scripts/verify-packages.mjs --version'],
    ]) {
      const text = readFileSync(path.join(workflows, file), 'utf8');
      assert.ok(text.includes(call), `${file} no longer runs the packed-artifact gate (\`${call}\`)`);
    }
    // And the npm script ci.yml calls has to exist.
    const root = JSON.parse(readFileSync(path.join(frontend, 'package.json'), 'utf8'));
    assert.ok(root.scripts['verify:packages'], 'frontend/package.json has no verify:packages script');
  });
});
