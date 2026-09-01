// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Publish the frontend packages to npmjs.com, leaf-first, and then assert the registry
// actually has them.
//
//   node scripts/publish-packages.mjs --version 0.14.0 --tag latest
//   node scripts/publish-packages.mjs --version 0.14.0-0 --tag next --dry-run
//
// Called by the `npm-packages` job in .github/workflows/release.yml, after
// scripts/set-package-versions.mjs has stamped the tag into the manifests. It is a
// script rather than inline YAML because the ordering below is not incidental and
// because none of this is testable inside a workflow file.
//
// 🔴 LEAF-FIRST IS NOT COSMETIC. The order comes from scripts/packages.mjs, the same
// list the build uses. Internal deps are EXACT-pinned peers, so a `widgets` that
// reaches the registry before the `dashboards` it pins is uninstallable — and an npm
// version is IMMUTABLE, so it stays uninstallable at that number forever. Publishing
// leaf-first means a mid-run failure leaves a prefix of the chain, every member of
// which is installable on its own.
//
// 🔴 AUTH IS OIDC, NOT A TOKEN. npm's trusted publishing works by the CLI noticing it
// is inside a GitHub Actions job with `id-token: write` and minting its own
// credential; provenance is then automatic and the `--provenance` flag is NOT needed.
// The consequence for this file is that everything which can go wrong with that setup
// arrives as a bare 404 or 403 from `npm publish`, so the two failures that will
// actually happen are detected and named here instead — see assertOidcReady() and the
// FIRST-PUBLISH message below.

import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { PackageError, resolvePackages } from './packages.mjs';
import { normalizeVersion, verifyManifest } from './set-package-versions.mjs';

// npm's own floor for trusted publishing (npm CLI >= 11.5.1, Node >= 22.14.0). Asserted
// rather than assumed: the runner's bundled npm is whatever setup-node's Node ships,
// and an older one does not fail with "OIDC unsupported" — it falls through to looking
// for a token it will not find, and reports a permissions error instead.
const MIN_NPM = [11, 5, 1];

const DIST_TAG = /^[a-z][a-z0-9-]*$/;

function fail(message) {
  throw new PackageError(message);
}

function run(command, args, options = {}) {
  return spawnSync(command, args, { encoding: 'utf8', ...options });
}

function parseArgs(argv) {
  const args = { dryRun: false };
  for (let i = 0; i < argv.length; i += 1) {
    if (argv[i] === '--version') args.version = argv[(i += 1)];
    else if (argv[i] === '--tag') args.tag = argv[(i += 1)];
    else if (argv[i] === '--dry-run') args.dryRun = true;
    else fail(`unrecognised argument '${argv[i]}'. Usage: --version <v> --tag <dist-tag> [--dry-run]`);
  }
  args.version = normalizeVersion(args.version);
  if (!args.tag || !DIST_TAG.test(args.tag)) {
    fail(`--tag must be a dist-tag like 'latest' or 'next' (got '${args.tag ?? ''}')`);
  }
  return args;
}

// -1 / 0 / +1, comparing only the numeric release triple. A prerelease npm (`12.0.0-pre`)
// compares as its release, which is the right call here: the floor is about a feature
// being present, not about ordering two prereleases.
export function compareTriple(raw, want) {
  const parts = raw.split('.').map((n) => Number.parseInt(n, 10) || 0);
  for (let i = 0; i < want.length; i += 1) {
    const have = parts[i] ?? 0;
    if (have !== want[i]) return have < want[i] ? -1 : 1;
  }
  return 0;
}

function assertNpmVersion() {
  const result = run('npm', ['--version']);
  if (result.status !== 0) fail('`npm --version` failed — there is no npm on PATH');
  const raw = result.stdout.trim();
  if (compareTriple(raw, MIN_NPM) < 0) {
    fail(`npm ${raw} is older than ${MIN_NPM.join('.')}, which is the floor for trusted publishing (OIDC)`);
  }
  return raw;
}

