// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom (set globally in vite.config.ts). The real i18n catalogs are
// wired in by importing the config for its side effect, so every assertion below
// is on the STRING AN OPERATOR SEES rather than on a translation key.
//
// Only two seams are faked:
//
//   1. the GraphQL transport (`gql`) — the module is spread through
//      `importOriginal`, so GraphQLRequestError, the token grammar and the rest
//      of the client stay genuine.
//
//   2. `./FenceMap` — MapLibre cannot run in jsdom (no WebGL, no canvas), and
//      the component loads it through a dynamic import that would never resolve
//      here.
//
// 🔴 WHAT THE FenceMap FAKE MAKES UNTESTABLE IN THIS FILE, and where it IS tested:
//
//   * the map INTERACTION itself — a click becoming a vertex, the projection, the
//     close gesture's pixel radius, the camera fit. The RULES behind all of it
//     live in `./geometry.ts` (`toVertex`, `isCloseGesture`, `boundsOf`) and are
//     pinned by `./geometry.test.ts` without a DOM; what remains inside FenceMap
//     is MapLibre wiring, which only a real browser can exercise.
//   * that MapLibre stays OUT of the console's entry chunk — that is a bundling
//     property, not a rendering one, and `bundle-boundaries.test.ts` is its gate.
//
// So the fake is deliberately inert: it renders ONE button that hands the form a
// vertex list the test staged. It reimplements no validation, no serialization
// and no geometry — every one of those runs for real below, which is the only
// reason the assertions on the document actually sent mean anything.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { FenceMapProps } from './FenceMap';
import { MAX_FENCE_VERTICES, type Vertex } from './geometry';

// vi.mock factories are hoisted above the imports, so the doubles they close over
// have to be created in a hoisted block or they are still in their TDZ.
const { gqlMock, staged, tenantBasemap } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  // What the fake map will emit on its next click. The TEST supplies the ring;
  // the fake invents nothing.
  staged: { vertices: [] as { lng: number; lat: number }[] },
  // The tenant's effective basemap, as the context would hand it over.
  tenantBasemap: {
    value: null as { tileUrl?: string | null; attribution?: string | null; centerLat?: number | null; centerLon?: number | null; zoom?: number | null } | null,
  },
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

// The tenant basemap arrives through context in the real app. Only the HOOK is
// faked — resolveBasemap/fallbackView above are the real ones, so the precedence
// rules run for real here rather than being reimplemented by a double.
vi.mock('@/auth/TenantProvider', () => ({
  useTenantBasemap: () => tenantBasemap.value,
}));

// The form's TokenField reads the entity token masks over whichever session is
// live, so it needs the auth flags. A geofence form is a TENANT-scoped screen, so
// that is the session it is given here — the same lane the real screen would use.
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ isAuthenticated: true, isIdentityAuthenticated: false }),
}));

// The fake reflects `disabled` and exposes `onClose` as a button. Both were
// props the form silently failed to wire, and a fake that ignored them could not
// have shown that — it would have made the form's omission invisible, which is
// the exact failure mode this file's header warns about.
//
// 🔴 That is why EVERY prop the form is responsible for computing is reflected here,
// including the ones no test asserted when they were added. `attribution` and the
// opening camera were the next two to arrive (ADR-079); a fake that took `tileUrl`
// alone would have let the form pass the tenant's credit line under a personal tile
// URL, and nothing here would have noticed.
vi.mock('./FenceMap', () => ({
  FenceMap: ({
    onChange,
    onClose,
    disabled,
    closed,
    tileUrl,
    attribution,
    initialCenter,
    initialZoom,
  }: FenceMapProps) => (
    <div
      data-testid="fake-map"
      data-disabled={String(!!disabled)}
      data-closed={String(!!closed)}
      data-tile-url={tileUrl ?? ''}
      data-attribution={attribution ?? ''}
      data-initial-center={initialCenter ? initialCenter.join(',') : ''}
      data-initial-zoom={initialZoom === undefined ? '' : String(initialZoom)}
    >
      <button type="button" data-testid="fake-map-emit" onClick={() => onChange(staged.vertices)}>
        draw
      </button>
      <button type="button" data-testid="fake-map-close" onClick={() => onClose?.()}>
        close
      </button>
    </div>
  ),
}));

