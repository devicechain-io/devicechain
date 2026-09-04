// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The config panel's type-specific fields, DERIVED from the option schema the widgets
// package owns (WIDGET_OPTIONS) instead of written out per widget type (ADR-076 half 1,
// decision 2).
//
// 🔴 WHAT THE `if (widget.type === …)` LADDER THIS REPLACES ACTUALLY COST. The ladder was
// exhaustive over nothing: adding a widget type compiled and produced a widget with no
// configuration UI, and — the quieter half — adding an OPTION to a type the ladder already
// handled compiled too, and produced a key the renderer reads and no author can set. Both
// had happened. `window`, `measurement`, `align`, `fontSize` and `fit` were honored at
// render and unauthorable here; `label` and `image` could not be given the frame title
// their own schemas declare; an alarm table could not be given a `precision` or a
// `selectionTarget`. Nothing failed, in either direction — the option simply did nothing,
// which reads to an author as a broken widget rather than a missing field.
//
// Deriving the field list closes both: this module walks WIDGET_OPTIONS[type] and emits a
// control per declared key, so the fields ARE the schema. What it cannot do by itself is
// prove the emitted controls actually write those keys — a control that renders nothing,
// or writes a misspelled one, still walks the same list. That is what
// WidgetConfigPanel.exhaustiveness.test.tsx drives and asserts, and it is the gate that
// makes this file worth anything.
//
// The chain that makes an authored key a key the RENDERER honors has one more link, and it
// is not asserted here: options.test.ts in @devicechain/widgets scans the renderer's own
// source and requires WIDGET_OPTIONS to declare exactly the keys the widgets read, in both
// directions, per type. This module writes what that table declares; that test proves the
// table is what the renderer reads.

import type { SlotDefinition, WidgetInstance, WidgetType } from '@devicechain/dashboards';
import {
  WIDGET_OPTIONS,
  clampNumberOption,
  numberOptionSpec,
  type NumberOptionSpec,
  type OptionSpec,
  type WidgetOptionSpecs,
} from '@devicechain/widgets';
import type { TFunction } from 'i18next';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Checkbox } from '@/components/ui/checkbox';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { useQuery } from '@/lib/hooks/use-query';
import {
  getDeviceCommandVocabulary,
  listCommandDefinitionsForDevice,
} from '@/lib/api/device-management';
import { commandChoices, type PickableCommand } from '@/routes/devices/commandVocabulary';

// ---- Keys, typed from the schema --------------------------------------------

// Every option key any widget declares. Derived rather than listed, so the wording tables
// below are held to the schema by the compiler: a new option does not build until it is
// given a label and a hint decision.
type OptionKey = {
  [T in WidgetType]: keyof (typeof WIDGET_OPTIONS)[T];
}[WidgetType] &
  string;

// Every (enum option, legal value) pair, as `key:value`. Same purpose one level down: a
// value added to an enum in the schema does not build until it is given a label, because
// an unlabelled dropdown entry renders its translation KEY as the choice text.
type EnumValueIdIn<T extends WidgetType> = {
  [K in keyof (typeof WIDGET_OPTIONS)[T]]: (typeof WIDGET_OPTIONS)[T][K] extends {
    kind: 'enum';
    values: readonly (infer V)[];
  }
    ? V extends string
      ? `${K & string}:${V}`
      : never
    : never;
}[keyof (typeof WIDGET_OPTIONS)[T]];

type EnumValueId = { [T in WidgetType]: EnumValueIdIn<T> }[WidgetType];

// The option keys that are enums — recovered from the pairs above rather than listed, for
// the same reason.
type EnumOptionKey = EnumValueId extends `${infer K}:${string}` ? K : never;

// ---- Wording ----------------------------------------------------------------

