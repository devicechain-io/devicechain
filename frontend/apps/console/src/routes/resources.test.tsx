// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 EVERY REGISTRY EDIT MUST CARRY THE ENTITY'S METADATA BACK.
//
// Every registry update is a FULL REPLACE — the stored record is rebuilt from the
// request — while every form on this side of it edits a name and a description.
// So an adapter that maps the form's fields straight onto a create request does
// not "leave metadata alone": it DELETES it, with a success toast, a fresh audit
// entry, and nothing anywhere that looks wrong. That is how it survived in every
// family at once until a geofence form went looking for it.
//
// The type system now blocks the mistake as it was actually made: `update*` takes
// `Required<…CreateRequest>` so an omitted field will not compile, and each
// `…Preserved(entity)` returns `Required<…>` so a NEW schema field breaks the
// projection rather than starting to be erased. What the compiler cannot see is an
// adapter that names the field and sends the wrong thing — `metadata: undefined`,
// or a fresh entity's — which is what this file is for.
//
// 🔑 The list is IMPORTED, never re-declared. `REGISTRY_RESOURCES` is the same
// array App.tsx routes from, so a family added there is covered here the moment it
// is added. A list copied into a test only ever covers what someone remembered to
// write down twice, which is the same failure as the bug.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { DocumentNode, OperationDefinitionNode, FieldNode } from 'graphql';

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

// The transport answers from the DOCUMENT rather than from a table of operation
// names: a search selection gets results + pagination, a *ByToken selection gets an
// array, anything else gets a single row. Deriving it means a query this test has
// never seen still answers plausibly instead of throwing, so a form is never
// blocked by scaffolding rather than by the thing under test.
function answerFor(doc: DocumentNode): Record<string, unknown> {
  const op = doc.definitions.find(
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
    if (children.includes('results')) {
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

/** The mutation requests actually sent to device-management. */
function requests(): Record<string, unknown>[] {
  return gqlMock.mock.calls
    .filter((call) => call[0] === 'device-management')
    .map((call) => (call[2] as { request?: Record<string, unknown> } | undefined)?.request)
    .filter((r): r is Record<string, unknown> => r != null);
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  window.localStorage.clear();
  gqlMock.mockImplementation((service: string, doc: DocumentNode) => {
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

async function saveEdit(node: React.ReactNode, rename = true): Promise<Record<string, unknown>[]> {
  render(<>{node}</>);
  if (rename) {
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: RENAMED } });
  }
  const save = await screen.findByRole('button', { name: 'Save changes' });
  await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(save);
  await waitFor(() => expect(requests().length).toBeGreaterThan(0));
  return requests();
}

// Fields the forms in this loop actually edit. EVERYTHING else the request names
// has to come back exactly as the entity had it — which is the whole property, and
// a far stronger one than "metadata survived": it fails on any field a projection
// crosses, drops or invents, including ones added to a family later.
const EDITED = new Set(['name', 'description']);

function assertOnlyTheEditsChanged(path: string, request: Record<string, unknown>) {
  const source = ENTITY as Record<string, unknown>;
  let checked = 0;
  for (const [key, value] of Object.entries(request)) {
    // `memberType` is injected per family by the group wrappers, and a family's
    // type token comes from the picker rather than the entity — neither is a
    // carry-forward, so neither is evidence either way.
    if (EDITED.has(key) || key === 'memberType' || key.endsWith('TypeToken')) continue;
    if (!(key in source)) continue;
    checked += 1;
    if (key === 'geometry') {
      // Re-serialized by the editor, so equal documents differ as strings.
      expect(JSON.parse(value as string), `${path} changed the geometry`).toEqual(
        JSON.parse(source[key] as string),
      );
      continue;
    }
    expect(value, `${path} changed ${key}, which no form on this page edits`).toEqual(source[key]);
  }
  // A loop that compared nothing would pass. Every family carries at least a
  // token and its metadata.
  expect(checked, `${path} had no carried fields to check`).toBeGreaterThanOrEqual(2);
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
      // The LAST request: a form may legitimately send more than one (a picker
      // save, then the entity), and it is the entity's own write that must carry it.
      const request = sent[sent.length - 1];
      expect(request.metadata, `${_path} dropped metadata on save`).toBe(METADATA);
      // …and it did not "preserve" everything by sending the stale record whole:
      // the operator's new name has to arrive, or the save did nothing at all.
      expect(request.name, `${_path} discarded the operator's edit`).toBe(RENAMED);
      expect(request.token).toBe(ENTITY.token);
      assertOnlyTheEditsChanged(_path, request);
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
    expect(sent[sent.length - 1].location).toEqual({
      expectedAccuracyMeters: 12.5,
      expectedUpdateIntervalSeconds: 30,
    });
  });

  // The counterweight: a profile that never declared one must still send null
  // rather than start inventing a declaration to be safe.
  it('sends null for a profile that never declared one', async () => {
    const sent = await saveEdit(profile!.renderForm({ ...ENTITY, location: null }, vi.fn()));
    expect(sent[sent.length - 1].location).toBeNull();
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
    fireEvent.change(screen.getByLabelText('Background'), { target: { value: '#ff0000' } });
    fireEvent.click(await screen.findByRole('button', { name: 'Save appearance' }));
    await waitFor(() => expect(requests().length).toBeGreaterThan(0));
    const request = requests()[requests().length - 1];

    expect(request.backgroundColor, `${_path} discarded the colour change`).toBe('#ff0000');
    // The three fields this tab does NOT edit, each of which it used to erase.
    expect(request.metadata, `${_path} dropped metadata on an appearance save`).toBe(METADATA);
    expect(request.imageUrl, `${_path} dropped the imageUrl`).toBe(ENTITY.imageUrl);
    expect(request.icon, `${_path} dropped the icon`).toBe(ENTITY.icon);
    expect(request.name, `${_path} dropped the name`).toBe(ENTITY.name);
    expect(request.token).toBe(ENTITY.token);
  });
});
