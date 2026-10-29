// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The anchor documents are hand-written strings (packages carry no codegen), so nothing else
// reads them against the schema they are sent to. This validates each one against the SDL
// device-management actually serves, read from the repository, as alarm-doc.schema.test does
// for the alarm documents.

import { readFileSync } from 'node:fs';

import { buildSchema, parse, validate } from 'graphql';
import { describe, expect, it } from 'vitest';

import { ANCHOR_DEVICES, CUSTOMER_DEVICES, DEVICE_NAMES, DEVICES_BY_TOKEN } from './queries';

const sdl = readFileSync(
  new URL('../../../../backend/services/device-management/graphql/schema.graphql', import.meta.url),
  'utf8',
);
const schema = buildSchema(sdl);

describe('anchor documents against the served device-management schema', () => {
  it('reads the device-management schema', () => {
    expect(schema.getType('CustomerDeviceSearchCriteria')).toBeDefined();
    expect(schema.getType('AnchorDeviceSearchCriteria')).toBeDefined();
  });

  const docs = { ANCHOR_DEVICES, CUSTOMER_DEVICES, DEVICES_BY_TOKEN, DEVICE_NAMES };
  for (const [name, doc] of Object.entries(docs)) {
    it(`${name} validates with no errors`, () => {
      const errors = validate(schema, parse(doc as unknown as string)).map((e) => e.message);
      expect(errors).toEqual([]);
    });
  }
});