import { GraphQLRequestError } from '@devicechain/client';
import { GeoFenceForm } from './GeoFenceForm';
import type { GeoFence } from '@/lib/api/geofences';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  staged.vertices = [];
  tenantBasemap.value = null;
  // 🔴 The form seeds its basemap fields from localStorage AT MOUNT, so a test
  // that applies a tile URL would otherwise hand it to every test after it —
  // including ones asserting the no-basemap path. vitest.setup.ts installs a
  // deterministic in-memory store; this clears it between cases.
  window.localStorage.clear();
  // The default transport: token masks answer (TokenField asks for them on
  // mount), and every device-management mutation succeeds by echoing nothing
  // the form reads. Individual tests override this.
  gqlMock.mockImplementation((service: string) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    return Promise.resolve({ createGeoFence: {}, updateGeoFence: {} });
  });
});

// ── Strings, exactly as an operator reads them ──────────────────────────────
const CLOSED_HINT = "Shape closed. Drag a corner to move it, or undo a corner to keep drawing.";
const UNDO_ACTION = 'Undo last point';
const UNREADABLE =
  'This geofence uses a shape this editor cannot draw. It is stored intact and is still ' +
  'enforced — only editing it here is unavailable. Leave it as it is rather than recreating it.';
const CREATE_ACTION = 'Create geofence';
const SAVE_ACTION = 'Save changes';
const BOUNDARY_LABEL = 'Boundary';

// ── Fixtures ────────────────────────────────────────────────────────────────
//
// 🔴 Longitude 12.4924 and latitude 41.8902 (the Colosseum) are BOTH individually
// valid as either coordinate — |lng| ≤ 180 and |lat| ≤ 90 hold for both numbers —
// so a [lat, lng] transposition cannot slip past on range alone. Only the exact
// ordered document asserted below catches it.
const ROME: Vertex[] = [
  { lng: 12.4924, lat: 41.8902 },
  { lng: 12.51, lat: 41.9 },
  { lng: 12.48, lat: 41.88 },
];
const ROME_RING = [
  [12.4924, 41.8902],
  [12.51, 41.9],
  [12.48, 41.88],
  [12.4924, 41.8902],
];

const OUTER_RING = ROME_RING;
// A second, smaller ring inside the first — a HOLE. Well-formed on its own; it is
// the SECOND ring that this editor refuses, not any defect in it.
const HOLE_RING = [
  [12.492, 41.889],
  [12.4925, 41.889],
  [12.4925, 41.8895],
  [12.492, 41.889],
];

function polygonDoc(rings: number[][][]): string {
  return JSON.stringify({
    kind: 'POLYGON_2D',
    geometry: { type: 'Polygon', coordinates: rings },
  });
}

function fence(overrides: Partial<GeoFence> = {}): GeoFence {
  return {
    id: 'gf-1',
    token: 'yard-perimeter',
    name: 'Yard perimeter',
    description: null,
    geometry: polygonDoc([OUTER_RING]),
    kind: 'POLYGON_2D',
    metadata: null,
    createdAt: '2026-08-09T12:00:00Z',
    ...overrides,
  };
}

// The mutation calls actually sent to device-management. TokenField's mask lookup
// goes to a different service, so filtering by service keeps that noise out.
function fenceCalls(): unknown[][] {
  return gqlMock.mock.calls.filter((call) => call[0] === 'device-management');
}

function requestFrom(call: unknown[]): { token: string; geometry: string; metadata?: unknown } {
  return (call[2] as { request: { token: string; geometry: string; metadata?: unknown } }).request;
}

function button(name: string): HTMLButtonElement {
  return screen.getByRole('button', { name }) as HTMLButtonElement;
}

// Stage a ring and hand it to the form the way the map would.
function draw(vertices: Vertex[]) {
  staged.vertices = vertices;
  fireEvent.click(screen.getByTestId('fake-map-emit'));
}

