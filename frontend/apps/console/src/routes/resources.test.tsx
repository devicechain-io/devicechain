// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 NO REGISTRY EDIT MAY DESTROY A FIELD ITS FORM DOES NOT TOUCH.
//
// Every form on this side edits a name and a description. What the server does
// with the fields the form did not send is the whole question — and the answer is
// mid-migration, so this file checks BOTH answers and reads which one applies off
// the mutation itself.
//
// A FULL-REPLACE update rebuilds the stored record from the request, so an adapter
// that maps the form's fields straight onto a create request does not "leave
// metadata alone": it DELETES it, with a success toast, a fresh audit entry, and
// nothing anywhere that looks wrong. That is how it survived in every family at
// once until a geofence form went looking for it. Those families are held by the
// type system as the mistake was actually made — `update*` takes
// `Required<…CreateRequest>` so an omitted field will not compile, and each
// `…Preserved(entity)` returns `Required<…>` so a NEW schema field breaks the
// projection rather than starting to be erased.
//
// A PARTIAL update leaves an absent field alone, and there the carry-forward
// INVERTS from the fix into the bug: a form that sends fields it does not edit is
// writing them from a snapshot it read minutes ago, so two operators on two tabs
// each overwrite the other. Its `…Preserved` projection is deleted, and what this
// file checks is that nothing grows back.
//
// 🔑 WHICH CONTRACT A FAMILY IS ON IS READ OFF ITS DOCUMENT, never a list kept
// here by hand — `$request: …UpdateRequest` is partial, `…CreateRequest` is full
// replace. The sweep converting the rest flips each family's assertion the moment
// its mutation changes, and a list would be one more thing to remember instead.
//
// What no compiler sees on either contract is an adapter that names a field and
// sends the wrong thing — `metadata: undefined`, or a fresh entity's — which is
// why this runs the real forms.
//
// 🔑 The list is IMPORTED, never re-declared. `REGISTRY_RESOURCES` is the same
// array App.tsx routes from, so a family added there is covered here the moment it
// is added. A list copied into a test only ever covers what someone remembered to
// write down twice, which is the same failure as the bug.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { parse } from 'graphql';
import type { DocumentNode, OperationDefinitionNode, FieldNode, TypeNode } from 'graphql';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

// MapLibre cannot run in jsdom, and the geofence form reaches it through a dynamic
// import that would never resolve here. The fake is inert on purpose: it draws
// nothing and validates nothing, so the ring this test saves is the one the form
// read out of the entity — which is exactly what a full-replace save must send.
vi.mock('@/routes/geofences/FenceMap', () => ({
  FenceMap: () => <div data-testid="fake-map" />,
}));

// The Identity tab gates its Save on `device:write`, and reads that from the auth
// context rather than a prop — so rendering the tab on its own needs one. The
// authority is REAL and checked by the real `hasAuthority`; only the provider is
// faked. Stubbing the check itself would leave the test unable to tell a form that
// respects the gate from one that ignores it.
vi.mock('@/auth/AuthProvider', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/auth/AuthProvider')>();
  return { ...actual, useAuth: () => ({ claims: { authorities: ['device:write'] } }) };
});

import { REGISTRY_RESOURCES } from './resources';
import type { RegistryResource } from '@/components/registry';

const METADATA = '{"site":"rome-yard","installedBy":"ops","costCentre":"CC-4417"}';

// A type row for the pickers. An instance form disables its save when NO types
// exist, so an empty list here would make every instance case pass by never
// sending anything at all.
const TYPE_ROW = {
  id: 'ty-1',
  token: 'type-1',
  name: 'Type One',
  icon: 'truck',
  backgroundColor: '#1f2937',
  foregroundColor: '#f9fafb',
  borderColor: '#374151',
  imageUrl: 'https://example.test/type.png',
  metadata: null,
  createdAt: '2026-08-01T00:00:00Z',
};