// The field label per option key.
//
// A Record over OptionKey rather than a lookup with a fallback: i18next renders an
// unmatched key as the key itself, so a fallback would put `widgetLabelWhatever` on screen
// as a field label. The catalogs are held to these values by widgetOptionFields.test.ts,
// which reads them per locale through getResource — t() cannot answer the question, since
// `fallbackLng: 'en'` resolves a missing Spanish string to the English one.
const OPTION_LABEL_KEYS: Record<OptionKey, string> = {
  title: 'widgetLabelTitle',
  text: 'widgetLabelText',
  url: 'widgetLabelImageUrl',
  alt: 'widgetLabelAltText',
  align: 'widgetLabelAlign',
  fontSize: 'widgetLabelFontSize',
  fit: 'widgetLabelFit',
  unit: 'widgetLabelUnit',
  min: 'widgetLabelMin',
  max: 'widgetLabelMax',
  precision: 'widgetLabelPrecision',
  measurement: 'widgetLabelMeasurement',
  window: 'widgetLabelWindow',
  flashOnChange: 'widgetFlashOnChange',
  state: 'widgetLabelState',
  severity: 'widgetLabelSeverity',
  acknowledged: 'widgetLabelAcknowledged',
  maxRows: 'widgetLabelMaxRows',
  selectionTarget: 'widgetLabelTargetSlot',
  commandName: 'widgetCommandHeading',
  commandLabel: 'widgetCommandHeading',
  parameterSchema: 'widgetCommandHeading',
  tileUrl: 'widgetLabelTileUrl',
  attribution: 'widgetLabelAttribution',
};

// The helper line under a field, or `null` for a field that needs none.
//
// 🔴 `| null` rather than an optional property, deliberately. An optional entry lets a new
// option key be added with no hint by simply not thinking about it — the omission and the
// decision look identical. Writing `null` is the decision; leaving it out does not compile.
const OPTION_HINT_KEYS: Record<OptionKey, string | null> = {
  title: null,
  text: null,
  url: null,
  alt: null,
  align: null,
  fontSize: 'widgetHintFontSize',
  fit: null,
  unit: null,
  min: null,
  max: null,
  precision: 'widgetHintPrecision',
  measurement: 'widgetHintMeasurement',
  window: 'widgetHintWindow',
  flashOnChange: 'widgetFlashOnChangeHint',
  state: 'widgetStateHint',
  severity: 'widgetSeverityHint',
  acknowledged: 'widgetAcknowledgedHint',
  maxRows: null,
  selectionTarget: 'widgetTargetSlotHint',
  commandName: 'widgetCommandFieldHint',
  commandLabel: 'widgetCommandFieldHint',
  parameterSchema: 'widgetCommandFieldHint',
  tileUrl: 'widgetTileUrlHint',
  attribution: 'widgetAttributionHint',
};

// Where one option means something different on different widgets. `maxRows` is the only
// case today — it bounds an alarm page on a table and a command history on a button — and
// a single sentence covering both would say nothing an author could act on.
const OPTION_HINT_OVERRIDES: Partial<Record<WidgetType, Partial<Record<OptionKey, string>>>> = {
  'alarm-table': { maxRows: 'widgetMaxRowsAlarmHint' },
  'command-button': { maxRows: 'widgetMaxRowsCommandHint' },
};

const ENUM_VALUE_LABEL_KEYS: Record<EnumValueId, string> = {
  'state:ACTIVE': 'widgetAlarmStateActive',
  'state:CLEARED': 'widgetAlarmStateCleared',
  'severity:CRITICAL': 'widgetAlarmSeverityCritical',
  'severity:MAJOR': 'widgetAlarmSeverityMajor',
  'severity:MINOR': 'widgetAlarmSeverityMinor',
  'severity:WARNING': 'widgetAlarmSeverityWarning',
  'severity:INDETERMINATE': 'widgetAlarmSeverityIndeterminate',
  'acknowledged:true': 'widgetAlarmAckAcknowledged',
  'acknowledged:false': 'widgetAlarmAckUnacknowledged',
  'align:left': 'widgetAlignLeft',
  'align:center': 'widgetAlignCenter',
  'align:right': 'widgetAlignRight',
  'fit:contain': 'widgetFitContain',
  'fit:cover': 'widgetFitCover',
  'fit:fill': 'widgetFitFill',
};