describe('GeoFenceForm', () => {
  // ── 1. THE SAFETY CASE ────────────────────────────────────────────────────
  //
  // 🔴 updateGeoFence is a FULL REPLACE. A fence whose geometry this editor
  // cannot represent must therefore not open at all: an editor that showed a
  // blank map would destroy the stored shape on the first save. A hole-bearing
  // polygon is the concrete case — readable as a document, not representable as
  // the single open ring this editor edits.
  it('refuses to edit a geometry it cannot represent, offering no way to save', async () => {
    render(<GeoFenceForm entity={fence({ geometry: polygonDoc([OUTER_RING, HOLE_RING]) })} onDone={vi.fn()} />);

    expect(await screen.findByText(UNREADABLE)).toBeTruthy();
    // No editor and, crucially, no save: there must be nothing to press.
    expect(screen.queryByTestId('fake-map-emit')).toBeNull();
    expect(screen.queryByRole('button', { name: SAVE_ACTION })).toBeNull();
    // 🔴 queryByLabelText(BOUNDARY_LABEL) is deliberately NOT used here. The
    // Boundary field carries no htmlFor — `label[for]` must reference a LABELABLE
    // element and the drawing surface is a div, so the map is named by its own
    // role="application" + aria-label instead. A label-text query would therefore
    // return null whether the editor is on the page or not, making it a check
    // that cannot fail. The label's TEXT is a real signal; its association is not.
    expect(screen.queryByText(BOUNDARY_LABEL)).toBeNull();
  });

  // The counterweight. Without it the test above would pass just as well because
  // the fixture was malformed in some unrelated way (a bad kind, an unclosed
  // ring, a typo in the envelope) — the SAME document minus the hole has to open
  // normally, so the hole is provably what did the refusing.
  it('opens the editor for the same document without the hole', async () => {
    render(<GeoFenceForm entity={fence({ geometry: polygonDoc([OUTER_RING]) })} onDone={vi.fn()} />);

    expect(await screen.findByTestId('fake-map-emit')).toBeTruthy();
    expect(screen.getByText(BOUNDARY_LABEL)).toBeTruthy();
    expect(screen.queryByText(UNREADABLE)).toBeNull();
    expect(button(SAVE_ACTION).disabled).toBe(false);
  });

  // ── 2. THE DOCUMENT ACTUALLY SENT ─────────────────────────────────────────
  it('sends a closed [longitude, latitude] ring in the kind envelope', async () => {
    const onDone = vi.fn();
    render(<GeoFenceForm onDone={onDone} />);

    fireEvent.change(await screen.findByLabelText('Token'), { target: { value: 'rome-yard' } });
    draw(ROME);
    fireEvent.click(button(CREATE_ACTION));

    await waitFor(() => expect(fenceCalls()).toHaveLength(1));
    const request = requestFrom(fenceCalls()[0]);
    expect(request.token).toBe('rome-yard');

    const doc = JSON.parse(request.geometry) as {
      kind: string;
      geometry: { type: string; coordinates: number[][][] };
    };
    expect(doc.kind).toBe('POLYGON_2D');
    expect(doc.geometry.type).toBe('Polygon');
    expect(doc.geometry.coordinates).toHaveLength(1);

    const ring = doc.geometry.coordinates[0];
    // 3 drawn corners become 4 positions, and the last is the first again.
    expect(ring).toHaveLength(4);
    expect(ring[3]).toEqual(ring[0]);
    // The whole ring, in order: this is what a transposition fails.
    expect(ring).toEqual(ROME_RING);

    // The counterweight to every negative case in this file: the happy path
    // really did complete, with the token the operator typed.
    expect(onDone).toHaveBeenCalledWith('Geofence “rome-yard” created');
  });

  // ── 3. SAVE IS GATED ON A VALID RING ──────────────────────────────────────
  //
  // The token is filled FIRST so the ring is the only gate left — otherwise the
  // empty-token guard would be doing the refusing and the ring rule could be
  // absent without the test noticing. Asserting the CHANGE from disabled to
  // enabled is what makes this fail if the gate is removed entirely.
  it('enables the save only once the ring has three corners', async () => {
    render(<GeoFenceForm onDone={vi.fn()} />);

    fireEvent.change(await screen.findByLabelText('Token'), { target: { value: 'rome-yard' } });
    expect(button(CREATE_ACTION).disabled, 'nothing drawn').toBe(true);

    draw(ROME.slice(0, 1));
    expect(button(CREATE_ACTION).disabled, 'one corner').toBe(true);

    draw(ROME.slice(0, 2));
    expect(button(CREATE_ACTION).disabled, 'two corners').toBe(true);

    draw(ROME);
    expect(button(CREATE_ACTION).disabled, 'three corners closes to a ring').toBe(false);
  });

  // ── 4. METADATA SURVIVES AN EDIT ──────────────────────────────────────────
  //
  // 🔴 THE CLAIM IS UNCHANGED AND THE MECHANISM IS INVERTED, WHICH IS WHY THIS
  // ASSERTION FLIPPED. Metadata the form never displays must survive an edit. It used
  // to survive because the form RE-SENT it — updateGeoFence replaced every field it
  // named, so an omitted one was a deleted one. The update is partial now, so a field
  // the request does not name is left alone, and re-sending metadata would mean writing
  // back a copy read when this form opened over whatever it has become since.
  //
  // So the test asserts the request does NOT carry it. A test still asserting the old
  // shape would be demanding the client keep doing the risky thing, and would go green
  // on exactly the regression that reintroduced it.
  it('leaves metadata out of an edit, so the server keeps what it holds', async () => {
    const metadata = '{"site":"rome-yard","installedBy":"ops","costCentre":"CC-4417"}';
    render(<GeoFenceForm entity={fence({ metadata })} onDone={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: SAVE_ACTION }));

    await waitFor(() => expect(fenceCalls()).toHaveLength(1));
    const call = fenceCalls()[0];
    // The existing token is the mutation's first argument, alongside the request.
    expect((call[2] as { token: string }).token).toBe('yard-perimeter');

    const request = requestFrom(call);
    expect('metadata' in request).toBe(false);
    // 🔴 THE COUNTERWEIGHT, and it is what keeps "omitted" from meaning "the form sends
    // nothing at all". The ring the operator did not touch still goes out — geometry is
    // a field this editor OWNS, and leaving it out would mean a fence could never be
    // reshaped. Absence has to be selective to mean anything.
    expect(JSON.parse(request.geometry)).toEqual(JSON.parse(polygonDoc([OUTER_RING])));
  });

  // ── 5. A SERVER REFUSAL IS VISIBLE AND RECOVERABLE ────────────────────────
  it('surfaces a server error inline and leaves the form usable', async () => {
    gqlMock.mockImplementation((service: string) => {
      if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
      return Promise.reject(new GraphQLRequestError('Request failed (503)', 503));
    });

    const onDone = vi.fn();
    render(<GeoFenceForm onDone={onDone} />);

    fireEvent.change(await screen.findByLabelText('Token'), { target: { value: 'rome-yard' } });
    draw(ROME);
    fireEvent.click(button(CREATE_ACTION));

    expect(await screen.findByText('Request failed (503)')).toBeTruthy();
    // The save did not "succeed quietly", and the form is still there to retry
    // from: the ring survived and the button is live again.
    expect(onDone).not.toHaveBeenCalled();
    expect(button(CREATE_ACTION).disabled).toBe(false);
    expect(screen.getByTestId('fake-map-emit')).toBeTruthy();
  });
});

