// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The option-schema vocabulary and the engine that checks an options bag against one.
//
// A widget's stored `options` is `Record<string, unknown>`: dashboard-management keeps a
// definition as opaque JSON and the definition parser does not look inside it. That leaves
// every option key owned by NOTHING unless something declares it, and this module is the
// language the declaration is written in. It is React-free and knows no widget type, so a
// headless validator, the widget SDK and the built-in widget package all read one engine —
// the built-in table (WIDGET_OPTIONS in @devicechain/widgets) is just one schema among the
// ones a registry can hold.
//
// The schema is JSON-serializable data. Rules that span more than one option are therefore
// declared as `constraints` (data), never as functions.

// ---- Spec vocabulary --------------------------------------------------------

// `required` marks an option whose ABSENCE leaves the widget with nothing to show.
// The renderer never throws on a missing option — an image with no `url` renders "No
// image URL", a command-button with no `commandName` renders "No command configured" —
// which is deliberate graceful degradation for a hand-edited definition, but is exactly
// the failure a generated dashboard must not ship. So `required` is an AUTHORING
// completeness contract, not a claim that the renderer rejects the option's absence.
//
// Absent here means absent AS THE RENDERER SEES IT, which for a required string includes
// the EMPTY string: every one of the four required built-in options is read into a
// truthiness test (`url ? … : 'No image URL'`, `!commandName`, `!target`), so `''`
// produces the same placeholder as an omitted key. Treating `''` as present would let a
// generated dashboard carrying `"url": ""` validate clean and render nothing.
interface OptionSpecBase {
  // What the option does. Short: this feeds the config panel's help text and the
  // published "available widgets" page. Required, and present on every descriptor a package
  // publishes. The one exception is not a type: @devicechain/widgets's render-only definitions
  // (`builtinWidgetDefinitionsWithoutDocs`) omit it so a host that only renders boards never
  // downloads prose it never shows, which is why the validator excuses a BUILT-IN id's missing doc.
  doc: string;
  required?: true;
  // Portable default text for the option's label and help. A host that cannot translate
  // an option it has never heard of falls back to these, so a third-party option is
  // understandable without a host message catalog. Optional so existing schemas stay valid.
  label?: string;
  help?: string;
  // Message ids a host MAY look up in its own catalog before falling back to the text
  // above. Built-ins declare today's console keys here; the host resolves them.
  labelId?: string;
  helpId?: string;
  // Names a SPECIALIZED authoring workflow for this option, chosen by the descriptor and never
  // inferred from the option's key. Absent means the generic field the spec kind implies.
  //
  // OPEN vocabulary, deliberately: any non-empty string registers, and a host that does not
  // implement the named workflow renders the generic field for that option. A closed list
  // here would make every package that names a newer control unregistrable on an older
  // host. Name a package's own controls in a namespace ('acme/valve-curve'); the built-in
  // names below are unprefixed.
  control?: string;
}

// The specialized workflows the DeviceChain console implements — what a built-in may name
// and expect honoured. Not a validation whitelist: a control outside this list is valid and
// simply falls back. `command-vocabulary` picks a device command and bakes its key, label and
// parameters together; `slot-target` picks one of the dashboard's slots; `basemap` marks the
// raster tile source/credit pair; `device-type` picks a device type from the tenant's catalogue and
// stores its token (on a string option, or a string field of a list item).
export const HOST_OPTION_CONTROLS = ['command-vocabulary', 'slot-target', 'basemap', 'device-type'] as const;
export type HostOptionControl = (typeof HOST_OPTION_CONTROLS)[number];

export interface StringOptionSpec extends OptionSpecBase {
  kind: 'string';
  // 'json' additionally requires the string to parse as JSON. Used for the command
  // button's baked parameterSchema, where a malformed schema is silently swallowed
  // (parseParameterSchema returns []) and renders as a bare Send button.
  format?: 'json';
}

export interface NumberOptionSpec extends OptionSpecBase {
  kind: 'number';
  min?: number;
  max?: number;
  integer?: true;
}

export interface BooleanOptionSpec extends OptionSpecBase {
  kind: 'boolean';
}

