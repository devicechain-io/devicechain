// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The catalog's runtime rejections. Each case asserts the issue PATH, not just "throws":
// a registry that fails with the wrong complaint, or with a complaint about the wrong
// field, is as unusable as one that did not fail.

import { describe, expect, it } from 'vitest';

import { defineWidgetDescriptor } from './widget-descriptor';
import {
  WidgetCatalogError,
  createWidgetCatalog,
  validateWidgetDescriptor,
  type DescriptorIssue,
} from './widget-catalog';
import type { WidgetDescriptor } from './widget-descriptor';

const OPTIONS = {
  title: { kind: 'string', doc: 'Heading.' },
  unit: { kind: 'string', doc: 'Unit.' },
  min: { kind: 'number', doc: 'Low end.' },
  max: { kind: 'number', doc: 'High end.' },
  window: { kind: 'number', integer: true, min: 1, doc: 'Retained samples.' },
  state: { kind: 'enum', values: ['ACTIVE', 'CLEARED'], doc: 'State filter.' },
  ack: { kind: 'enum', values: ['true', 'false'], doc: 'Ack filter.' },
  maxRows: { kind: 'number', integer: true, min: 1, doc: 'Rows.' },
} as const;

const valid = () =>
  defineWidgetDescriptor({
    id: 'acme/tank-level',
    version: '1.2.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Tank level' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: { window: { option: 'window', min: 1 } },
    options: OPTIONS,
    constraints: [{ kind: 'greater-than', key: 'max', than: 'min' }],
    defaults: { title: 'Tank', min: 0, max: 100 },
    interactions: {},
  });

// A descriptor with one field overwritten, deliberately outside the type system: these
// are the shapes JSON and JS callers can produce that the compiler never sees.
const withField = (field: string, value: unknown): unknown => ({ ...valid(), [field]: value });
const issuesOf = (d: unknown): DescriptorIssue[] => validateWidgetDescriptor(d);
const pathsOf = (d: unknown): string[] => issuesOf(d).map((i) => i.path);

