// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Tests for the schema publishing pipeline: the sanitizer, the gate over its
// output, and the floors under discovery.
//
// Run with `npm run test:schema` (node --test). The docs build runs the generator,
// not this file — so a build alone proves the pipeline produced SOMETHING, and only
// these prove it produced the right thing and that the gates can still fail.

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { sanitizeSdl, sanitizeText, hasCitation } from './sanitize.mjs';
import { scan } from './gate.mjs';
import {
  REPO, GenerateError, discover, reconcile, resolve, buildArtifacts,
} from './generate-schema.mjs';
import { SCHEMAS, REQUIRED_OUTPUTS } from './schemas.manifest.mjs';

const clean = (s) => sanitizeText(s);

// ---------------------------------------------------------------------------
// The sanitizer, against the forms that are actually in the tree.
// ---------------------------------------------------------------------------

test('strips the citation and keeps the sentence, in every form the corpus uses', () => {
  const cases = [
    // trailing parenthetical — the naive strip leaves "… ()."
    ['A persisted, lifecycle-tracked command to a device (ADR-012).',
      'A persisted, lifecycle-tracked command to a device.'],
    // compound, slash-separated: nothing but citations inside, so the aside goes
    ['A DETECT rule declared on a device profile (ADR-051 / ADR-053 §5 / ADR-045). The rule',
      'A DETECT rule declared on a device profile. The rule'],
    // slash-compact numbering
    ['A uniform (type, token) reference to an entity (ADR-013/044), returned from a query.',
      'A uniform (type, token) reference to an entity, returned from a query.'],
    // attributive, mid-sentence
    ['The compiled predicate\'s static worst-case cost (the ADR-023 gate cleared) — null.',
      'The compiled predicate\'s static worst-case cost (the gate cleared) — null.'],
    // em-dash lead-in before terminal punctuation
    ['Admin (instance-scoped) GraphQL API — ADR-033.', 'Admin (instance-scoped) GraphQL API.'],
    // block opening on its own citation
    ['(ADR-033). Identities, memberships and tenants.', 'Identities, memberships and tenants.'],
    // prose inside the aside survives; only the citation leaves
    ['A globally-defined, named bundle of authorities (ADR-008 RBAC / ADR-033). Scope',
      'A globally-defined, named bundle of authorities (RBAC). Scope'],
    ['An immutable published snapshot of a definition (ADR-039 versioning).',
      'An immutable published snapshot of a definition (versioning).'],
    ['(a CEL selector over member-entity facet attributes — ADR-061 G3).',
      '(a CEL selector over member-entity facet attributes).'],
    // aside left dangling on a preposition goes whole
    ['AI Inference ADMIN GraphQL Schema (ADR-056 §4, re-homed by ADR-065)',
      'AI Inference ADMIN GraphQL Schema'],
    // a coordinate reached through "/" and through "," — both orphan without it
    ['classification facet (ADR-061 G2 / SD-5). It declares which keys are facets',
      'classification facet. It declares which keys are facets'],
    ['Streams a device profile\'s DETECT detections as they fire (ADR-037 / ADR-057, slice 7c):',
      'Streams a device profile\'s DETECT detections as they fire:'],
    // a coordinate opening an aside, with no number anywhere near it
    ['The outcome of compiling one automation canvas (slice 9a).',
      'The outcome of compiling one automation canvas.'],
    ['One canvas node\'s disposition for a single firing (slice 9e node trace).',
      'One canvas node\'s disposition for a single firing (node trace).'],
    ['reserved for when state-budget enforcement lands (slice 6c-2 — unbuilt today)',
      'reserved for when state-budget enforcement lands (unbuilt today)'],
    ['re-marshals to identical canonical bytes (§3.2); the two surfaces',
      're-marshals to identical canonical bytes; the two surfaces'],
  ];
  for (const [input, expected] of cases) assert.equal(clean(input), expected, input);
});

test('a digit-bearing aside survives the empty-parenthetical rule', () => {
  // "(1–100)" has no letters. A rule that removed letter-free asides would eat it.
  assert.equal(
    clean('Per-tenant ADR-063 shed-priority override (1–100), a contention preference.'),
    'Per-tenant shed-priority override (1–100), a contention preference.',
  );
});

