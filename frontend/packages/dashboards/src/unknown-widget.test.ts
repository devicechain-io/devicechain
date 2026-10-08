// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A widget type this build does not know — one a newer release added — must survive a
// load and a save untouched. The console loads a board, the author edits something else,
// and the save writes the whole definition back: if the parser dropped or normalized the
// newer widget, that save would silently delete or damage it for every viewer that DOES
// know it. So an unknown type becomes a placeholder that carries the stored object
// verbatim, and the serializer writes that object back.
//
// These tests compare BYTES on purpose. A deep-equal would pass over a reordered key or a
// normalized box, and either is a rewrite of a document this build cannot read.

import { describe, expect, it } from 'vitest';

import { isDirty, parseDashboardDefinition, serializeDefinition } from './definition';
import { bringToFront, deleteWidget, setCanvasGrid, setWidgetBox, updateWidget } from './editor-model';
import { pruneSlots } from './slots';
import type { DashboardDefinition, WidgetInstance } from './types';

// Spelled as a literal rather than imported, so the expectation is the stored contract
// and not whatever the constant happens to say.
const PLACEHOLDER = 'unknown-widget';

const box = (over = {}) => ({ col: 0, colSpan: 4, row: 0, rowSpan: 3, z: 0, ...over });

// A widget from a newer release. Everything about it is deliberately awkward for this
// parser: key order that is not the parser's, a box the known-widget path would
// normalize (fractional col, zero span, no z), a datasource kind this build has never
// heard of, a first-class field it does not read, and an unknown type nested inside
// that field.
function newerWidget(): Record<string, unknown> {
  return {
    type: 'card-deck',
    options: { title: 'Fleet', selectionTarget: 'machine', order: 'type', future: { nested: [1, 'two', null] } },
    id: 'deck-1',
    layout: { base: { col: 2.4, colSpan: 0, row: 3, rowSpan: 9 }, tablet: { col: 0, colSpan: 24, row: 3, rowSpan: 9, z: 1 } },
    datasource: { kind: 'slot', slot: 'site', measurements: [], extra: true },
    deck: {
      cards: [
        {
          id: 'hauler',
          deviceTypes: ['sp-hauler'],
          grid: { columns: 12, rowHeight: 36, gap: 8 },
          widgets: [
            {
              id: 'fuel',
              type: 'latest-card',
              layout: { base: box({ colSpan: 6 }) },
              datasource: { kind: 'member', measurements: ['fuel_pct'] },
              options: { title: 'Fuel', unit: '%', notAnOption: 1 },
            },
            {
              id: 'radar',
              type: 'hologram',
              layout: { base: box() },
              datasource: { kind: 'slot', slot: 'nestedOnly', measurements: [] },
            },
          ],
        },
      ],
      fallback: 'generic',
    },
  };
}

function board(widgets: unknown[], slots?: Record<string, unknown>): Record<string, unknown> {
  return {
    schemaVersion: 1,
    title: 'Board',
    canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
    widgets,
    ...(slots ? { slots } : {}),
  };
}

const label = { id: 'l1', type: 'label', layout: { base: box() }, options: { text: 'hello' } };

// The stored JSON of widget `index` after a parse → serialize round trip.
function savedWidget(def: DashboardDefinition, index: number): string {
  const stored = JSON.parse(serializeDefinition(def)) as { widgets: unknown[] };
  return JSON.stringify(stored.widgets[index]);
}

