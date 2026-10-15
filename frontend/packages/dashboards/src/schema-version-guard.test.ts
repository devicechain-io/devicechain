// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import {
  DashboardDefinitionError,
  DashboardSchemaTooNewError,
  parseDashboardDefinition,
  serializeDefinition,
} from './index';
import { CURRENT_SCHEMA_VERSION } from './types';

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

describe('a board saved by a newer version', () => {
  it('is refused with the typed error rather than parsed', () => {
    const newer = CURRENT_SCHEMA_VERSION + 1;
    let thrown: unknown;
    try {
      parseDashboardDefinition(doc(newer));
    } catch (err) {
      thrown = err;
    }
    expect(thrown).toBeInstanceOf(DashboardSchemaTooNewError);
    const e = thrown as DashboardSchemaTooNewError;
    expect(e.found).toBe(newer);
    // A caller that only knows the long-standing error still refuses it, never draws it.
    expect(e).toBeInstanceOf(DashboardDefinitionError);
  });

  it('is refused whatever else is wrong with its shape', () => {
    expect(() => parseDashboardDefinition({ schemaVersion: CURRENT_SCHEMA_VERSION + 7, widgets: 'x' })).toThrow(
      DashboardSchemaTooNewError,
    );
  });

  it('does not make an ordinary invalid document the typed error', () => {
    let thrown: unknown;
    try {
      parseDashboardDefinition({ schemaVersion: CURRENT_SCHEMA_VERSION, widgets: 'x' });
    } catch (err) {
      thrown = err;
    }
    expect(thrown).toBeInstanceOf(DashboardDefinitionError);
    expect(thrown).not.toBeInstanceOf(DashboardSchemaTooNewError);
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