// ── The close gesture ───────────────────────────────────────────────────────
//
// 🔴 These exist because the form ADVERTISED this gesture and did not implement
// it. The map's own aria-label tells the operator "click the first corner to
// close the shape", and FenceMap swallows that click before it can become a
// vertex — so with no onClose handler passed, the documented gesture produced no
// vertex, no state change and no feedback of any kind. Silent, and invisible to
// every other test here.
describe('closing the ring', () => {
  const closeIt = async () => {
    staged.vertices = ROME;
    fireEvent.click(await screen.findByTestId('fake-map-emit'));
    fireEvent.click(screen.getByTestId('fake-map-close'));
  };

  it('stops the map taking further corners and says so', async () => {
    render(<GeoFenceForm onDone={vi.fn()} />);
    // Before: drawing is live and nothing claims otherwise.
    expect((await screen.findByTestId('fake-map')).getAttribute('data-closed')).toBe('false');
    expect(screen.queryByText(CLOSED_HINT)).toBeNull();

    await closeIt();

    expect(screen.getByTestId('fake-map').getAttribute('data-closed')).toBe('true');
    expect(screen.getByText(CLOSED_HINT)).toBeTruthy();
    // 🔴 And it is still EDITABLE. Closing must not freeze the shape — an earlier
    // version conflated the two and made a finished ring unmodifiable.
    expect(screen.getByTestId('fake-map').getAttribute('data-disabled')).toBe('false');
  });

  it('does not discard the ring — a closed shape is still saveable', async () => {
    render(<GeoFenceForm onDone={vi.fn()} />);
    fireEvent.change(await screen.findByLabelText('Token'), { target: { value: 'rome-yard' } });
    await closeIt();
    await waitFor(() =>
      expect(
        (screen.getByRole('button', { name: CREATE_ACTION }) as HTMLButtonElement).disabled,
      ).toBe(false),
    );
  });

  it('reopens for drawing when a corner is undone', async () => {
    render(<GeoFenceForm onDone={vi.fn()} />);
    await closeIt();
    expect(screen.getByTestId('fake-map').getAttribute('data-closed')).toBe('true');

    fireEvent.click(screen.getByRole('button', { name: UNDO_ACTION }));

    expect(screen.getByTestId('fake-map').getAttribute('data-closed')).toBe('false');
    expect(screen.queryByText(CLOSED_HINT)).toBeNull();
  });

  it('opens an EXISTING fence closed, so a click cannot append to a finished ring', async () => {
    // The counterweight to the create tests above, which all start open: the
    // initial state is derived, not hardcoded to either value.
    render(<GeoFenceForm entity={fence({ geometry: polygonDoc([OUTER_RING]) })} onDone={vi.fn()} />);
    expect((await screen.findByTestId('fake-map')).getAttribute('data-closed')).toBe('true');
  });

  // 🔴 THE REGRESSION THIS SEPARATION EXISTS TO PREVENT. The form used to pass
  // `busy || closed` as `disabled`, so a saved fence — which opens closed —
  // arrived with every corner frozen. Nothing could be dragged, nothing removed;
  // the only way to change a stored shape was to clear it and start again. No
  // test noticed, because the fake ignored both props.
  it('leaves an existing fence EDITABLE even though it opens closed', async () => {
    render(<GeoFenceForm entity={fence({ geometry: polygonDoc([OUTER_RING]) })} onDone={vi.fn()} />);
    const fake = await screen.findByTestId('fake-map');
    expect(fake.getAttribute('data-closed')).toBe('true');
    expect(fake.getAttribute('data-disabled')).toBe('false');
  });

  it('freezes the map only while a save is in flight', async () => {
    let release: (v: unknown) => void = () => {};
    gqlMock.mockImplementation((service: string) => {
      if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
      return new Promise((res) => {
        release = res;
      });
    });
    render(<GeoFenceForm entity={fence({ geometry: polygonDoc([OUTER_RING]) })} onDone={vi.fn()} />);
    const save = await screen.findByRole('button', { name: SAVE_ACTION });
    expect(screen.getByTestId('fake-map').getAttribute('data-disabled')).toBe('false');

    fireEvent.click(save);
    await waitFor(() =>
      expect(screen.getByTestId('fake-map').getAttribute('data-disabled')).toBe('true'),
    );
    release({ updateGeoFence: {} });
  });
});

