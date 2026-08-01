// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// This test reads fixtures off disk, so it needs the Node typings. See the note in
// options.test.ts — the reference is program-wide, not file-scoped.
/// <reference types="node" />

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { parseDashboardDefinition, WIDGET_TYPES, type WidgetType } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { validateWidgetOptions } from './options';
import { WIDGET_BINDS_DATASOURCE, WIDGET_CHANNEL } from './registry';

// ---- The cross-workspace gate --------------------------------------------------
//
// The widgetlab sim scenario authors its dashboards in Go and this package renders
// them. Neither side can check the other: Go cannot run the parser, and TypeScript
// cannot run the builders. They meet at a committed fixture — Go writes exactly what
// Provision publishes (and a Go test fails if it goes stale), and this holds that
// document to the real parser and the real option schemas.
//
// 🔴 "It parses" is nearly worthless on its own, which is the whole reason this file
// is longer than that. parseDashboardDefinition throws on only three things — a
// non-array `widgets`, an unknown widget `type`, and a missing `layout.base` — and
// degrades everything else silently: a malformed datasource is dropped to undefined,
// a wrong-typed option is read back as the default, a schemaVersion mismatch is never
// rejected. A board of ten legal type strings in layout boxes, bound to nothing and
// configured with nothing, parses perfectly. So every check below is about what
// SURVIVED the parse, not about whether it threw.

const FIXTURES = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'testdata', 'widgetlab');

const GALLERY = 'wl-gallery';
const STRESS = 'wl-stress';

// Every fixture on disk, so a board that exists is never silently uncovered. The two
// names above are still spelled out because the assertions differ per board — the
// gallery is the catalog, the stress board is the exerciser — but a THIRD board
// appearing with no test of its own is itself a failure, and so is one of these two
// disappearing while the tests that name it quietly stop running.
function fixtureTokens(): string[] {
  return readdirSync(FIXTURES)
    .filter((name) => name.endsWith('.json'))
    .map((name) => name.slice(0, -'.json'.length))
    .sort();
}

function raw(token: string): Record<string, unknown> {
  return JSON.parse(readFileSync(join(FIXTURES, `${token}.json`), 'utf8')) as Record<string, unknown>;
}

function parsed(token: string) {
  return parseDashboardDefinition(raw(token));
}

// The pathological axes each widget type can show as a BOARD PLACEMENT.
//
// Coverage is asserted on the gallery, never on the stress board: a command-button's
// pathology is that the ack never arrives and an entity-selector's is an empty
// candidate set — neither is a second layout box, so requiring all ten on the stress
// board would accrete placements nominal in all but location. Instead each type
// declares whether it has such an axis, and the stress board is held to THAT.
const PATHOLOGICAL_AXES = {
  'timeseries-chart': 'a spike that blows the axis, and a series that stops',
  'latest-card': 'an unrounded float at full width',
  gauge: 'a value outside the scale, so the needle pins',
  table: 'rows that appear and disappear as metrics are dropped',
  label: 'text far longer than its box, which the frame clips',
  image: 'a source that cannot decode',
} as const satisfies Partial<Record<WidgetType, string>>;

