// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Publishes the GraphQL SDL to docs/static/schema/, where the docs build copies it
// verbatim into docs/build/ and Netlify serves it at /schema/.
//
// The point is machine consumption. An agent asked to integrate against
// DeviceChain today spends most of its effort reading Go, because the docs give
// the shape of an answer and only the source gives the field names — and the one
// documented alternative, introspection, is disabled by default. This puts the
// authoritative identifiers on a URL, with no instance and no token.
//
// Generated at build time and never committed, so the published copy cannot drift
// from the source. Run with --check to validate without writing, which is how CI
// catches a tree change in a pull request that triggers no docs build.

import { readFileSync, writeFileSync, mkdirSync, rmSync, existsSync, readdirSync } from 'node:fs';
import { dirname, join, basename, relative } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { sanitizeSdl } from './sanitize.mjs';
import { scan, formatFindings } from './gate.mjs';
import { PLANES, FILENAME_CONVENTIONS, SCHEMAS, REQUIRED_OUTPUTS } from './schemas.manifest.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
export const REPO = join(HERE, '..', '..');
export const OUT = join(HERE, '..', 'static', 'schema');

/** Thrown rather than exited, so the floors below can be exercised by a test. */
export class GenerateError extends Error {
  constructor(headline, detail) {
    super(detail ? `${headline}\n\n${detail}` : headline);
    this.headline = headline;
    this.detail = detail;
  }
}

/** Every SDL file present in the tree, found without consulting the manifest. */
export function discover(repo = REPO) {
  const services = join(repo, 'backend', 'services');
  if (!existsSync(services)) {
    // The docs site builds from the monorepo with docs/ as its base directory, so
    // the backend tree is expected to be present. If it ever is not, this has to be
    // a loud failure: the alternative is a green build that publishes nothing.
    throw new GenerateError(
      `cannot see ${relative(repo, services)} from the docs build`,
      'The generator reads the schemas out of the backend tree at build time.\n'
      + 'A build that cannot reach it must not publish a partial or empty schema set.',
    );
  }
  const found = [];
  for (const area of readdirSync(services, { withFileTypes: true })) {
    if (!area.isDirectory()) continue;
    const dir = join(services, area.name, 'graphql');
    if (!existsSync(dir)) continue;
    for (const f of readdirSync(dir)) {
      // Both extensions. user-management uses .gql for all three of its schemas, so
      // a *.graphql glob drops login/auth, the admin API and settings.
      if (/\.(graphql|gql)$/.test(f)) found.push(`backend/services/${area.name}/graphql/${f}`);
    }
  }
  return found.sort();
}

/** The bidirectional floor: discovery and the manifest must agree, both ways. */
export function reconcile(discovered, schemas = SCHEMAS) {
  const declared = new Set(schemas.map((s) => s.source));
  const present = new Set(discovered);

  const undeclared = discovered.filter((s) => !declared.has(s));
  if (undeclared.length) {
    throw new GenerateError(
      `${undeclared.length} schema file(s) in the tree with no manifest entry`,
      `${undeclared.map((s) => `  ${s}`).join('\n')}\n\n`
      + 'Add each to SCHEMAS in scripts/schemas.manifest.mjs. A new schema is not\n'
      + 'published until it is declared, so that it gets an auth plane rather than being\n'
      + 'served as an undifferentiated file.',
    );
  }

  const missing = schemas.map((s) => s.source).filter((s) => !present.has(s));
  if (missing.length) {
    throw new GenerateError(
      `${missing.length} manifest entr(ies) point at a file that does not exist`,
      `${missing.map((s) => `  ${s}`).join('\n')}\n\n`
      + 'A schema was moved, renamed or deleted. Update SCHEMAS in\n'
      + 'scripts/schemas.manifest.mjs. This is the direction that fires on a rename —\n'
      + 'without it a renamed directory would drop out of discovery silently, and a glob\n'
      + 'checked against its own directory listing cannot see that at all.',
    );
  }
}

