// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The import pipeline's contract: an export imports back to the same board, a foreign
// binding is found and repaired before anything is saved, bad or oversized input is refused,
// and a widget this console cannot resolve survives as a placeholder that blocks publishing.

import { parseDashboardDefinition, serializeDefinition, unresolvedWidgets } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';
import { defaultWidgetRegistry } from '../editor/widgetRegistry';
import { exportDefinitionJson } from '../editor/exportDefinition';
import { DASHBOARD_TEMPLATES } from './templates';
import {
  applyBindingChoices,
  buildImportPayload,
  checkBindingRefs,
  collectBindingRefs,
  DashboardImportError,
  MAX_DEFINITION_BYTES,
  prepareImport,
} from './importDefinition';

const catalog = defaultWidgetRegistry().catalog;
const box = { base: { col: 0, colSpan: 6, row: 0, rowSpan: 4, z: 0 } };

const board = {
  schemaVersion: 1,
  title: 'Plant',
  widgets: [
    {
      id: 'w1',
      type: 'latest-card',
      layout: box,
      options: { title: 'Temperature', measurement: 'temperature' },
      datasource: { kind: 'slot', slot: 'press', measurements: ['temperature'] },
    },
    {
      id: 'w2',
      type: 'alarm-count',
      layout: box,
      options: { title: 'Alarms', state: 'ACTIVE' },
      datasource: { kind: 'slot', slot: 'site', measurements: [] },
    },
  ],
  slots: {
    press: { type: 'device', label: 'Press', defaultBinding: { kind: 'device', deviceToken: 'press-7' } },
    site: {
      type: 'anchor',
      defaultBinding: { kind: 'anchor', anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'plant-1' } },
    },
  },
};

const parsed = () => parseDashboardDefinition(board, { catalog });
const exists = (have: string[]) => async (_kind: string, token: string) => have.includes(token);

describe('import round trip', () => {
  it('imports an export back to the identical definition', () => {
    const original = parsed();
    const prepared = prepareImport(exportDefinitionJson(original, false), catalog);
    expect(serializeDefinition(prepared.definition)).toBe(serializeDefinition(original));
    expect(prepared.unresolved).toEqual([]);
    expect(prepared.optionIssues).toEqual([]);
  });

  it('survives the full path: bindings that exist are untouched and the payload equals the original', async () => {
    const original = parsed();
    const prepared = prepareImport(exportDefinitionJson(original, false), catalog);
    const statuses = await checkBindingRefs(collectBindingRefs(prepared.definition), exists(['press-7', 'plant-1']));
    expect(statuses.every((s) => s.exists)).toBe(true);
    const repaired = applyBindingChoices(prepared.definition, statuses, {});
    expect(buildImportPayload(repaired, 'Plant')).toBe(serializeDefinition(original));
  });

  it('a template export imports with every slot unbound and offered for binding', async () => {
    const prepared = prepareImport(exportDefinitionJson(parsed(), true), catalog);
    const refs = collectBindingRefs(prepared.definition);
    // The device slot is offered; the unbound anchor slot is left to the editor.
    expect(refs).toEqual([expect.objectContaining({ slot: 'press', token: null, kind: 'device' })]);
    const statuses = await checkBindingRefs(refs, exists([]));
    const bound = applyBindingChoices(prepared.definition, statuses, { press: 'press-9' });
    expect(bound.slots?.press.defaultBinding).toEqual({ kind: 'device', deviceToken: 'press-9' });
  });
});

