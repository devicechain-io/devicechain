// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { CURRENT_SCHEMA_VERSION, DashboardDefinitionError, parseDashboardDefinition, serializeDefinition } from './index';

const doc = (schemaVersion: number) => ({
  schemaVersion,
  title: 'Plant',
  widgets: [
    {
      id: 'w1',
      type: 'text',
      title: 'Hello',
      layout: { base: { mode: 'grid', col: 0, row: 0, cols: 4, rows: 3 } },
    },
  ],
});

function thrownBy(fn: () => unknown): unknown {
  try {
    fn();
  } catch (err) {
    return err;
  }
  throw new Error('expected a throw');
}

describe('a board saved by a newer version', () => {
  it('is refused with the stable code and the version it found', () => {
    const newer = CURRENT_SCHEMA_VERSION + 1;
    const e = thrownBy(() => parseDashboardDefinition(doc(newer)));
    // The long-standing class, so a caller that knows nothing of the code still refuses it.
    expect(e).toBeInstanceOf(DashboardDefinitionError);
    expect((e as DashboardDefinitionError).code).toBe('schemaTooNew');
    expect((e as DashboardDefinitionError).found).toBe(newer);
    expect((e as Error).name).toBe('DashboardDefinitionError');
  });

  it('is refused by any version above the current one, a fractional one included', () => {
    for (const v of [CURRENT_SCHEMA_VERSION + 0.5, CURRENT_SCHEMA_VERSION + 100]) {
      expect((thrownBy(() => parseDashboardDefinition(doc(v))) as DashboardDefinitionError).code).toBe('schemaTooNew');
    }
  });

  it('is refused whatever else is wrong with its shape', () => {
    const e = thrownBy(() => parseDashboardDefinition({ schemaVersion: CURRENT_SCHEMA_VERSION + 7, widgets: 'x' }));
    expect((e as DashboardDefinitionError).code).toBe('schemaTooNew');
  });

  it('does not give an ordinary invalid document the code', () => {
    const e = thrownBy(() => parseDashboardDefinition({ schemaVersion: CURRENT_SCHEMA_VERSION, widgets: 'x' }));
    expect(e).toBeInstanceOf(DashboardDefinitionError);
    expect((e as DashboardDefinitionError).code).toBeUndefined();
  });

  it('leaves the current version parsing, and round-tripping at that version', () => {
    const def = parseDashboardDefinition(doc(CURRENT_SCHEMA_VERSION));
    expect(def.schemaVersion).toBe(CURRENT_SCHEMA_VERSION);
    expect(def.widgets).toHaveLength(1);
    expect(parseDashboardDefinition(JSON.parse(serializeDefinition(def))).schemaVersion).toBe(CURRENT_SCHEMA_VERSION);
  });

  it('leaves an older version converting to the current one', () => {
    const def = parseDashboardDefinition({ ...doc(1), schemaVersion: 1 });
    expect(def.schemaVersion).toBe(CURRENT_SCHEMA_VERSION);
  });
});
