// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The React-free catalog of widget descriptors: what the definition parser and the
// headless validators see of a widget registry. Constructing one validates EVERY
// descriptor and the set as a whole, reports every problem together, and returns a deeply
// frozen, independent instance — no module state, no global registration, so two hosts on
// one page can hold two catalogs that do not affect each other.

import { validateOptions, hasOwn, type OptionIssue, type WidgetOptionSpecs } from './option-schema';
import {
  SDK_API_VERSION,
  isBuiltinWidgetId,
  isNamespacedWidgetId,
  pinnedAxes,
  type WidgetChannel,
  type WidgetResizeMode,
  type WidgetDescriptor,
} from './widget-descriptor';
import { isRecord } from './is-record';

export interface DescriptorIssue {
  // The widget id the issue is about, or `#<index>` when the descriptor has no usable id.
  id: string;
  // Where in the descriptor, e.g. `defaults.max` or `binding.window.option`. Empty for an
  // issue about the descriptor as a whole.
  path: string;
  message: string;
}

export interface WidgetCatalog {
  has(id: string): boolean;
  get(id: string): WidgetDescriptor | undefined;
  // Descriptors in the order they were supplied.
  list(): readonly WidgetDescriptor[];
}

export class WidgetCatalogError extends Error {
  readonly issues: readonly DescriptorIssue[];

  constructor(issues: readonly DescriptorIssue[], subject = 'widget catalog') {
    super(describeIssues(subject, issues));
    this.name = 'WidgetCatalogError';
    this.issues = Object.freeze([...issues]);
  }
}

export function describeIssues(subject: string, issues: readonly DescriptorIssue[]): string {
  const lines = issues.map((i) => `  - ${i.id}${i.path ? `.${i.path}` : ''}: ${i.message}`);
  return `invalid ${subject} (${issues.length} problem${issues.length === 1 ? '' : 's'}):\n${lines.join('\n')}`;
}

// ---- Descriptor validation --------------------------------------------------

const CHANNELS: readonly WidgetChannel[] = ['measurement', 'alarm', 'control', 'selection', 'location', 'aggregate', 'none'];
const SEMVER = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;
const SELECTOR_KINDS = ['device', 'anchor', 'slot'];

// The keys each channel's binding policy may carry, mirroring ChannelBindingPolicy.
const BINDING_KEYS: Record<WidgetChannel, readonly string[]> = {
  measurement: ['window', 'windowSeconds', 'members', 'presence', 'history'],
  alarm: ['pageSize', 'state', 'severity', 'acknowledged', 'groupBy', 'countBySeverity', 'deviceType'],
  control: ['pageSize'],
  location: [],
  aggregate: ['windowSeconds'],
  selection: [],
  none: [],
};

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (!isRecord(value)) return false;
  const proto = Object.getPrototypeOf(value);
  return proto === Object.prototype || proto === null;
}

const join = (base: string, key: string): string => (base ? `${base}.${key}` : key);

// validateWidgetDescriptor checks one descriptor at runtime and returns every problem it
// finds — the same invariants the types express, because JSON from a package manifest or a
// JS caller bypasses the compiler. Accepts `unknown` for that reason.
export function validateWidgetDescriptor(descriptor: unknown): DescriptorIssue[] {
  const rawId = isRecord(descriptor) && typeof descriptor.id === 'string' ? descriptor.id : '';
  return collectDescriptorIssues(descriptor, rawId || '#?');
}