// One fixture that is a SUPERSET of every family's shape. Each resource reads only
// the keys it knows (a device's deviceType, a fence's geometry, a group's
// memberType), and the extras are inert. A family whose entity needs a key that is
// missing here fails loudly — its save stays disabled and the assertion below
// reports no request at all — rather than passing quietly.
const ENTITY = {
  id: 'e-1',
  token: 'thing-1',
  name: 'Thing One',
  description: 'A thing.',
  metadata: METADATA,
  createdAt: '2026-08-01T00:00:00Z',
  externalId: 'ext-1',
  memberType: 'device',
  membershipMode: 'static',
  // 🔴 Distinctive, not null. These are the fields the fix RESTORES for types and
  // groups, and with every one of them null a projection that crossed two of them
  // (`imageUrl: g.icon`) or hardcoded one to null would satisfy every assertion
  // below — null === null. A fixture of nulls cannot fail on a null.
  icon: 'warehouse',
  backgroundColor: '#0b3d2e',
  foregroundColor: '#e7f5ef',
  borderColor: '#145c43',
  imageUrl: 'https://example.test/thing.png',
  manufacturer: null,
  model: null,
  profile: null,
  category: 'sensor',
  activeVersion: 1,
  deviceTypeCount: 0,
  location: null,
  kind: 'POLYGON_2D',
  geometry: JSON.stringify({
    kind: 'POLYGON_2D',
    geometry: { type: 'Polygon', coordinates: [[[12.49, 41.89], [12.51, 41.9], [12.48, 41.88], [12.49, 41.89]]] },
  }),
  deviceType: TYPE_ROW,
  assetType: TYPE_ROW,
  customerType: TYPE_ROW,
  areaType: TYPE_ROW,
};

// 🔴 THE GENERATED DOCUMENTS ARE NOT DocumentNodes. The console's codegen runs in
// `documentMode: 'string'`, so `graphql()` hands back a TypedDocumentString — a
// String subclass with no `.definitions` at all. Anything deriving behaviour from
// a document has to PARSE it first.
//
// Reading `.definitions` off one yields undefined and throws on the next access,
// and that is not hypothetical: the transport below did exactly that on every
// call for as long as it existed. The suite still passed, because it asserts over
// `gqlMock.mock.calls` — which are recorded when the call is MADE, before the fake
// throws — so every form was being driven against a transport that answered
// nothing, and no assertion here was in a position to notice. Parse once, keyed by
// the text, since a render re-sends the same handful.
const parsedDocs = new Map<string, DocumentNode>();

function docNode(doc: unknown): DocumentNode {
  const text = String(doc);
  let node = parsedDocs.get(text);
  if (!node) {
    node = parse(text);
    parsedDocs.set(text, node);
  }
  return node;
}

// The transport answers from the DOCUMENT rather than from a table of operation
// names: a search selection gets results + pagination, a *ByToken selection gets an
// array, anything else gets a single row. Deriving it means a query this test has
// never seen still answers plausibly instead of throwing, so a form is never
// blocked by scaffolding rather than by the thing under test.
function answerFor(doc: unknown): Record<string, unknown> {
  const op = docNode(doc).definitions.find(
    (d): d is OperationDefinitionNode => d.kind === 'OperationDefinition',
  );
  const out: Record<string, unknown> = {};
  for (const sel of op?.selectionSet.selections ?? []) {
    if (sel.kind !== 'Field') continue;
    const field = sel as FieldNode;
    const key = field.alias?.value ?? field.name.value;
    const children = (field.selectionSet?.selections ?? [])
      .filter((s): s is FieldNode => s.kind === 'Field')
      .map((s) => s.name.value);
    if (!field.selectionSet) {
      // A leaf selects no children, so its shape is not derivable from the
      // document — the entity fakes above would all be wrong. An empty list is the
      // one answer that suits every leaf a form reads here: `facetValues` returns a
      // list of in-use values, and "none yet" is a real state the field handles.
      out[key] = [];
    } else if (children.includes('results')) {
      out[key] = {
        results: [TYPE_ROW],
        pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 },
      };
    } else if (key.endsWith('ByToken')) {
      out[key] = [TYPE_ROW];
    } else {
      out[key] = TYPE_ROW;
    }
  }
  return out;
}

/**
 * The mutation writes actually sent to device-management, each kept WITH the
 * document that carried it — because the document is what says which of the two
 * update contracts applies, and they demand opposite assertions.
 *
 * A partial update also moves the token out of the request and alongside it, so
 * the identifying token is captured from either place.
 */
type Write = { doc: unknown; request: Record<string, unknown>; token: unknown };

function writes(): Write[] {
  const out: Write[] = [];
  for (const call of gqlMock.mock.calls) {
    if (call[0] !== 'device-management') continue;
    const vars = call[2] as { request?: Record<string, unknown>; token?: unknown } | undefined;
    if (vars?.request == null) continue;
    out.push({ doc: call[1], request: vars.request, token: vars.token });
  }
  return out;
}

