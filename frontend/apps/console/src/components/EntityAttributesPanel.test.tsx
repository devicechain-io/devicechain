// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 WHAT THIS FILE IS FOR, AND IT IS NOT "THE FORM SAVES".
//
// A facet value is only worth writing if BROWSE CAN SEE IT, and the two ways to write a
// value Browse cannot see both look like success on screen:
//
//   - the wrong SCOPE. The selector lowering reads exactly one EntityAttribute scope
//     (SHARED). A CLIENT- or SERVER-scoped row with the right key and the right value
//     is valid, saves cleanly, and matches nothing.
//   - the wrong VALUE TYPE. A scalar leaf pins value_type, so "3" stored as STRING is
//     invisible to `attr["floors"] == 3` — and the row reads back perfectly.
//
// Neither produces an error, a warning, or a visible difference. So the assertions here
// are on the REQUEST THAT LEAVES, with 'SHARED' and the declared value type written as
// LITERALS in this file rather than imported from the module under test: a fixture built
// from the constant it is checking cannot notice the constant moving.
//
// The other half of the proof is in Go —
// backend/services/device-management/model/api_facet_value_authoring_test.go — which
// takes the same scope/type/CEL and shows an entity authored that way actually appears
// in a Browse preview, and disappears when either is changed. This file proves the
// console emits those values; that file proves those values are the matching ones.
// AUTHORED_SELECTOR below is the string the two ends share.
//
// Only the GraphQL transport, the auth claims and the two report channels are faked. The
// real i18n catalogs, the real API module, the real selector composer and the real
// validation all run.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { DecodedClaims } from '@devicechain/client';

const { gqlMock, toastMock, authState } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  toastMock: vi.fn(),
  authState: { claims: null as unknown },
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => authState }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));
// Deletion of a non-facet row goes through a confirm; accept it so the request under
// test is the one that reaches the wire.
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => () => Promise.resolve(true) }));

import { buildSelector } from '@/lib/selector';
import { EntityAttributesPanel, facetValueIssue, selectorReferencesKey } from './EntityAttributesPanel';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  authState.claims = claimsWith(['device:read', 'device:write']);
});

const claimsWith = (authorities: string[]): DecodedClaims => ({
  tenant: 'acme',
  username: 'operator@acme.test',
  roles: [],
  authorities,
  typ: 'access',
});

// 🔑 THE SCOPE, SPELLED OUT. Not imported from lib/api/entity-attributes — see the header.
const FACET_SCOPE = 'SHARED';

// 🔑 THE CEL THE BACKEND TEST PINS. Keep in step with `authoredSelector` in
// api_facet_value_authoring_test.go; the composer assertion below is what notices if the
// console ever stops composing exactly this for the value authored here.
const AUTHORED_SELECTOR = 'attr["climate"] == "arid"';

const CLIMATE = {
  id: 'fk-1',
  memberType: 'device',
  key: 'climate',
  valueType: 'STRING',
  source: 'attribute',
  values: ['arid', 'temperate', 'tropical'],
  label: 'Climate',
};

const FLOORS = {
  id: 'fk-2',
  memberType: 'area',
  key: 'floors',
  valueType: 'LONG',
  source: 'attribute',
  values: null,
  label: null,
};

function attributeRow(over: Record<string, unknown> = {}) {
  return {
    id: 'ea-1',
    entityType: 'device',
    scope: FACET_SCOPE,
    attrKey: 'climate',
    valueType: 'STRING',
    value: 'arid',
    lastUpdated: '2026-09-01T00:00:00Z',
    ...over,
  };
}

/** The three reads the panel issues, answered by what the document asks for. */
function respondWith(opts: {
  facets?: unknown[];
  attributes?: unknown[];
  groups?: unknown[];
}) {
  gqlMock.mockImplementation((_service: string, doc: string) => {
    if (doc.includes('facetKeys')) {
      return Promise.resolve({
        facetKeys: {
          results: opts.facets ?? [],
          pagination: { pageStart: 0, pageEnd: 0, totalRecords: 0 },
        },
      });
    }
    if (doc.includes('query EntityAttributes')) {
      return Promise.resolve({
        entityAttributes: {
          results: opts.attributes ?? [],
          pagination: { totalRecords: (opts.attributes ?? []).length },
        },
      });
    }
    if (doc.includes('entityGroups')) {
      return Promise.resolve({
        entityGroups: {
          results: opts.groups ?? [],
          pagination: { totalRecords: (opts.groups ?? []).length },
        },
      });
    }
    if (doc.includes('setEntityAttribute')) {
      return Promise.resolve({ setEntityAttribute: attributeRow() });
    }
    if (doc.includes('deleteEntityAttribute')) {
      return Promise.resolve({ deleteEntityAttribute: true });
    }
    throw new Error(`unexpected document: ${doc}`);
  });
}