function collectDescriptorIssues(d: unknown, id: string): DescriptorIssue[] {
  const issues: DescriptorIssue[] = [];
  const add = (path: string, message: string): void => {
    issues.push({ id, path, message });
  };

  if (!isPlainObject(d)) {
    add('', 'a descriptor must be a plain object');
    return issues;
  }

  jsonOnly(d, '', add, new Set());

  // id — shape and the reserved rule (a trusted-code convention, not an enforcement).
  if (typeof d.id !== 'string' || d.id === '') {
    add('id', 'id must be a non-empty string');
  } else if (!isBuiltinWidgetId(d.id) && !isNamespacedWidgetId(d.id)) {
    add('id', `id ${JSON.stringify(d.id)} must be a reserved built-in id or a namespaced "scope/name" id`);
  }

  if (typeof d.version !== 'string' || !SEMVER.test(d.version)) {
    add('version', `version must be MAJOR.MINOR.PATCH, got ${JSON.stringify(d.version)}`);
  }
  if (d.sdkApiVersion !== SDK_API_VERSION) {
    add('sdkApiVersion', `unsupported sdkApiVersion ${JSON.stringify(d.sdkApiVersion)}; this build supports ${SDK_API_VERSION}`);
  }
  if (typeof d.optionsVersion !== 'number' || !Number.isInteger(d.optionsVersion) || d.optionsVersion < 1) {
    add('optionsVersion', `optionsVersion must be a positive integer, got ${JSON.stringify(d.optionsVersion)}`);
  }

  if (!isRecord(d.display)) {
    add('display', 'display must be an object');
  } else {
    if (typeof d.display.title !== 'string' || d.display.title === '') {
      add('display.title', 'display.title must be a non-empty string');
    }
    // A host renders these as text and groups its add menu by category, so a non-string
    // here is a crash (or an "[object Object]" heading) in someone else's console.
    for (const text of ['description', 'category', 'titleId', 'categoryId'] as const) {
      const value = d.display[text];
      if (value !== undefined && typeof value !== 'string') add(`display.${text}`, `display.${text} must be a string`);
    }
  }

  const channel = CHANNELS.find((c) => c === d.channel);
  if (!channel) add('channel', `channel must be one of ${CHANNELS.join(', ')}, got ${JSON.stringify(d.channel)}`);

  // A built-in is documented in the widgets package's own table (see its builtin-option-docs.ts), not on
  // the descriptor the renderer loads, so only an extension's descriptor must carry its option docs.
  const specs = checkOptions(d.options, !isBuiltinWidgetId(d.id), add);
  const constraints = checkConstraints(d.constraints, specs, add);

  if (specs) checkDefaults(d.defaults, specs, constraints, typeof d.id === 'string' ? d.id : id, add);
  else if (!isRecord(d.defaults)) add('defaults', 'defaults must be an object');

  checkDatasource(d.datasource, channel, add);
  if (channel) checkBinding(d.binding, channel, specs, add);
  checkInteractions(d.interactions, channel, add);
  checkLayout(d.layout, add);

  if (d.freshness !== undefined) {
    if (d.freshness !== true) add('freshness', 'freshness must be true when present');
    else if (channel && channel !== 'measurement') add('freshness', 'freshness applies only to the measurement channel');
  }

  return issues;
}

type Add = (path: string, message: string) => void;

// Descriptor values must survive JSON.stringify unchanged: no function, symbol, bigint,
// undefined, NaN/Infinity or non-plain object (a class instance, Date, Map…).
function jsonOnly(value: unknown, path: string, add: Add, ancestors: Set<object>): void {
  const here = path || '(descriptor)';
  switch (typeof value) {
    case 'string':
    case 'boolean':
      return;
    case 'number':
      if (!Number.isFinite(value)) add(path, `${here} is ${value}; descriptors hold JSON values only`);
      return;
    case 'undefined':
      add(path, `${here} is undefined; omit the key instead (descriptors hold JSON values only)`);
      return;
    case 'function':
    case 'symbol':
    case 'bigint':
      add(path, `${here} is a ${typeof value}; descriptors hold JSON values only`);
      return;
  }
  if (value === null) return;
  const obj = value as object;
  if (ancestors.has(obj)) {
    add(path, `${here} is a circular reference; descriptors hold JSON values only`);
    return;
  }
  ancestors.add(obj);
  if (Array.isArray(obj)) {
    obj.forEach((item, i) => jsonOnly(item, `${path}[${i}]`, add, ancestors));
  } else if (!isPlainObject(obj)) {
    add(path, `${here} is not a plain object; descriptors hold JSON values only`);
  } else {
    if (Object.getOwnPropertySymbols(obj).length > 0) add(path, `${here} has a symbol key; descriptors hold JSON values only`);
    for (const [key, item] of Object.entries(obj)) jsonOnly(item, join(path, key), add, ancestors);
  }
  ancestors.delete(obj);
}

