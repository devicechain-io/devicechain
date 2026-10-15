// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { parseDashboardDefinition, serializeDefinition } from './definition';
import {
  copyWidget,
  duplicateWidget,
  parseWidgetClipboard,
  pasteWidgets,
  serializeWidgetClipboard,
  WIDGET_CLIPBOARD_FORMAT,
} from './editor-clipboard';
import { basePlacement } from './editor-model';
import type { DashboardDefinition, GridPlacement, ResolvedWidgetInstance, WidgetInstance } from './types';

const at = (col: number, row: number, cols = 3, rows = 3): GridPlacement => ({ mode: 'grid', col, row, cols, rows });

function board(widgets: WidgetInstance[], slots?: DashboardDefinition['slots']): DashboardDefinition {
  return {
    schemaVersion: 2,
    title: 'Board',
    canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
    widgets,
    ...(slots ? { slots } : {}),
  };
}

const gauge = (id: string, p: GridPlacement, slot?: string): WidgetInstance => ({
  id,
  type: 'gauge',
  layout: { base: p },
  options: { title: id, min: 0, max: 100 },
  ...(slot ? { datasource: { kind: 'slot' as const, slot, measurements: ['temp'] } } : {}),
});

const reloads = (d: DashboardDefinition) =>
  expect(() => parseDashboardDefinition(JSON.parse(serializeDefinition(d)))).not.toThrow();

const widgetOf = (d: DashboardDefinition, id: string) => d.widgets.find((w) => w.id === id)!;

describe('duplicateWidget', () => {
  it('gives the copy a new id, the same span and options, in the first free slot', () => {
    // a fills cols 0-2, b cols 3-5: the first free 3x3 is col 6, row 0.
    const d = board([gauge('a', at(0, 0)), gauge('b', at(3, 0))]);
    const r = duplicateWidget(d, 'a')!;
    expect(r.id).not.toBe('a');
    expect(r.definition.widgets).toHaveLength(3);
    const copy = widgetOf(r.definition, r.id);
    expect(basePlacement(copy)).toEqual(at(6, 0));
    expect(copy.options).toEqual({ title: 'a', min: 0, max: 100 });
    // A copy, not a shared object: editing one never edits the other.
    expect(copy.options).not.toBe(widgetOf(d, 'a').options);
    // Nothing else moved.
    expect(basePlacement(widgetOf(r.definition, 'b'))).toEqual(at(3, 0));
    reloads(r.definition);
  });

  it('wraps to the next row when the row is full', () => {
    const d = board([gauge('a', at(0, 0, 6, 2)), gauge('b', at(6, 0, 6, 2))]);
    const r = duplicateWidget(d, 'a')!;
    const copy = widgetOf(r.definition, r.id);
    expect(basePlacement(copy)).toEqual(at(0, 2, 6, 2));
  });

  it('keeps the slot binding on the same board', () => {
    const d = board([gauge('a', at(0, 0), 'slot-1')], {
      'slot-1': { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'pump-1' } },
    });
    const r = duplicateWidget(d, 'a')!;
    expect(widgetOf(r.definition, r.id).datasource).toEqual({ kind: 'slot', slot: 'slot-1', measurements: ['temp'] });
    expect(r.definition.slots).toEqual(d.slots);
    expect(r.unbound).toEqual([]);
  });

  it('puts a copy of a relative widget on the grid at its own size', () => {
    const d = board([
      gauge('host', at(0, 0, 4, 4)),
      {
        id: 'badge',
        type: 'label',
        layout: { base: { mode: 'relative', host: 'host', anchor: 'top-right', size: { cols: 2, rows: 1 } } },
        options: { text: 'hi' },
      },
    ]);
    const r = duplicateWidget(d, 'badge')!;
    expect(basePlacement(widgetOf(r.definition, r.id))).toEqual(at(4, 0, 2, 1));
    reloads(r.definition);
  });

  it('a `fill` relative widget takes its host’s span', () => {
    const d = board([
      gauge('host', at(0, 0, 4, 4)),
      {
        id: 'over',
        type: 'label',
        layout: { base: { mode: 'relative', host: 'host', anchor: 'center', size: { fill: 'host' } } },
        options: { text: 'x' },
      },
    ]);
    const r = duplicateWidget(d, 'over')!;
    expect(basePlacement(widgetOf(r.definition, r.id))).toEqual(at(4, 0, 4, 4));
  });

  it('refuses a placeholder for a widget this console cannot show', () => {
    const d = board([
      { id: 'u', type: 'unknown-widget', layout: { base: at(0, 0) }, raw: { id: 'u', type: 'acme/thing' } },
    ]);
    expect(duplicateWidget(d, 'u')).toBeUndefined();
    expect(copyWidget(d, 'u', 'b1')).toBeUndefined();
  });
});