// ── The corner counter and the basemap fields ───────────────────────────────
describe('the corner counter', () => {
  it('counts the corners the operator placed, not the positions sent', () => {
    // 🔴 The wire carries a CLOSING position too, so a 3-corner ring is 4
    // positions. An earlier draft showed that 4, which disagreed with the list
    // column's "Corners: 3" for the same fence with nothing to bridge them.
    // The limit shown is correspondingly the corner limit, one below the
    // server's position limit — otherwise the UI offers a corner the server
    // refuses.
    render(<GeoFenceForm onDone={vi.fn()} />);
    staged.vertices = ROME;
    fireEvent.click(screen.getByTestId('fake-map-emit'));

    expect(screen.getByTestId('fence-vertex-count').textContent).toBe(
      `${ROME.length} of ${MAX_FENCE_VERTICES} corners`,
    );
    expect(MAX_FENCE_VERTICES).toBe(511);
  });

  it('uses the singular form for a single corner', () => {
    // The _one plural was unreachable while the counter reported positions
    // (which are 0 or >= 2, never 1). Counting corners makes it reachable, so it
    // is a string that can actually render rather than dead weight.
    render(<GeoFenceForm onDone={vi.fn()} />);
    staged.vertices = [ROME[0]];
    fireEvent.click(screen.getByTestId('fake-map-emit'));

    expect(screen.getByTestId('fence-vertex-count').textContent).toBe(
      `1 of ${MAX_FENCE_VERTICES} corners`,
    );
  });
});