export interface EnumOptionSpec<V extends string = string> extends OptionSpecBase {
  kind: 'enum';
  values: readonly V[];
  // Portable text and optional host message ids for the choices and for the "unset" entry,
  // by the same lookup-then-fall-back rule as `label`. Absent, a choice shows its raw value.
  valueLabels?: Readonly<Record<string, string>>;
  valueLabelIds?: Readonly<Record<string, string>>;
  placeholder?: string;
  placeholderId?: string;
}

// The scalar kinds a list item's fields may take. Not `list` itself: nesting is not offered,
// so every option stays authorable by one flat editor row.
export type ListItemFieldSpec = StringOptionSpec | NumberOptionSpec | BooleanOptionSpec | EnumOptionSpec;

// An ordered list of small records, e.g. thresholds: `[{ value, level, label? }]`. Each item is
// an object whose fields are declared once in `item`; a field marked `required` must be present
// in every item, and a key the item does not declare is refused (a free `color` next to a
// severity `level` is exactly what this exists to turn away).
export interface ListOptionSpec extends OptionSpecBase {
  kind: 'list';
  item: Readonly<Record<string, ListItemFieldSpec>>;
  maxItems?: number;
  // The ORDER of the entries means something (tab order, say), so an editor offers move up /
  // move down on each. Absent or false, a list is a set the editor need not let the author reorder.
  ordered?: boolean;
}

export type OptionSpec = StringOptionSpec | NumberOptionSpec | BooleanOptionSpec | EnumOptionSpec | ListOptionSpec;

export type WidgetOptionSpecs = Record<string, OptionSpec>;

// A rule spanning more than one option, as DATA. `greater-than` requires the value of
// `key` to be strictly greater than the value of `than`, and is only judged when both are
// finite numbers (an absent or mistyped value is reported by its own spec, not twice).
export interface GreaterThanConstraint<K extends string = string> {
  kind: 'greater-than';
  key: K;
  than: K;
}

export type OptionConstraint<K extends string = string> = GreaterThanConstraint<K>;

// ---- Derived compile-time types ---------------------------------------------

// The value type a spec accepts. Enum first: an EnumOptionSpec's values carry the literal
// union, which is the whole point of declaring one.
export type SpecValue<S> = S extends { kind: 'list'; item: infer I }
  ? OptionsOf<I>[]
  : S extends { kind: 'enum'; values: readonly (infer V)[] }
  ? V extends string
    ? V
    : never
  : S extends { kind: 'number' }
    ? number
    : S extends { kind: 'boolean' }
      ? boolean
      : S extends { kind: 'string' }
        ? string
        : never;

export type RequiredOptionKeys<S> = {
  [K in keyof S]: S[K] extends { required: true } ? K : never;
}[keyof S];

// The typed options bag a schema describes: every declared key at its declared type,
// required ones mandatory.
export type OptionsOf<S> = {
  [K in RequiredOptionKeys<S>]: SpecValue<S[K]>;
} & {
  [K in Exclude<keyof S, RequiredOptionKeys<S>>]?: SpecValue<S[K]>;
};

// A descriptor's starting values: every declared key at its declared type, but EVERY key
// optional. A fresh widget is allowed to be authoring-incomplete — an image has no `url`
// until the author supplies one — so `required` cannot bind the defaults.
export type DefaultsOf<S> = { [K in keyof S]?: SpecValue<S[K]> };

// The declared keys of a given kind, for typed references from a binding policy. The `infer`
// makes the test distribute over a union of specs, so the broad WidgetOptionSpecs (every
// key a union of the four kinds) still names its number keys instead of collapsing to never.
export type OptionKeysOfKind<S, Kind extends OptionSpec['kind']> = {
  [K in keyof S & string]: S[K] extends infer X ? (X extends { kind: Kind } ? K : never) : never;
}[keyof S & string];

// ---- Runtime validation -----------------------------------------------------

export type OptionIssueCode =
  // a key the renderer for this widget type reads nothing from
  | 'unknown'
  // present, but the wrong JavaScript type — read back as undefined, rendered as default
  | 'type'
  // a string/number outside the option's declared legal values or bounds
  | 'enum'
  | 'range'
  // a `required` option absent, so the widget renders its "not configured" placeholder
  | 'missing'
  // a format:'json' string that does not parse
  | 'json'
  // a rule spanning more than one option (see OptionConstraint)
  | 'invariant';