describe('validateWidgetDescriptor', () => {
  it('accepts a well-formed extension descriptor', () => {
    expect(issuesOf(valid())).toEqual([]);
  });

  it('accepts a reserved built-in id, but rejects a bare unknown one (path id)', () => {
    expect(issuesOf({ ...valid(), id: 'gauge' })).toEqual([]);
    const issues = issuesOf({ ...valid(), id: 'tank-level' });
    expect(issues.map((i) => i.path)).toEqual(['id']);
    expect(issues[0].message).toMatch(/namespaced/);
    expect(pathsOf({ ...valid(), id: 'Acme/Tank' })).toEqual(['id']);
    expect(pathsOf({ ...valid(), id: 'a/b/c' })).toEqual(['id']);
  });

  it('requires an option doc of an extension, and of nothing built in (path options.<key>.doc)', () => {
    const undocumented = { title: { kind: 'string' }, window: OPTIONS.window, min: OPTIONS.min, max: OPTIONS.max };
    expect(pathsOf({ ...valid(), options: undocumented })).toEqual(['options.title.doc']);
    // A built-in is documented in the widgets package's table, so its runtime descriptor may omit it.
    expect(issuesOf({ ...valid(), id: 'gauge', options: undocumented })).toEqual([]);
    // Present, a doc must still be a string -- on a built-in too.
    expect(pathsOf({ ...valid(), id: 'gauge', options: { ...undocumented, title: { kind: 'string', doc: 7 } } })).toEqual([
      'options.title.doc',
    ]);
  });

  it('accepts freshness: true on a measurement widget, and rejects anything else (path freshness)', () => {
    expect(issuesOf({ ...valid(), freshness: true })).toEqual([]);
    expect(pathsOf({ ...valid(), freshness: false })).toEqual(['freshness']);
    expect(pathsOf({ ...valid(), freshness: 'yes' })).toEqual(['freshness']);
  });

  it('rejects a malformed semver (path version)', () => {
    for (const version of ['1.2', '1.2.x', 'v1.2.3', '01.2.3', '', 3]) {
      expect(pathsOf(withField('version', version))).toEqual(['version']);
    }
  });

  it('rejects an unsupported sdkApiVersion (path sdkApiVersion)', () => {
    expect(pathsOf(withField('sdkApiVersion', 2))).toEqual(['sdkApiVersion']);
    expect(pathsOf(withField('sdkApiVersion', undefined))).toContain('sdkApiVersion');
  });

  it('rejects a bad optionsVersion (path optionsVersion)', () => {
    for (const v of [0, -1, 1.5, '1']) expect(pathsOf(withField('optionsVersion', v))).toEqual(['optionsVersion']);
  });

  it('rejects non-JSON values at the path they sit on', () => {
    const cases: [string, unknown, string][] = [
      ['a function in display', { ...valid().display, render: () => null }, 'display.render'],
      ['undefined in display', { ...valid().display, description: undefined }, 'display.description'],
      ['NaN in display', { ...valid().display, description: NaN }, 'display.description'],
      ['a Date in display', { ...valid().display, description: new Date() }, 'display.description'],
      ['a symbol in display', { ...valid().display, description: Symbol('x') }, 'display.description'],
    ];
    for (const [name, display, path] of cases) {
      const issues = issuesOf(withField('display', display));
      expect(issues.map((i) => i.path), name).toContain(path);
    }
    // A function nested in an array inside the binding policy.
    const nested = issuesOf({ ...valid(), interactions: { selection: true, extra: [() => 1] } });
    expect(nested.map((i) => i.path)).toContain('interactions.extra[0]');
  });

  it('rejects a circular descriptor instead of recursing forever', () => {
    const d: Record<string, unknown> = { ...valid() };
    d.self = d;
    expect(pathsOf(d)).toContain('self');
  });

  it('rejects defaults that disagree with the schema, including a constraint violation', () => {
    expect(pathsOf({ ...valid(), defaults: { title: 3 } })).toEqual(['defaults.title']);
    expect(pathsOf({ ...valid(), defaults: { window: 0 } })).toEqual(['defaults.window']);
    expect(pathsOf({ ...valid(), defaults: { nope: 1 } })).toEqual(['defaults.nope']);
    const constraint = issuesOf({ ...valid(), defaults: { min: 10, max: 5 } });
    expect(constraint.map((i) => i.path)).toEqual(['defaults.max']);
    expect(constraint[0].message).toBe('max (5) must be greater than min (10)');
  });

  it('does not treat a missing REQUIRED option as a defaults fault', () => {
    const d = {
      ...valid(),
      options: { ...OPTIONS, url: { kind: 'string', required: true, doc: 'Source.' } },
      defaults: { title: 'Image' },
    };
    expect(issuesOf(d)).toEqual([]);
  });

  it('rejects a constraint that names a missing or non-number key', () => {
    expect(pathsOf({ ...valid(), constraints: [{ kind: 'greater-than', key: 'max', than: 'ghost' }] })).toEqual([
      'constraints[0].than',
    ]);
    expect(pathsOf({ ...valid(), constraints: [{ kind: 'greater-than', key: 'title', than: 'min' }] })).toEqual([
      'constraints[0].key',
    ]);
    expect(pathsOf({ ...valid(), constraints: [{ kind: 'less-than', key: 'max', than: 'min' }] })).toEqual([
      'constraints[0].kind',
    ]);
  });

  it('rejects a binding that names a missing key, or a key of the wrong kind', () => {
    const at = (binding: unknown) => issuesOf({ ...valid(), binding }).map((i) => i.path);
    expect(at({ window: { option: 'ghost' } })).toEqual(['binding.window.option']);
    expect(at({ window: { option: 'title' } })).toEqual(['binding.window.option']);
    expect(at({ window: { option: 'window', min: 'x' } })).toEqual(['binding.window.min']);
    expect(at({ pageSize: { const: 5 } })).toEqual(['binding.pageSize']);
  });

  it('checks alarm and control binding policies', () => {
    const alarm = (binding: unknown) =>
      issuesOf({
        ...valid(),
        channel: 'alarm',
        datasource: { mode: 'optional', selectors: ['device'] },
        binding,
      }).map((i) => i.path);
    const good = { pageSize: { option: 'maxRows', default: 50, min: 1 }, state: { option: 'state' }, acknowledged: { option: 'ack' } };
    expect(alarm(good)).toEqual([]);
    expect(alarm({ ...good, pageSize: { const: 50 } })).toEqual([]);
    expect(alarm({ state: { option: 'state' } })).toEqual(['binding.pageSize']);
    expect(alarm({ ...good, pageSize: { option: 'title' } })).toEqual(['binding.pageSize.option']);
    expect(alarm({ ...good, pageSize: { const: 0 } })).toEqual(['binding.pageSize.const']);
    expect(alarm({ ...good, pageSize: { const: 5, option: 'maxRows' } })).toEqual(['binding.pageSize']);
    expect(alarm({ ...good, state: { option: 'min' } })).toEqual(['binding.state.option']);
    // acknowledged must reference an enum limited to 'true'/'false'.
    expect(alarm({ ...good, acknowledged: { option: 'state' } })).toEqual(['binding.acknowledged.option']);
    expect(alarm({ ...good, window: { option: 'window' } })).toEqual(['binding.window']);
  });

  it('checks the members binding that makes a measurement widget a fleet widget', () => {
    const at = (members: unknown, channel = 'measurement') =>
      issuesOf({ ...valid(), channel, binding: { members } }).map((i) => i.path);
    // Always, or when a declared option holds a value of it.
    expect(at({ const: true })).toEqual([]);
    expect(at({ option: 'state', equals: 'ACTIVE' })).toEqual([]);
    expect(at({ option: 'title', equals: 'anything' })).toEqual([]);
    // Neither of the two shapes, or both at once.
    expect(at({ const: false })).toEqual(['binding.members.const']);
    expect(at({ const: true, option: 'state', equals: 'ACTIVE' })).toEqual(['binding.members']);
    expect(at({})).toEqual(['binding.members']);
    expect(at({ option: 'state' })).toEqual(['binding.members']);
    expect(at('yes')).toEqual(['binding.members']);
    // The option must exist, be text, and the value must be one the enum can hold.
    expect(at({ option: 'ghost', equals: 'x' })).toEqual(['binding.members.option']);
    expect(at({ option: 'min', equals: 'x' })).toEqual(['binding.members.option']);
    expect(at({ option: 'state', equals: 'PAUSED' })).toEqual(['binding.members.equals']);
    // Only the measurement channel reads a fleet.
    expect(at({ const: true }, 'alarm')).toContain('binding.members');
  });

  it('checks the groupBy binding that makes an alarm widget group by device type', () => {
    const at = (groupBy: unknown, channel = 'alarm') =>
      issuesOf({
        ...valid(),
        channel,
        datasource: { mode: 'optional', selectors: ['device'] },
        binding: { pageSize: { const: 50 }, groupBy },
      }).map((i) => i.path);
    expect(at({ option: 'state', equals: 'ACTIVE' })).toEqual([]);
    // A mode needs a switch: a constant is not one for grouping.
    expect(at({ const: true })).toEqual(['binding.groupBy']);
    expect(at({ option: 'state' })).toEqual(['binding.groupBy']);
    expect(at({ option: 'ghost', equals: 'x' })).toEqual(['binding.groupBy.option']);
    expect(at({ option: 'min', equals: 'x' })).toEqual(['binding.groupBy.option']);
    expect(at({ option: 'state', equals: 'PAUSED' })).toEqual(['binding.groupBy.equals']);
    // Only the alarm channel groups.
    expect(at({ option: 'state', equals: 'ACTIVE' }, 'control')).toContain('binding.groupBy');
  });

  it('checks the countBySeverity binding that makes an alarm widget read counts instead of rows', () => {
    const at = (countBySeverity: unknown, channel = 'alarm') =>
      issuesOf({
        ...valid(),
        channel,
        datasource: { mode: 'optional', selectors: ['device'] },
        binding: { pageSize: { const: 1 }, countBySeverity },
      }).map((i) => i.path);
    expect(at({ const: true })).toEqual([]);
    // A constant switch and nothing else.
    expect(at({ const: false })).toEqual(['binding.countBySeverity']);
    expect(at({ option: 'state', equals: 'ACTIVE' })).toEqual(['binding.countBySeverity']);
    expect(at({ const: true, option: 'state' })).toEqual(['binding.countBySeverity']);
    expect(at(true)).toEqual(['binding.countBySeverity']);
    // Only the alarm channel counts alarms.
    expect(at({ const: true }, 'control')).toContain('binding.countBySeverity');
  });

  it('checks that the deviceType binding names a declared string option', () => {
    const at = (deviceType: unknown, channel = 'alarm') =>
      issuesOf({
        ...valid(),
        channel,
        datasource: { mode: 'optional', selectors: ['device'] },
        binding: { pageSize: { const: 1 }, deviceType },
      }).map((i) => i.path);
    expect(at({ option: 'state' })).toEqual([]);
    expect(at({ option: 'nope' })).toEqual(['binding.deviceType.option']);
    expect(at({ option: 'state' }, 'control')).toContain('binding.deviceType');
  });

  it('accepts an aggregate-channel widget that pins its span, and nothing else in its binding', () => {
    const OPTS = { ...OPTIONS, windowSeconds: { kind: 'number', integer: true, min: 60, max: 604800, doc: 'Span.' } } as const;
    const { constraints: _constraints, ...base } = valid();
    const aggregate = (binding: unknown, extra: object = {}) =>
      issuesOf({ ...base, channel: 'aggregate', options: OPTS, defaults: { title: 'T' }, binding, ...extra }).map((i) => i.path);
    expect(aggregate({ windowSeconds: { option: 'windowSeconds' } })).toEqual([]);
    expect(aggregate({})).toEqual([]);
    // A sample count means nothing to a widget that folds a span; the page size is not its either.
    expect(aggregate({ window: { option: 'window' } })).toEqual(['binding.window']);
    expect(aggregate({ pageSize: { const: 5 } })).toEqual(['binding.pageSize']);
    // The span option must be a number option that exists.
    expect(aggregate({ windowSeconds: { option: 'title' } })).toEqual(['binding.windowSeconds.option']);
    // Aggregates are not "freshness": the frame's age readout is a measurement-channel feature.
    expect(aggregate({}, { freshness: true })).toEqual(['freshness']);
  });

  it('rejects channel/datasource mismatches', () => {
    for (const channel of ['none', 'selection']) {
      const issues = issuesOf({ ...valid(), channel, binding: {} });
      expect(issues.map((i) => i.path)).toEqual(['datasource.mode']);
    }
    const none = { ...valid(), channel: 'none', binding: {}, datasource: { mode: 'none' } };
    expect(issuesOf(none)).toEqual([]);
    // control: device and slot only.
    const control = (selectors: unknown[]) =>
      issuesOf({
        ...valid(),
        channel: 'control',
        binding: { pageSize: { const: 20 } },
        datasource: { mode: 'required', selectors },
      }).map((i) => i.path);
    expect(control(['device', 'slot'])).toEqual([]);
    expect(control(['device', 'anchor'])).toEqual(['datasource.selectors[1]']);
    expect(pathsOf({ ...valid(), datasource: { mode: 'required', selectors: [] } })).toEqual(['datasource.selectors']);
    expect(pathsOf({ ...valid(), datasource: { mode: 'required', selectors: ['galaxy'] } })).toEqual([
      'datasource.selectors[0]',
    ]);
    expect(pathsOf({ ...valid(), datasource: { mode: 'maybe' } })).toEqual(['datasource.mode']);
  });

  it('rejects channel/interaction mismatches', () => {
    const at = (interactions: unknown) => pathsOf({ ...valid(), interactions });
    expect(at({ commandSend: true })).toEqual(['interactions.commandSend']);
    expect(at({ alarmActions: true })).toEqual(['interactions.alarmActions']);
    expect(at({ selection: true, candidates: true })).toEqual([]);
    expect(at({ selection: false })).toEqual(['interactions.selection']);
    expect(at({ teleport: true })).toEqual(['interactions.teleport']);
  });

  it('rejects editor wording and control hints of the wrong shape', () => {
    const opt = (extra: Record<string, unknown>) => ({ ...valid(), options: { ...OPTIONS, state: { ...OPTIONS.state, ...extra } } });
    expect(issuesOf(opt({ control: 'slot-target', labelId: 'x', valueLabels: { ACTIVE: 'On' }, valueLabelIds: { CLEARED: 'y' } }))).toEqual([]);
    // Open vocabulary: a control this host has never heard of registers (and falls back).
    expect(issuesOf(opt({ control: 'acme/colour-wheel' }))).toEqual([]);
    expect(pathsOf(opt({ control: '' }))).toEqual(['options.state.control']);
    expect(pathsOf(opt({ control: 7 }))).toEqual(['options.state.control']);
    expect(pathsOf(opt({ labelId: 7 }))).toEqual(['options.state.labelId']);
    expect(pathsOf(opt({ valueLabels: { ACTIVE: 1 } }))).toEqual(['options.state.valueLabels.ACTIVE']);
    expect(pathsOf(opt({ valueLabelIds: { PENDING: 'z' } }))).toEqual(['options.state.valueLabelIds.PENDING']);
    expect(pathsOf(opt({ valueLabels: ['On'] }))).toEqual(['options.state.valueLabels']);
    expect(pathsOf(withField('display', { title: 'T', category: { en: 'X' } }))).toEqual(['display.category']);
    expect(pathsOf(withField('display', { title: 'T', categoryId: 3 }))).toEqual(['display.categoryId']);
  });

  it('rejects an unknown channel and a malformed option schema', () => {
    expect(pathsOf(withField('channel', 'telemetry'))).toContain('channel');
    expect(pathsOf({ ...valid(), options: { x: { kind: 'color', doc: '' } } })).toContain('options.x.kind');
    expect(pathsOf({ ...valid(), options: { x: { kind: 'enum', values: [], doc: '' } } })).toContain('options.x.values');
  });

  it('reports several faults together', () => {
    const issues = issuesOf({ ...valid(), version: 'x', sdkApiVersion: 2, defaults: { title: 1 }, interactions: { commandSend: true } });
    expect(issues.map((i) => i.path).sort()).toEqual(['defaults.title', 'interactions.commandSend', 'sdkApiVersion', 'version']);
  });

  it('rejects a non-object descriptor', () => {
    expect(issuesOf(null)).toHaveLength(1);
    expect(issuesOf('gauge')).toHaveLength(1);
  });
});

