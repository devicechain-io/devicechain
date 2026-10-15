// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The replay player parses its board with the deployment's catalog and plays it with the
// registry that parsed it: an installed extension widget on a recorded board is drawn from
// the recording, not shown as a placeholder, and a built-in saved against another settings
// version is reported, not silently rendered.

import { UNKNOWN_WIDGET_TYPE } from '@devicechain/dashboards';
import { SITE, fixtureJson } from '../../../packages/dashboards/src/testing/recording-fixture';
import { createHostWidgetRegistry } from '@devicechain/host-widgets';
import { ACME_TANK_WIDGET_JSON, acmeTankWidget } from '@devicechain/host-widgets/fixtures/acme-tank';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ registry: undefined as unknown }));

// The deployment's composition, with the fixture package installed in it.
vi.mock('@devicechain/host-widgets', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/host-widgets')>();
  return { ...actual, hostWidgetRegistry: () => h.registry };
});

import ReplayApp from './App';
import { setReplayLocale } from './i18n/config';
import { loadReplay, sha256Hex } from './load';
import { BOARD_URL, ORIGIN, RECORDING_URL, manualTicker, stubFetch } from './testing/harness';

beforeEach(async () => {
  await setReplayLocale('en');
  h.registry = createHostWidgetRegistry([acmeTankWidget]);
});
afterEach(cleanup);

const box = { base: { mode: 'grid' as const, col: 0, row: 0, cols: 8, rows: 4 } };

// The shared fixture board, bound to a device the fixture recording holds.
function boardText(extra: unknown[] = []): string {
  return JSON.stringify({
    title: 'Plant',
    // A replay declares its site through exactly one anchor slot.
    slots: {
      site: { type: 'anchor', defaultBinding: { kind: 'anchor', anchor: SITE } },
      tank: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'sp-hauler-01' } },
    },
    widgets: [{ ...ACME_TANK_WIDGET_JSON, datasource: { kind: 'slot', slot: 'tank', measurements: ['fuel_pct'] } }, ...extra],
  });
}

async function files(board: string) {
  const bytes = new TextEncoder().encode(board);
  const doc = fixtureJson();
  doc.board = { ...doc.board, sha256: await sha256Hex(bytes.buffer as ArrayBuffer) };
  return { [RECORDING_URL]: JSON.stringify(doc), [BOARD_URL]: board };
}

describe('the replay player with an installed extension package', () => {
  it('parses the recorded board as that widget, and plays it with the registry that parsed it', async () => {
    const stub = stubFetch(await files(boardText()));
    const replay = await loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: stub.fetchFn, origin: ORIGIN });
    expect(replay.definition.widgets[0].type).toBe(ACME_TANK_WIDGET_JSON.type);
    expect(replay.registry).toBe(h.registry);
  });

  it("flags a built-in saved against another settings version instead of rendering it", async () => {
    const stale = { id: 'g', type: 'gauge', widget: { version: '1.0.0', optionsVersion: 9 }, layout: { base: { ...box.base, row: 4 } }, options: {} };
    const stub = stubFetch(await files(boardText([stale])));
    const replay = await loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: stub.fetchFn, origin: ORIGIN });
    const gauge = replay.definition.widgets.find((w) => w.id === 'g')!;
    expect(gauge.type).toBe(UNKNOWN_WIDGET_TYPE);
    expect(gauge.reason?.code).toBe('options-version');
  });

  it('draws the extension from the recording on the page', async () => {
    const stub = stubFetch(await files(boardText()));
    render(
      <ReplayApp recordingUrl={RECORDING_URL} boardUrl={BOARD_URL} fetchFn={stub.fetchFn} origin={ORIGIN} ticker={manualTicker()} />,
    );
    await screen.findByTestId('transport-bar');
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30));
    });
    // The recorded opening value of the bound measurement, printed by the package's own component.
    expect(screen.getByTestId('acme-tank-value-tank-1').textContent).toBe('90%');
    expect(screen.queryByText(/needs a newer viewer/)).toBeNull();
  });
});