// ---------------------------------------------------------------------------
// 🔴 The two ways trusted publishing is silently not in effect.
//
// Both were measured, and the first one is npm's OWN DOCUMENTED EXAMPLE:
//
//  1. An `_authToken` line in the resolved npm config. `actions/setup-node` writes
//     `//registry.npmjs.org/:_authToken=${NODE_AUTH_TOKEN}` whenever `registry-url:`
//     is set, which npm's trusted-publishing example tells you to set. With no
//     NODE_AUTH_TOKEN — the intended state — it expands to empty, npm reads the line
//     as "auth is configured", never starts the OIDC exchange, and reports ENEEDAUTH
//     or E404 (actions/setup-node#1551, npm/documentation#1960). release.yml therefore
//     omits the input; this is what proves it stayed omitted.
//  2. A dropped `id-token: write` permission. GitHub then simply does not set
//     ACTIONS_ID_TOKEN_REQUEST_URL, and the symptom is the same anonymous 404.
//
// `npm config ls -l` is the instrument rather than a scan of .npmrc paths, because it
// is npm's own resolution: it reports `//registry.npmjs.org/:_authToken = (protected)`
// no matter which of the four config files the line came from, and prints nothing at
// all when it is unset. Measured both ways.
// ---------------------------------------------------------------------------
export function authTokenLines(configDump) {
  return configDump
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => /:_authToken\s*=/.test(line));
}

export function configuredRegistry(configDump) {
  const line = configDump.split('\n').find((l) => /^registry\s*=/.test(l.trim()));
  return line ? line.split('=').slice(1).join('=').trim().replace(/^"|"$/g, '') : null;
}

function assertOidcReady() {
  // Only meaningful inside Actions. A maintainer publishing by hand — which is how the
  // FIRST version of every package has to go out — is authenticated with a real token
  // and an interactive 2FA prompt, and both checks below would be wrong there.
  if (process.env.GITHUB_ACTIONS !== 'true') return;

  if (!process.env.ACTIONS_ID_TOKEN_REQUEST_URL) {
    fail(
      'no OIDC token endpoint in the environment, so trusted publishing cannot work.\n' +
        "  The job is missing `permissions: id-token: write`. Without this check the symptom would be\n" +
        '  an anonymous 404 from npm publish, which says nothing about the permission.',
    );
  }

  const dump = run('npm', ['config', 'ls', '-l']).stdout ?? '';
  const registry = configuredRegistry(dump);
  if (registry !== 'https://registry.npmjs.org/') {
    fail(`the configured registry is '${registry}', but trusted publishing only works against https://registry.npmjs.org/`);
  }
  const tokens = authTokenLines(dump);
  if (tokens.length > 0) {
    fail(
      `the npm config carries an auth token, which SUPPRESSES the OIDC exchange:\n` +
        `${tokens.map((t) => `    ${t}`).join('\n')}\n` +
        '  This is almost always `registry-url:` on actions/setup-node writing an empty\n' +
        '  //registry.npmjs.org/:_authToken=${NODE_AUTH_TOKEN} line. Remove the input.',
    );
  }
}

// ---------------------------------------------------------------------------
// What the registry already holds.
//
// Measured, not assumed, because the three cases have to be told apart and two of them
// are both "E404":
//   - package absent entirely -> `npm view <name> versions --json` exits 1
//   - package present         -> exits 0 with a JSON array... EXCEPT with exactly one
//                                version published, where npm prints a bare STRING.
//                                That is precisely the state every package is in right
//                                after the manual bootstrap publish, so the string case
//                                is the normal one on the first automated release.
// ---------------------------------------------------------------------------
export function parseVersions(stdout) {
  const parsed = JSON.parse(stdout);
  return Array.isArray(parsed) ? parsed : [parsed];
}

function publishedVersions(name) {
  const result = run('npm', ['view', name, 'versions', '--json']);
  if (result.status !== 0) return null; // package does not exist at all
  return parseVersions(result.stdout);
}

function distTags(name) {
  const result = run('npm', ['view', name, 'dist-tags', '--json']);
  if (result.status !== 0) return null;
  return JSON.parse(result.stdout);
}