describe('foreign bindings', () => {
  it('finds the tokens this tenant does not have', async () => {
    const statuses = await checkBindingRefs(collectBindingRefs(parsed()), exists(['plant-1']));
    expect(statuses.map((s) => [s.slot, s.exists])).toEqual([
      ['press', false],
      ['site', true],
    ]);
  });

  it('asks the tenant once per token even when several slots share it', async () => {
    const def = parseDashboardDefinition(
      {
        ...board,
        slots: {
          a: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'dup' } },
          b: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'dup' } },
        },
      },
      { catalog },
    );
    let asked = 0;
    await checkBindingRefs(collectBindingRefs(def), async () => {
      asked++;
      return true;
    });
    expect(asked).toBe(1);
  });

  it('a failed lookup fails the check rather than reading as missing or present', async () => {
    await expect(
      checkBindingRefs(collectBindingRefs(parsed()), async () => {
        throw new Error('network down');
      }),
    ).rejects.toThrow('network down');
  });

  it('rebinds a missing device and a missing anchor target, keeping the anchor relationship', async () => {
    const statuses = await checkBindingRefs(collectBindingRefs(parsed()), exists([]));
    const repaired = applyBindingChoices(parsed(), statuses, { press: 'press-2', site: 'plant-9' });
    expect(repaired.slots?.press.defaultBinding).toEqual({ kind: 'device', deviceToken: 'press-2' });
    expect(repaired.slots?.site.defaultBinding).toEqual({
      kind: 'anchor',
      anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'plant-9' },
    });
  });

  it('clears a missing binding when no replacement is chosen, and never touches one that exists', async () => {
    const statuses = await checkBindingRefs(collectBindingRefs(parsed()), exists(['plant-1']));
    const repaired = applyBindingChoices(parsed(), statuses, { press: undefined, site: 'someone-else' });
    expect(repaired.slots?.press.defaultBinding).toBeUndefined();
    // The slot itself stays: the widgets still reference it and the editor can bind it.
    expect(repaired.slots?.press.label).toBe('Press');
    expect(repaired.slots?.site.defaultBinding).toEqual(parsed().slots?.site.defaultBinding);
  });

  it('treats an anchor target type this console cannot look up as missing', async () => {
    const def = parseDashboardDefinition(
      {
        ...board,
        slots: {
          site: {
            type: 'anchor',
            defaultBinding: { kind: 'anchor', anchor: { relationship: 'r', targetType: 'group', targetToken: 'g1' } },
          },
        },
      },
      { catalog },
    );
    const [status] = await checkBindingRefs(collectBindingRefs(def), async () => true);
    expect(status.supported).toBe(false);
    expect(status.exists).toBe(false);
  });
});

describe('refusals', () => {
  it.each([
    ['', 'empty'],
    ['   ', 'empty'],
    ['{not json', 'notJson'],
    ['[]', 'invalid'],
    ['42', 'invalid'],
    [JSON.stringify({ title: 'x' }), 'invalid'], // no widgets array
    [JSON.stringify({ widgets: [{ layout: box }] }), 'invalid'], // a widget with no type
    [JSON.stringify({ widgets: [{ type: 'label' }] }), 'invalid'], // a widget that cannot be placed
  ])('refuses %j as %s', (text, code) => {
    expect(() => prepareImport(text, catalog)).toThrow(DashboardImportError);
    try {
      prepareImport(text, catalog);
    } catch (err) {
      expect((err as DashboardImportError).code).toBe(code);
    }
  });

  it('refuses an oversized definition before parsing it', () => {
    const big = JSON.stringify({ widgets: [], pad: 'x'.repeat(MAX_DEFINITION_BYTES) });
    try {
      prepareImport(big, catalog);
      throw new Error('expected a refusal');
    } catch (err) {
      expect((err as DashboardImportError).code).toBe('tooLarge');
    }
  });

  it('counts bytes, not characters, against the limit', () => {
    // Each '€' is three bytes: under the limit in characters, over it in bytes.
    const text = JSON.stringify({ widgets: [], pad: '€'.repeat(Math.ceil(MAX_DEFINITION_BYTES / 3)) });
    expect(text.length).toBeLessThan(MAX_DEFINITION_BYTES);
    expect(() => prepareImport(text, catalog)).toThrow(/limit/);
  });

  it('refuses oversized text as too large without reading it as JSON first', () => {
    // Not JSON at all: a check that ran after JSON.parse would report it as malformed.
    const text = '{' + 'x'.repeat(MAX_DEFINITION_BYTES);
    expect(() => prepareImport(text, catalog)).toThrow(expect.objectContaining({ code: 'tooLarge' }));
  });

  it('refuses a payload that grew past the limit after the review', () => {
    // The input was under the limit; the title the user typed pushes the saved board over.
    expect(() => buildImportPayload(parsed(), 'x'.repeat(MAX_DEFINITION_BYTES))).toThrow(
      expect.objectContaining({ code: 'tooLarge' }),
    );
  });
});