test('the article agrees with whatever the strip left behind', () => {
  assert.equal(clean('not an ADR-042 token'), 'not a token');
  assert.equal(clean('a ADR-042 identity claim'), 'an identity claim');
  assert.equal(clean('An ADR-042 token'), 'A token');
});

test('a citation broken across two comment lines is repaired, not mangled', () => {
  // Both halves are on different lines; a line-oriented pass leaves "ADR-053 §5 /"
  // stranded on one and "ADR-045);" on the other.
  const input = 'The rule-authoring home is device-management (rules are PROFILE-homed, ADR-053 §5 /\n'
    + 'ADR-045); event-processing owns the DETECT schema.';
  assert.equal(
    clean(input),
    'The rule-authoring home is device-management (rules are PROFILE-homed);'
    + ' event-processing owns the DETECT schema.',
  );

  // Through the document path the strip takes the line break with it, so the block
  // is re-flowed back to the width it had rather than left as one long line.
  const doc = input.split('\n').map((l) => `# ${l}`).join('\n');
  for (const line of sanitizeSdl(doc).text.split('\n')) assert.ok(line.length <= 85, line);
});

test('"!" is not treated as sentence-ending punctuation', () => {
  // The corpus has "— null when\n# !ok. It decodes …". Pulling "!" up to the previous
  // line to tidy a stranded full stop would produce "null when!ok".
  const input = 'The compiled JSON — null when (ADR-023)\n!ok. It decodes to the same rule.';
  assert.match(clean(input), /null when\n!ok\./);
});