// Returns the schema when it is structurally sound enough to check references and defaults
// against, else undefined (the faults are already reported).
function checkOptions(options: unknown, requireDoc: boolean, add: Add): WidgetOptionSpecs | undefined {
  if (!isRecord(options)) {
    add('options', 'options must be an object mapping option keys to specs');
    return undefined;
  }
  return checkOptionsAt(options, 'options.', requireDoc, add) ? (options as unknown as WidgetOptionSpecs) : undefined;
}

// Checks a table of option specs, reporting each fault under `prefix` + key. True when the table
// is structurally sound. The same rules judge a list option's item fields (prefix
// `options.<key>.item.`), so a field cannot be declared more loosely than an option.
function checkOptionsAt(options: Record<string, unknown>, prefix: string, requireDoc: boolean, add: Add): boolean {
  let sound = true;
  for (const [key, spec] of Object.entries(options)) {
    const at = `${prefix}${key}`;
    if (!isRecord(spec)) {
      add(at, 'an option spec must be an object');
      sound = false;
      continue;
    }
    if (typeof spec.doc !== 'string' && (requireDoc || spec.doc !== undefined)) {
      add(`${at}.doc`, 'doc must be a string');
    }
    for (const text of ['label', 'help', 'labelId', 'helpId', 'placeholder', 'placeholderId'] as const) {
      if (spec[text] !== undefined && typeof spec[text] !== 'string') add(`${at}.${text}`, `${text} must be a string`);
    }
    // Any non-empty string: a control this host does not implement is a fallback to the
    // generic field, not a fault (see OptionSpecBase.control).
    if (spec.control !== undefined && (typeof spec.control !== 'string' || spec.control === '')) {
      add(`${at}.control`, `control must be a non-empty string, got ${JSON.stringify(spec.control)}`);
    }
    switch (spec.kind) {
      case 'string':
      case 'boolean':
        break;
      case 'number':
        for (const bound of ['min', 'max'] as const) {
          if (spec[bound] !== undefined && typeof spec[bound] !== 'number') add(`${at}.${bound}`, `${bound} must be a number`);
        }
        break;
      case 'enum':
        if (!Array.isArray(spec.values) || spec.values.length === 0 || !spec.values.every((v) => typeof v === 'string')) {
          add(`${at}.values`, 'an enum option needs a non-empty list of string values');
          sound = false;
        }
        for (const table of ['valueLabels', 'valueLabelIds'] as const) {
          const labels = spec[table];
          if (labels === undefined) continue;
          if (!isRecord(labels)) {
            add(`${at}.${table}`, `${table} must be an object mapping enum values to strings`);
            continue;
          }
          for (const [value, label] of Object.entries(labels)) {
            if (typeof label !== 'string') add(`${at}.${table}.${value}`, `${table} entries must be strings`);
            else if (Array.isArray(spec.values) && !spec.values.includes(value)) {
              add(`${at}.${table}.${value}`, `${JSON.stringify(value)} is not one of this option's values`);
            }
          }
        }
        break;
      case 'list': {
        if (spec.maxItems !== undefined && (typeof spec.maxItems !== 'number' || !Number.isInteger(spec.maxItems) || spec.maxItems < 1)) {
          add(`${at}.maxItems`, 'maxItems must be a whole number of at least 1');
        }
        // A non-boolean `ordered` ("yes", 1) would be read as truthy by one host and ignored by
        // another; refuse it so every host sees the same list.
        if (spec.ordered !== undefined && typeof spec.ordered !== 'boolean') {
          add(`${at}.ordered`, 'ordered must be true or false');
        }
        if (!isRecord(spec.item) || Object.keys(spec.item).length === 0) {
          add(`${at}.item`, 'a list option needs an item object declaring at least one field');
          sound = false;
          break;
        }
        // A field is a scalar spec checked with the same rules as a top-level option, but
        // nesting a list is not offered: the editor row is flat.
        const fields = spec.item as Record<string, unknown>;
        const flat = Object.fromEntries(Object.entries(fields).filter(([, f]) => !isRecord(f) || f.kind !== 'list'));
        for (const key of Object.keys(fields)) {
          if (!(key in flat)) add(`${at}.item.${key}`, 'a list item field cannot itself be a list');
        }
        if (Object.keys(flat).length !== Object.keys(fields).length) {
          sound = false;
          break;
        }
        if (!checkOptionsAt(flat, `${at}.item.`, requireDoc, add)) sound = false;
        break;
      }
      default:
        add(`${at}.kind`, `kind must be string, number, boolean, enum or list, got ${JSON.stringify(spec.kind)}`);
        sound = false;
    }
  }
  return sound;
}