describe('external content', () => {
  it('lists the origin of every absolute URL the board would fetch, once each', () => {
    const text = JSON.stringify({
      ...board,
      canvas: { background: { imageUrl: 'https://cdn.example.com/bg.png' } },
      widgets: [
        ...board.widgets,
        { id: 'i1', type: 'image', layout: box, options: { url: 'https://img.example.org/a.png' } },
        { id: 'i2', type: 'image', layout: box, options: { url: 'https://img.example.org/b.png' } },
        // Protocol-relative: fetched over the page's own scheme.
        { id: 'i4', type: 'image', layout: box, options: { url: '//pixel.example.com/p.gif' } },
        { id: 'i3', type: 'image', layout: box, options: { url: '/static/local.png' } },
        { id: 'm1', type: 'map', layout: box, options: { tileUrl: 'http://tiles.example.net/{z}/{x}/{y}.png' } },
        // A widget this console cannot resolve still renders once its package is installed.
        { id: 'x1', type: 'vendor.frame', layout: box, options: { nested: { src: 'https://vendor.example.io/x' } } },
      ],
    });
    expect(prepareImport(text, catalog).externalOrigins).toEqual([
      'http://tiles.example.net',
      'https://cdn.example.com',
      'https://img.example.org',
      'https://pixel.example.com',
      'https://vendor.example.io',
    ]);
  });

  it('reports none for a board with no absolute URLs', () => {
    expect(prepareImport(JSON.stringify(board), catalog).externalOrigins).toEqual([]);
  });
});

describe('widgets this console cannot resolve', () => {
  const withUnknown = JSON.stringify({
    ...board,
    widgets: [...board.widgets, { id: 'x1', type: 'vendor.thermo-ring', layout: box, options: { a: 1 } }],
  });

  it('keeps them as placeholders, reports them, and keeps their stored JSON', () => {
    const prepared = prepareImport(withUnknown, catalog);
    expect(prepared.unresolved.map((u) => u.widgetId)).toEqual(['x1']);
    expect(unresolvedWidgets(prepared.definition)).toHaveLength(1);
    const out = JSON.parse(buildImportPayload(prepared.definition, 'Plant')) as { widgets: Array<Record<string, unknown>> };
    expect(out.widgets.find((w) => w.id === 'x1')).toMatchObject({ type: 'vendor.thermo-ring', options: { a: 1 } });
  });
});

describe('the new dashboard', () => {
  it('takes the title the user chose', () => {
    const out = JSON.parse(buildImportPayload(parsed(), '  Copy of Plant ')) as { title: string };
    expect(out.title).toBe('Copy of Plant');
  });
});

describe('starter templates', () => {
  it.each(DASHBOARD_TEMPLATES.map((t) => [t.id, t.json]))('%s is a clean board with no stored entity', (_id, json) => {
    const prepared = prepareImport(json, catalog);
    expect(prepared.unresolved).toEqual([]);
    expect(prepared.optionIssues).toEqual([]);
    expect(prepared.concreteSelectors).toBe(0);
    expect(prepared.externalOrigins).toEqual([]);
    expect(prepared.definition.widgets.length).toBeGreaterThan(0);
    // A template names no entity of its own tenant.
    for (const ref of collectBindingRefs(prepared.definition)) expect(ref.token).toBeNull();
  });

  it('device detail and fleet overview ask for a device to bind; the alarms board asks for none', () => {
    const slotsOf = (id: string) =>
      collectBindingRefs(prepareImport(DASHBOARD_TEMPLATES.find((t) => t.id === id)!.json, catalog).definition).map((r) => r.slot);
    expect(slotsOf('device-detail')).toEqual(['device']);
    expect(slotsOf('fleet-overview')).toEqual(['focus']);
    expect(slotsOf('alarms-board')).toEqual([]);
  });
});