describe('copy and paste', () => {
  const source = () =>
    board([gauge('a', at(0, 0), 'slot-1'), gauge('b', at(3, 0), 'slot-2')], {
      'slot-1': { type: 'device', label: 'pump-1', defaultBinding: { kind: 'device', deviceToken: 'pump-1' } },
      'slot-2': { type: 'device', label: 'pump-2', defaultBinding: { kind: 'device', deviceToken: 'pump-2' } },
    });

  const roundTrip = (d: DashboardDefinition, id: string, boardToken: string) => {
    const clip = copyWidget(d, id, boardToken)!;
    const text = serializeWidgetClipboard(clip);
    expect(JSON.parse(text).format).toBe(WIDGET_CLIPBOARD_FORMAT);
    return parseWidgetClipboard(text)!;
  };

  it('pastes on the same board keeping the slot it was bound to', () => {
    const d = source();
    const parsed = roundTrip(d, 'a', 'board-1');
    const r = pasteWidgets(d, parsed.clipboard, 'board-1');
    expect(r.ids).toHaveLength(1);
    const pasted = widgetOf(r.definition, r.ids[0]);
    expect(pasted.datasource).toEqual({ kind: 'slot', slot: 'slot-1', measurements: ['temp'] });
    expect(basePlacement(pasted)).toEqual(at(6, 0));
    expect(r.unbound).toEqual([]);
    reloads(r.definition);
  });

  it('pastes onto another board UNBOUND where that board has no such slot, and says why', () => {
    const target = board([gauge('x', at(0, 0))]);
    const parsed = roundTrip(source(), 'a', 'board-1');
    const r = pasteWidgets(target, parsed.clipboard, 'board-2');
    const pasted = widgetOf(r.definition, r.ids[0]);
    // Not dropped: the widget points at a slot of the same kind, with nothing bound to it. An absent
    // datasource would mean "tenant-wide" to an alarm widget; an unbound slot means "no device yet".
    expect(pasted.datasource?.kind).toBe('slot');
    const slot = (pasted.datasource as { slot: string }).slot;
    expect(r.definition.slots?.[slot]).toEqual({ type: 'device', label: 'pump-1' });
    expect(r.definition.slots?.[slot]?.defaultBinding).toBeUndefined();
    // The measurement names it reads are kept.
    expect(pasted.datasource?.measurements).toEqual(['temp']);
    expect(r.unbound).toEqual([{ widgetId: r.ids[0], slot, from: 'slot-1', reason: 'missing' }]);
    reloads(r.definition);
  });

  it('does not bind a pasted widget to a same-named slot on another board that holds another device', () => {
    const target = board([gauge('x', at(0, 0), 'slot-1')], {
      'slot-1': { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'boiler-9' } },
    });
    const parsed = roundTrip(source(), 'a', 'board-1');
    const r = pasteWidgets(target, parsed.clipboard, 'board-2');
    const pasted = widgetOf(r.definition, r.ids[0]);
    const slot = (pasted.datasource as { slot: string }).slot;
    expect(slot).not.toBe('slot-1');
    expect(r.definition.slots?.['slot-1']?.defaultBinding).toEqual({ kind: 'device', deviceToken: 'boiler-9' });
    expect(r.unbound).toEqual([{ widgetId: r.ids[0], slot, from: 'slot-1', reason: 'different' }]);
  });

  it('keeps the binding on another board whose slot of that name is bound to the same device', () => {
    const target = board([gauge('x', at(0, 0), 'slot-1')], {
      'slot-1': { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'pump-1' } },
    });
    const parsed = roundTrip(source(), 'a', 'board-1');
    const r = pasteWidgets(target, parsed.clipboard, 'board-2');
    expect(widgetOf(r.definition, r.ids[0]).datasource).toMatchObject({ slot: 'slot-1' });
    expect(r.unbound).toEqual([]);
  });

  it('pastes into a narrower board at no more than its columns', () => {
    const wide = board([gauge('w', at(0, 0, 10, 3))]);
    const narrow: DashboardDefinition = { ...board([]), canvas: { ...board([]).canvas, grid: { ...board([]).canvas.grid, columns: 6 } } };
    const r = pasteWidgets(narrow, roundTrip(wide, 'w', 'b1').clipboard, 'b2');
    expect(basePlacement(widgetOf(r.definition, r.ids[0]))).toEqual(at(0, 0, 6, 3));
  });

  it('refuses a widget type the registry does not have, and names it', () => {
    const clip = copyWidget(source(), 'a', 'b1')!;
    const text = serializeWidgetClipboard({
      ...clip,
      widgets: [{ ...clip.widgets[0], type: 'acme/tank' } as unknown as ResolvedWidgetInstance],
    });
    const parsed = parseWidgetClipboard(text)!;
    expect(parsed.clipboard.widgets).toHaveLength(0);
    expect(parsed.refused).toEqual([{ type: 'acme/tank', reason: expect.any(String) }]);
  });

  it('is not fooled by text that is not a widget clipboard', () => {
    expect(parseWidgetClipboard('hello')).toBeUndefined();
    expect(parseWidgetClipboard('{"format":"something-else"}')).toBeUndefined();
    expect(parseWidgetClipboard(JSON.stringify({ format: WIDGET_CLIPBOARD_FORMAT, version: 99, board: 'x' }))).toBeUndefined();
    // The right envelope around a definition that does not parse.
    expect(
      parseWidgetClipboard(JSON.stringify({ format: WIDGET_CLIPBOARD_FORMAT, version: 1, board: 'x', definition: { widgets: 'no' } })),
    ).toBeUndefined();
  });
});