// What an unset enum means, which is not the same sentence for every enum: the alarm
// filters unset mean "every one of them", while an unset alignment means the widget's own
// default. Offering "Any" for an alignment would describe a behaviour the renderer does
// not have.
const ENUM_PLACEHOLDER_KEYS: Record<EnumOptionKey, string> = {
  state: 'widgetPlaceholderAny',
  severity: 'widgetPlaceholderAny',
  acknowledged: 'widgetPlaceholderAny',
  align: 'widgetPlaceholderDefault',
  fit: 'widgetPlaceholderDefault',
};

// Every translation key this module can ask for, gathered so the catalogs can be held to
// them (widgetOptionFields.test.ts). The tables above make the compiler prove each option
// and each enum value HAS a key; only reading the catalogs can prove the key resolves to a
// real string in each locale, and t() cannot be asked — `fallbackLng: 'en'` answers a
// missing Spanish string with the English one.
export const OPTION_WORDING = {
  labels: OPTION_LABEL_KEYS,
  hints: OPTION_HINT_KEYS,
  hintOverrides: OPTION_HINT_OVERRIDES,
  enumValues: ENUM_VALUE_LABEL_KEYS,
  enumPlaceholders: ENUM_PLACEHOLDER_KEYS,
} as const;

// ---- Which control authors which key ----------------------------------------

// A control that authors an option from something other than the option's own type — the
// command vocabulary, the dashboard's slots. Everything else is derived from the spec kind.
type CustomControl = 'command' | 'slot';

// 🔴 Exhaustive over OptionKey, and `null` is the ordinary answer. The point is the same as
// the hint table's: a new key gets a control decision, not a default. `null` means "render
// the control the spec kind implies", which is what most keys want.
//
// The three command keys share ONE control because they are one choice: picking a command
// bakes its key, its label and its parameter descriptors together (ADR-043 decision 3), and
// a free-text box for `parameterSchema` would invite an author to hand-write JSON the
// button silently ignores when it does not parse.
const CUSTOM_CONTROL: Record<OptionKey, CustomControl | null> = {
  title: null,
  text: null,
  url: null,
  alt: null,
  align: null,
  fontSize: null,
  fit: null,
  unit: null,
  min: null,
  max: null,
  precision: null,
  measurement: null,
  window: null,
  flashOnChange: null,
  state: null,
  severity: null,
  acknowledged: null,
  maxRows: null,
  selectionTarget: 'slot',
  commandName: 'command',
  commandLabel: 'command',
  parameterSchema: 'command',
  tileUrl: null,
  attribution: null,
};

// The option keys this module names directly rather than reaching through the schema walk —
// the custom controls know which keys they author, and the map's cross-field notice reads
// two of them. Named at module scope and TYPED as OptionKey, for two reasons: a misspelling
// becomes a compile error instead of a key nothing reads, and the i18n lint walks bare
// literals inside a JSX tree (they look like user-facing text and are not).
const KEY_SELECTION_TARGET: OptionKey = 'selectionTarget';
const KEY_COMMAND_NAME: OptionKey = 'commandName';
const KEY_COMMAND_LABEL: OptionKey = 'commandLabel';
const KEY_PARAMETER_SCHEMA: OptionKey = 'parameterSchema';
const KEY_TILE_URL: OptionKey = 'tileUrl';
const KEY_ATTRIBUTION: OptionKey = 'attribution';

// The one field rendered ABOVE the data-source block. A widget's title is what an author
// reaches for first and the schema lists it first on every type; everything else reads as
// a detail of the binding and belongs after it.
const LEADING_KEYS: ReadonlySet<string> = new Set<OptionKey>(['title']);

// ---- The derived field list --------------------------------------------------

type OptionField =
  | { id: string; leading: boolean; kind: 'schema'; key: string; spec: OptionSpec }
  | { id: CustomControl; leading: false; kind: CustomControl };