function checkConstraints(
  constraints: unknown,
  specs: WidgetOptionSpecs | undefined,
  add: Add,
): { kind: 'greater-than'; key: string; than: string }[] {
  if (constraints === undefined) return [];
  if (!Array.isArray(constraints)) {
    add('constraints', 'constraints must be a list');
    return [];
  }
  const valid: { kind: 'greater-than'; key: string; than: string }[] = [];
  constraints.forEach((c, i) => {
    const at = `constraints[${i}]`;
    if (!isRecord(c) || c.kind !== 'greater-than') {
      add(`${at}.kind`, `constraint kind must be "greater-than", got ${JSON.stringify(isRecord(c) ? c.kind : c)}`);
      return;
    }
    let ok = true;
    for (const field of ['key', 'than'] as const) {
      const name = c[field];
      if (typeof name !== 'string') {
        add(`${at}.${field}`, `${field} must name a declared option`);
        ok = false;
      } else if (specs && (!hasOwn(specs, name) || specs[name].kind !== 'number')) {
        add(`${at}.${field}`, `${field} ${JSON.stringify(name)} must name a declared number option`);
        ok = false;
      }
    }
    if (ok) valid.push({ kind: 'greater-than', key: c.key as string, than: c.than as string });
  });
  return valid;
}

function checkDefaults(
  defaults: unknown,
  specs: WidgetOptionSpecs,
  constraints: { kind: 'greater-than'; key: string; than: string }[],
  id: string,
  add: Add,
): void {
  if (!isRecord(defaults)) {
    add('defaults', 'defaults must be an object');
    return;
  }
  // 'missing' is not a defaults fault: a fresh widget may be authoring-incomplete (an image
  // has no url until the author gives one), so `required` binds a SAVED widget, not the
  // starting point.
  const found: OptionIssue[] = validateOptions(specs, defaults, { constraints, label: id }).filter(
    (issue) => issue.code !== 'missing',
  );
  for (const issue of found) add(`defaults.${issue.key}`, issue.message);
}

function checkDatasource(datasource: unknown, channel: WidgetChannel | undefined, add: Add): void {
  if (!isRecord(datasource)) {
    add('datasource', 'datasource must be an object');
    return;
  }
  const mode = datasource.mode;
  if (mode !== 'none' && mode !== 'optional' && mode !== 'required') {
    add('datasource.mode', `mode must be none, optional or required, got ${JSON.stringify(mode)}`);
    return;
  }
  if ((channel === 'none' || channel === 'selection') && mode !== 'none') {
    add('datasource.mode', `a ${channel}-channel widget opens no data channel, so its datasource mode must be "none"`);
  }
  if (mode === 'none') {
    if (datasource.selectors !== undefined) add('datasource.selectors', 'a datasource mode of "none" takes no selectors');
    return;
  }
  const selectors = datasource.selectors;
  if (!Array.isArray(selectors) || selectors.length === 0) {
    add('datasource.selectors', `a datasource mode of "${mode}" needs a non-empty list of selector kinds`);
    return;
  }
  selectors.forEach((s, i) => {
    if (typeof s !== 'string' || !SELECTOR_KINDS.includes(s)) {
      add(`datasource.selectors[${i}]`, `selector must be one of ${SELECTOR_KINDS.join(', ')}, got ${JSON.stringify(s)}`);
    } else if (channel === 'control' && s !== 'device' && s !== 'slot') {
      add(`datasource.selectors[${i}]`, 'a command target must be a device, so a control widget accepts only device and slot selectors');
    }
  });
}