/** Every write that reached the wire, as {operation, variables}. */
function writes(): { doc: string; vars: Record<string, unknown> }[] {
  return gqlMock.mock.calls
    .filter((c) => /mutation (Set|Delete)EntityAttribute/.test(c[1] as string))
    // 🔴 String(...) is load-bearing. The client-preset's `documentMode: 'string'` emits
    // a TypedDocumentString that EXTENDS String, so `toContain` treats the raw object as
    // an array of characters and reports a 232-element mismatch instead of the document.
    .map((c) => ({ doc: String(c[1]), vars: (c[2] ?? {}) as Record<string, unknown> }));
}

const SAVE = 'Save';
const CLEAR = 'Clear';

describe('authoring a facet value', () => {
  // 🔴 THE PRIMARY USE CASE IS THE EMPTY ONE. An entity with no attributes at all must
  // still show every declared axis: an axis you cannot see is an axis you cannot set,
  // and "declared but unset" is exactly the state the user came here to fix. A fixture
  // built by "create entity, set attributes, render" can never exercise this.
  it('shows a declared facet the entity has no value for', async () => {
    respondWith({ facets: [CLIMATE], attributes: [] });
    render(<EntityAttributesPanel entityType="device" entityToken="therm-1" />);

    expect(await screen.findByText('Climate')).toBeTruthy();
    expect(screen.getByText('Not set')).toBeTruthy();
    // Nothing to clear when there is no row — Clear is a DELETE, not a write of "".
    expect(screen.queryByRole('button', { name: CLEAR })).toBeNull();
  });

  it('writes the value at the facet scope, under the declared value type', async () => {
    respondWith({ facets: [CLIMATE], attributes: [] });
    render(<EntityAttributesPanel entityType="device" entityToken="therm-1" />);

    const field = await screen.findByLabelText('Climate');
    fireEvent.change(field, { target: { value: 'arid' } });
    fireEvent.click(screen.getByRole('button', { name: SAVE }));

    await waitFor(() => expect(writes()).toHaveLength(1));
    expect(writes()[0].vars.request).toEqual({
      entityType: 'device',
      entity: 'therm-1',
      // 🔴 The two fields the whole feature turns on.
      scope: FACET_SCOPE,
      valueType: 'STRING',
      attrKey: 'climate',
      value: 'arid',
    });
  });

  // The value type comes from the DECLARATION, not from the text. "3" is a perfectly
  // good STRING, and stored as one it is invisible to `attr["floors"] == 3`.
  it('carries a numeric facet’s declared type, not one inferred from the text', async () => {
    respondWith({ facets: [FLOORS], attributes: [] });
    render(<EntityAttributesPanel entityType="area" entityToken="hq" />);

    const field = await screen.findByLabelText('floors');
    fireEvent.change(field, { target: { value: '3' } });
    fireEvent.click(screen.getByRole('button', { name: SAVE }));

    await waitFor(() => expect(writes()).toHaveLength(1));
    expect((writes()[0].vars.request as Record<string, unknown>).valueType).toBe('LONG');
  });

  // 🔴 THE SERVER DOES NOT REFUSE THIS ONE. A LONG write it cannot parse is COERCED to
  // unset (normalizeAttributeValue) — a successful mutation that stores no value, so the
  // toast says saved and Browse still says 0. The panel has to be the gate.
  it('refuses a value the declared type cannot hold, instead of sending it', async () => {
    respondWith({ facets: [FLOORS], attributes: [] });
    render(<EntityAttributesPanel entityType="area" entityToken="hq" />);

    const field = await screen.findByLabelText('floors');
    fireEvent.change(field, { target: { value: 'hot' } });
    fireEvent.click(screen.getByRole('button', { name: SAVE }));

    expect(await screen.findByText(/not a whole number/)).toBeTruthy();
    expect(writes()).toHaveLength(0);
  });

  // Clearing is a delete. Writing "" would leave a row that still exists, still satisfies
  // `"climate" in attr`, and matches `attr["climate"] == ""`.
  it('clears by deleting the row, not by writing an empty value', async () => {
    respondWith({ facets: [CLIMATE], attributes: [attributeRow()] });
    render(<EntityAttributesPanel entityType="device" entityToken="therm-1" />);

    fireEvent.click(await screen.findByRole('button', { name: CLEAR }));

    await waitFor(() => expect(writes()).toHaveLength(1));
    const [write] = writes();
    expect(write.doc).toContain('mutation DeleteEntityAttribute');
    expect(write.vars).toEqual({
      entityType: 'device',
      entity: 'therm-1',
      scope: FACET_SCOPE,
      attrKey: 'climate',
    });
  });

  // 🔴 THE CONFUSION THIS PANEL EXISTS TO EXPLAIN. A device that reports its own
  // `climate` produces a CLIENT-scoped row with the right key and the right value — and
  // Browse still says 0. If the panel counted it as the facet's value, the screen would
  // agree with the user and disagree with the engine.
  it('does not treat another scope’s row as the facet’s value', async () => {
    respondWith({
      facets: [CLIMATE],
      attributes: [attributeRow({ id: 'ea-client', scope: 'CLIENT' })],
    });
    render(<EntityAttributesPanel entityType="device" entityToken="therm-1" />);

    expect(await screen.findByText('Not set')).toBeTruthy();
    // It is still shown — read-only, scope-labelled — because hiding it would hide the
    // answer to "I set climate and Browse says 0".
    expect(screen.getByText('CLIENT')).toBeTruthy();
    // Read-only means read-only: no delete affordance on a row the operator did not write.
    expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull();
  });

  it('warns that the write moves the entity through a group that filters on the key', async () => {
    respondWith({
      facets: [CLIMATE],
      attributes: [],
      groups: [
        {
          id: 'g-1',
          token: 'arid-fleet',
          name: 'Arid fleet',
          memberType: 'device',
          membershipMode: 'dynamic',
          selector: AUTHORED_SELECTOR,
        },
      ],
    });
    render(<EntityAttributesPanel entityType="device" entityToken="therm-1" />);

    expect(await screen.findByText(/Arid fleet/)).toBeTruthy();
  });

  it('offers no editing affordance without device:write', async () => {
    authState.claims = claimsWith(['device:read']);
    respondWith({ facets: [CLIMATE], attributes: [attributeRow()] });
    render(<EntityAttributesPanel entityType="device" entityToken="therm-1" />);

    expect(await screen.findByText('Climate')).toBeTruthy();
    expect(screen.queryByRole('button', { name: SAVE })).toBeNull();
    expect(screen.queryByRole('button', { name: CLEAR })).toBeNull();
  });
});