export interface OptionIssue {
  // The id of the widget whose schema the bag was checked against (a built-in type or a
  // namespaced extension id). NOT a widget instance id.
  widgetId: string;
  // The offending option key. For a constraint, the key the fix belongs on.
  key: string;
  code: OptionIssueCode;
  message: string;
}

export interface ValidateOptionsContext {
  constraints?: readonly OptionConstraint[];
  // Names the widget in messages and on each issue.
  label: string;
}

// hasOwnProperty.call rather than `in` or Object.hasOwn: every specs table inherits
// Object.prototype, so `in` reports 'toString', 'constructor' and friends as declared.
// A stored definition arrives via JSON.parse, which makes '__proto__' an ordinary own key
// — a shape a real definition can carry. (And the package targets ES2020.)
export function hasOwn(obj: object, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(obj, key);
}

// validateOptions reports every way an options bag disagrees with its schema. An empty
// result means every key present is declared, at a value the schema accepts, and no
// required option is missing.
//
// It is deliberately a REPORT rather than a throw: the renderer is tolerant by design and
// must stay that way for a hand-edited definition, so the strictness belongs to whoever
// produces definitions (fixtures, generators, the config panel) and to the tests that
// gate them. Callers decide which codes are fatal — a stored dashboard carrying an
// 'unknown' leftover is untidy; a generated one carrying 'missing' is broken.
export function validateOptions(
  specs: WidgetOptionSpecs,
  options: Record<string, unknown> | undefined,
  context: ValidateOptionsContext,
): OptionIssue[] {
  const { label: widgetId } = context;
  const issues: OptionIssue[] = [];
  const bag = options ?? {};

  for (const [key, spec] of Object.entries(specs)) {
    const value = bag[key];
    // '' counts as absent for a required option — see OptionSpecBase.
    if (value === undefined || (spec.required && value === '')) {
      if (spec.required) {
        issues.push({ widgetId, key, code: 'missing', message: `${key} is required but not set` });
      }
      continue;
    }
    issues.push(...checkValue(widgetId, key, spec, value));
  }

  for (const key of Object.keys(bag)) {
    if (hasOwn(specs, key)) continue;
    issues.push({
      widgetId,
      key,
      code: 'unknown',
      message: `${key} is not read by a ${widgetId} widget`,
    });
  }

  for (const constraint of context.constraints ?? []) {
    const issue = checkConstraint(constraint, bag);
    if (issue) issues.push({ widgetId, ...issue });
  }

  return issues;
}

function checkConstraint(
  constraint: OptionConstraint,
  bag: Record<string, unknown>,
): Pick<OptionIssue, 'key' | 'code' | 'message'> | undefined {
  switch (constraint.kind) {
    case 'greater-than': {
      const value = bag[constraint.key];
      const than = bag[constraint.than];
      if (typeof value !== 'number' || typeof than !== 'number') return undefined;
      if (!Number.isFinite(value) || !Number.isFinite(than)) return undefined;
      if (value > than) return undefined;
      return {
        key: constraint.key,
        code: 'invariant',
        message: `${constraint.key} (${value}) must be greater than ${constraint.than} (${than})`,
      };
    }
  }
}