/** Unwrap `Foo!` / `[Foo!]!` down to the name. */
function namedTypeOf(type: TypeNode | undefined): string | undefined {
  let t = type;
  while (t && (t.kind === 'NonNullType' || t.kind === 'ListType')) t = t.type;
  return t?.kind === 'NamedType' ? t.name.value : undefined;
}

/**
 * Which update contract a mutation is written against, read off the declared type
 * of its `$request` variable.
 *
 * 🔴 An unrecognised name THROWS rather than defaulting. The two contracts want
 * opposite things from the same field, so a silent default would quietly pick one
 * of them for a family nobody had looked at — and the wrong pick passes.
 */
function contractOf(doc: unknown): 'partial' | 'full-replace' {
  const op = docNode(doc).definitions.find(
    (d): d is OperationDefinitionNode => d.kind === 'OperationDefinition',
  );
  const declared = op?.variableDefinitions?.find((d) => d.variable.name.value === 'request');
  const name = namedTypeOf(declared?.type);
  if (name?.endsWith('UpdateRequest')) return 'partial';
  if (name?.endsWith('CreateRequest')) return 'full-replace';
  throw new Error(
    `${op?.name?.value ?? 'an unnamed mutation'} declares $request as ${name ?? 'nothing'}, which ` +
      'names neither update contract — teach contractOf about it rather than letting it guess.',
  );
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  window.localStorage.clear();
  gqlMock.mockImplementation((service: string, doc: unknown) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    return Promise.resolve(answerFor(doc));
  });
});

// Save an edit through whatever the resource renders, and hand back what went out.
//
// 🔴 It TYPES A NEW NAME first, and that is not incidental. Saving an untouched
// form makes the form's values identical to the entity's, which makes
// `{...preserved(e), name: req.name}` and the INVERTED `{name: req.name,
// ...preserved(e)}` indistinguishable — both send the same bytes, both compile
// under `Required<…>`, and the inversion would silently discard whatever the
// operator typed. Renaming is what tells the two apart.
const RENAMED = 'Renamed by the operator';

async function saveEdit(node: React.ReactNode, rename = true): Promise<Write[]> {
  render(<>{node}</>);
  if (rename) {
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: RENAMED } });
  }
  const save = await screen.findByRole('button', { name: 'Save changes' });
  await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(save);
  await waitFor(() => expect(writes().length).toBeGreaterThan(0));
  return writes();
}

// Fields the forms in this loop actually edit. EVERYTHING else the request names
// has to come back exactly as the entity had it — which is the whole property, and
// a far stronger one than "metadata survived": it fails on any field a projection
// crosses, drops or invents, including ones added to a family later.
//
// `geometry` is here because the geofence editor OWNS the boundary: it is the field
// that form exists to change, and it goes out on every save whether or not the
// operator moved a corner. Reading it as a carry-forward would demand the one form
// that draws a shape stop sending the shape.
const EDITED = new Set(['name', 'description', 'geometry']);

function assertOnlyTheEditsChanged(
  path: string,
  request: Record<string, unknown>,
  contract: 'partial' | 'full-replace',
) {
  const source = ENTITY as Record<string, unknown>;
  // `memberType` is injected per family by the group wrappers, and a family's
  // type token comes from the picker rather than the entity — neither is a
  // carry-forward, so neither is evidence either way.
  const carried = Object.keys(request).filter(
    (k) => !EDITED.has(k) && k !== 'memberType' && !k.endsWith('TypeToken') && k in source,
  );

  if (contract === 'partial') {
    // Here ABSENCE is the carry-forward, so the list has to be empty. A field
    // named in it is one this form does not edit and is now writing back from a
    // snapshot it read minutes ago — which is the lost update the server-side
    // change exists to remove. Naming them makes a regrown projection say which.
    expect(carried, `${path} carried fields forward on a PARTIAL update`).toEqual([]);
    return;
  }

  for (const key of carried) {
    expect(request[key], `${path} changed ${key}, which no form on this page edits`).toEqual(
      source[key],
    );
  }
  // A loop that compared nothing would pass. Every family carries at least a
  // token and its metadata.
  expect(carried.length, `${path} had no carried fields to check`).toBeGreaterThanOrEqual(2);
}

/**
 * Metadata must survive the save. Under full replace that means SENDING it; under
 * a partial update it means NOT sending it, since anything sent is written and an
 * explicit null clears the column outright.
 */
