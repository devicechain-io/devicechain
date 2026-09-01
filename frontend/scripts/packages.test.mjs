// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Tests for the release-time package plumbing: the ordered package list, the version
// stamper, and the parts of the publisher that can be exercised without a registry.
//
//   node --test scripts/packages.test.mjs
//
// 🔴 WHY THIS FILE EXISTS AT ALL. Everything under test here runs exactly once per
// release, inside a job nobody watches, against a registry where a mistake is
// PERMANENT — an npm version cannot be republished. The failure mode that matters is
// not a crash: it is a rewriter that quietly matches nothing and a verifier that then
// passes over the empty result. So the tests below are weighted towards "prove the
// thing actually did something", and several of them are negative controls that assert
// a check FAILS when it should.

import assert from 'node:assert/strict';
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, describe, it } from 'node:test';

import { DEPENDENCY_FIELDS, ORDER, PackageError, SCOPE, resolvePackages } from './packages.mjs';
import {
  defaultPackagesDir,
  main as stampMain,
  normalizeVersion,
  stampManifest,
  verifyManifest,
} from './set-package-versions.mjs';
import { authTokenLines, compareTriple, configuredRegistry, parseVersions } from './publish-packages.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const realPackages = defaultPackagesDir();
const scratch = mkdtempSync(path.join(tmpdir(), 'dc-packages-test-'));
after(() => rmSync(scratch, { recursive: true, force: true }));

function tmp(name) {
  const dir = path.join(scratch, name);
  mkdirSync(dir, { recursive: true });
  return dir;
}

// A throwaway packages/ tree carrying copies of the REAL manifests. Copies, not
// fixtures: a fixture written by hand would be a second statement of what the tree
// contains, and would keep passing after the tree changed underneath it.
function copyOfRealPackages(name) {
  const root = tmp(name);
  for (const pkgName of ORDER) {
    mkdirSync(path.join(root, pkgName), { recursive: true });
    cpSync(path.join(realPackages, pkgName, 'package.json'), path.join(root, pkgName, 'package.json'));
  }
  return root;
}

describe('resolvePackages', () => {
  it('returns the real packages in dependency order', () => {
    const found = resolvePackages(realPackages);
    assert.deepEqual(found.map((p) => p.name), ORDER);
    assert.equal(found[0].pkg.name, '@devicechain/brand');
  });

  // The reach control. Every caller loops over this result and reports success when the
  // loop finds nothing to complain about, so an empty result is not a trivial edge case
  // — it is the one input that makes every downstream assertion vacuous.
  it('refuses an empty directory rather than returning nothing', () => {
    assert.throws(() => resolvePackages(tmp('empty')), PackageError);
  });

  it('refuses a directory it cannot read', () => {
    assert.throws(() => resolvePackages(path.join(scratch, 'does-not-exist')), PackageError);
  });

  it('refuses a package that is not in the order, because it would never be published', () => {
    const root = copyOfRealPackages('unordered');
    mkdirSync(path.join(root, 'newcomer'));
    writeFileSync(path.join(root, 'newcomer', 'package.json'), '{"name":"@devicechain/newcomer"}');
    assert.throws(() => resolvePackages(root), /newcomer/);
  });

  it('refuses an ordered package that has been removed', () => {
    const root = copyOfRealPackages('missing');
    rmSync(path.join(root, 'dashboards'), { recursive: true });
    assert.throws(() => resolvePackages(root), /dashboards/);
  });
});

describe('normalizeVersion', () => {
  it('strips the leading v so the release tag can be handed over verbatim', () => {
    assert.equal(normalizeVersion('v0.14.0'), '0.14.0');
    assert.equal(normalizeVersion('0.14.0-rc.1'), '0.14.0-rc.1');
    assert.equal(normalizeVersion(' 0.14.0 '), '0.14.0');
  });

  for (const bad of ['', undefined, 'latest', '0.14', 'v0.14.0.1', 'release-0.14.0']) {
    it(`rejects ${JSON.stringify(bad)}`, () => {
      assert.throws(() => normalizeVersion(bad), PackageError);
    });
  }

  // 🔴 The placeholder is the value that means "no version was passed". Accepting it
  // would let a release stamp `0.0.0-dev` everywhere, pass every assertion in this
  // file, and publish a package that looks deliberately versioned.
  it('refuses to stamp the committed placeholder', () => {
    assert.throws(() => normalizeVersion('0.0.0-dev'), /placeholder/);
  });
});