function sleep(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------
function main(argv) {
  const { version, tag, dryRun } = parseArgs(argv);
  const here = path.dirname(fileURLToPath(import.meta.url));
  const packages = resolvePackages(path.join(path.dirname(here), 'packages'));

  console.log(`npm ${assertNpmVersion()}`);
  assertOidcReady();
  console.log(`Publishing ${packages.length} packages at ${version} under dist-tag '${tag}'.\n`);

  // 🔴 EVERY manifest is checked BEFORE the first publish. Checking each one just
  // before its own publish would be too late: by the time `widgets` is found to carry
  // a wrong version, `client` and `dashboards` are already on the registry at numbers
  // that can never be reused.
  const problems = packages.flatMap(({ name, pkg }) => verifyManifest(name, pkg, version));
  if (problems.length > 0) {
    fail(
      `the manifests do not carry ${version}, so nothing was published:\n` +
        `${problems.map((p) => `  ${p}`).join('\n')}\n` +
        '  Run scripts/set-package-versions.mjs first.',
    );
  }

  const plan = [];
  for (const { dir, pkg } of packages) {
    const existing = publishedVersions(pkg.name);
    if (existing === null) {
      // 🔴 THE FIRST PUBLISH OF A PACKAGE CANNOT HAPPEN HERE, and this is the message
      // that says so rather than letting npm report a bare 403. A trusted publisher can
      // only be configured on a package that already EXISTS, and npm's staged-publish
      // flow explicitly cannot stage a brand-new package. So version one of each
      // package is a manual, interactive, 2FA'd `npm publish` from a maintainer's
      // machine; this job takes over from version two.
      fail(
        `${pkg.name} does not exist on the registry.\n` +
          '  A trusted publisher (OIDC) can only be configured on a package that already exists, and\n' +
          '  npm cannot stage a brand-new package either — so the FIRST version of each package must be\n' +
          '  published by hand by a maintainer, who then configures the trusted publisher on npmjs.com.\n' +
          '  This job publishes every version after that one.',
      );
    }
    if (existing.includes(version)) {
      // Not an error. A release job re-run after a later step failed would otherwise
      // die here on the packages that already went out, and there is no way to
      // un-publish and retry — npm's 72-hour window burns the number permanently.
      // The final assertion below still has to pass for these.
      console.log(`  ${pkg.name}@${version} is already on the registry — skipping the publish`);
      continue;
    }
    plan.push({ dir, name: pkg.name });
  }

  for (const [index, { dir, name }] of plan.entries()) {
    console.log(`\n==> npm publish ${name}@${version} --tag ${tag}`);
    if (dryRun) {
      console.log('    (--dry-run: not published)');
      continue;
    }
    const result = spawnSync('npm', ['publish', '--tag', tag], { stdio: 'inherit', cwd: dir });
    if (result.status !== 0) {
      fail(
        `publishing ${name} failed (exit ${result.status}).\n` +
          `  ${index} of this run's ${plan.length} packages were already published and cannot be withdrawn:\n` +
          `    ${plan.slice(0, index).map((p) => p.name).join(', ') || '(none)'}\n` +
          '  Fix the cause and re-run this job: packages already at this version are skipped, not retried.',
      );
    }
  }

  if (dryRun) {
    console.log('\n--dry-run: skipping the registry assertions, since nothing was published.');
    return;
  }

  // ---------------------------------------------------------------------------
  // 🔴 ASSERT THE REGISTRY, not the exit status of `npm publish`. A publish that
  // "succeeded" and a publish that never ran look identical from here otherwise —
  // and the dist-tag half is the one that actually decides what `npm install <pkg>`
  // resolves to, which no publish exit code reports on.
  //
  // Retried, because the registry's read path is a CDN and a just-published version
  // can take a few seconds to appear on it. A bounded retry that ends in a hard
  // failure is the point; an unbounded one, or none at all, would turn propagation
  // lag into either a hang or a false red.
  // ---------------------------------------------------------------------------
  const failures = [];
  for (const { pkg } of packages) {
    let lastSeen = '<absent>';
    let ok = false;
    for (let attempt = 1; attempt <= 12; attempt += 1) {
      const versions = publishedVersions(pkg.name) ?? [];
      const tags = distTags(pkg.name) ?? {};
      lastSeen = `versions=${versions.includes(version)} dist-tags=${JSON.stringify(tags)}`;
      if (versions.includes(version) && tags[tag] === version) {
        console.log(`  ${pkg.name}@${version} is on the registry, ${tag} -> ${version}`);
        ok = true;
        break;
      }
      if (attempt < 12) sleep(5000);
    }
    if (!ok) failures.push(`${pkg.name}: expected ${version} present and ${tag} -> ${version}; saw ${lastSeen}`);
  }
  if (failures.length > 0) {
    fail(`the registry does not agree that the publish happened:\n${failures.map((f) => `  ${f}`).join('\n')}`);
  }

  console.log(`\nAll ${packages.length} packages are published at ${version} under '${tag}'.`);
}

if (fileURLToPath(import.meta.url) === path.resolve(process.argv[1] ?? '')) {
  try {
    main(process.argv.slice(2));
  } catch (err) {
    if (!(err instanceof PackageError)) throw err;
    console.error(`\n==> publish-packages: ${err.message}\n`);
    process.exit(1);
  }
}
