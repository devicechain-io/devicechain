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
vi.mock('@devicechain/client', () => ({ gql: gqlMock }));

const { publishDashboard, INVALID_DEFINITION_MARKER } = await import('./dashboards');

function widget(id: string, type: WidgetType, options?: Record<string, unknown>): WidgetInstance {
  return { id, type, layout: { base: { col: 0, row: 0, colSpan: 4, rowSpan: 2, z: 0 } }, options };
}

beforeEach(() => {
  gqlMock.mockReset();
  gqlMock.mockResolvedValue({ publishDashboard: { version: 7 } });
});

describe('publishDashboard', () => {
  it('publishes a board whose widgets are all configured', async () => {
    const definition = { widgets: [widget('w1', 'gauge', { title: 'Temp', min: 0, max: 100 })] };

    await expect(
      publishDashboard('ops', { definition, label: 'v1', expectedUpdatedAt: '2026-08-02T10:00:00Z' }),
    ).resolves.toEqual({ version: 7 });

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
    ).resolves.toEqual({ version: 7 });
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