function assertMetadataSurvives(
  path: string,
  request: Record<string, unknown>,
  contract: 'partial' | 'full-replace',
  what: string,
) {
  if (contract === 'full-replace') {
    expect(request.metadata, `${path} dropped metadata on ${what}`).toBe(METADATA);
    return;
  }
  expect(
    'metadata' in request,
    `${path} sent metadata on a PARTIAL ${what} — a form that does not edit it must omit it, ` +
      'and sending null would clear the column',
  ).toBe(false);
}

describe('every registry resource carries metadata through an edit', () => {
  // The counterweight to the loop below: if the import ever resolved to an empty
  // array — a barrel gone missing, a mock swallowing it — `it.each([])` reports
  // "no tests" as a pass, and the whole file would become decoration.
  it('has resources to check', () => {
    expect(REGISTRY_RESOURCES.length).toBeGreaterThanOrEqual(13);
  });

  it.each(REGISTRY_RESOURCES.map((r) => [r.basePath, r] as const))(
    '%s',
    async (_path, resource: RegistryResource<unknown>) => {
      const sent = await saveEdit(resource.renderForm(ENTITY, vi.fn()));
      // The LAST write: a form may legitimately send more than one (a picker
      // save, then the entity), and it is the entity's own write that must carry it.
      const { doc, request, token } = sent[sent.length - 1];
      const contract = contractOf(doc);
      assertMetadataSurvives(_path, request, contract, 'save');
      // …and it did not "preserve" everything by sending the stale record whole:
      // the operator's new name has to arrive, or the save did nothing at all.
      expect(request.name, `${_path} discarded the operator's edit`).toBe(RENAMED);
      // A partial update names its subject alongside the request rather than
      // inside it — the token left the input entirely, so an update can no longer
      // MOVE a token the way the full-replace path could.
      expect(
        contract === 'partial' ? token : request.token,
        `${_path} did not identify the entity it was updating`,
      ).toBe(ENTITY.token);
      assertOnlyTheEditsChanged(_path, request, contract);
    },
  );
});

// Metadata is what the loop above can check uniformly, because every family has
// it. It is not the only field at risk, and this is the other one that would have
// been destroyed silently — checked here by name because nothing generic can.
describe('a device profile keeps the position declaration no form edits', () => {
  const DECLARED = {
    ...ENTITY,
    location: { expectedAccuracyMeters: 12.5, expectedUpdateIntervalSeconds: 30 },
  };
  const profile = REGISTRY_RESOURCES.find((r) => r.basePath === '/device-profiles');

  it('is a resource this file actually found', () => {
    expect(profile).toBeTruthy();
  });

  // 🔴 The declaration says devices on this profile report their own position. It
  // is replaced wholesale on update — the schema spells that out — and no console
  // form edits it, so before this it was cleared by an operator renaming the
  // profile. The visible symptom would have been a device's map surfaces going
  // quiet, several steps away from anything anyone had touched.
  it('sends the declaration back unchanged', async () => {
    const sent = await saveEdit(profile!.renderForm(DECLARED, vi.fn()));
    expect(sent[sent.length - 1].request.location).toEqual({
      expectedAccuracyMeters: 12.5,
      expectedUpdateIntervalSeconds: 30,
    });
  });

  // The counterweight: a profile that never declared one must still send null
  // rather than start inventing a declaration to be safe.
  it('sends null for a profile that never declared one', async () => {
    const sent = await saveEdit(profile!.renderForm({ ...ENTITY, location: null }, vi.fn()));
    expect(sent[sent.length - 1].request.location).toBeNull();
  });
});