test('a line that was only a citation is dropped, not left as a bare hash', () => {
  const sdl = [
    '# together they filter the (anchorType, anchorToken) columns on event_anchors',
    '# (ADR-044).',
    'type Foo {',
  ].join('\n');
  const out = sanitizeSdl(sdl).text.split('\n');
  assert.equal(out.filter((l) => l.trim() === '#').length, 0, 'a bare "#" was left behind');
  assert.equal(out.at(-1), 'type Foo {');
  assert.match(out.slice(0, -1).join(' ').replace(/# /g, ''), /columns on event_anchors\.$/);
});

test('a block is rewrapped only when a strip made it wider than it started', () => {
  const narrow = ['# One tier offer: on the menu of every tenant at this tier', '# (ADR-065 decision 10).'];
  const out = sanitizeSdl(narrow.join('\n')).text.split('\n');
  assert.ok(out.every((l) => l.length <= 58), out.join('\n'));
});

// ---------------------------------------------------------------------------
// What the sanitizer must NOT touch. The collateral-damage half.
// ---------------------------------------------------------------------------

test('a document with no citation comes back byte-identical', () => {
  const sdl = [
    '# Copyright The DeviceChain Authors',
    '# SPDX-License-Identifier: Apache-2.0',
    '',
    '# Creates count devices numbered (startIndex .. startIndex+count-1) from the',
    '# templates. Geometry is {"coordinates":[[[lon,lat],...]]}.',
    '# Times are RFC3339; coordinates are WGS84 / EPSG:4326.',
    '# The operator has to make a decision here; read-only in this slice.',
    'type Query {',
    '    devices(criteria: DeviceSearchCriteria!): DeviceSearchResults!',
    '}',
  ].join('\n');
  assert.equal(sanitizeSdl(sdl).text, sdl);
});

test('non-comment lines are never rewritten', () => {
  const sdl = ['# A device (ADR-012).', 'type Device { adr: String! }'].join('\n');
  assert.equal(sanitizeSdl(sdl).text.split('\n')[1], 'type Device { adr: String! }');
});

test('"a slice of" is prose, not a roadmap coordinate', () => {
  assert.equal(hasCitation('applies to a slice of the fleet'), false);
  assert.equal(clean('applies to a slice of the fleet (ADR-061).'), 'applies to a slice of the fleet.');
});

// ---------------------------------------------------------------------------
// The gate. Every rule shown to fire, and shown not to fire on lookalikes.
// ---------------------------------------------------------------------------

test('the gate reports every form of dead citation it claims to', () => {
  const planted = [
    '# See ADR-012 for the rationale.',
    '# See the ADR for the rationale.',
    '# The canonical form is defined in §3.2.',
    '# Landed in slice 9e.',
    '# Landed in slice C4b.',
    '# Landed in slice b.',
    '# Refined by SD-3.',
    '# See decision 7 for the cascade.',
    '# Recorded under .agent-os/product/decisions.md.',
  ];
  for (const line of planted) {
    const found = scan('planted.graphql', line, new Set([1]));
    assert.ok(found.length > 0, `gate did not fire on: ${line}`);
  }
});

test('the gate reports the wreckage a bad strip leaves behind', () => {
  const planted = [
    '# A uniform reference to an entity ().',
    '# A bundle of authorities (, RBAC).',
    '# A bundle of authorities (RBAC /).',
    '# A persisted command to a device .',
    '# The instance admin API —.',
    '# An operator-defined tier,.',
  ];
  for (const line of planted) {
    const found = scan('planted.graphql', line, new Set([1]));
    assert.ok(found.length > 0, `gate did not fire on: ${line}`);
  }
});

test('the gate leaves public standards and ordinary English alone', () => {
  const fine = [
    '# Times are RFC3339 strings.',
    '# Coordinates are WGS84 / EPSG:4326 decimal degrees.',
    '# The operator has to make a decision — displayOrder is not a rank.',
    '# Read-only in this slice.',
    '# A device credential rotates on demand.',
  ];
  for (const line of fine) {
    assert.deepEqual(scan('fine.graphql', line, new Set([1])), [], line);
  }
});

test('the mangle rules do not fire on punctuation the sanitizer never wrote', () => {
  // Both of these are correct as authored and both trip a mangle rule. Scoping the
  // rules to rewritten lines is the only thing keeping them quiet.
  const untouched = [
    '# Geometry is {"coordinates":[[[lon,lat],...]]}.',
    '# Creates devices numbered (startIndex .. startIndex+count-1).',
  ];
  for (const line of untouched) {
    assert.deepEqual(scan('untouched.graphql', line), [], line);
    assert.ok(scan('touched.graphql', line, new Set([1])).length > 0,
      'the rule this is guarding against should still exist');
  }
});

test('NEGATIVE CONTROL: the gate is not vacuous against the real corpus', () => {
  // A gate over generated output goes quiet when the generator changes shape, and a
  // clean run then proves nothing. Run the same gate over the UNSANITIZED sources:
  // it has to light up, or every green run above is meaningless.
  let raw = 0;
  for (const s of SCHEMAS) {
    const text = readFileSync(join(REPO, s.source), 'utf8');
    const all = new Set(text.split('\n').map((_l, i) => i + 1));
    raw += scan(s.source, text, all).length;
  }
  assert.ok(raw > 100, `gate found only ${raw} problems in the raw corpus; expected the ~163 citations`);
});

// ---------------------------------------------------------------------------
// The floors under discovery. Each shown to fire.
// ---------------------------------------------------------------------------

test('the real tree reconciles against the manifest, both directions', () => {
  reconcile(discover());
});

test('a schema in the tree with no manifest entry fails the build', () => {
  assert.throws(
    () => reconcile([...discover(), 'backend/services/brand-new/graphql/schema.graphql']),
    (e) => e instanceof GenerateError && /no manifest entry/.test(e.headline),
  );
});

test('a renamed directory fails the build — the direction a glob cannot see', () => {
  // Rename backend/services/device-state/graphql/ and the glob simply returns one
  // fewer file. Nothing in the forward direction notices. This is what does.
  const renamed = discover().filter((s) => !s.startsWith('backend/services/device-state/'));
  assert.throws(
    () => reconcile(renamed),
    (e) => e instanceof GenerateError && /does not exist/.test(e.headline),
  );
});

test('an unrecognized schema filename fails rather than guessing an auth plane', () => {
  assert.throws(
    () => resolve({ source: 'backend/services/x/graphql/internal_schema.graphql', area: 'x' }),
    (e) => e instanceof GenerateError && /unrecognized schema filename/.test(e.headline),
  );
});

// ---------------------------------------------------------------------------
// The published set.
// ---------------------------------------------------------------------------

test('all three .gql-sourced schemas are published, asserted by name', () => {
  const names = new Set(buildArtifacts().map((a) => a.name));
  // Not a count: a *.graphql glob drops all three, and 11 files looks as complete
  // as 14 does. The one it drops silently is login.
  for (const required of REQUIRED_OUTPUTS) assert.ok(names.has(required), `missing ${required}`);
});

test('the required-output check can actually fail', () => {
  // Not by removing a schema — the manifest floor fires on that first, in both
  // directions, so that scenario says nothing about whether THIS check works.
  //
  // The reachable failure is a change to the naming scheme. Relabel the area and
  // every source still reconciles perfectly, while the published URL moves out from
  // under every link to it. That is the one the floor is blind to.
  const relabelled = SCHEMAS.map((s) => (
    s.area === 'user-management' ? { ...s, area: 'users' } : s));

  reconcile(discover(), relabelled); // the floor is satisfied: no source path moved
  assert.throws(
    () => buildArtifacts(REPO, relabelled),
    (e) => e instanceof GenerateError && /required output/.test(e.headline),
    'the required-output check cannot fire, which makes it decoration',
  );
});

test('the placeholder area is explained in the index rather than served as a schema', () => {
  const artifacts = buildArtifacts();
  assert.equal(artifacts.find((a) => a.name === 'event-sources.graphql'), undefined);

  const index = JSON.parse(artifacts.find((a) => a.name === 'index.json').text);
  const row = index.areas.find((a) => a.area === 'event-sources');
  assert.equal(row.api, false);
  assert.match(row.note, /no GraphQL API of its own/);
});

test('the index gives every published schema a plane, a token and an endpoint', () => {
  const artifacts = buildArtifacts();
  const index = JSON.parse(artifacts.find((a) => a.name === 'index.json').text);

  for (const area of index.areas) {
    if (area.api === false) continue;
    assert.ok(index.planes[area.plane], `${area.area}: unknown plane ${area.plane}`);
    assert.ok(area.token, `${area.area}: no token named`);
    assert.match(area.endpoint, /^\/api\/[a-z-]+\/(graphql|admin\/graphql|settings\/graphql)$/);
    assert.ok(artifacts.some((a) => `/schema/${a.name}` === area.schema), `${area.area}: ${area.schema} not published`);
  }
  // Every published artifact is reachable from the index — no orphan files.
  const listed = new Set(index.areas.filter((a) => a.schema).map((a) => a.schema));
  for (const a of artifacts) {
    if (a.name === 'index.json') continue;
    assert.ok(listed.has(`/schema/${a.name}`), `${a.name} is served but absent from the index`);
  }
});

test('the two admin planes and the settings plane keep their mounts', () => {
  const index = JSON.parse(buildArtifacts().find((a) => a.name === 'index.json').text);
  const at = (schema) => index.areas.find((a) => a.schema === schema);

  assert.equal(at('/schema/user-management-admin.graphql').endpoint, '/api/user-management/admin/graphql');
  assert.equal(at('/schema/user-management-settings.graphql').endpoint, '/api/user-management/settings/graphql');
  assert.equal(at('/schema/ai-inference-admin.graphql').endpoint, '/api/ai-inference/admin/graphql');
  assert.equal(at('/schema/user-management-admin.graphql').plane, 'identity');
  assert.equal(at('/schema/device-management.graphql').plane, 'tenant');
  assert.equal(at('/schema/ai-inference.graphql').plane, 'service');
});

test('the published SDL still parses as the schema it came from', () => {
  // The sanitizer only edits comments, so every type, field and directive must
  // survive byte-for-byte. Comparing the non-comment lines is a cheap way to say so
  // without pulling a GraphQL parser into the docs build.
  const strip = (t) => t.split('\n').filter((l) => !/^[ \t]*#/.test(l)).join('\n');
  for (const artifact of buildArtifacts()) {
    if (artifact.name === 'index.json') continue;
    const entry = SCHEMAS.find((s) => s.source.endsWith(`/${artifact.name.replace(/\.graphql$/, '')}`)
      || artifact.name === `${s.area}.graphql` && /\/schema\.(graphql|gql)$/.test(s.source)
      || artifact.name === `${s.area}-admin.graphql` && /admin_schema\./.test(s.source)
      || artifact.name === `${s.area}-settings.graphql` && /settings_schema\./.test(s.source));
    assert.ok(entry, `no source for ${artifact.name}`);
    assert.equal(strip(artifact.text), strip(readFileSync(join(REPO, entry.source), 'utf8')), artifact.name);
  }
});