// optionFieldsFor walks a widget type's declared options in schema order and returns the
// fields that author them. Keys sharing a custom control collapse into one field, emitted
// where the first of them appears.
function optionFieldsFor(type: WidgetType): OptionField[] {
  const specs: WidgetOptionSpecs = WIDGET_OPTIONS[type];
  const seen = new Set<CustomControl>();
  const fields: OptionField[] = [];
  for (const [key, spec] of Object.entries(specs)) {
    const custom = CUSTOM_CONTROL[key as OptionKey] ?? null;
    if (custom) {
      if (seen.has(custom)) continue;
      seen.add(custom);
      fields.push({ id: custom, leading: false, kind: custom });
      continue;
    }
    fields.push({ id: key, leading: LEADING_KEYS.has(key), kind: 'schema', key, spec });
  }
  return fields;
}

export interface OptionFieldContext {
  widget: WidgetInstance;
  // Write (or, with undefined/''/false, drop) a single option key.
  setOption: (key: string, value: string | number | boolean | undefined) => void;
  // Write several keys at once, for a control that authors more than one.
  setOptions: (patch: Record<string, string | number | undefined>) => void;
  // The dashboard's slots — the choices a selection-target picker offers.
  slots: Record<string, SlotDefinition> | undefined;
  // The widget's target device, where it has one: the command vocabulary comes from it.
  deviceToken: string | undefined;
}

// WidgetOptionFields renders one half of the derived field list — the leading fields above
// the data-source block, the rest below it. Splitting on the field's own `leading` flag
// rather than on two lists means the two halves are exactly the whole list by construction.
export function WidgetOptionFields({
  ctx,
  leading,
}: {
  ctx: OptionFieldContext;
  leading: boolean;
}) {
  const fields = optionFieldsFor(ctx.widget.type).filter((f) => f.leading === leading);
  return (
    <>
      {fields.map((field) => (
        // The testid is how the exhaustiveness gate finds a field to drive; the gate then
        // proves the field writes the keys it is here to write.
        <div key={field.id} data-testid={`widget-option-field-${field.id}`}>
          <OptionFieldControl field={field} ctx={ctx} />
        </div>
      ))}
    </>
  );
}

function OptionFieldControl({ field, ctx }: { field: OptionField; ctx: OptionFieldContext }) {
  switch (field.kind) {
    case 'schema':
      return <SchemaField widgetType={ctx.widget.type} option={field.key} spec={field.spec} ctx={ctx} />;
    case 'command':
      return <CommandField ctx={ctx} />;
    case 'slot':
      return <SlotTargetField ctx={ctx} />;
  }
}

// ---- Controls derived from the spec kind -------------------------------------

function SchemaField({
  widgetType,
  option,
  spec,
  ctx,
}: {
  widgetType: WidgetType;
  option: string;
  spec: OptionSpec;
  ctx: OptionFieldContext;
}) {
  const { t } = useTranslation('dashboards');
  const label = t(OPTION_LABEL_KEYS[option as OptionKey]);
  const hintKey =
    OPTION_HINT_OVERRIDES[widgetType]?.[option as OptionKey] ?? OPTION_HINT_KEYS[option as OptionKey];
  const hint = hintKey ? t(hintKey) : undefined;

  switch (spec.kind) {
    case 'boolean':
      // A checkbox carries its own label, so it is not wrapped in a FormField — the label
      // would then be announced twice and shown above a control that already says it.
      return <BooleanField option={option} label={label} hint={hint} ctx={ctx} />;
    case 'string':
      return (
        <FormField label={label} description={hint}>
          <Input
            value={optString(ctx.widget, option)}
            onChange={(e) => ctx.setOption(option, e.target.value)}
          />
        </FormField>
      );
    case 'number':
      return (
        <FormField label={label} description={hint}>
          <NumberInput
            widgetType={widgetType}
            option={option}
            value={optNumber(ctx.widget, option)}
            onChange={(v) => ctx.setOption(option, v)}
          />
        </FormField>
      );
    case 'enum':
      return (
        <FormField label={label} description={hint}>
          <Combobox
            options={enumChoices(t, option, spec.values)}
            value={optString(ctx.widget, option)}
            onChange={(v) => ctx.setOption(option, v || undefined)}
            placeholder={t(ENUM_PLACEHOLDER_KEYS[option as EnumOptionKey])}
          />
        </FormField>
      );
  }
}