describe('stampManifest', () => {
  it('rewrites the version and every internal range, in every dependency map', () => {
    const pkg = {
      name: '@devicechain/widgets',
      version: '0.0.0-dev',
      dependencies: { echarts: '^6.0.0' },
      peerDependencies: { '@devicechain/client': '0.0.0-dev', react: '^19.2.8' },
      devDependencies: { '@devicechain/dashboards': '0.0.0-dev', typescript: '~7.0.2' },
      optionalDependencies: { '@devicechain/brand': '0.0.0-dev' },
    };
    stampManifest(pkg, '0.14.0');

    assert.equal(pkg.version, '0.14.0');
    assert.equal(pkg.peerDependencies['@devicechain/client'], '0.14.0');
    assert.equal(pkg.devDependencies['@devicechain/dashboards'], '0.14.0');
    assert.equal(pkg.optionalDependencies['@devicechain/brand'], '0.14.0');
    // Third-party ranges are left exactly as written.
    assert.equal(pkg.dependencies.echarts, '^6.0.0');
    assert.equal(pkg.peerDependencies.react, '^19.2.8');
    assert.equal(pkg.devDependencies.typescript, '~7.0.2');
  });

  it('reports what it changed, so a run that matched nothing is visible', () => {
    const changes = stampManifest({ name: 'x', version: '0.0.0-dev' }, '0.14.0');
    assert.deepEqual(changes, ['version 0.0.0-dev -> 0.14.0']);
    assert.deepEqual(stampManifest({ name: 'x', version: '0.14.0' }, '0.14.0'), []);
  });

  // Every field this covers is one DEPENDENCY_FIELDS names; the point of asserting the
  // list is that adding a field to it without teaching the stamper about it, or the
  // reverse, is a silent hole.
  it('covers exactly the dependency maps the shared list names', () => {
    assert.deepEqual(DEPENDENCY_FIELDS, [
      'dependencies',
      'peerDependencies',
      'devDependencies',
      'optionalDependencies',
    ]);
  });
});

describe('verifyManifest', () => {
  it('passes a fully stamped manifest', () => {
    const pkg = { version: '0.14.0', peerDependencies: { '@devicechain/client': '0.14.0' } };
    assert.deepEqual(verifyManifest('widgets', pkg, '0.14.0'), []);
  });

  it('catches a version that did not move', () => {
    const problems = verifyManifest('widgets', { version: '0.0.0-dev' }, '0.14.0');
    assert.equal(problems.length, 2); // the version, and the stray placeholder
    assert.match(problems[0], /version is 0\.0\.0-dev/);
  });

  it('catches an internal range that did not move', () => {
    const pkg = { version: '0.14.0', peerDependencies: { '@devicechain/client': '0.13.0' } };
    const problems = verifyManifest('widgets', pkg, '0.14.0');
    assert.equal(problems.length, 1);
    assert.match(problems[0], /peerDependencies\.@devicechain\/client is 0\.13\.0/);
  });

  // 🔴 The catch-all, and the reason it is a raw string search rather than another walk
  // over DEPENDENCY_FIELDS: the failure it is for is a field this code does not know
  // about. `overrides` is exactly that shape — npm honours it, the stamper never looks
  // at it, and a walk over the known maps would report clean.
  it('catches a placeholder hiding in a field the stamper does not know about', () => {
    const pkg = { version: '0.14.0', overrides: { '@devicechain/client': '0.0.0-dev' } };
    const problems = verifyManifest('widgets', pkg, '0.14.0');
    assert.equal(problems.length, 1);
    assert.match(problems[0], /still appears somewhere in the manifest/);
  });
});