// The bridge to the Go half. If the composer ever stops emitting this exact CEL for the
// value this panel authors, the backend test is proving something about a string the
// console no longer sends — and only this assertion would notice.
describe('the authored value and the Browse filter agree', () => {
  it('composes the selector the backend test pins', () => {
    const built = buildSelector([
      { key: 'climate', valueType: 'STRING', operator: 'eq', values: ['arid'] },
    ]);
    expect(built.selector).toBe(AUTHORED_SELECTOR);
    expect(built.issues).toEqual([]);
  });
});

describe('facetValueIssue', () => {
  it('accepts what each declared type can actually hold', () => {
    expect(facetValueIssue('STRING', 'arid')).toBeNull();
    expect(facetValueIssue('LONG', '-3')).toBeNull();
    expect(facetValueIssue('DOUBLE', '1.5e3')).toBeNull();
    expect(facetValueIssue('BOOLEAN', 'false')).toBeNull();
    expect(facetValueIssue('JSON', '{"a":1}')).toBeNull();
  });

  it('rejects what the server would silently coerce to unset', () => {
    expect(facetValueIssue('LONG', '3.5')).toBe('invalidLong');
    expect(facetValueIssue('DOUBLE', 'hot')).toBe('invalidDouble');
    // Go's ParseBool takes "1"/"T"/"yes"-adjacent spellings and canonicalizes them, but
    // the composed CEL literal is `true`/`false` — so anything else is a value you can
    // store and cannot filter on.
    expect(facetValueIssue('BOOLEAN', '1')).toBe('invalidBoolean');
    expect(facetValueIssue('JSON', '{')).toBe('invalidJson');
    // Empty is not a value: unsetting is Clear, which deletes.
    expect(facetValueIssue('STRING', '   ')).toBe('valueRequired');
  });
});

describe('selectorReferencesKey', () => {
  it('finds both shapes the composer emits, and does not match a lookalike', () => {
    expect(selectorReferencesKey('attr["climate"] == "arid"', 'climate')).toBe(true);
    expect(selectorReferencesKey('"climate" in attr', 'climate')).toBe(true);
    // A key that is a PREFIX of another must not claim the other's groups.
    expect(selectorReferencesKey('attr["climate_zone"] == "arid"', 'climate')).toBe(false);
    // The VALUE happening to equal the key is not a reference to the key.
    expect(selectorReferencesKey('attr["region"] == "climate"', 'climate')).toBe(false);
    expect(selectorReferencesKey(null, 'climate')).toBe(false);
  });
});
