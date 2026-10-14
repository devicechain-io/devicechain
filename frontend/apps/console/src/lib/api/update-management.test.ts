// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The documents file must cover EVERY operation the update-management schema serves,
// so the pages built later (artifacts, signers, a device's Updates tab) never have to
// touch generated output. Codegen alone cannot say an operation is missing — it only
// generates what is written — so this reads the served schema and asserts each Query
// and Mutation field is selected somewhere in the documents.
import { describe, it, expect } from 'vitest';
import SCHEMA from '../../../../../../backend/services/update-management/graphql/schema.graphql?raw';
import DOCUMENTS from './update-management.ts?raw';

/** The field names declared directly on a root type. */
function rootFields(type: 'Query' | 'Mutation'): string[] {
  const start = SCHEMA.indexOf(`type ${type} {`);
  if (start < 0) throw new Error(`no ${type} type in the schema`);
  const body = SCHEMA.slice(start).split(/\n}\n/)[0];
  // Strip descriptions and argument lists, leaving `name` at the start of a field.
  const noDocs = body.replace(/"""[\s\S]*?"""/g, '');
  const names: string[] = [];
  let depth = 0;
  for (const line of noDocs.split('\n').slice(1)) {
    const m = depth === 0 ? line.match(/^\s+([a-zA-Z]+)\s*[(:]/) : null;
    if (m) names.push(m[1]);
    depth += (line.match(/\(/g)?.length ?? 0) - (line.match(/\)/g)?.length ?? 0);
  }
  return names;
}

describe('update-management documents', () => {
  it('parses a plausible set of root fields (control for the checks below)', () => {
    expect(rootFields('Query')).toContain('artifacts');
    expect(rootFields('Mutation')).toContain('registerArtifactSigner');
    expect(rootFields('Query').length).toBeGreaterThanOrEqual(7);
    expect(rootFields('Mutation').length).toBeGreaterThanOrEqual(8);
  });

  it.each([...rootFields('Query'), ...rootFields('Mutation')])('selects %s', (field) => {
    expect(DOCUMENTS).toMatch(new RegExp(String.raw`\b${field}(\(|\s*\{|\s*\n)`));
  });

  it('selects the filters and limits the pages depend on', () => {
    expect(SCHEMA).toMatch(/input ArtifactSearchCriteria[\s\S]*?trustStatus: ArtifactTrustStatus/);
    expect(DOCUMENTS).toContain('maxArtifactBytes');
    expect(DOCUMENTS).toContain('allowUnverifiedArtifacts');
    expect(DOCUMENTS).toContain('signedBy');
    expect(DOCUMENTS).toContain('soleSignedArtifactCount');
  });
});