// The second surface, and the one the compiler is least able to help with: the
// Appearance tab edits icon + colours and nothing else, so it too must start from
// the family's preserved projection. Derived by LABEL from the same array — the
// four type families reach it two different ways (a detail tab, or the single
// detail-extra slot), and asking for the label finds both.
describe('the appearance tab carries metadata through a save', () => {
  const appearanceSurfaces = REGISTRY_RESOURCES.flatMap((r) => {
    const tab = r.detailTabs?.find((t) => t.label === 'common:colAppearance');
    if (tab) return [[r.basePath, (e: unknown) => tab.render(e, vi.fn())] as const];
    if (r.detailExtraLabel === 'common:colAppearance' && r.renderDetailExtra) {
      const render = r.renderDetailExtra;
      return [[r.basePath, (e: unknown) => render(e, vi.fn())] as const];
    }
    return [];
  });

  // 🔴 Not decoration. The derivation above is a filter over labels, and a filter
  // that matches nothing yields an empty `it.each`, which vitest reports as a pass.
  // Four type families expose an appearance surface; if that stops being true this
  // says so instead of quietly checking nothing.
  it('found an appearance surface for all four type families', () => {
    expect(appearanceSurfaces.map(([p]) => p).sort()).toEqual([
      '/area-types',
      '/asset-types',
      '/customer-types',
      '/device-types',
    ]);
  });

  it.each(appearanceSurfaces)('%s', async (_path, renderTab) => {
    render(<>{renderTab(ENTITY)}</>);
    // Change a colour, so this is a real appearance save rather than a no-op that
    // would pass against a tab that had stopped sending its own edits.
    // The colour control is the kit's ColorPicker (react-colorful in our own popover)
    // rather than a native <input type="color">, whose OS dialog took none of our
    // theme. Driving it means opening the popover and typing a hex, which is also the
    // path a user takes when a brand guide hands them a value.
    // Popover opens on click (Select opens on pointerdown — the two Radix primitives
    // differ, which is exactly the kind of detail worth writing down once).
    fireEvent.click(screen.getByRole('button', { name: 'Background' }));
    fireEvent.change(await screen.findByRole('textbox', { name: 'Background' }), {
      target: { value: '#ff0000' },
    });
    fireEvent.click(await screen.findByRole('button', { name: 'Save appearance' }));
    await waitFor(() => expect(writes().length).toBeGreaterThan(0));
    const { doc, request, token } = writes()[writes().length - 1];
    const contract = contractOf(doc);

    expect(request.backgroundColor, `${_path} discarded the colour change`).toBe('#ff0000');
    assertMetadataSurvives(_path, request, contract, 'appearance save');

    // 🔴 The icon IS one of this tab's own fields — it edits an icon and three
    // colours. It belongs in the request on BOTH contracts, carrying the value the
    // form seeded from the entity, because the operator changed a colour and left
    // it alone. That is a real assertion either way: a tab that sent only the
    // control it touched would drop the icon on every colour save.
    //
    // (The list here used to call it untouched. Under full replace the two
    // readings are indistinguishable — the same bytes go out for both — so the
    // mislabel cost nothing until absence started to MEAN something.)
    expect(request.icon, `${_path} dropped the icon it edits`).toBe(ENTITY.icon);

    // These it genuinely does not edit, and used to erase.
    const untouched = ['imageUrl', 'name'] as const;
    if (contract === 'full-replace') {
      for (const key of untouched) {
        expect(request[key], `${_path} dropped the ${key}`).toBe(ENTITY[key]);
      }
    } else {
      // Under a partial update sending them is the regression, so the same fields
      // are checked from the other side.
      expect(
        untouched.filter((k) => k in request),
        `${_path} carried fields the appearance tab does not edit`,
      ).toEqual([]);
    }
    expect(
      contract === 'partial' ? token : request.token,
      `${_path} did not identify the entity it was updating`,
    ).toBe(ENTITY.token);
  });
});

// 🔴 THE TABS THE TWO LOOPS ABOVE NEVER REACH. They drive `renderForm` and the
// appearance tab, and nothing else — so a mutation planting `metadata: null` in
// the Identity tab passed this entire file. Both tabs below were rewritten for the
// partial contract, which makes "what does it send" the whole question about them.
describe('a device-type tab sends only the fields it edits', () => {
  const deviceTypes = REGISTRY_RESOURCES.find((r) => r.basePath === '/device-types');
  const identity = deviceTypes?.detailTabs?.find((t) => t.value === 'identity');

  // The counterweight, in the same shape as the others here: a `find` that missed
  // would make every test below vacuous rather than failing.
  it('is a tab this file actually found', () => {
    expect(identity).toBeTruthy();
  });

  it('sends the two discovery facets and nothing else', async () => {
    render(<>{identity!.render(ENTITY, vi.fn())}</>);
    fireEvent.change(screen.getByLabelText('Manufacturer'), { target: { value: 'Acme' } });
    fireEvent.click(await screen.findByRole('button', { name: 'Save identity' }));
    await waitFor(() => expect(writes().length).toBeGreaterThan(0));

    const { doc, request, token } = writes()[writes().length - 1];
    expect(contractOf(doc)).toBe('partial');
    expect(token, 'the identity tab did not identify its device type').toBe(ENTITY.token);
    // Exact, not a subset. The failure this guards is a field ARRIVING — metadata
    // read when the tab opened, or a null that clears a column nobody edited — so
    // an assertion that only checked the two facets were present would miss it.
    expect(Object.keys(request).sort()).toEqual(['manufacturer', 'model']);
    expect(request.manufacturer, 'the identity tab discarded the edit').toBe('Acme');
  });
});