function checkBinding(binding: unknown, channel: WidgetChannel, specs: WidgetOptionSpecs | undefined, add: Add): void {
  if (!isRecord(binding)) {
    add('binding', 'binding must be an object');
    return;
  }
  const allowed = BINDING_KEYS[channel];
  for (const key of Object.keys(binding)) {
    if (!allowed.includes(key)) add(`binding.${key}`, `a ${channel}-channel widget has no binding policy "${key}"`);
  }
  if (allowed.includes('pageSize') && binding.pageSize === undefined) {
    add('binding.pageSize', `a ${channel}-channel widget must declare binding.pageSize`);
  }

  const ref = (key: string, accept: (spec: WidgetOptionSpecs[string]) => boolean, expect: string, allowConst: boolean): void => {
    const policy = binding[key];
    if (policy === undefined) return;
    const at = `binding.${key}`;
    if (!isRecord(policy)) {
      add(at, `${key} must be an object`);
      return;
    }
    const hasConst = policy.const !== undefined;
    const hasOption = policy.option !== undefined;
    if (hasConst) {
      if (!allowConst) add(`${at}.const`, `${key} takes an option reference, not a constant`);
      else if (typeof policy.const !== 'number' || !Number.isInteger(policy.const) || policy.const < 1) {
        add(`${at}.const`, `const must be a positive integer, got ${JSON.stringify(policy.const)}`);
      }
      if (hasOption) add(at, `${key} must be a constant or an option reference, not both`);
      return;
    }
    if (!hasOption) {
      add(at, `${key} must be a constant or an option reference`);
      return;
    }
    const name = policy.option;
    if (typeof name !== 'string') {
      add(`${at}.option`, 'option must name a declared option key');
    } else if (specs) {
      if (!hasOwn(specs, name)) add(`${at}.option`, `option ${JSON.stringify(name)} is not a declared option key`);
      else if (!accept(specs[name])) add(`${at}.option`, `option ${JSON.stringify(name)} must be ${expect}`);
    }
    if (policy.min !== undefined && typeof policy.min !== 'number') add(`${at}.min`, 'min must be a number');
    if (policy.default !== undefined) {
      const wantsNumber = key === 'window' || key === 'windowSeconds' || key === 'pageSize';
      if (typeof policy.default !== (wantsNumber ? 'number' : 'string')) {
        add(`${at}.default`, `default must be a ${wantsNumber ? 'number' : 'string'}`);
      }
    }
  };

  const isNumber = (s: WidgetOptionSpecs[string]): boolean => s.kind === 'number';
  const isText = (s: WidgetOptionSpecs[string]): boolean => s.kind === 'string' || s.kind === 'enum';
  const isBoolEnum = (s: WidgetOptionSpecs[string]): boolean =>
    s.kind === 'enum' && s.values.every((v) => v === 'true' || v === 'false');

  ref('window', isNumber, 'a number option', false);
  ref('windowSeconds', isNumber, 'a number option', false);
  ref('pageSize', isNumber, 'a number option', true);
  ref('state', isText, 'a string or enum option', false);
  ref('severity', isText, 'a string or enum option', false);
  ref('deviceType', isText, 'a string or enum option', false);
  ref('acknowledged', isBoolEnum, 'an enum option whose values are only "true" and "false"', false);

  // A MODE binding turns a behaviour on: `members` makes a measurement widget a fleet widget
  // (`{ const: true }` always, or `{ option, equals }`), and `groupBy` makes an alarm widget group by
  // device type (`{ option, equals }` only). Either way a declared option holding `equals` is the switch.
  const modeBinding = (key: 'members' | 'groupBy'): void => {
    const mode = binding[key];
    if (mode === undefined) return;
    const m = isRecord(mode) ? mode : {};
    const at = `binding.${key}`;
    const allowConst = key === 'members';
    const isConst = m.const !== undefined;
    if (isConst ? !allowConst || m.option !== undefined || m.equals !== undefined : typeof m.option !== 'string' || typeof m.equals !== 'string') {
      add(at, `${key} must be ${allowConst ? '{ const: true } or ' : ''}{ option, equals } naming a declared option and a value of it`);
    } else if (isConst) {
      if (m.const !== true) add(`${at}.const`, `const must be true, got ${JSON.stringify(m.const)}`);
    } else if (specs) {
      const spec = hasOwn(specs, m.option as string) ? specs[m.option as string] : undefined;
      if (!spec) add(`${at}.option`, `option ${JSON.stringify(m.option)} is not a declared option key`);
      else if (!isText(spec)) add(`${at}.option`, `option ${JSON.stringify(m.option)} must be a string or enum option`);
      else if (spec.kind === 'enum' && !spec.values.includes(m.equals as string)) {
        add(`${at}.equals`, `${JSON.stringify(m.equals)} is not one of the option's values`);
      }
    }
  };
  // `presence` and `history` are constant switches and nothing else: `{ const: true }`. `presence` makes a
  // measurement widget read its devices' connectivity (online or offline, last seen) in place of their
  // readings; `history` makes it read one device's stored history in time buckets, not the live feed.
  for (const key of ['presence', 'history'] as const) {
    const flag = binding[key];
    if (flag !== undefined && !(isRecord(flag) && flag.const === true && Object.keys(flag).length === 1)) {
      add(`binding.${key}`, 'must be { const: true }');
    }
  }
  modeBinding('members');
  modeBinding('groupBy');
  // `countBySeverity` is a constant switch and nothing else: `{ const: true }`.
  const counts = binding.countBySeverity;
  if (counts !== undefined && !(isRecord(counts) && counts.const === true && Object.keys(counts).length === 1)) {
    add('binding.countBySeverity', 'must be { const: true }');
  }
}