/** Resolve one manifest entry into everything the index needs to say about it. */
export function resolve(entry) {
  const file = basename(entry.source);
  const convention = FILENAME_CONVENTIONS.find((c) => c.match.test(file));
  if (!convention) {
    throw new GenerateError(
      `unrecognized schema filename: ${entry.source}`,
      'The auth plane and the published name are both derived from the filename.\n'
      + 'Either rename the file to one of schema.*, admin_schema.*, settings_schema.*, or\n'
      + 'add the new convention to FILENAME_CONVENTIONS in scripts/schemas.manifest.mjs.\n'
      + 'Failing here is deliberate: guessing the plane is how an admin mutation ends up\n'
      + 'documented as though a tenant developer could call it.',
    );
  }
  const plane = entry.plane ?? convention.plane;
  if (!PLANES[plane]) throw new GenerateError(`${entry.source}: unknown auth plane "${plane}"`);

  return {
    ...entry,
    published: `${entry.area}${convention.suffix}.graphql`,
    endpoint: `/api/${entry.area}${convention.mount}`,
    plane,
  };
}

/** Everything the run would publish, gated. Throws rather than writing anything. */
export function buildArtifacts(repo = REPO, schemas = SCHEMAS) {
  reconcile(discover(repo), schemas);
  const resolved = schemas.map(resolve);

  const artifacts = [];
  for (const s of resolved) {
    if (s.publish === false) continue;
    const { text, touched } = sanitizeSdl(readFileSync(join(repo, s.source), 'utf8'));
    artifacts.push({ name: s.published, text, touched });
  }

  const index = {
    description:
      'The GraphQL schemas DeviceChain serves, one file per functional area and auth '
      + 'plane. These are generated from the schemas the services parse at startup, so '
      + 'they cannot drift from the running API. Introspection is disabled by default on '
      + 'a DeviceChain instance, which makes these files the way to read the API without '
      + 'deploying one.',
    endpointPattern:
      'https://<your-host>{endpoint} — the ingress routes /api/<area> to that area\'s '
      + 'service and strips the prefix, so the request arrives at the service\'s own mount.',
    planes: PLANES,
    areas: resolved.map((s) => {
      if (s.publish === false) return { area: s.area, api: false, note: s.note };
      const row = {
        area: s.area,
        plane: s.plane,
        token: PLANES[s.plane].token,
        endpoint: s.endpoint,
        schema: `/schema/${s.published}`,
      };
      if (s.note) row.note = s.note;
      return row;
    }),
  };
  artifacts.push({ name: 'index.json', text: `${JSON.stringify(index, null, 2)}\n` });

  // Assert the .gql-sourced schemas by name. A count would look just as complete.
  const names = new Set(artifacts.map((a) => a.name));
  const absent = REQUIRED_OUTPUTS.filter((n) => !names.has(n));
  if (absent.length) {
    throw new GenerateError(
      `${absent.length} required output(s) missing`,
      `${absent.map((n) => `  ${n}`).join('\n')}\n\n`
      + 'These are the three .gql-suffixed schemas. Their absence is exactly what a\n'
      + '*.graphql glob produces, and it is invisible in a file count.',
    );
  }

  // The gate, over what is about to be published rather than over its sources.
  const findings = artifacts.flatMap((a) => scan(a.name, a.text, a.touched));
  if (findings.length) {
    throw new GenerateError(
      `${findings.length} unpublishable reference(s) in generated output`,
      `${formatFindings(findings)}\n\n`
      + 'A citation into the private planning repository is a dead pointer for every\n'
      + 'reader of docs.devicechain.io. Either extend the sanitizer in scripts/sanitize.mjs\n'
      + 'so it consumes this form, or reword the comment in the schema source.',
    );
  }

  return artifacts;
}

function main() {
  const check = process.argv.includes('--check');
  let artifacts;
  try {
    artifacts = buildArtifacts();
  } catch (err) {
    if (!(err instanceof GenerateError)) throw err;
    console.error(`\ngenerate-schema: ${err.message}\n`);
    process.exit(1);
  }

  if (check) {
    console.log(`generate-schema: ${artifacts.length} artifact(s) validated (--check, nothing written)`);
    return;
  }

  // Own the whole directory: a schema deleted upstream must disappear here too, and
  // `docusaurus clear` does not touch static/.
  rmSync(OUT, { recursive: true, force: true });
  mkdirSync(OUT, { recursive: true });
  for (const a of artifacts) writeFileSync(join(OUT, a.name), a.text);

  console.log(`generate-schema: wrote ${artifacts.length} artifact(s) to static/schema/`);
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) main();
