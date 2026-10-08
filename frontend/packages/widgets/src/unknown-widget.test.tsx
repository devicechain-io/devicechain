// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A board can carry a widget type this build does not know (a newer release added it).
// The parser turns it into a placeholder; these pin what the widget layer does with it:
// say so on the board, read nothing, and leave its stored object alone.

import {
  migrateToSlots,
  parseDashboardDefinition,
  pruneSlots,
  serializeDefinition,
  setWidgetBox,
  type DashboardHub,
  type WidgetInstance,
} from '@devicechain/dashboards';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ConnectedWidget } from './connected-widget';
import { stripUnknownOptions, validateDefinitionOptions } from './definition-options';

afterEach(cleanup);

const box = { col: 0, colSpan: 4, row: 0, rowSpan: 3, z: 0 };

// A newer widget that names a slot and carries option keys no widget here declares —
// everything a known widget would subscribe to or have stripped.
const newer = {
  id: 'deck-1',
  type: 'card-deck',
  layout: { base: box },
  datasource: { kind: 'slot', slot: 'site', measurements: ['fuel_pct'] },
  options: { title: 'Fleet', selectionTarget: 'machine', order: 'type', notAnOptionHere: true },
  deck: { cards: [] },
};

function parsedBoard() {
  return parseDashboardDefinition({
    widgets: [newer],
    slots: { site: { type: 'anchor' }, machine: { type: 'device' } },
  });
}

function spyHub() {
  const hub = {
    subscribeWidget: vi.fn(() => () => {}),
    subscribeAlarms: vi.fn(() => () => {}),
    subscribeCommands: vi.fn(() => () => {}),
    subscribeLocations: vi.fn(() => () => {}),
    isDatasourceAvailable: vi.fn(async () => true),
  };
  return { hub, asHub: hub as unknown as DashboardHub };
}

describe('a widget of an unknown type', () => {
  it('renders a placeholder that says a newer viewer is needed and names the type', async () => {
    const { asHub } = spyHub();
    render(<ConnectedWidget widget={parsedBoard().widgets[0]} hub={asHub} />);
    await act(async () => {});
    expect(screen.getByText('This widget needs a newer viewer')).toBeTruthy();
    expect(screen.getByText(/card-deck/)).toBeTruthy();
  });

  it('subscribes to nothing on any channel', async () => {
    const { hub, asHub } = spyHub();
    render(<ConnectedWidget widget={parsedBoard().widgets[0]} hub={asHub} />);
    await act(async () => {});
    expect(hub.subscribeWidget).not.toHaveBeenCalled();
    expect(hub.subscribeAlarms).not.toHaveBeenCalled();
    expect(hub.subscribeCommands).not.toHaveBeenCalled();
    expect(hub.subscribeLocations).not.toHaveBeenCalled();
  });

  it('reports no option issues and has no options stripped', () => {
    const def = parsedBoard();
    expect(validateDefinitionOptions(def)).toEqual([]);
    // Identity is the no-op signal: the console calls this on every load.
    expect(stripUnknownOptions(def)).toBe(def);
    const stored = JSON.parse(serializeDefinition(stripUnknownOptions(def))) as { widgets: WidgetInstance[] };
    expect(JSON.stringify(stored.widgets[0])).toBe(JSON.stringify(newer));
  });
});

// The console's own path, end to end in pure functions: load (parse, slot migration,
// unknown-option strip), an unrelated edit that goes through the canvas's slot prune,
// then save. The newer widget and the slots only it uses must come out as they went in.
describe('the console load → edit → save path', () => {
  it('leaves an unknown widget and its slots exactly as stored', () => {
    const stored = {
      schemaVersion: 1,
      title: 'Board',
      canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
      widgets: [{ id: 'l1', type: 'label', layout: { base: box }, options: { text: 'hi' } }, newer],
      slots: { site: { type: 'anchor' }, machine: { type: 'device' } },
    };
    let def = migrateToSlots(parseDashboardDefinition(JSON.parse(JSON.stringify(stored))));
    def = stripUnknownOptions(def);
    def = pruneSlots(setWidgetBox(def, 'l1', { ...box, row: 5 }));
    const saved = JSON.parse(serializeDefinition(def)) as typeof stored;
    expect(JSON.stringify(saved.widgets[1])).toBe(JSON.stringify(newer));
    expect(saved.slots).toEqual(stored.slots);
  });
});

