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
// A third way was closed at the SERVER, which is the only place it could be closed: a value
// the declared type cannot hold is now REFUSED rather than coerced to unset. The panel's own
// check is the friendly message, and the test named "lets through the two range cases" is the
// honest record of what a pattern cannot see that a parser can.
//
// None of the three produces an error, a warning, or a visible difference on its own. So
// the assertions here are on the REQUEST THAT LEAVES, with 'SHARED' and the declared value
// type written as LITERALS in this file rather than imported from the module under test: a
// fixture built from the constant it is checking cannot notice the constant moving.
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

import { GraphQLRequestError } from '@devicechain/client';
import { buildSelector } from '@/lib/selector';
import {
  EntityAttributesPanel,
  facetRowKey,
  facetValueIssue,
  selectorReferencesKey,
} from './EntityAttributesPanel';

// Contract breaches the transport double saw, drained after every test. See respondWith.
let violations: string[] = [];

afterEach(() => {
  cleanup();
  // 🔴 THE STANDING CHECK ON EVERY TEST IN THIS FILE, not one test's assertion: every
  // request the panel makes must be scoped to the entity and family it was mounted for.
  const seen = violations;
  violations = [];
  expect(seen, 'the panel sent a request scoped to something other than this entity').toEqual([]);
});
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  violations = [];
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

// The area-mounted tests share one mount point, so the double and the render agree in one
// place rather than at six call sites — a mismatch is now a test failure, not a shrug.
const AREA = { entityType: 'area', entityToken: 'hq' };

const ELEVATION = {
  id: 'fk-4',
  memberType: 'area',
  key: 'elevation',
  valueType: 'DOUBLE',
  source: 'attribute',
  values: null,
  label: null,
};