describe('the basemap fields', () => {
  it('does not apply a tile URL until the field is left', async () => {
    // 🔴 The map is torn down and rebuilt whenever its style changes, so applying
    // per keystroke rebuilt it ~40 times while typing a template — each rebuild
    // firing real tile requests at a truncated URL and resetting the camera.
    // The credit line is seeded first so this test measures the BLUR and nothing else.
    // Without it the tile URL would be discarded as uncredited on commit, and the
    // assertion below would be reading the licence rule while claiming to read the
    // debounce — passing or failing for a reason it never mentions.
    window.localStorage.setItem('dc.geofence.tileAttribution', '© Mine');
    render(<GeoFenceForm onDone={vi.fn()} />);
    const input = (await screen.findByLabelText('Tile URL')) as HTMLInputElement;

    fireEvent.change(input, { target: { value: 'https://tiles.example.com/{z}/' } });
    expect(input.value).toBe('https://tiles.example.com/{z}/'); // the draft follows the keyboard
    expect(screen.getByTestId('fake-map').getAttribute('data-tile-url')).toBe('');

    fireEvent.blur(input);
    expect(screen.getByTestId('fake-map').getAttribute('data-tile-url')).toBe(
      'https://tiles.example.com/{z}/',
    );
  });

  it('remembers the applied tile URL in this browser only', () => {
    render(<GeoFenceForm onDone={vi.fn()} />);
    const input = screen.getByLabelText('Tile URL');
    fireEvent.change(input, { target: { value: 'https://tiles.example.com/a.png' } });
    expect(window.localStorage.getItem('dc.geofence.tileUrl')).toBeNull(); // not yet
    fireEvent.blur(input);
    expect(window.localStorage.getItem('dc.geofence.tileUrl')).toBe(
      'https://tiles.example.com/a.png',
    );
  });
});

