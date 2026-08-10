// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The history panel answers one question — what did the engine see at version N —
// and the whole value of the answer is that its three outcomes stay distinct.
//
// Two seams are faked: the GraphQL transport, and `./FenceMap` (MapLibre cannot
// run in jsdom). The fake map reflects `vertices`, `ghost` and `disabled` into
// the DOM, because those three are exactly what this panel is responsible for
// passing — a fake that dropped them would make a panel that never passed them
// look identical to one that does. That has already happened twice in this area.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { FenceMapProps } from './FenceMap';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

vi.mock('./FenceMap', () => ({
  FenceMap: ({ vertices, ghost, disabled }: FenceMapProps) => (
    <div
      data-testid="fake-map"
      data-vertices={vertices.length}
      data-ghost={ghost?.length ?? 0}
      data-disabled={String(!!disabled)}
    />
  ),
}));

import { FenceHistoryPanel } from './FenceHistoryPanel';
import type { GeoFence } from '@/lib/api/geofences';

afterEach(cleanup);
beforeEach(() => gqlMock.mockReset());

const RING = [
  [12.49, 41.89],
  [12.5, 41.89],
  [12.495, 41.9],
  [12.49, 41.89],
];
const OTHER_RING = [
  [1, 1],
  [2, 1],
  [1.5, 2],
  [1, 1],
];
const doc = (rings: number[][][]) =>
  JSON.stringify({ kind: 'POLYGON_2D', geometry: { type: 'Polygon', coordinates: rings } });

function fence(over: Partial<GeoFence> = {}): GeoFence {
  return {
    id: 'gf-1',
    token: 'yard',
    name: 'Yard',
    description: null,
    geometry: doc([RING]),
    kind: 'POLYGON_2D',
    metadata: null,
    createdAt: '2026-08-09T12:00:00Z',
    ...over,
  };
}

/** Answers the two archive queries; `fences` is what the snapshot contains. */
function transport(latest: number, fences: { token: string; geometry: string }[]) {
  gqlMock.mockImplementation((_service: string, document: { toString(): string }) => {
    const q = String(document);
    if (q.includes('currentFenceSetVersion')) return Promise.resolve({ currentFenceSetVersion: latest });
    return Promise.resolve({ geoFenceSetSnapshot: { version: latest, fences } });
  });
}

const NONE_RECORDED =
  'No fence set has been recorded yet. One is frozen every time a geofence is created, ' +
  'changed or deleted.';

describe('FenceHistoryPanel', () => {
  it('shows the frozen ring, with the current shape as a ghost beside it', async () => {
    transport(7, [{ token: 'yard', geometry: doc([OTHER_RING]) }]);
    render(<FenceHistoryPanel entity={fence()} />);

    const map = await screen.findByTestId('fake-map');
    // 3 corners from the SNAPSHOT, 3 from the current geometry as the ghost —
    // asserted separately so a panel that passed the same ring to both would fail.
    expect(map.getAttribute('data-vertices')).toBe('3');
    expect(map.getAttribute('data-ghost')).toBe('3');
  });

  it('renders the past read-only, because a frozen version cannot be rewritten', async () => {
    transport(7, [{ token: 'yard', geometry: doc([OTHER_RING]) }]);
    render(<FenceHistoryPanel entity={fence()} />);
    expect((await screen.findByTestId('fake-map')).getAttribute('data-disabled')).toBe('true');
  });

  // 🔴 THE THREE OUTCOMES. Collapsing any pair of them tells an operator
  // something false, so each is asserted to render its OWN thing and not the
  // others — "no map" alone would be satisfied by all three.
  it('says the fence was ABSENT from that version rather than drawing nothing', async () => {
    transport(7, [{ token: 'somewhere-else', geometry: doc([OTHER_RING]) }]);
    render(<FenceHistoryPanel entity={fence()} />);

    expect(await screen.findByTestId('fence-history-absent')).toBeTruthy();
    expect(screen.queryByTestId('fake-map')).toBeNull();
    expect(screen.queryByTestId('fence-history-unreadable')).toBeNull();
  });

  it('says the shape was UNREADABLE here, not that the fence was missing', async () => {
    const holed = doc([RING, [[12.492, 41.892], [12.494, 41.892], [12.493, 41.894], [12.492, 41.892]]]);
    transport(7, [{ token: 'yard', geometry: holed }]);
    render(<FenceHistoryPanel entity={fence()} />);

    expect(await screen.findByTestId('fence-history-unreadable')).toBeTruthy();
    expect(screen.queryByTestId('fence-history-absent')).toBeNull();
    expect(screen.queryByTestId('fake-map')).toBeNull();
  });

  it('explains an empty archive instead of showing version zero', async () => {
    // 🔴 Zero does not mean "version 0" — it means nothing has ever been minted.
    // Rendering a stepper pinned at 0 would invite an operator to decode that.
    transport(0, []);
    render(<FenceHistoryPanel entity={fence()} />);

    expect(await screen.findByText(NONE_RECORDED)).toBeTruthy();
    expect(screen.queryByLabelText('Fence set version')).toBeNull();
  });

  it('opens at the latest version and marks it as current', async () => {
    transport(7, [{ token: 'yard', geometry: doc([OTHER_RING]) }]);
    render(<FenceHistoryPanel entity={fence()} />);

    const input = (await screen.findByLabelText('Fence set version')) as HTMLInputElement;
    expect(input.value).toBe('7');
    expect(screen.getByTestId('fence-history-is-current')).toBeTruthy();
  });

  it('steps back through versions and stops at the first', async () => {
    transport(3, [{ token: 'yard', geometry: doc([OTHER_RING]) }]);
    render(<FenceHistoryPanel entity={fence()} />);

    const older = await screen.findByRole('button', { name: 'Older' });
    const input = () => screen.getByLabelText('Fence set version') as HTMLInputElement;

    fireEvent.click(older);
    await waitFor(() => expect(input().value).toBe('2'));
    // Stepping away from the latest must drop the "current" marker, or it would
    // claim an old version is what events are being stamped with now.
    expect(screen.queryByTestId('fence-history-is-current')).toBeNull();

    fireEvent.click(older);
    await waitFor(() => expect(input().value).toBe('1'));
    expect((older as HTMLButtonElement).disabled).toBe(true);
  });

  it('clamps a typed version into the range that exists', async () => {
    transport(5, [{ token: 'yard', geometry: doc([OTHER_RING]) }]);
    render(<FenceHistoryPanel entity={fence()} />);
    const input = (await screen.findByLabelText('Fence set version')) as HTMLInputElement;

    fireEvent.change(input, { target: { value: '900' } });
    await waitFor(() => expect(input.value).toBe('5'));

    fireEvent.change(input, { target: { value: '0' } });
    await waitFor(() => expect(input.value).toBe('1'));
  });

  it('asks the server for the version the operator chose', async () => {
    transport(4, [{ token: 'yard', geometry: doc([OTHER_RING]) }]);
    render(<FenceHistoryPanel entity={fence()} />);
    await screen.findByTestId('fake-map');

    fireEvent.click(screen.getByRole('button', { name: 'Older' }));

    await waitFor(() => {
      const asked = gqlMock.mock.calls
        .filter(([, d]) => String(d).includes('geoFenceSetSnapshot'))
        .map(([, , vars]) => (vars as { version: number }).version);
      // The panel must actually re-query — rendering the previous snapshot under a
      // new number would look identical and be a lie.
      expect(asked).toContain(3);
    });
  });
});