function checkValue(widgetId: string, key: string, spec: OptionSpec, value: unknown): OptionIssue[] {
  const issue = (code: OptionIssueCode, message: string): OptionIssue[] => [{ widgetId, key, code, message }];

  switch (spec.kind) {
    case 'string':
      if (typeof value !== 'string') {
        return issue('type', `${key} must be a string, got ${describe(value)}`);
      }
      if (spec.format === 'json' && !parsesAsJson(value)) {
        return issue('json', `${key} must be valid JSON`);
      }
      return [];

    case 'number':
      // Number.isFinite mirrors optNumber: NaN and ±Infinity survive JSON round-trips
      // through some producers and are read back as undefined by the renderer.
      if (typeof value !== 'number' || !Number.isFinite(value)) {
        return issue('type', `${key} must be a finite number, got ${describe(value)}`);
      }
      if (spec.integer && !Number.isInteger(value)) {
        return issue('range', `${key} must be a whole number, got ${value}`);
      }
      if (spec.min !== undefined && value < spec.min) {
        return issue('range', `${key} must be at least ${spec.min}, got ${value}`);
      }
      if (spec.max !== undefined && value > spec.max) {
        return issue('range', `${key} must be at most ${spec.max}, got ${value}`);
      }
      return [];

    case 'boolean':
      // optBoolean treats anything but literal `true` as false, so a wrong type here is a
      // silently-disabled feature rather than a visible error.
      if (typeof value !== 'boolean') {
        return issue('type', `${key} must be a boolean, got ${describe(value)}`);
      }
      return [];

    case 'enum':
      if (typeof value !== 'string') {
        return issue('type', `${key} must be a string, got ${describe(value)}`);
      }
      if (!spec.values.includes(value)) {
        return issue('enum', `${key} must be one of ${spec.values.join(', ')}, got ${JSON.stringify(value)}`);
      }
      return [];

    case 'list': {
      if (!Array.isArray(value)) return issue('type', `${key} must be a list, got ${describe(value)}`);
      if (spec.maxItems !== undefined && value.length > spec.maxItems) {
        return issue('range', `${key} holds at most ${spec.maxItems} entries, got ${value.length}`);
      }
      const out: OptionIssue[] = [];
      value.forEach((entry, i) => {
        const at = `${key}[${i}]`;
        // Reported under the ENTRY's own key (`thresholds[0].level`), so the author is pointed at the cell.
        const at_ = (path: string, code: OptionIssueCode, message: string): OptionIssue => ({ widgetId, key: path, code, message });
        if (typeof entry !== 'object' || entry === null || Array.isArray(entry)) {
          out.push(at_(at, 'type', `${at} must be an object, got ${describe(entry)}`));
          return;
        }
        const record = entry as Record<string, unknown>;
        for (const [field, fieldSpec] of Object.entries(spec.item)) {
          const fieldValue = record[field];
          if (fieldValue === undefined || (fieldSpec.required && fieldValue === '')) {
            if (fieldSpec.required) out.push(at_(`${at}.${field}`, 'missing', `${at}.${field} is required but not set`));
            continue;
          }
          out.push(...checkValue(widgetId, `${at}.${field}`, fieldSpec, fieldValue));
        }
        for (const field of Object.keys(record)) {
          if (!hasOwn(spec.item, field)) {
            out.push(at_(`${at}.${field}`, 'unknown', `${at}.${field} is not a field of ${key} entries`));
          }
        }
      });
      return out;
    }
  }
}

function parsesAsJson(value: string): boolean {
  try {
    JSON.parse(value);
    return true;
  } catch {
    return false;
  }
}

// describe names the offending value's type in a message, quoting a string so an empty
// one is visible and distinguishing null from an object.
function describe(value: unknown): string {
  if (value === null) return 'null';
  if (typeof value === 'string') return `string ${JSON.stringify(value)}`;
  if (Array.isArray(value)) return 'an array';
  return `${typeof value} ${JSON.stringify(value)}`;
}

// numberSpec returns the declared bounds for a numeric option, so an authoring UI can
// constrain its input from the schema rather than from a second, hand-written copy of the
// same numbers.
//
// This exists because the console's numeric inputs were unbounded: a `precision` of
// -1 is a legal number, reaches Number.prototype.toFixed, and throws a RangeError
// DURING RENDER. formatValue clamps so the renderer survives, but the definition is
// still wrong and the author had no way to know. A panel that reads the schema cannot
// offer the value at all.
export function numberSpec(specs: WidgetOptionSpecs, key: string): NumberOptionSpec | undefined {
  if (!hasOwn(specs, key)) return undefined;
  const spec = specs[key];
  return spec.kind === 'number' ? spec : undefined;
}