// The VALUES come from the spec in hand, so a dropdown cannot offer one the validator
// rejects or omit one an author could previously express. Only the labels are looked up.
function enumChoices(t: TFunction, option: string, values: readonly string[]): ComboboxOption[] {
  return values.map((value) => ({
    value,
    label: t(ENUM_VALUE_LABEL_KEYS[`${option}:${value}` as EnumValueId]),
  }));
}

function BooleanField({
  option,
  label,
  hint,
  ctx,
}: {
  option: string;
  label: string;
  hint: string | undefined;
  ctx: OptionFieldContext;
}) {
  return (
    <label className="flex cursor-pointer items-start gap-2 text-sm text-foreground">
      <Checkbox
        className="mt-0.5"
        checked={ctx.widget.options?.[option] === true}
        onCheckedChange={(c) => ctx.setOption(option, c === true)}
      />
      <span>
        <span className="font-medium">{label}</span>
        {hint && <span className="mt-0.5 block text-xs text-muted-foreground">{hint}</span>}
      </span>
    </label>
  );
}

// ---- Command selection (command-button) -------------------------------------

// CommandField lets the author pick which command the button issues, from the target
// device's PUBLISHED command vocabulary (ADR-043 decision 3). Picking one bakes its key,
// label, and parameter schema into the widget's options — so the widget renders its typed
// form at runtime with no device→profile resolution. Requires a target device to be
// chosen first (that's where the command list comes from).
//
// When the device's profile CONSTRAINS its vocabulary, the picker offers published
// commands only, because published is what the enqueue gate accepts. Baking a draft there
// would produce a button that looks correct in the editor, renders correctly on the
// dashboard, and fails only when an operator presses it. Draft-only commands are still
// NAMED below the picker rather than omitted: an author who just wrote a command
// definition and can't find it needs to be told it is unpublished, not left to conclude
// the editor is broken.
//
// When the profile does NOT constrain (no profile, never published, or no definitions —
// ADR-043 decision 4), the gate accepts any key, so the drafts ARE offerable and are
// offered. Restricting to published there would leave the whole unconstrained device
// class — most devices, pre-GA — with an empty picker and no way to author a command
// button at all, for a button that would have worked.
function CommandField({ ctx }: { ctx: OptionFieldContext }) {
  const { t } = useTranslation('dashboards');
  const deviceToken = ctx.deviceToken;
  const commandName = optString(ctx.widget, KEY_COMMAND_NAME);
  const { data, loading, error } = useQuery(
    async () => {
      if (!deviceToken) return commandChoices(null, []);
      const [vocabulary, drafts] = await Promise.all([
        getDeviceCommandVocabulary(deviceToken),
        listCommandDefinitionsForDevice(deviceToken),
      ]);
      return commandChoices(vocabulary, drafts);
    },
    [deviceToken],
  );
  const selectable = data?.selectable ?? [];
  const draftOnly = data?.draftOnly ?? [];
  const constrained = data?.constrained ?? false;

  const options: ComboboxOption[] = selectable.map((def) => ({
    value: def.commandKey,
    label: def.name ? `${def.name} (${def.commandKey})` : def.commandKey,
  }));

  const select = (def: PickableCommand | undefined) =>
    ctx.setOptions({
      [KEY_COMMAND_NAME]: def?.commandKey,
      [KEY_COMMAND_LABEL]: def?.name ?? def?.commandKey,
      [KEY_PARAMETER_SCHEMA]: def?.parameterSchema ?? undefined,
    });

  // A command baked from a previous target device may not exist on the current one (the
  // author repointed the device without re-picking), or may have been unpublished since.
  // Flag it — non-destructively, since the datasource and options update through separate
  // handlers — so the author re-picks rather than silently shipping a button that fails.
  const staleSelection =
    commandName !== '' && !loading && !error && !selectable.some((d) => d.commandKey === commandName);

  return (
    <div className="space-y-3 rounded-md border border-border p-3">
      <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {t('widgetCommandHeading')}
      </div>
      {!deviceToken ? (
        <p className="text-xs text-muted-foreground">{t('widgetCommandSelectDevice')}</p>
      ) : loading ? (
        <p className="text-xs text-muted-foreground">{t('widgetCommandLoading')}</p>
      ) : error ? (
        <p className="text-xs text-muted-foreground">{t('widgetCommandLoadError', { error })}</p>
      ) : (
        <>
          {staleSelection && (
            <p className="text-xs text-destructive">{t('widgetCommandStale', { commandName })}</p>
          )}
          {selectable.length === 0 ? (
            <p className="text-xs text-muted-foreground">{t('widgetCommandNoneDefined')}</p>
          ) : (
            <FormField label={t('widgetCommandHeading')} description={t('widgetCommandFieldHint')}>
              <Combobox
                options={options}
                value={commandName}
                onChange={(key) => select(selectable.find((d) => d.commandKey === key))}
                placeholder={t('widgetCommandSelectPlaceholder')}
              />
            </FormField>
          )}
          {!constrained && selectable.length > 0 && (
            <p className="text-xs text-muted-foreground">{t('widgetCommandUnpublishedHint')}</p>
          )}
          {draftOnly.length > 0 && (
            <div className="space-y-1 rounded-md bg-muted/50 p-2">
              <p className="text-xs text-muted-foreground">{t('widgetCommandDraftOnlyLabel')}</p>
              {/* Actual (untranslated) command names — data, not prose. */}
              <p className="text-xs font-medium text-muted-foreground">{draftOnly.join(', ')}</p>
              <p className="text-xs text-muted-foreground">
                {t('widgetCommandDraftOnlyPublishHint', { count: draftOnly.length })}
              </p>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// ---- Selection target (entity-selector, alarm-table) -------------------------

// SlotTargetField picks which slot a widget re-points when a viewer selects an entity in
// it. A scoped slot becomes a member picker (which device within the parent context); a
// root slot a context picker (which building/customer the dashboard shows). The target is
// stored in the widget's options; the runtime resolves the candidate set from the target
// slot's scope.
//
// Both widgets that declare `selectionTarget` get it, which is a change: the alarm table's
// originator drill-down reads the same key and had no way to author it, so an alarm table
// could never re-point anything.
function SlotTargetField({ ctx }: { ctx: OptionFieldContext }) {
  const { t } = useTranslation('dashboards');
  const slots = ctx.slots;
  const names = Object.keys(slots ?? {});
  const options: ComboboxOption[] = names.map((name) => {
    const slot = slots![name];
    const role = slot.scope
      ? t('widgetRoleMemberOf', { parent: slot.scope.parent })
      : slot.type === 'anchor'
        ? t('widgetRoleContext')
        : t('widgetRoleDevice');
    return { value: name, label: name, description: role };
  });

  return (
    <div className="space-y-3 rounded-md border border-border p-3">
      <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {t('widgetSelectionTargetHeading')}
      </div>
      {options.length === 0 ? (
        <p className="text-xs text-muted-foreground">{t('widgetSelectionTargetEmptyHint')}</p>
      ) : (
        <FormField label={t('widgetLabelTargetSlot')} description={t('widgetTargetSlotHint')}>
          <Combobox
            options={options}
            value={optString(ctx.widget, KEY_SELECTION_TARGET)}
            onChange={(v) => ctx.setOption(KEY_SELECTION_TARGET, v || undefined)}
            placeholder={t('widgetPlaceholderSelectSlot')}
          />
        </FormField>
      )}
    </div>
  );
}

// ---- Cross-field notices ------------------------------------------------------

// 🔴 A widget tile source with no credit line is IGNORED at render — the two halves are
// one value, and this tier never reaches the server's validation, so the renderer discards
// the pair rather than drawing a provider's tiles uncredited. Saying so is the whole point:
// without it the option silently does nothing and reads as broken, which is the complaint
// the basemap work started from. The geofence editor carries the same message.
//
// It lives outside the derived field list because it is not a field: it reads two options
// and writes neither, which is exactly what the exhaustiveness gate would (correctly) call
// a decorative container if it were emitted as one.
//
// Which widget it applies to is asked of the SCHEMA — "does this type declare both halves
// of the tile pair?" — rather than written as `type === 'map'`. The catalog is planned to
// carry a second map (a raster one and a vector one, two entries with one core), and a
// literal type test is precisely the thing that would let the second one ship with the
// warning silently missing. Stated limit: declaring the pair is a PROXY for rendering
// tiles, not proof of it; a type that declared both and did not discard the unattributed
// pair at render would be told something untrue here. Nothing declares them today but the
// map, and the discard rule lives with the widget that does.
export function WidgetOptionNotices({ widget }: { widget: WidgetInstance }) {
  const { t } = useTranslation('dashboards');
  if (!declaresTilePair(widget.type)) return null;
  if (optString(widget, KEY_TILE_URL).trim() === '') return null;
  if (optString(widget, KEY_ATTRIBUTION).trim() !== '') return null;
  return (
    <p className="text-destructive text-xs" data-testid="widget-tile-uncredited">
      {t('widgetTileUncredited')}
    </p>
  );
}

// declaresTilePair answers whether a widget type reads BOTH halves of the basemap override.
//
// An own-property test rather than `in`, which for these two literal keys is a convention
// and not a live hazard — neither 'tileUrl' nor 'attribution' is inherited from
// Object.prototype, so `in` would answer identically today. It is written the way the
// schema's own readers are written so that a later caller passing a key from data (where
// 'constructor' and friends ARE reachable) does not have to notice the difference.
function declaresTilePair(type: WidgetType): boolean {
  const specs: WidgetOptionSpecs = WIDGET_OPTIONS[type];
  const has = (key: string) => Object.prototype.hasOwnProperty.call(specs, key);
  return has(KEY_TILE_URL) && has(KEY_ATTRIBUTION);
}

// ---- options readers (panel-local; strict-typed inputs) ---------------------

function optString(widget: WidgetInstance, key: string): string {
  const value = widget.options?.[key];
  return typeof value === 'string' ? value : '';
}

function optNumber(widget: WidgetInstance, key: string): number | undefined {
  const value = widget.options?.[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined;
}

// A numeric option input, bounded by its own schema.
//
// 🔴 IT CLAMPS ON BLUR, NOT PER KEYSTROKE, and the difference is the whole design. An
// onChange clamp fights the author mid-word: with a minimum of 1, typing "05" to mean
// 5 rewrites the 0 to a 1 under the cursor and stores 15; on an integer option,
// typing "1.5" deletes the fraction the instant the 5 lands. A controlled input
// writes the corrected value straight back into the field, so each of those is
// visible and unrecoverable without retyping.
//
// So the draft is held locally while the field has focus and reconciled when it is
// not. min/max/step still drive the spinner and the browser's own validity state
// during typing; the clamp is what stops an out-of-range value being STORED, since
// typing bypasses the spinner entirely.
//
// The bounds come from WIDGET_OPTIONS rather than a second copy of the same numbers
// here, so the panel and the validator cannot disagree about one option. They can
// still disagree about a RULE spanning two — a gauge min above its max is authorable
// here and rejected there — because that is not something a single input can see.
function NumberInput({
  widgetType,
  option,
  value,
  onChange,
}: {
  widgetType: WidgetType;
  option: string;
  value: number | undefined;
  onChange: (value: number | undefined) => void;
}) {
  const spec: NumberOptionSpec | undefined = numberOptionSpec(widgetType, option);
  const [draft, setDraft] = useState<string | null>(null);

  const commit = (raw: string) => {
    setDraft(null);
    onChange(raw === '' ? undefined : clampNumberOption(spec, Number(raw)));
  };

  return (
    <Input
      type="number"
      value={draft ?? value ?? ''}
      min={spec?.min}
      max={spec?.max}
      step={spec?.integer ? 1 : undefined}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={(e) => commit(e.target.value)}
      onKeyDown={(e) => {
        // Enter commits too: an author who types a value and presses Enter expects it
        // applied, not held as a draft until they click somewhere else.
        if (e.key === 'Enter') commit((e.target as HTMLInputElement).value);
      }}
    />
  );
}