// ── Escapability ────────────────────────────────────────────────────────────
describe('a closed ring that loses corners', () => {
  // 🔴 THE DEAD-EDITOR TRAP. While closed, a map click does not add a corner.
  // Alt-remove the corners of a saved fence one by one and, without the reset,
  // the operator reaches an empty map with clicks inert, Undo and Clear both
  // disabled (nothing to undo), and no control anywhere that reopens drawing.
  // The form has to be abandoned. Removal happens on the MAP, so no button test
  // would have found this.
  it('reopens for drawing when it falls below three corners', async () => {
    render(<GeoFenceForm entity={fence({ geometry: polygonDoc([OUTER_RING]) })} onDone={vi.fn()} />);
    expect((await screen.findByTestId('fake-map')).getAttribute('data-closed')).toBe('true');

    staged.vertices = ROME.slice(0, 2); // what a removal leaves behind
    fireEvent.click(screen.getByTestId('fake-map-emit'));

    expect(screen.getByTestId('fake-map').getAttribute('data-closed')).toBe('false');
  });

  it('stays closed while it still encloses something', async () => {
    // The counterweight: without it, the test above would also pass against a
    // form that had simply stopped closing rings at all.
    render(<GeoFenceForm entity={fence({ geometry: polygonDoc([OUTER_RING]) })} onDone={vi.fn()} />);
    expect((await screen.findByTestId('fake-map')).getAttribute('data-closed')).toBe('true');

    staged.vertices = ROME; // still three corners
    fireEvent.click(screen.getByTestId('fake-map-emit'));

    expect(screen.getByTestId('fake-map').getAttribute('data-closed')).toBe('true');
  });

  it('leaves an emptied ring drawable rather than inert', async () => {
    render(<GeoFenceForm entity={fence({ geometry: polygonDoc([OUTER_RING]) })} onDone={vi.fn()} />);
    await screen.findByTestId('fake-map');
    staged.vertices = [];
    fireEvent.click(screen.getByTestId('fake-map-emit'));

    const fake = screen.getByTestId('fake-map');
    expect(fake.getAttribute('data-closed')).toBe('false');
    expect(fake.getAttribute('data-disabled')).toBe('false');
  });
});

describe('the edit hint', () => {
  it('is absent on an untouched map, where its gestures have no target', async () => {
    render(<GeoFenceForm onDone={vi.fn()} />);
    await screen.findByTestId('fake-map');
    expect(screen.queryByTestId('fence-edit-hint')).toBeNull();
  });

  it('appears once there is a corner to edit', async () => {
    render(<GeoFenceForm onDone={vi.fn()} />);
    staged.vertices = ROME;
    fireEvent.click(await screen.findByTestId('fake-map-emit'));
    expect(screen.getByTestId('fence-edit-hint')).toBeTruthy();
  });
});

// ── The basemap cascade (ADR-079) ───────────────────────────────────────────
//
// The editor's own two fields are a PERSONAL override, kept in localStorage, sitting
// on top of the tenant's basemap. What these assert is what the form hands the map —
// the real resolveBasemap/fallbackView run underneath, so the precedence rules are
// exercised rather than restated.

const TENANT_TILES = 'https://tenant.example.invalid/{z}/{x}/{y}.png';
const PERSONAL_TILES = 'https://personal.example.invalid/{z}/{x}/{y}.png';