describe('the real manifests, stamped end to end', () => {
  const version = '9.9.9-test';

  it('are NOT already stamped — otherwise the assertions below prove nothing', () => {
    // The negative control. If the committed manifests happened to carry the target
    // version already, every check in the next test would pass without the stamper
    // running at all.
    const before = resolvePackages(realPackages).flatMap(({ name, pkg }) =>
      verifyManifest(name, pkg, version),
    );
    assert.ok(before.length > 0, 'the committed manifests already satisfy the post-stamp check');
    assert.ok(
      before.some((p) => /0\.0\.0-dev/.test(p)),
      'the committed manifests should still carry the 0.0.0-dev placeholder',
    );
  });

  it('come out with the version and every internal range moved, written to disk', () => {
    const root = copyOfRealPackages('stamped');
    stampMain([`v${version}`], root);

    let internalRanges = 0;
    for (const { name, dir } of resolvePackages(root)) {
      const raw = readFileSync(path.join(dir, 'package.json'), 'utf8');
      assert.deepEqual(verifyManifest(name, JSON.parse(raw), version), []);
      assert.ok(!raw.includes('0.0.0-dev'), `${name} still mentions the placeholder`);
      assert.ok(raw.endsWith('}\n'), `${name} should be written with a trailing newline`);
      for (const field of DEPENDENCY_FIELDS) {
        internalRanges += Object.keys(JSON.parse(raw)[field] ?? {}).filter((d) => d.startsWith(SCOPE)).length;
      }
    }

    // Positive, not "no problems found": the chain really does declare internal edges,
    // so a run that rewrote none of them is a failure rather than a clean sheet.
    assert.ok(internalRanges >= 3, `expected internal ${SCOPE}* ranges to exist, found ${internalRanges}`);
  });

  it('refuses to write when the version is not a version', () => {
    const root = copyOfRealPackages('rejected');
    assert.throws(() => stampMain(['not-a-version'], root), PackageError);
    // ...and left the manifests alone.
    assert.ok(readFileSync(path.join(root, 'client', 'package.json'), 'utf8').includes('0.0.0-dev'));
  });
});

describe('publisher helpers', () => {
  // npm prints a bare STRING for a package with exactly one published version, and an
  // array for every other count. One version is the state each package is in right
  // after the manual bootstrap publish, so this is the normal case on the first
  // automated release rather than an edge case.
  it('reads npm view versions --json in both of its shapes', () => {
    assert.deepEqual(parseVersions('"0.14.0-0"'), ['0.14.0-0']);
    assert.deepEqual(parseVersions('["0.14.0-0","0.14.0"]'), ['0.14.0-0', '0.14.0']);
  });

  // The exact two shapes `npm config ls -l` produces, measured: the line is present and
  // redacted when a token is configured anywhere, and absent entirely when it is not.
  // This is the check standing between a release and npm's own documented example,
  // which configures auth in a way that silently disables OIDC.
  it('spots an auth token in the resolved npm config', () => {
    const withToken = 'registry = "https://registry.npmjs.org/"\n//registry.npmjs.org/:_authToken = (protected)\n';
    const without = 'registry = "https://registry.npmjs.org/"\nprefix = "/usr/local"\n';
    assert.deepEqual(authTokenLines(withToken), ['//registry.npmjs.org/:_authToken = (protected)']);
    assert.deepEqual(authTokenLines(without), []);
    // A scoped registry's token counts too — it is the same suppression.
    assert.equal(authTokenLines('//npm.pkg.github.com/:_authToken=abc\n').length, 1);
  });

  it('reads the resolved registry back out of the same dump', () => {
    assert.equal(
      configuredRegistry('foo = 1\nregistry = "https://registry.npmjs.org/"\n'),
      'https://registry.npmjs.org/',
    );
    assert.equal(configuredRegistry('foo = 1\n'), null);
    // ...and is not fooled by a key that merely ends in "registry".
    assert.equal(configuredRegistry('@devicechain:registry = "https://example.test/"\n'), null);
  });

  it('compares npm versions against the trusted-publishing floor', () => {
    assert.ok(compareTriple('11.5.0', [11, 5, 1]) < 0);
    assert.ok(compareTriple('11.5.1', [11, 5, 1]) === 0);
    assert.ok(compareTriple('11.17.0', [11, 5, 1]) > 0);
    assert.ok(compareTriple('9.8.1', [11, 5, 1]) < 0);
    assert.ok(compareTriple('12.0.0-pre.1', [11, 5, 1]) > 0);
  });
});

describe('the scripts stay wired to the workflow that calls them', () => {
  // 🔴 These files are only ever run by release.yml, on a tag, and nothing else in CI
  // executes them. A rename or a moved directory therefore surfaces as a failed
  // RELEASE — after images and the chart have already been pushed. Cheap to pin here.
  it('release.yml calls the stamper and the publisher by the paths they live at', () => {
    // `includes` rather than `assert.match`, because a failed regex assertion prints the
    // ENTIRE 700-line workflow as the "actual" value and buries the one line that matters.
    const workflow = readFileSync(path.join(here, '..', '..', '.github', 'workflows', 'release.yml'), 'utf8');
    for (const script of ['scripts/set-package-versions.mjs', 'scripts/publish-packages.mjs']) {
      assert.ok(workflow.includes(script), `release.yml no longer calls ${script}`);
    }
  });
});
