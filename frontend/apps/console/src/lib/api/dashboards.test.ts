// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The publish gate.
//
// 🔴 THIS FILE EXISTS BECAUSE A DISABLED BUTTON IS NOT A GATE. The version drawer also
// disables Publish and lists what is wrong, but that is one rendering of one component:
// it can be bypassed by a caller that does not go through it, and it goes green the
// moment someone drops the prop. The refusal under test here is publishDashboard's own,
// which every publish in the console passes through — so the assertions below are about
// what happens BEFORE the request, and the sharpest one is that no request happens.

import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { WidgetInstance, WidgetType } from '@devicechain/dashboards';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock('@devicechain/client', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  gql: gqlMock,
}));

const {
  createDashboard,
  publishDashboard,
  activateDashboardVersion,
  getPublishedDashboard,
  getPublishedVersion,
  isNotPublishedError,
  INVALID_DEFINITION_MARKER,
  UNRESOLVED_WIDGETS_MARKER,
} = await import('./dashboards');
const { parseDashboardDefinition } = await import('@devicechain/dashboards');
const { createHostWidgetRegistry } = await import('@devicechain/host-widgets');
const { ACME_TANK_WIDGET_JSON, acmeTankWidget } = await import('@devicechain/host-widgets/fixtures/acme-tank');
const { GraphQLRequestError } = await import('@devicechain/client');

function widget(id: string, type: WidgetType, options?: Record<string, unknown>): WidgetInstance {
  return { id, type, layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 4, rows: 2 } }, options };
}

beforeEach(() => {
  gqlMock.mockReset();
  gqlMock.mockResolvedValue({
    publishDashboard: { version: { version: 7 }, dashboard: { updatedAt: '2026-08-02T10:00:00Z' } },
  });
});

describe('publishDashboard', () => {
  it('publishes a board whose widgets are all configured', async () => {
    const definition = { widgets: [widget('w1', 'gauge', { title: 'Temp', min: 0, max: 100 })] };

    await expect(
      publishDashboard('ops', { definition, label: 'v1', expectedUpdatedAt: '2026-08-02T10:00:00Z' }),
    ).resolves.toEqual({ version: 7, updatedAt: '2026-08-02T10:00:00Z' });

    expect(gqlMock).toHaveBeenCalledTimes(1);
    const [area, , variables] = gqlMock.mock.calls[0];
    expect(area).toBe('dashboard-management');
    expect(variables).toMatchObject({
      token: 'ops',
      label: 'v1',
      // 🔑 The definition validated and the baseline sent must describe the same
      // document. The mutation carries no definition — the server freezes the draft it
      // holds — so expectedUpdatedAt is the only thing tying the check to what is
      // actually published, and dropping it would make every green check meaningless
      // rather than merely weaker.
      expectedUpdatedAt: '2026-08-02T10:00:00Z',
    });
  });

  // The counterweight to the refusals below: a gate is only useful while well-formed
  // input still passes untouched, and 'no options at all' is the shape a freshly added
  // chart carries.
  it('publishes a widget that declares no options when none are required', async () => {
    await expect(
      publishDashboard('ops', {
        definition: { widgets: [widget('w1', 'timeseries-chart')] },
        expectedUpdatedAt: null,
      }),
    ).resolves.toEqual({ version: 7, updatedAt: '2026-08-02T10:00:00Z' });
    expect(gqlMock).toHaveBeenCalledTimes(1);
  });

  // 🔴 THE ASSERTION THAT MATTERS: not that it rejected, but that it rejected BEFORE the
  // network. A check that runs after the mutation has already been accepted would leave
  // an immutable version published and an error on screen — strictly worse than no gate,
  // because the author would believe the publish failed.
  it('refuses a misconfigured board without sending anything', async () => {
    const definition = {
      widgets: [
        widget('w1', 'gauge', { title: 'Temp', min: 0, max: 100 }),
        widget('w2', 'image', { title: 'Floor plan' }), // required `url` never set
      ],
    };

    await expect(
      publishDashboard('ops', { definition, expectedUpdatedAt: null }),
    ).rejects.toThrow(INVALID_DEFINITION_MARKER);
    expect(gqlMock).not.toHaveBeenCalled();
  });

  // Reaching this throw means the drawer's own check was bypassed, so the message is
  // read by whoever is debugging that — it names the widget ID (the handle that is
  // stable and greppable, where a title is neither) and the option that is wrong, not
  // just that "something" was invalid. The author-facing wording is the drawer's job,
  // through the translated per-code strings.
  it('names the offending widget and option in the error', async () => {
    const definition = { widgets: [widget('w2', 'image', { title: 'Floor plan' })] };
    await expect(
      publishDashboard('ops', { definition, expectedUpdatedAt: null }),
    ).rejects.toThrow(/w2 \(image\).*url/);
  });

  // Every issue code is fatal, and the one that is easiest to argue should not be — a
  // leftover key the renderer ignores — is exactly the one that makes an author believe
  // they configured something. `minimum` on a gauge reads as a scale bound and does
  // nothing at all. The repair for it is stripUnknownOptions, not a weaker gate.
  it('refuses a board carrying an option no widget reads', async () => {
    const definition = { widgets: [widget('g', 'gauge', { min: 0, max: 10, minimum: 5 })] };
    await expect(
      publishDashboard('ops', { definition, expectedUpdatedAt: null }),
    ).rejects.toThrow(/minimum/);
    expect(gqlMock).not.toHaveBeenCalled();
  });
});

