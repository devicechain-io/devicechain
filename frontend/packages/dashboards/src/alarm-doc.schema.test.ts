// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The alarm documents are hand-written strings (packages carry no codegen), so nothing
// reads them against the schema they are sent to: a field device-management stops serving
// used to leave them compiling and passing every test here, and fail only against a live
// server with `Cannot query field ...`. This validates each one against the SDL
// device-management actually serves, read from the repository.

import { readFileSync } from 'node:fs';

import { buildSchema, parse, validate } from 'graphql';
import { describe, expect, it } from 'vitest';

import { ACKNOWLEDGE_ALARM, ALARM_STREAM, ALARMS_QUERY, CLEAR_ALARM } from './internal/alarm-doc';

const sdl = readFileSync(
  new URL('../../../../backend/services/device-management/graphql/schema.graphql', import.meta.url),
  'utf8',
);
const schema = buildSchema(sdl);

describe('alarm documents against the served device-management schema', () => {
  // Floor: a schema that failed to resolve to device-management's would validate
  // nothing meaningful.
  it('reads the device-management schema', () => {
    expect(schema.getType('Alarm')).toBeDefined();
    expect(schema.getType('AlarmEvent')).toBeDefined();
  });

  const docs = { ALARMS_QUERY, ALARM_STREAM, ACKNOWLEDGE_ALARM, CLEAR_ALARM };
  for (const [name, doc] of Object.entries(docs)) {
    it(`${name} validates with no errors`, () => {
      const errors = validate(schema, parse(doc as unknown as string)).map((e) => e.message);
      expect(errors).toEqual([]);
    });
  }
});
