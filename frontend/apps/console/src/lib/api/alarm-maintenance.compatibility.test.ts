// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { buildASTSchema, parse, visit, validate, Kind } from 'graphql';
import { describe, expect, it } from 'vitest';
import * as generated from '@/gql/device-management/graphql';
import * as optionalDocuments from '@/gql/alarm-maintenance/graphql';

const sdl = readFileSync(resolve(process.cwd(), '../../../backend/services/device-management/graphql/schema.graphql'), 'utf8');
const oldSDL = visit(parse(sdl), {
  enter(node) {
    if ('name' in node && node.name && /^AlarmSuppression/.test(node.name.value)) return null;
  },
  FieldDefinition(node, _key, _parent, _path, ancestors) {
    const owner = ancestors.find(a => 'kind' in a && a.kind === Kind.OBJECT_TYPE_DEFINITION);
    const name = owner && !Array.isArray(owner) && 'name' in owner ? owner.name?.value : '';
    if ((name === 'Query' || name === 'Mutation') && /[Ss]uppression/.test(node.name.value)) return null;
    if (name === 'Alarm' && ['suppression', 'raisedDuringSuppression'].includes(node.name.value)) return null;
    if (name === 'AlarmEvent' && ['suppressedUntil', 'suppressionWindowToken', 'raisedDuringSuppression'].includes(node.name.value)) return null;
  },
  InputValueDefinition(node) {
    if (node.name.value === 'raisedDuringSuppression') return null;
  },
});
const oldSchema = buildASTSchema(oldSDL);
const schema = buildASTSchema(parse(sdl));
const documents = Object.entries({ ...generated, ...optionalDocuments }).filter(([name]) => name.endsWith('Document'));

describe('rolling compatibility with device-management before suppression', () => {
  it('enumerates every generated document, including negative controls', () => {
    expect(documents.length).toBeGreaterThan(100);
    expect(documents.filter(([name]) => name.startsWith('AlarmMaintenance')).length).toBeGreaterThanOrEqual(8);
  });
  for (const [name, document] of documents) {
    it(name, () => {
      const ast = parse(String(document));
      expect(validate(schema, ast).map(e => e.message)).toEqual([]);
      const errors = validate(oldSchema, ast).map(e => e.message);
      if (name.startsWith('AlarmMaintenance')) expect(errors.length, name).toBeGreaterThan(0);
      else expect(errors, `${name} must continue working on an old pod`).toEqual([]);
    });
  }
});