const MANAGED = {
  id: 'fk-3',
  memberType: 'device',
  key: 'managed',
  valueType: 'BOOLEAN',
  source: 'attribute',
  values: null,
  label: 'Managed',
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

/**
 * The three reads the panel issues, answered by what the document asks for.
 *
 * 🔴 EVERY READ ANSWERS ONLY ITS OWN SCOPE, AND THAT IS THE POINT. A double that ignored
 * variables would answer `listFacetKeys(entityType)` and `listFacetKeys()` identically, so
 * dropping the family filter — every family's facets offered on every entity, an area
 * showing a device's axes and writing values no selector will look for — survived the whole
 * suite. A fake that skips the argument under test measures the fixture.
 */
function respondWith(opts: {
  entityType?: string;
  entityToken?: string;
  facets?: unknown[];
  attributes?: unknown[];
  groups?: unknown[];
}) {
  const family = opts.entityType ?? 'device';
  const token = opts.entityToken ?? 'therm-1';
  // 🔴 RECORDED AND RE-ASSERTED IN afterEach, NOT JUST THROWN HERE. An expectation that
  // fails inside the transport double is swallowed by the panel's own error handling —
  // the query rejects, useQuery renders an ErrorState, and the run can end green. The
  // violations list is what makes the check survive being caught.
  const need = (actual: unknown, want: unknown, what: string) => {
    if (actual !== want) {
      violations.push(`${what}: sent ${JSON.stringify(actual)}, expected ${JSON.stringify(want)}`);
    }
  };
  gqlMock.mockImplementation((_service: string, doc: string, vars: Record<string, any>) => {
    if (doc.includes('facetKeys')) {
      // The registry is per member family. Anything else is a bug in the caller, not a
      // shape this double should paper over.
      need(vars.criteria.memberType, family, 'facetKeys.memberType');
      return Promise.resolve({
        facetKeys: {
          results: opts.facets ?? [],
          pagination: { pageStart: 0, pageEnd: 0, totalRecords: 0 },
        },
      });
    }
    if (doc.includes('query EntityAttributes')) {
      need(vars.criteria.entityType, family, 'entityAttributes.entityType');
      need(vars.criteria.entity, token, 'entityAttributes.entity');
      // No scope filter: the panel reads every scope on purpose, so it can show the
      // CLIENT/SERVER rows that explain a Browse miss.
      need(vars.criteria.scope, undefined, 'entityAttributes.scope');
      return Promise.resolve({
        entityAttributes: {
          results: opts.attributes ?? [],
          pagination: { totalRecords: (opts.attributes ?? []).length },
        },
      });
    }
    if (doc.includes('entityGroups')) {
      need(vars.memberType, family, 'entityGroups.memberType');
      return Promise.resolve({
        entityGroups: {
          results: opts.groups ?? [],
          pagination: { totalRecords: (opts.groups ?? []).length },
        },
      });
    }
    if (doc.includes('setEntityAttribute')) {
      need(vars.request.entityType, family, 'setEntityAttribute.entityType');
      need(vars.request.entity, token, 'setEntityAttribute.entity');
      return Promise.resolve({ setEntityAttribute: attributeRow() });
    }
    if (doc.includes('deleteEntityAttribute')) {
      need(vars.entityType, family, 'deleteEntityAttribute.entityType');
      need(vars.entity, token, 'deleteEntityAttribute.entity');
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
    respondWith({ ...AREA, facets: [FLOORS], attributes: [] });
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
    respondWith({ ...AREA, facets: [FLOORS], attributes: [] });
    render(<EntityAttributesPanel entityType="area" entityToken="hq" />);

    const field = await screen.findByLabelText('floors');
    fireEvent.change(field, { target: { value: 'hot' } });
    fireEvent.click(screen.getByRole('button', { name: SAVE }));

    expect(await screen.findByText(/not a whole number/)).toBeTruthy();
    expect(writes()).toHaveLength(0);
  });

  // 🔴 THE HOLE IN THE CLIENT CHECK, AND WHAT THE OPERATOR SEES THROUGH IT. `1e400` is
  // well-formed decimal text that the panel's pattern accepts and the server's ParseFloat
  // refuses (+Inf). The write must fail visibly and the box must keep the text — under the
  // old coercion the mutation SUCCEEDED, the toast said saved, and the value was NULL.
  it('shows the server’s refusal of a value the client pattern let through', async () => {
    respondWith({ ...AREA, facets: [ELEVATION], attributes: [] });
    const passing = gqlMock.getMockImplementation()!;
    gqlMock.mockImplementation((service: string, doc: string, vars: Record<string, any>) => {
      if (String(doc).includes('setEntityAttribute')) {
        return Promise.reject(
          new GraphQLRequestError('attribute value "1e400" is not a DOUBLE (a finite number)', 200),
        );
      }
      return passing(service, doc, vars);
    });
    render(<EntityAttributesPanel entityType="area" entityToken="hq" />);

    const field = await screen.findByLabelText('elevation');
    fireEvent.change(field, { target: { value: '1e400' } });
    fireEvent.click(screen.getByRole('button', { name: SAVE }));

    expect(await screen.findByText(/is not a DOUBLE/)).toBeTruthy();
    expect(screen.getByLabelText('elevation')).toHaveProperty('value', '1e400');
    expect(toastMock).not.toHaveBeenCalled();
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

  // 🔴 THE BOOLEAN BRANCH IS A WHOLE SECOND EDITOR — a Combobox over a fixed pair, not the
  // Input every other type uses — and until this test nothing exercised it. Wiring its
  // onChange to nothing, or offering the wrong pair, would have survived the entire suite
  // while the only two values a BOOLEAN facet can hold became unauthorable.
  it('authors a BOOLEAN facet through its own editor, under the declared type', async () => {
    respondWith({ facets: [MANAGED], attributes: [] });
    render(<EntityAttributesPanel entityType="device" entityToken="therm-1" />);

    // The trigger is a button, so the name rides on aria-labelledby rather than a <label>.
    const trigger = await screen.findByRole('button', { name: /Managed/ });
    fireEvent.click(trigger);
    // Exactly the two storable values, and nothing else — 'yes'/'1' are spellings the
    // server would normalize but the composed CEL literal is `true`/`false`. (The kit's
    // Combobox renders its choices as plain buttons rather than listbox options, so they
    // are addressed by that role; that is the kit's markup, not this panel's.)
    expect(screen.getByRole('button', { name: 'true' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'false' })).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'false' }));
    fireEvent.click(screen.getByRole('button', { name: SAVE }));

    await waitFor(() => expect(writes()).toHaveLength(1));
    expect(writes()[0].vars.request).toEqual({
      entityType: 'device',
      entity: 'therm-1',
      scope: FACET_SCOPE,
      valueType: 'BOOLEAN',
      attrKey: 'managed',
      value: 'false',
    });
  });

  // 🔴 A STRANDED VALUE MUST NOT READ AS A CORRECT ONE. Re-declaring a facet's type rewrites
  // the declaration and no rows, so the value stays stored under the old type and its own
  // axis stops matching it. The row used to print the DECLARED type beside it, which turned
  // a silent mismatch into an active claim that the value was fine.
  it('says so when the stored value carries a different type than the declaration', async () => {
    respondWith({
      facets: [CLIMATE], // declared STRING
      attributes: [attributeRow({ valueType: 'JSON' })],
    });
    render(<EntityAttributesPanel entityType="device" entityToken="therm-1" />);

    expect(await screen.findByText(/stored as JSON/)).toBeTruthy();
    expect(screen.getByText(/does not match it/)).toBeTruthy();
    // Not "unset" — the value is there, it is the TYPE that is wrong, and conflating the
    // two would send the user looking for a value they already authored.
    expect(screen.queryByText('Not set')).toBeNull();
  });

  it('says nothing about the type when the stored row agrees with the declaration', async () => {
    respondWith({ facets: [CLIMATE], attributes: [attributeRow()] });
    render(<EntityAttributesPanel entityType="device" entityToken="therm-1" />);

    expect(await screen.findByRole('button', { name: CLEAR })).toBeTruthy();
    expect(screen.queryByText(/stored as/)).toBeNull();
  });

  // 🔴 THE FAILED READ THAT RENDERED AS AN ANSWER. The declarations and the values are two
  // independent queries; folding on the declarations alone answered a failed attribute read
  // with "every facet is unset" — Save offered, Clear absent, error never shown. One save
  // from that screen overwrites whatever was really stored.
  it('reports a failed attribute read instead of showing every facet as unset', async () => {
    gqlMock.mockImplementation((_service: string, doc: string) => {
      if (doc.includes('facetKeys')) {
        return Promise.resolve({
          facetKeys: { results: [CLIMATE], pagination: { pageStart: 0, pageEnd: 1, totalRecords: 1 } },
        });
      }
      if (doc.includes('query EntityAttributes')) {
        return Promise.reject(new GraphQLRequestError('the attribute read failed', 200));
      }
      return Promise.resolve({ entityGroups: { results: [], pagination: { totalRecords: 0 } } });
    });
    render(<EntityAttributesPanel entityType="device" entityToken="therm-1" />);

    expect(await screen.findByText('the attribute read failed')).toBeTruthy();
    expect(screen.queryByText('Not set')).toBeNull();
    expect(screen.queryByRole('button', { name: SAVE })).toBeNull();
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

// 🔴 THE PANEL IS MOUNTED ON FOUR FAMILIES FROM FOUR PLACES, so "which family did it ask
// about" is a real question with a wrong answer available. Facets are declared PER FAMILY:
// dropping the filter would offer an area a device's axes and write values under keys no
// area selector will ever look for. The standing afterEach check covers every test in this
// file; this one names the behaviour so the failure reads as a scoping bug.
describe('every read is scoped to the entity it was mounted for', () => {
  it('asks the registry for this family, not for all of them', async () => {
    respondWith({ ...AREA, facets: [FLOORS], attributes: [] });
    render(<EntityAttributesPanel entityType="area" entityToken="hq" />);

    expect(await screen.findByLabelText('floors')).toBeTruthy();
    const criteria = gqlMock.mock.calls
      .filter((c) => String(c[1]).includes('facetKeys'))
      .map((c) => (c[2] as { criteria: { memberType: string } }).criteria);
    expect(criteria).toHaveLength(1);
    expect(criteria[0].memberType).toBe('area');
  });
});

// The row's identity is what re-seeds its draft from the server. If it stops moving when the
// stored row moves, the box goes on showing the text the user typed as though it were saved
// — which is the "looks correct, is not stored" shape in miniature.
describe('facetRowKey', () => {
  const row = (over: Record<string, unknown> = {}) =>
    attributeRow(over) as unknown as Parameters<typeof facetRowKey>[1];

  it('moves when the stored row appears, changes value, changes type, or goes away', () => {
    const unset = facetRowKey(CLIMATE, undefined);
    const stored = facetRowKey(CLIMATE, row());
    const reValued = facetRowKey(CLIMATE, row({ value: 'temperate' }));
    // 🔑 The case the value-only key missed: a re-type stores the SAME text under a new
    // value_type, so a key over the value alone never moved and the row never re-seeded.
    const reTyped = facetRowKey(CLIMATE, row({ valueType: 'JSON' }));
    const keys = [unset, stored, reValued, reTyped];
    expect(new Set(keys).size).toBe(keys.length);
  });

  it('does not move when nothing about the stored row did', () => {
    expect(facetRowKey(CLIMATE, row())).toBe(facetRowKey(CLIMATE, row()));
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

  it('rejects what the server would refuse, before the round trip', () => {
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

  // 🔴 AND HERE IS WHAT IT CANNOT SEE, WHICH IS WHY IT IS NOT THE GATE. Both of these are
  // well-formed decimal text that this pattern accepts and the SERVER'S PARSER refuses:
  // `1e400` overflows float64 to +Inf, and a 20-digit LONG overflows int64. They used to be
  // coerced to unset / silently rounded to a different number; the server refuses them now,
  // and the test below shows the refusal reaching the operator.
  it('lets through the two range cases only the parser can catch', () => {
    expect(facetValueIssue('DOUBLE', '1e400')).toBeNull();
    expect(facetValueIssue('LONG', '12345678901234567890')).toBeNull();
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