function readSpan(value: unknown, path: string, add: Add): { cols: number; rows: number } | undefined {
  if (!isRecord(value)) {
    add(path, `${path} must be an object with cols and rows`);
    return undefined;
  }
  let ok = true;
  for (const axis of ['cols', 'rows'] as const) {
    const n = value[axis];
    if (typeof n !== 'number' || !Number.isInteger(n) || n < 1) {
      add(`${path}.${axis}`, `${axis} must be a positive integer, got ${JSON.stringify(n)}`);
      ok = false;
    }
  }
  for (const key of Object.keys(value)) {
    if (key !== 'cols' && key !== 'rows') add(`${path}.${key}`, `unknown span field "${key}"`);
  }
  return ok ? { cols: value.cols as number, rows: value.rows as number } : undefined;
}

function checkLayout(layout: unknown, add: Add): void {
  if (layout === undefined) return;
  if (!isRecord(layout)) {
    add('layout', 'layout must be an object');
    return;
  }
  for (const key of Object.keys(layout)) {
    if (!['defaultSpan', 'minSpan', 'maxSpan', 'resize'].includes(key)) add(`layout.${key}`, `unknown layout field "${key}"`);
  }
  let def: { cols: number; rows: number } | undefined;
  if (layout.defaultSpan === undefined) add('layout.defaultSpan', 'layout needs a defaultSpan');
  else def = readSpan(layout.defaultSpan, 'layout.defaultSpan', add);
  const min = layout.minSpan === undefined ? undefined : readSpan(layout.minSpan, 'layout.minSpan', add);
  const max = layout.maxSpan === undefined ? undefined : readSpan(layout.maxSpan, 'layout.maxSpan', add);

  const mode = layout.resize ?? 'both';
  if (mode !== 'both' && mode !== 'horizontal' && mode !== 'vertical' && mode !== 'none') {
    add('layout.resize', `resize must be both, horizontal, vertical or none, got ${JSON.stringify(layout.resize)}`);
    return;
  }

  // minSpan <= defaultSpan <= maxSpan, per axis. An absent minSpan is 1 x 1.
  const floor = min ?? { cols: 1, rows: 1 };
  for (const axis of ['cols', 'rows'] as const) {
    if (def && floor[axis] > def[axis]) add('layout.minSpan', `minSpan.${axis} (${floor[axis]}) is above defaultSpan.${axis} (${def[axis]})`);
    if (def && max && def[axis] > max[axis]) add('layout.maxSpan', `maxSpan.${axis} (${max[axis]}) is below defaultSpan.${axis} (${def[axis]})`);
    if (!def && min && max && min[axis] > max[axis]) add('layout.maxSpan', `maxSpan.${axis} is below minSpan.${axis}`);
  }

  // A restricted mode pins an axis at defaultSpan, so a declared bound must not contradict it.
  // The free axis stays as open as the bounds leave it: no maxSpan means no ceiling there.
  if (!def) return;
  for (const axis of pinnedAxes(mode as WidgetResizeMode)) {
    for (const [name, bound] of [['minSpan', min], ['maxSpan', max]] as const) {
      if (bound && bound[axis] !== def[axis]) {
        add('layout.resize', `resize "${mode}" pins ${axis} at ${def[axis]}, but ${name}.${axis} is ${bound[axis]}`);
      }
    }
  }
}