// A widget this console cannot resolve blocks a NEW version -- before any request -- while
// everything that resolves still publishes. The board is parsed the way the console parses it
// (with a catalog), so an unresolved widget is the parser's own placeholder.
describe('publishDashboard with extension widgets', () => {
  const layout = { base: { mode: 'grid' as const, col: 0, row: 0, cols: 4, rows: 2 } };
  const board = (widgets: unknown[]) => ({ title: 'T', slots: { tank: { type: 'device' } }, widgets });
  const installed = createHostWidgetRegistry([acmeTankWidget]);
  const tank = { ...ACME_TANK_WIDGET_JSON, layout };

  it('publishes a board whose extension widget is installed, judged by ITS schema', async () => {
    const definition = parseDashboardDefinition(board([tank]), { catalog: installed.catalog });
    await expect(
      publishDashboard('ops', { definition, catalog: installed.catalog, expectedUpdatedAt: null }),
    ).resolves.toEqual({ version: 7, updatedAt: '2026-08-02T10:00:00Z' });
    expect(gqlMock).toHaveBeenCalledTimes(1);
  });

  it("reports an installed extension's invalid option, which a built-ins-only gate could not judge", async () => {
    const bad = { ...tank, options: { ...tank.options, capacity: 'lots' } };
    const definition = parseDashboardDefinition(board([bad]), { catalog: installed.catalog });
    await expect(
      publishDashboard('ops', { definition, catalog: installed.catalog, expectedUpdatedAt: null }),
    ).rejects.toThrow(/tank-1 \(acme\/tank-level\).*capacity/);
    expect(gqlMock).not.toHaveBeenCalled();
  });

  it('refuses, before any request, a board with a widget whose package is not installed', async () => {
    const none = createHostWidgetRegistry([]);
    const definition = parseDashboardDefinition(board([tank]), { catalog: none.catalog });
    await expect(publishDashboard('ops', { definition, expectedUpdatedAt: null })).rejects.toThrow(
      UNRESOLVED_WIDGETS_MARKER,
    );
    expect(gqlMock).not.toHaveBeenCalled();
  });

  it('refuses a built-in saved against a settings version this release does not read', async () => {
    const stale = { id: 'g', type: 'gauge', widget: { version: '1.0.0', optionsVersion: 9 }, layout, options: {} };
    const definition = parseDashboardDefinition(board([stale]), { catalog: installed.catalog });
    await expect(publishDashboard('ops', { definition, expectedUpdatedAt: null })).rejects.toThrow(
      /g \(gauge\).*settings version/,
    );
    expect(gqlMock).not.toHaveBeenCalled();
  });
});

// Publishing activates, and a publish is not a draft edit: the returned updatedAt is the
// draft's UNCHANGED one, which the caller keeps as its save baseline.
describe('publishing and serving', () => {
  it('activates a version without sending any draft precondition', async () => {
    gqlMock.mockResolvedValue({ activateDashboardVersion: { publishedVersion: 3 } });
    await expect(activateDashboardVersion('ops', 3)).resolves.toBe(3);
    const [area, , variables] = gqlMock.mock.calls[0];
    expect(area).toBe('dashboard-management');
    expect(variables).toEqual({ token: 'ops', version: 3 });
  });

  it('reads the published snapshot, not the draft', async () => {
    gqlMock.mockResolvedValue({
      publishedDashboard: { token: 'ops', version: 2, definition: '{"live":true}' },
    });
    const live = await getPublishedDashboard('ops');
    expect(live?.definition).toBe('{"live":true}');
    // The query text names the published door and never selects the draft's updatedAt.
    const doc = String(gqlMock.mock.calls[0][1]);
    expect(doc).toContain('publishedDashboard(token: $token)');
    expect(doc).not.toContain('updatedAt');
  });

  it('reads which version is live from metadata only', async () => {
    gqlMock.mockResolvedValue({ dashboard: { publishedVersion: 4 } });
    await expect(getPublishedVersion('ops')).resolves.toBe(4);
    expect(String(gqlMock.mock.calls[0][1])).not.toContain('definition');
  });

  it('recognises the NOT_PUBLISHED refusal by its code, not its wording', () => {
    const refused = new GraphQLRequestError('anything at all', 200, [
      { message: 'anything at all', extensions: { code: 'NOT_PUBLISHED' } },
    ]);
    expect(isNotPublishedError(refused)).toBe(true);
    expect(
      isNotPublishedError(
        new GraphQLRequestError('dashboard has not been published', 200, [
          { message: 'dashboard has not been published' },
        ]),
      ),
    ).toBe(false);
    expect(isNotPublishedError(new Error('NOT_PUBLISHED'))).toBe(false);
  });
});

describe('createDashboard', () => {
  it('sends the definition it is given verbatim (an import, a clone or a template)', async () => {
    gqlMock.mockResolvedValue({ createDashboard: { token: 'copy' } });
    const definition = '{"schemaVersion":1,"title":"Plant","widgets":[]}';
    await createDashboard({ token: 'copy', name: 'Copy', definition });
    expect(gqlMock).toHaveBeenCalledTimes(1);
    expect(gqlMock.mock.calls[0][2]).toEqual({
      request: { token: 'copy', name: 'Copy', description: null, definition },
    });
  });

  it('seeds an empty board titled with the name when no definition is given', async () => {
    gqlMock.mockResolvedValue({ createDashboard: { token: 'blank' } });
    await createDashboard({ token: 'blank', name: 'Blank' });
    const sent = (gqlMock.mock.calls[0][2] as { request: { definition: string } }).request.definition;
    expect(JSON.parse(sent)).toMatchObject({ title: 'Blank', widgets: [] });
  });
});
