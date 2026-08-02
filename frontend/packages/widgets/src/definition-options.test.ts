// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { WidgetInstance, WidgetType } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { stripUnknownOptions, validateDefinitionOptions } from './definition-options';

// A widget carrying nothing but the fields this module reads. `layout` is required by
// the type and never looked at here, so it is the same empty box everywhere — a board
// whose widgets overlap is a different concern with its own checks.
function widget(id: string, type: WidgetType, options?: Record<string, unknown>): WidgetInstance {
  return { id, type, layout: { base: { col: 0, row: 0, colSpan: 4, rowSpan: 2, z: 0 } }, options };
}

function board(...widgets: WidgetInstance[]) {
  return { widgets };
}

describe('validateDefinitionOptions', () => {
  it('reports nothing for a board every widget of which is configured', () => {
    expect(
      validateDefinitionOptions(
        board(
          widget('w1', 'gauge', { title: 'Temp', min: 0, max: 100 }),
          widget('w2', 'label', { text: 'Zone A' }),
          widget('w3', 'timeseries-chart'),
        ),
      ),
    ).toEqual([]);
  });

  // 🔑 THE REASON THIS IS A FUNCTION AND NOT A LOOP AT EACH CALL SITE. Three widgets of
  // the SAME type, one broken: an issue list that dropped the widget id would name
  // `commandName` on a board with three command buttons and leave the author to guess
  // which. So the assertion is on the id, not merely on the presence of an issue.
  it('attributes each issue to the widget that carries it', () => {
    const issues = validateDefinitionOptions(
      board(
        widget('btn-a', 'command-button', { commandName: 'reboot' }),
        widget('btn-b', 'command-button', { commandName: '' }),
        widget('btn-c', 'command-button', { commandName: 'calibrate' }),
      ),
    );
    expect(issues.map((i) => [i.widgetId, i.key, i.code])).toEqual([['btn-b', 'commandName', 'missing']]);
  });

  it('reports every issue on a board, not just the first', () => {
    const issues = validateDefinitionOptions(
      board(
        widget('g', 'gauge', { min: 100, max: 0 }),
        widget('i', 'image', {}),
        widget('t', 'table', { precision: 500 }),
      ),
    );
    expect(issues.map((i) => [i.widgetId, i.code])).toEqual([
      ['g', 'invariant'],
      ['i', 'missing'],
      ['t', 'range'],
    ]);
  });

  // The title is what an author recognises a widget by; the id is generated. But a
  // blank or non-string title must NOT be carried through as one — the console renders
  // `title || widgetId`, and an empty string there would silently fall back while a
  // whitespace one would render as a nameless row.
  it('carries a usable title and omits an unusable one', () => {
    const titled = validateDefinitionOptions(board(widget('w1', 'image', { title: 'Floor plan' })));
    expect(titled[0].title).toBe('Floor plan');

    for (const bad of ['', '   ', 42, null, undefined]) {
      const issues = validateDefinitionOptions(board(widget('w1', 'image', { title: bad })));
      // A non-string title is itself an issue, so filter to the one about `url`.
      const urlIssue = issues.find((i) => i.key === 'url');
      expect(urlIssue?.title, `title ${JSON.stringify(bad)}`).toBeUndefined();
    }
  });

  it('reports nothing for a board with no widgets', () => {
    expect(validateDefinitionOptions(board())).toEqual([]);
  });
});

describe('stripUnknownOptions', () => {
  it('removes only the keys no widget of that type reads', () => {
    const stripped = stripUnknownOptions(
      board(widget('w1', 'gauge', { title: 'Temp', min: 0, max: 100, minimum: 0, legacyUnit: 'C' })),
    );
    expect(stripped.widgets[0].options).toEqual({ title: 'Temp', min: 0, max: 100 });
  });

  it('leaves the rest of the definition alone', () => {
    const before = { title: 'Board', widgets: [widget('w1', 'label', { text: 'x', stale: 1 })] };
    const after = stripUnknownOptions(before);
    expect(after.title).toBe('Board');
    expect(after.widgets[0].id).toBe('w1');
    expect(after.widgets[0].layout).toEqual(before.widgets[0].layout);
  });

  // The identity contract, and it is load-bearing rather than an optimisation: the
  // console holds definitions in React state and decides "dirty" by comparing them, so
  // a fresh-but-equal object would present a no-op repair as an unsaved edit — and the
  // author would be asked to save a change that changed nothing.
  it('returns the same object, and the same widgets, when there is nothing to strip', () => {
    const clean = board(widget('w1', 'gauge', { min: 0, max: 10 }), widget('w2', 'label', { text: 'x' }));
    expect(stripUnknownOptions(clean)).toBe(clean);

    // One dirty widget must not re-create the clean ones either, or every widget on the
    // board becomes a new object for one leftover key.
    const mixed = board(widget('w1', 'gauge', { min: 0, max: 10 }), widget('w2', 'label', { text: 'x', junk: 1 }));
    const after = stripUnknownOptions(mixed);
    expect(after).not.toBe(mixed);
    expect(after.widgets[0]).toBe(mixed.widgets[0]);
    expect(after.widgets[1]).not.toBe(mixed.widgets[1]);
  });

  it('leaves a widget with no options bag untouched', () => {
    const none = board(widget('w1', 'timeseries-chart'));
    expect(stripUnknownOptions(none)).toBe(none);
  });

  // A stored definition arrives through JSON.parse, which makes '__proto__' an ordinary
  // own key rather than the accessor an object literal would hit. It is not in any specs
  // table, so it must be stripped — and the check below is that the RESULT has no such
  // own key and no polluted prototype, not merely that the call returned.
  it('strips a prototype-named key a parsed definition can carry', () => {
    const options = JSON.parse('{"text":"hi","__proto__":{"polluted":true}}') as Record<string, unknown>;
    expect(Object.prototype.hasOwnProperty.call(options, '__proto__')).toBe(true);

    const after = stripUnknownOptions(board(widget('w1', 'label', options)));
    const bag = after.widgets[0].options as Record<string, unknown>;
    expect(bag).toEqual({ text: 'hi' });
    expect(Object.prototype.hasOwnProperty.call(bag, '__proto__')).toBe(false);
    expect(({} as Record<string, unknown>).polluted).toBeUndefined();
  });

  // The pair that makes the repair complete: what stripUnknownOptions removes is
  // exactly what validateDefinitionOptions reports as 'unknown', so applying it clears
  // every 'unknown' issue and leaves every other one standing for the author to fix.
  it('clears the unknown issues it is offered for, and only those', () => {
    const before = board(
      widget('w1', 'gauge', { min: 0, max: 10, minimum: 5 }),
      widget('w2', 'image', { url: '', legacy: true }),
    );
    expect(validateDefinitionOptions(before).map((i) => i.code).sort()).toEqual([
      'missing',
      'unknown',
      'unknown',
    ]);
    expect(validateDefinitionOptions(stripUnknownOptions(before)).map((i) => i.code)).toEqual(['missing']);
  });
});