describe('createWidgetCatalog', () => {
  // The published contract has exactly one way to obtain a catalog, and it validates. A
  // catalog builder that skips validation, exported from the package entry, would be a
  // privileged path around every check below.
  it('is the only catalog constructor the package exports, and it rejects an invalid descriptor', async () => {
    const api: Record<string, unknown> = await import('./index');
    expect(api.createWidgetCatalog).toBe(createWidgetCatalog);
    expect(api).not.toHaveProperty('sealCatalog');
    const invalid = { ...valid(), version: 'latest' } as WidgetDescriptor;
    let error: WidgetCatalogError | undefined;
    try {
      (api.createWidgetCatalog as typeof createWidgetCatalog)([invalid]);
    } catch (e) {
      error = e as WidgetCatalogError;
    }
    expect(error).toBeInstanceOf(WidgetCatalogError);
    expect(error!.issues.map((i) => `${i.id}:${i.path}`)).toEqual(['acme/tank-level:version']);
  });

  it('reports a duplicate id (path id), including two copies of a built-in', () => {
    const gauge = { ...valid(), id: 'gauge' } as WidgetDescriptor;
    const attempt = () => createWidgetCatalog([gauge, { ...gauge }]);
    expect(attempt).toThrow(WidgetCatalogError);
    try {
      attempt();
    } catch (e) {
      const issues = (e as WidgetCatalogError).issues;
      expect(issues).toEqual([expect.objectContaining({ id: 'gauge', path: 'id' })]);
      expect(issues[0].message).toMatch(/duplicate/);
    }
  });

  it('throws ONE error carrying every issue from every descriptor', () => {
    const bad1 = { ...valid(), id: 'no-namespace' };
    const bad2 = { ...valid(), id: 'acme/two', version: 'nope' };
    const dup = { ...valid() };
    let error: WidgetCatalogError | undefined;
    try {
      createWidgetCatalog([bad1, bad2, valid(), dup] as WidgetDescriptor[]);
    } catch (e) {
      error = e as WidgetCatalogError;
    }
    expect(error).toBeInstanceOf(WidgetCatalogError);
    expect(error!.issues.map((i) => `${i.id}:${i.path}`)).toEqual([
      'no-namespace:id',
      'acme/two:version',
      'acme/tank-level:id',
    ]);
    // The message names every problem, so one failed start is enough to fix the composition.
    expect(error!.message).toContain('3 problems');
    expect(error!.message).toContain('acme/two.version');
  });

  it('returns a deeply frozen catalog that lists in insertion order', () => {
    const catalog = createWidgetCatalog([valid(), { ...valid(), id: 'acme/another' }]);
    expect(catalog.list().map((d) => d.id)).toEqual(['acme/tank-level', 'acme/another']);
    expect(catalog.has('acme/another')).toBe(true);
    expect(catalog.has('toString')).toBe(false);
    expect(catalog.get('acme/missing')).toBeUndefined();

    const d = catalog.get('acme/tank-level')!;
    expect(Object.isFrozen(catalog)).toBe(true);
    expect(Object.isFrozen(catalog.list())).toBe(true);
    expect(Object.isFrozen(d)).toBe(true);
    expect(Object.isFrozen(d.display)).toBe(true);
    expect(Object.isFrozen(d.defaults)).toBe(true);
    expect(Object.isFrozen(d.binding)).toBe(true);
    expect(() => {
      (d as { version: string }).version = '9.9.9';
    }).toThrow(TypeError);
    expect(() => {
      (d.defaults as Record<string, unknown>).title = 'changed';
    }).toThrow(TypeError);
    expect(() => {
      (catalog.list() as WidgetDescriptor[]).push(d);
    }).toThrow(TypeError);
  });

  it('deep-freezes a descriptor the caller already froze shallowly', () => {
    // Object.freeze is shallow, so a frozen OUTER object says nothing about its children.
    // A freeze that stops at the first frozen object would leave these mutable.
    const d = Object.freeze({ ...valid(), id: 'acme/pre-frozen', defaults: { title: 'Tank' } });
    const stored = createWidgetCatalog([d]).get('acme/pre-frozen')!;
    expect(Object.isFrozen(stored.defaults)).toBe(true);
    expect(Object.isFrozen(stored.datasource)).toBe(true);
    expect(() => {
      (stored.defaults as Record<string, unknown>).title = 'changed';
    }).toThrow(TypeError);
  });

  it('keeps two catalogs independent', () => {
    const a = createWidgetCatalog([valid()]);
    const b = createWidgetCatalog([{ ...valid(), id: 'acme/other' }]);
    expect(a.has('acme/other')).toBe(false);
    expect(b.has('acme/tank-level')).toBe(false);
    expect(a.list().map((d) => d.id)).toEqual(['acme/tank-level']);
    expect(b.list().map((d) => d.id)).toEqual(['acme/other']);
  });
});