// clampNumberOption folds a raw number into what a numeric option's schema allows, or
// returns undefined when it is not a number at all.
//
// Separate from the input that calls it because the correction is the part worth
// testing: an authoring UI's keystroke handling is awkward to exercise, and the
// question that actually matters — what value gets STORED for a given input — is a
// pure function of the schema and the number.
export function clampNumberOption(spec: NumberOptionSpec | undefined, value: number): number | undefined {
  if (!Number.isFinite(value)) return undefined;
  if (!spec) return value;
  let next = spec.integer ? Math.trunc(value) : value;
  if (spec.min !== undefined) next = Math.max(spec.min, next);
  if (spec.max !== undefined) next = Math.min(spec.max, next);
  return next;
}

// enumValues returns the legal values of an enum option, so an authoring UI can build its
// dropdown from the schema instead of a private copy of the same list.
//
// A dropdown offering a value the schema rejects lets a panel author a definition the
// validator refuses; one MISSING a value silently removes a filter an author could
// previously express. Both are invisible without a shared source.
export function enumValues(specs: WidgetOptionSpecs, key: string): readonly string[] | undefined {
  if (!hasOwn(specs, key)) return undefined;
  const spec = specs[key];
  return spec.kind === 'enum' ? spec.values : undefined;
}

// applyOptionPatch is the VALIDATED write a registered editor is given: it returns the
// options bag with `patch` applied, or throws on a write the schema cannot hold.
//
// An editor is developer code running in the host page, so a mistake in it should be loud at
// the moment it is made, not a half-configured widget discovered at publish. The rules:
//   - a key the schema does not declare is refused (an editor cannot grow the stored bag);
//   - a value of the wrong kind, an enum value outside `values`, or a `format: 'json'`
//     string that does not parse, is refused;
//   - a number is folded into its bounds, as the generic field does;
//   - `undefined`, an empty string, and `false` DROP the key (an unset option reads as its
//     default, and an opt-in flag is absent-when-off), as the generic fields do.
// Keys the patch does not name — including ones outside the schema that an older release
// stored — are carried through untouched.
export function applyOptionPatch(
  specs: WidgetOptionSpecs,
  options: Readonly<Record<string, unknown>> | undefined,
  patch: Readonly<Record<string, unknown>>,
  label = 'widget',
): Record<string, unknown> {
  const next: Record<string, unknown> = { ...(options ?? {}) };
  for (const [key, value] of Object.entries(patch)) {
    if (!hasOwn(specs, key)) throw new Error(`${label}: "${key}" is not a declared option`);
    const spec = specs[key];
    if (
      value === undefined ||
      value === '' ||
      (spec.kind === 'boolean' && value === false) ||
      (spec.kind === 'list' && Array.isArray(value) && value.length === 0)
    ) {
      delete next[key];
      continue;
    }
    const refuse = (expected: string): never => {
      throw new TypeError(`${label}: option "${key}" takes ${expected}, got ${JSON.stringify(value)}`);
    };
    switch (spec.kind) {
      case 'string':
        if (typeof value !== 'string') refuse('a string');
        // A `format: 'json'` option is judged as the option validator judges it: a string
        // that does not parse is refused here rather than flagged later at publish.
        if (spec.format === 'json' && !parsesAsJson(value as string)) refuse('a JSON string');
        next[key] = value;
        break;
      case 'boolean':
        if (typeof value !== 'boolean') refuse('a boolean');
        next[key] = value;
        break;
      case 'number': {
        if (typeof value !== 'number') refuse('a finite number');
        const clamped = clampNumberOption(spec, value as number);
        if (clamped === undefined) refuse('a finite number');
        next[key] = clamped;
        break;
      }
      case 'enum':
        if (typeof value !== 'string' || !spec.values.includes(value)) {
          refuse(`one of ${spec.values.map((v) => JSON.stringify(v)).join(', ')}`);
        }
        next[key] = value;
        break;
      case 'list': {
        // Judged exactly as the validator judges a stored list: a wrong-typed field, a missing
        // required one, or a key the entries do not declare is refused here, not found at publish.
        const bad = checkValue(label, key, spec, value);
        if (bad.length > 0) refuse(`a list of ${key} entries (${bad[0].message})`);
        next[key] = value;
        break;
      }
    }
  }
  return next;
}