function checkInteractions(interactions: unknown, channel: WidgetChannel | undefined, add: Add): void {
  if (!isRecord(interactions)) {
    add('interactions', 'interactions must be an object');
    return;
  }
  const only: Record<string, WidgetChannel> = { alarmActions: 'alarm', commandSend: 'control' };
  for (const [key, value] of Object.entries(interactions)) {
    const at = `interactions.${key}`;
    if (key !== 'alarmActions' && key !== 'commandSend' && key !== 'selection' && key !== 'candidates') {
      add(at, `unknown interaction "${key}"`);
    } else if (value !== true) {
      add(at, `${key} must be true when declared (omit it otherwise)`);
    } else if (channel && only[key] && only[key] !== channel) {
      add(at, `${key} is only available to ${only[key]}-channel widgets, not ${channel}`);
    }
  }
}

// ---- Catalog ----------------------------------------------------------------

// Freezes in place, recursively. Idempotent, so a table shared between registries (the
// built-in option specs are) is simply frozen again. Descriptors hold JSON values only, so
// this never meets a function or a cycle.
//
// It descends even into an object that is ALREADY frozen: Object.freeze is shallow, so a
// caller's `Object.freeze(descriptor)` says nothing about `descriptor.defaults`, and
// stopping at the first frozen object would leave those children mutable.
function deepFreeze<T>(value: T): T {
  if (typeof value === 'object' && value !== null) {
    Object.freeze(value);
    for (const child of Object.values(value)) deepFreeze(child);
  }
  return value;
}

// createWidgetCatalog validates every descriptor and the set (duplicate ids), then throws
// ONE WidgetCatalogError carrying every issue, or returns a sealed catalog.
export function createWidgetCatalog(descriptors: readonly WidgetDescriptor[]): WidgetCatalog {
  const issues = collectCatalogIssues(descriptors);
  if (issues.length > 0) throw new WidgetCatalogError(issues);
  return sealCatalog(descriptors);
}

// The issues for a set of descriptors: each descriptor's own, plus duplicate ids. A pure
// report — it cannot produce a catalog. Exposed so the widget registry can report descriptor
// and implementation faults together before it builds anything.
export function collectCatalogIssues(descriptors: readonly unknown[]): DescriptorIssue[] {
  if (!Array.isArray(descriptors)) {
    return [{ id: '#?', path: '', message: 'descriptors must be an array' }];
  }
  const issues: DescriptorIssue[] = [];
  const seen = new Map<string, number>();
  descriptors.forEach((d, index) => {
    const id = isRecord(d) && typeof d.id === 'string' && d.id !== '' ? d.id : `#${index}`;
    issues.push(...collectDescriptorIssues(d, id));
    if (id.startsWith('#')) return;
    const first = seen.get(id);
    if (first === undefined) seen.set(id, index);
    else issues.push({ id, path: 'id', message: `duplicate widget id (first registered at position ${first}, again at ${index})` });
  });
  return issues;
}

// Builds the sealed catalog from descriptors ALREADY validated by collectCatalogIssues.
// Module-private on purpose: the only way to obtain a catalog is createWidgetCatalog, which
// validates first, so the published contract has no unvalidated path to a catalog.
function sealCatalog(descriptors: readonly WidgetDescriptor[]): WidgetCatalog {
  const byId = new Map<string, WidgetDescriptor>();
  for (const d of descriptors) byId.set(d.id, deepFreeze(d));
  const ordered: readonly WidgetDescriptor[] = Object.freeze([...byId.values()]);
  return Object.freeze({
    has: (id: string) => byId.has(id),
    get: (id: string) => byId.get(id),
    list: () => ordered,
  });
}