describe('unknown widget types', () => {
  it('parse to a placeholder instead of failing the whole board', () => {
    const def = parseDashboardDefinition(board([label, newerWidget()]));
    expect(def.widgets).toHaveLength(2);
    const placeholder = def.widgets[1];
    expect(placeholder.type).toBe(PLACEHOLDER);
    expect(placeholder.id).toBe('deck-1');
    // The placeholder is placed where the widget was stored, through the same box rules
    // every widget is placed by.
    expect(placeholder.layout.base).toEqual({ col: 2, colSpan: 1, row: 3, rowSpan: 9, z: 0 });
    // It reads nothing and configures nothing: whatever the newer widget binds is in its
    // raw object, and the hub must never see it as a selector this build understands.
    expect(placeholder.datasource).toBeUndefined();
    expect(placeholder.options).toBeUndefined();
    // The known widget beside it is unaffected.
    expect(def.widgets[0]).toEqual(label);
  });

  it('keep the stored widget verbatim, detached from the caller’s object', () => {
    const stored = newerWidget();
    const def = parseDashboardDefinition(board([stored]));
    const placeholder = def.widgets[0] as WidgetInstance & { raw?: unknown };
    expect(JSON.stringify(placeholder.raw)).toBe(JSON.stringify(newerWidget()));
    // Mutating the input afterwards must not reach the placeholder (and so the save).
    (stored.options as Record<string, unknown>).title = 'changed';
    expect(JSON.stringify(placeholder.raw)).toBe(JSON.stringify(newerWidget()));
  });

  it('serialize back byte-identical, nested unknown type included', () => {
    const def = parseDashboardDefinition(board([label, newerWidget()]));
    expect(savedWidget(def, 1)).toBe(JSON.stringify(newerWidget()));
    // The known widget serializes exactly as it would on a board without the newer one.
    const without = parseDashboardDefinition(board([label]));
    expect(savedWidget(def, 0)).toBe(savedWidget(without, 0));
  });

  it('survive any number of load/save cycles unchanged', () => {
    let text = JSON.stringify(board([label, newerWidget()]));
    const first = serializeDefinition(parseDashboardDefinition(JSON.parse(text)));
    text = first;
    for (let i = 0; i < 3; i++) text = serializeDefinition(parseDashboardDefinition(JSON.parse(text)));
    expect(text).toBe(first);
  });

  it('serialize a placeholder as its raw object, not as the placeholder', () => {
    // Built by hand rather than by the parser, so this pins the serializer on its own.
    const raw = newerWidget();
    const def: DashboardDefinition = {
      schemaVersion: 1,
      title: 'Board',
      canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
      widgets: [
        {
          id: 'deck-1',
          type: PLACEHOLDER,
          layout: { base: { col: 2, colSpan: 1, row: 3, rowSpan: 9, z: 0 }, tablet: { col: 0, colSpan: 24, row: 3, rowSpan: 9, z: 1 } },
          raw,
        } as unknown as WidgetInstance,
      ],
    };
    expect(savedWidget(def, 0)).toBe(JSON.stringify(newerWidget()));
  });

  it('is not dirty after a load with no edit', () => {
    const a = parseDashboardDefinition(board([label, newerWidget()]));
    const b = parseDashboardDefinition(board([label, newerWidget()]));
    expect(isDirty(a, b)).toBe(false);
  });

  it('keep an id-less stored widget id-less', () => {
    const { id: _id, ...noId } = newerWidget();
    const def = parseDashboardDefinition(board([noId]));
    expect(def.widgets[0].id).toMatch(/^w-/); // the canvas still needs a key
    expect(savedWidget(def, 0)).toBe(JSON.stringify(noId));
  });

  it('carry a move through the save, and nothing else', () => {
    const def = parseDashboardDefinition(board([newerWidget()]));
    const moved = setWidgetBox(def, 'deck-1', { col: 6, colSpan: 12, row: 0, rowSpan: 4, z: 2 });
    const saved = JSON.parse(savedWidget(moved, 0)) as Record<string, unknown>;
    const expected = newerWidget();
    expected.layout = { ...(expected.layout as object), base: { col: 6, colSpan: 12, row: 0, rowSpan: 4, z: 2 } };
    // Same keys in the same order, only the layout changed.
    expect(JSON.stringify(saved)).toBe(JSON.stringify(expected));
  });

  it('carry a bring-to-front and a canvas regrid through the save', () => {
    const two = parseDashboardDefinition(board([label, newerWidget()]));
    const front = bringToFront(two, 'deck-1');
    const frontBox = front.widgets[1].layout.base;
    expect((JSON.parse(savedWidget(front, 1)) as { layout: { base: unknown } }).layout.base).toEqual(frontBox);

    // Shrinking the grid clamps every widget back inside it, placeholders included.
    const regrid = setCanvasGrid(two, { columns: 1 });
    const regridLayout = regrid.widgets[1].layout;
    expect(regridLayout.base).not.toEqual(two.widgets[1].layout.base); // the regrid did move it
    expect((JSON.parse(savedWidget(regrid, 1)) as { layout: unknown }).layout).toEqual(regridLayout);
  });

  it('write a changed id back (a copied placeholder must not share the original’s id)', () => {
    const def = parseDashboardDefinition(board([newerWidget()]));
    const copy = updateWidget(def, 'deck-1', { ...def.widgets[0], id: 'deck-2' });
    const saved = JSON.parse(savedWidget(copy, 0)) as Record<string, unknown>;
    expect(saved.id).toBe('deck-2');
    expect(Object.keys(saved)).toEqual(Object.keys(newerWidget()));
  });

  it('are removed by a delete like any widget', () => {
    const def = parseDashboardDefinition(board([label, newerWidget()]));
    const stored = JSON.parse(serializeDefinition(deleteWidget(def, 'deck-1'))) as { widgets: unknown[] };
    expect(stored.widgets).toHaveLength(1);
  });

  it('re-parse an already-parsed placeholder as itself (parse is idempotent)', () => {
    // The parser's contract is that an already-typed definition comes back unchanged. A
    // placeholder handed back in must not be wrapped again: the save would then write
    // the wrapper, and the stored widget's type would become the placeholder type.
    const once = parseDashboardDefinition(board([label, newerWidget()]));
    const twice = parseDashboardDefinition(once);
    expect(twice).toEqual(once);
    expect(serializeDefinition(twice)).toBe(serializeDefinition(once));
    expect(savedWidget(twice, 1)).toBe(JSON.stringify(newerWidget()));

    // An edit made before the re-parse is kept by it, and still reaches the save.
    const moved = setWidgetBox(once, 'deck-1', { col: 6, colSpan: 12, row: 0, rowSpan: 4, z: 2 });
    const movedTwice = parseDashboardDefinition(moved);
    expect(serializeDefinition(movedTwice)).toBe(serializeDefinition(moved));

    // An id-less stored widget stays id-less through a re-parse too.
    const { id: _id, ...noId } = newerWidget();
    const noIdTwice = parseDashboardDefinition(parseDashboardDefinition(board([noId])));
    expect(savedWidget(noIdTwice, 0)).toBe(JSON.stringify(noId));
  });

  it('hold a stored placeholder type with no raw object literally, rather than trusting it', () => {
    // Nothing this build writes carries the placeholder type, but a hand-edited or
    // foreign document might. Without a raw object it is not a placeholder this parser
    // built, and it is not a type this build renders, so it is held raw like any other.
    const odd = { id: 'x', type: PLACEHOLDER, layout: { base: box() }, raw: 'not an object' };
    const def = parseDashboardDefinition(board([odd]));
    expect(def.widgets[0].type).toBe(PLACEHOLDER);
    expect(savedWidget(def, 0)).toBe(JSON.stringify(odd));
  });

  it('keep a box field this build does not know when the box is moved', () => {
    // A newer release may add a field to a box. Moving the placeholder writes the fields
    // this build knows and must leave that one in place.
    const stored = { ...newerWidget(), layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 3, z: 0, minRows: 2 } } };
    const def = parseDashboardDefinition(board([stored]));
    const moved = setWidgetBox(def, 'deck-1', { col: 1, colSpan: 4, row: 0, rowSpan: 3, z: 0 });
    const saved = JSON.parse(savedWidget(moved, 0)) as { layout: { base: unknown } };
    expect(JSON.stringify(saved.layout.base)).toBe(
      JSON.stringify({ col: 1, colSpan: 4, row: 0, rowSpan: 3, z: 0, minRows: 2 }),
    );
  });

  it('drop a stored offset the edit cleared, and write one the edit set', () => {
    const stored = { ...newerWidget(), layout: { base: { ...box(), offset: { x: 3, y: 4 } } } };
    const def = parseDashboardDefinition(board([stored]));
    const cleared = setWidgetBox(def, 'deck-1', box({ col: 1 }));
    expect((JSON.parse(savedWidget(cleared, 0)) as { layout: { base: object } }).layout.base).not.toHaveProperty('offset');
    const nudged = setWidgetBox(def, 'deck-1', { ...box(), offset: { x: 5, y: 6 } });
    expect((JSON.parse(savedWidget(nudged, 0)) as { layout: { base: { offset: unknown } } }).layout.base.offset).toEqual({
      x: 5,
      y: 6,
    });
  });

  it('still reject a widget with no type, an empty one, or a non-string one', () => {
    // A missing type is a broken document, not a newer widget: there is nothing to name
    // and no viewer that could render it.
    expect(() => parseDashboardDefinition(board([{ layout: { base: box() } }]))).toThrow(/unknown type/);
    expect(() => parseDashboardDefinition(board([{ type: 7, layout: { base: box() } }]))).toThrow(/unknown type/);
    expect(() => parseDashboardDefinition(board([{ type: '', layout: { base: box() } }]))).toThrow(/unknown type/);
  });

  it('still reject an unknown widget with no base box (it could not be placed)', () => {
    expect(() =>
      parseDashboardDefinition(board([{ id: 'x', type: 'hologram', layout: { tablet: box() } }])),
    ).toThrow(/no 'base' box/);
  });
});

describe('pruneSlots with an unknown widget', () => {
  // The console prunes unused slots on every canvas change. A slot only a newer widget
  // uses is invisible to the parsed model, so without this a drag anywhere on the board
  // would delete the newer widget's slots on the next save.
  const slots = {
    site: { type: 'anchor' },
    machine: { type: 'device', scope: { parent: 'site', strategy: 'manual' } },
    nestedOnly: { type: 'device' },
    unused: { type: 'device' },
  };

  it('keeps every slot the raw widget names, and still prunes the rest', () => {
    const def = parseDashboardDefinition(board([newerWidget()], slots));
    const pruned = pruneSlots(def);
    expect(Object.keys(pruned.slots ?? {}).sort()).toEqual(['machine', 'nestedOnly', 'site']);
  });

  it('does not keep a slot the raw widget only mentions as text', () => {
    const chatty = { id: 'x', type: 'hologram', layout: { base: box() }, options: { title: 'unused' } };
    const def = parseDashboardDefinition(board([chatty], { unused: { type: 'device' } }));
    expect(pruneSlots(def).slots).toBeUndefined();
  });
});