describe('the layout block', () => {
  const issuesFor = (layout: unknown): DescriptorIssue[] => issuesOf(withField('layout', layout));
  const GOOD = { defaultSpan: { cols: 8, rows: 4 }, minSpan: { cols: 4, rows: 2 }, maxSpan: { cols: 24, rows: 12 } };

  it('is optional, and a well-formed one passes', () => {
    expect(issuesOf(valid())).toEqual([]);
    expect(issuesFor(GOOD)).toEqual([]);
    expect(issuesFor({ defaultSpan: { cols: 1, rows: 1 } })).toEqual([]);
  });

  it('needs a defaultSpan of positive integers', () => {
    expect(issuesFor({}).map((i) => i.path)).toEqual(['layout.defaultSpan']);
    for (const bad of [0, -1, 1.5, '2', null]) {
      expect(issuesFor({ defaultSpan: { cols: bad, rows: 2 } }).map((i) => i.path)).toEqual(['layout.defaultSpan.cols']);
    }
    expect(issuesFor({ defaultSpan: { cols: 2, rows: 2 }, minSpan: { cols: 2, rows: 0 } }).map((i) => i.path)).toEqual([
      'layout.minSpan.rows',
    ]);
    expect(issuesFor({ ...GOOD, extra: 1 }).map((i) => i.path)).toEqual(['layout.extra']);
    expect(issuesFor('big').map((i) => i.path)).toEqual(['layout']);
  });

  it('enforces minSpan <= defaultSpan <= maxSpan on each axis', () => {
    expect(issuesFor({ ...GOOD, minSpan: { cols: 9, rows: 2 } }).map((i) => i.path)).toEqual(['layout.minSpan']);
    expect(issuesFor({ ...GOOD, maxSpan: { cols: 24, rows: 3 } }).map((i) => i.path)).toEqual(['layout.maxSpan']);
    // An absent minSpan is 1 x 1, so any default clears it.
    expect(issuesFor({ defaultSpan: { cols: 1, rows: 1 }, maxSpan: { cols: 1, rows: 1 } })).toEqual([]);
  });

  it('rejects an unknown resize mode', () => {
    expect(issuesFor({ ...GOOD, resize: 'diagonal' }).map((i) => i.path)).toEqual(['layout.resize']);
  });

  it('pins the restricted axis: horizontal pins rows, vertical pins cols, none pins both', () => {
    const rowsPinned = { defaultSpan: { cols: 8, rows: 2 }, minSpan: { cols: 4, rows: 2 }, maxSpan: { cols: 24, rows: 2 } };
    expect(issuesFor({ ...rowsPinned, resize: 'horizontal' })).toEqual([]);
    // Each contradicting bound is its own issue: here both minSpan and maxSpan disagree.
    expect(issuesFor({ ...rowsPinned, resize: 'vertical' }).map((i) => i.path)).toEqual(['layout.resize', 'layout.resize']);
    expect(issuesFor({ ...GOOD, resize: 'horizontal' }).map((i) => i.message)).toEqual([
      'resize "horizontal" pins rows at 4, but minSpan.rows is 2',
      'resize "horizontal" pins rows at 4, but maxSpan.rows is 12',
    ]);
    expect(issuesFor({ ...rowsPinned, minSpan: { cols: 4, rows: 1 }, resize: 'horizontal' }).map((i) => i.path)).toEqual([
      'layout.resize',
    ]);
    const colsPinned = { defaultSpan: { cols: 6, rows: 3 }, minSpan: { cols: 6, rows: 1 }, maxSpan: { cols: 6, rows: 9 } };
    expect(issuesFor({ ...colsPinned, resize: 'vertical' })).toEqual([]);
    const fixed = { defaultSpan: { cols: 6, rows: 3 }, minSpan: { cols: 6, rows: 3 }, maxSpan: { cols: 6, rows: 3 } };
    expect(issuesFor({ ...fixed, resize: 'none' })).toEqual([]);
    expect(issuesFor({ ...colsPinned, resize: 'none' }).map((i) => i.path)).toEqual(['layout.resize', 'layout.resize']);
  });

  it('pins an axis without a minSpan or maxSpan: the mode itself holds it at the default', () => {
    // No bound is needed on the pinned axis, and none is forced on the free one.
    expect(issuesFor({ defaultSpan: { cols: 8, rows: 1 }, resize: 'horizontal' })).toEqual([]);
    expect(issuesFor({ defaultSpan: { cols: 8, rows: 2 }, minSpan: { cols: 4, rows: 2 }, resize: 'horizontal' })).toEqual([]);
    expect(issuesFor({ defaultSpan: { cols: 3, rows: 3 }, resize: 'none' })).toEqual([]);
  });
});