describe('the widgetlab fixtures', () => {
  // 🔴 The fixture directory and the boards this file tests must be the same set.
  //
  // The tokens below are read by NAME, so a renamed dashboard leaves its old fixture
  // on disk and every assertion here goes on passing about a board that no longer
  // exists, while the real one is parsed by nothing. The Go side asserts the same
  // equality against the manifest; this end catches the case where a fixture is
  // present and simply has no test.
  it('are exactly the boards this file covers', () => {
    expect(fixtureTokens()).toEqual([GALLERY, STRESS].sort());
  });

  it('parse without throwing', () => {
    expect(() => parsed(GALLERY)).not.toThrow();
    expect(() => parsed(STRESS)).not.toThrow();
  });

  // The gallery is the catalog, so it must carry every widget type there is. The Go
  // side asserts this too, off the same WIDGET_TYPES list; holding it on both sides
  // means a widget added without a placement fails in the lane that added it AND
  // here, where the real parser has confirmed the placement actually survives.
  it('give the gallery a surviving instance of every widget type', () => {
    const present = new Set(parsed(GALLERY).widgets.map((w) => w.type));
    for (const type of WIDGET_TYPES) {
      expect(present, `the gallery has no surviving ${type} widget`).toContain(type);
    }
  });

  // 🔴 Pinned to the board's DESIGN, not to the fixture's own value.
  //
  // The per-board scope check reads what the fixture declares, so deleting a scope
  // deletes the expectation with it and the check passes over a flattened hierarchy.
  // The gallery is built around a context cascade — an anchor slot a root selector
  // re-points, and a device slot scoped to it that follows — and without the scope
  // the child selector offers every device in the tenant instead of the zone's
  // members, which is a working-looking widget demonstrating the opposite of the
  // feature it exists to show.
  it('give the gallery a scoped context hierarchy', () => {
    const slots = parsed(GALLERY).slots ?? {};
    const anchors = Object.entries(slots).filter(([, slot]) => slot.type === 'anchor');
    expect(anchors.length, 'the gallery declares no anchor slot, so there is no root context').toBe(1);

    const scoped = Object.entries(slots).filter(([, slot]) => slot.scope !== undefined);
    expect(scoped.length, 'no gallery slot is scoped to another, so the cascade is flat and a ' +
      'root rebind fans out to nothing').toBeGreaterThan(0);

    const [anchorName] = anchors[0];
    for (const [name, slot] of scoped) {
      expect(slot.type, `scoped slot ${name} is not a device slot`).toBe('device');
      expect(slot.scope?.parent, `scoped slot ${name} does not hang off the anchor`).toBe(anchorName);
    }
  });

  it('put nothing on the stress board that has no pathological axis', () => {
    for (const widget of parsed(STRESS).widgets) {
      expect(
        Object.keys(PATHOLOGICAL_AXES),
        `${widget.type} is on the stress board but declares no pathological axis, so its ` +
          `placement is nominal in all but location`,
      ).toContain(widget.type);
    }
  });

  it('show every declared pathological axis somewhere on the stress board', () => {
    // A declared axis nothing displays is indistinguishable from one that does not
    // exist — the same failure as a generated pathology bound to no widget.
    const present = new Set(parsed(STRESS).widgets.map((w) => w.type));
    for (const type of Object.keys(PATHOLOGICAL_AXES)) {
      expect(present, `${type} declares a pathological axis that no stress widget shows`).toContain(type);
    }
  });
});

// ---- What survived the parse ---------------------------------------------------