describe('the tenant basemap', () => {
  it('is used when this browser has no personal override', async () => {
    tenantBasemap.value = { tileUrl: TENANT_TILES, attribution: '© Tenant Tiles' };
    render(<GeoFenceForm onDone={vi.fn()} />);

    const fake = await screen.findByTestId('fake-map');
    expect(fake.getAttribute('data-tile-url')).toBe(TENANT_TILES);
    expect(fake.getAttribute('data-attribution')).toBe('© Tenant Tiles');
  });

  it('is overridden by this browser’s own tile URL', async () => {
    tenantBasemap.value = { tileUrl: TENANT_TILES, attribution: '© Tenant Tiles' };
    window.localStorage.setItem('dc.geofence.tileUrl', PERSONAL_TILES);
    window.localStorage.setItem('dc.geofence.tileAttribution', '© Mine');
    render(<GeoFenceForm onDone={vi.fn()} />);

    const fake = await screen.findByTestId('fake-map');
    expect(fake.getAttribute('data-tile-url')).toBe(PERSONAL_TILES);
    expect(fake.getAttribute('data-attribution')).toBe('© Mine');
  });

  // 🔴 The licence rule, at the surface an author actually types into, and it has TWO
  // halves. Pasting a tile URL into the personal field must not leave the TENANT's
  // credit line under it — and must not draw those tiles with no credit line either.
  // The second half is why the override is discarded whole rather than half-applied:
  // this tier is entered in the browser and never reaches the server's validation, so
  // there is nowhere else the pair can be enforced.
  it('ignores a personal tile URL that arrives with no credit line', async () => {
    tenantBasemap.value = { tileUrl: TENANT_TILES, attribution: '© Tenant Tiles' };
    window.localStorage.setItem('dc.geofence.tileUrl', PERSONAL_TILES);
    render(<GeoFenceForm onDone={vi.fn()} />);

    const fake = await screen.findByTestId('fake-map');
    expect(fake.getAttribute('data-tile-url')).toBe(TENANT_TILES);
    expect(fake.getAttribute('data-attribution')).toBe('© Tenant Tiles');
    // 🔴 And SAYS so. A rule this silent is indistinguishable from the field being
    // broken — which is the complaint that started this whole line of work.
    expect(screen.getByTestId('geofence-tile-uncredited')).toBeTruthy();
  });

  // 🔴 A SURVIVING MUTATION, found in review, and the more instructive of the two
  // controls. Dropping the `tileUrl` half of the condition — warning whenever the
  // ATTRIBUTION is blank, regardless of whether a URL was ever entered — passed the
  // whole suite, because no test rendered the form in the state nearly every user is
  // actually in: both fields empty, inheriting the tenant basemap.
  //
  // The lesson generalises past this test. A negative control proves a warning CAN be
  // absent; it does not prove it is absent on the input that will OCCUR. The case
  // below was picked because it exercises the feature; this one is picked because it
  // is the default.
  it('says nothing at all when no personal basemap has been entered', async () => {
    tenantBasemap.value = { tileUrl: TENANT_TILES, attribution: '© Tenant Tiles' };
    render(<GeoFenceForm onDone={vi.fn()} />);

    await screen.findByTestId('fake-map');
    expect(screen.queryByTestId('geofence-tile-uncredited')).toBeNull();
  });

  // The other control: the message must be capable of being absent WITH a URL set,
  // and the override must still work when it is complete. Without this, "always
  // ignore the personal URL" and "always show the warning" both pass.
  it('applies a personal tile source that brings its own credit line, with no warning', async () => {
    tenantBasemap.value = { tileUrl: TENANT_TILES, attribution: '© Tenant Tiles' };
    window.localStorage.setItem('dc.geofence.tileUrl', PERSONAL_TILES);
    window.localStorage.setItem('dc.geofence.tileAttribution', '© Mine');
    render(<GeoFenceForm onDone={vi.fn()} />);

    const fake = await screen.findByTestId('fake-map');
    expect(fake.getAttribute('data-tile-url')).toBe(PERSONAL_TILES);
    expect(fake.getAttribute('data-attribution')).toBe('© Mine');
    expect(screen.queryByTestId('geofence-tile-uncredited')).toBeNull();
  });

  it('opens a NEW fence at the tenant centre, in [longitude, latitude] order', async () => {
    tenantBasemap.value = { tileUrl: TENANT_TILES, attribution: '© T', centerLat: 33.7468, centerLon: -84.3903, zoom: 12 };
    render(<GeoFenceForm onDone={vi.fn()} />);

    const fake = await screen.findByTestId('fake-map');
    expect(fake.getAttribute('data-initial-center')).toBe('-84.3903,33.7468');
    expect(fake.getAttribute('data-initial-zoom')).toBe('12');
  });

  // 🔴 A fallback, never an override. Editing a fence in Rome from a tenant centred on
  // Atlanta must not open on Atlanta — the existing ring's bounds win.
  it('does NOT apply the tenant centre to a fence that already has a ring', async () => {
    tenantBasemap.value = { tileUrl: TENANT_TILES, attribution: '© T', centerLat: 33.7468, centerLon: -84.3903, zoom: 12 };
    render(<GeoFenceForm entity={fence({ geometry: polygonDoc([OUTER_RING]) })} onDone={vi.fn()} />);

    const fake = await screen.findByTestId('fake-map');
    expect(fake.getAttribute('data-initial-center')).toBe('');
  });

  // The counterweight: with nothing configured anywhere, the editor behaves exactly as
  // it did before this feature existed.
  it('passes no basemap at all when neither tier has one', async () => {
    render(<GeoFenceForm onDone={vi.fn()} />);

    const fake = await screen.findByTestId('fake-map');
    expect(fake.getAttribute('data-tile-url')).toBe('');
    expect(fake.getAttribute('data-initial-center')).toBe('');
  });
});