describe('the parsed widgetlab boards', () => {
  for (const token of [GALLERY, STRESS]) {
    describe(token, () => {
      it('keeps every widget the fixture declares', () => {
        const before = (raw(token).widgets as unknown[]).length;
        expect(before).toBeGreaterThan(0);
        expect(parsed(token).widgets).toHaveLength(before);
      });

      it('declares the schema version this package is written for', () => {
        // Pinned to the CONSTANT, not to the fixture's own value. Comparing parsed
        // against raw only proves the parser preserved whatever it was given —
        // mutate the fixture and both sides move together, so the check passes for a
        // document written to a schema this package does not implement. The parser
        // never rejects a mismatch (it defaults the field), so nothing else would
        // notice until a future schema change reinterpreted the whole document.
        expect(parsed(token).schemaVersion).toBe(1);
        expect(raw(token).schemaVersion, 'the fixture omits schemaVersion, so the parser ' +
          'defaulted it and the assertion above proved nothing').toBe(1);
      });

      // The one the parser degrades most quietly: parseDatasource drops a malformed
      // selector to undefined, so a widget keeps its frame, its title and its layout
      // and binds to nothing at all.
      it('keeps every datasource, still pointing at a declared slot', () => {
        const definition = parsed(token);
        const declared = new Set(Object.keys(definition.slots ?? {}));
        const authored = new Map(
          (raw(token).widgets as Array<Record<string, unknown>>)
            .filter((w) => w.datasource !== undefined)
            .map((w) => [w.id as string, w.datasource as Record<string, unknown>]),
        );
        expect(authored.size).toBeGreaterThan(0);

        for (const widget of definition.widgets) {
          const source = authored.get(widget.id);
          if (!source) continue;
          expect(widget.datasource, `${widget.id}'s datasource was dropped by the parser`).toBeDefined();
          expect(widget.datasource?.kind).toBe(source.kind);
          if (widget.datasource?.kind === 'slot') {
            expect(declared, `${widget.id} binds a slot the board does not declare`).toContain(
              widget.datasource.slot,
            );
          }
          // The SELECTOR — the part that decides what the widget actually shows.
          // stringArrayAt coerces anything non-array to [], which the hub reads as
          // "every measurement", so a mangled list widens the subscription instead
          // of breaking it. Checking kind and slot alone leaves that invisible.
          expect(
            widget.datasource && 'measurements' in widget.datasource ? widget.datasource.measurements : undefined,
            `${widget.id}'s measurement selector did not survive the parse`,
          ).toEqual(source.measurements);
        }
      });

      it('round-trips every option the fixture authored', () => {
        // Options are not parsed at all — they pass through opaquely — so this is
        // the check that a key was not silently lost between the two sides.
        //
        // The non-empty guard is what stops it being vacuous: with every options bag
        // stripped, `{}` equals `{}` for every widget and the test passes over a
        // board configured with nothing, which is the exact state this file exists
        // to catch.
        const authored = new Map(
          (raw(token).widgets as Array<Record<string, unknown>>).map((w) => [w.id as string, w.options ?? {}]),
        );
        const configured = [...authored.values()].filter((o) => Object.keys(o as object).length > 0);
        expect(
          configured.length,
          'no widget on this board authors any option, so the round-trip compares empty to empty',
        ).toBeGreaterThan(0);

        for (const widget of parsed(token).widgets) {
          expect(widget.options ?? {}, `${widget.id}'s options did not round-trip`).toEqual(
            authored.get(widget.id),
          );
        }
      });

      // Every widget must be configured at all. `required` covers four keys across
      // ten types, so nine of ten widgets could arrive with an empty bag and
      // validateWidgetOptions would report nothing — a board of correctly-typed
      // widgets showing defaults, which is what "configured with nothing" looks like.
      it('configures every widget it places', () => {
        for (const widget of parsed(token).widgets) {
          expect(
            Object.keys(widget.options ?? {}),
            `${widget.id} authors no options at all, so it renders entirely on defaults`,
          ).not.toHaveLength(0);
        }
      });

      // Slots are the binding layer, and parseSlotBinding drops a malformed binding
      // silently: the slot survives, the board opens, and every widget on it renders
      // an unbound placeholder. The scope is what makes the context hierarchy work,
      // and it disappears the same way.
      it('keeps every slot binding and scope the fixture declares', () => {
        const definition = parsed(token);
        const authored = raw(token).slots as Record<string, Record<string, unknown>>;
        expect(Object.keys(authored).length).toBeGreaterThan(0);

        for (const [name, slot] of Object.entries(authored)) {
          const got = definition.slots?.[name];
          expect(got, `slot ${name} did not survive the parse`).toBeDefined();
          expect(got?.type).toBe(slot.type);

          const binding = slot.defaultBinding as Record<string, unknown> | undefined;
          expect(got?.defaultBinding, `slot ${name}'s default binding was dropped`).toBeDefined();
          expect(got?.defaultBinding?.kind).toBe(binding?.kind);
          if (got?.defaultBinding?.kind === 'device') {
            expect(got.defaultBinding.deviceToken).toBe(binding?.deviceToken);
          }
          if (got?.defaultBinding?.kind === 'anchor') {
            expect(got.defaultBinding.anchor.targetToken).toBe(
              (binding?.anchor as Record<string, unknown>)?.targetToken,
            );
          }

          const scope = slot.scope as Record<string, unknown> | undefined;
          if (scope) {
            expect(got?.scope, `slot ${name}'s scope was dropped, flattening the hierarchy`).toBeDefined();
            expect(got?.scope?.parent).toBe(scope.parent);
          }
        }
      });

      // 🔑 The check the option schemas were built for, finally wired to something
      // that produces definitions. Every bag on the board, against the schema the
      // renderer actually reads.
      it('authors only options the widgets read, at the types they read them', () => {
        const issues = parsed(token).widgets.flatMap((widget) =>
          validateWidgetOptions(widget.type, widget.options).map(
            (issue) => `${widget.id} (${widget.type}): ${issue.code} on ${issue.key} — ${issue.message}`,
          ),
        );
        expect(issues).toEqual([]);
      });

      // 🔴 A widget that loses its datasource ENTIRELY used to pass every gate in the
      // arc, because every layer shared one shape: `if (!widget.datasource) continue`.
      // A mangled selector failed; no selector object at all — strictly worse — did
      // not. Live, that widget keeps its frame, its title and its layout and binds to
      // nothing, which is the parser's most quietly degraded case.
      //
      // The check could not be a blanket requirement, because label, image and
      // entity-selector legitimately carry none. WIDGET_BINDS_DATASOURCE answers it
      // per type and is exhaustive over WidgetType — it existed one PR later, built
      // for the config panel, and nobody wired it back here.
      it('gives a datasource to every widget type that binds one, and none to the rest', () => {
        for (const widget of parsed(token).widgets) {
          if (WIDGET_BINDS_DATASOURCE[widget.type]) {
            expect(
              widget.datasource,
              `${widget.id} (${widget.type}) binds no datasource; it renders its frame and ` +
                `its title over nothing at all`,
            ).toBeDefined();
          } else {
            expect(
              widget.datasource,
              `${widget.id} (${widget.type}) carries a datasource, but nothing on this widget ` +
                `reads one`,
            ).toBeUndefined();
          }
        }
      });

      it('gives a selection widget the slot it drives', () => {
        for (const widget of parsed(token).widgets) {
          if (WIDGET_CHANNEL[widget.type] !== 'selection') continue;
          expect(widget.options?.selectionTarget, `${widget.id} drives no slot`).toBeTruthy();
        }
      });
    });
  }
});
