// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The config panel's widget-specific fields, DERIVED from the option schema the widget's
// DESCRIPTOR declares (the registry owns it) rather than written out per widget type.
//
// 🔴 WHAT THE `if (widget.type === …)` LADDER THIS REPLACED ACTUALLY COST. The ladder was
// exhaustive over nothing: adding a widget type compiled and produced a widget with no
// configuration UI, and — the quieter half — adding an OPTION to a type the ladder already
// handled compiled too, and produced a key the renderer reads and no author can set. Both
// had happened: `window`, `measurement`, `align`, `fontSize` and `fit` were honored at
// render and unauthorable here, and nothing failed — the option simply did nothing, which
// reads to an author as a broken widget rather than a missing field.
//
// Deriving the field list closes both: this module walks the descriptor's options and emits
// a control per declared key, so the fields ARE the schema. What it cannot do by itself is
// prove the emitted controls actually write those keys — a control that renders nothing, or
// writes a misspelled one, still walks the same list. That is what
// WidgetConfigPanel.exhaustiveness.test.tsx drives and asserts. The chain that makes an
// authored key a key the RENDERER honors has one more link, asserted elsewhere:
// options.test.ts in @devicechain/widgets scans the renderer's own source and requires the
// built-in schemas to declare exactly the keys the widgets read.
//
// 🔴 NOTHING HERE KNOWS A WIDGET BY ID OR AN OPTION BY NAME. The control for an option is
// chosen by the spec: its kind (string / number / boolean / enum) gives the generic field,
// and a specialized workflow is chosen ONLY by an explicit `control` the descriptor
// declares. An extension that happens to name an option `commandName` gets a text box
// unless it declares the command control. Wording comes from the spec too: a host message
// id the console can translate, else the portable text the package shipped, so a
// third-party option is understandable with no console translation key at all.

import {
  clampNumberOption,
  type KnownWidgetInstance,
  type NumberOptionSpec,
  type OptionControl,
  type OptionSpec,
  type SlotDefinition,
  type WidgetDescriptor,
} from '@devicechain/dashboards';
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

import { enumChoiceLabel, enumPlaceholder, fieldHelp, fieldLabel, useMessageLookup } from './optionWording';

// ---- Which control authors which option -------------------------------------

// The specialized workflows this host implements for the controls a descriptor may name. The
// command and slot controls collapse every option that names them into ONE field, because
// they are one choice: picking a command bakes its key, its label and its parameter
// descriptors together (ADR-043 decision 3), and a free-text box for the parameter schema
// would invite an author to hand-write JSON the button silently ignores when it does not
// parse. `basemap` has no field of its own — its options render as ordinary strings — and
// only drives the notice below.
type CustomControl = Extract<OptionControl, 'command-vocabulary' | 'slot-target'>;

const FIELD_ID: Record<CustomControl, string> = {
  'command-vocabulary': 'command',
  'slot-target': 'slot',
};

function customControlOf(spec: OptionSpec): CustomControl | null {
  return spec.control === 'command-vocabulary' || spec.control === 'slot-target' ? spec.control : null;
}

// The command workflow bakes these three keys together; the basemap notice reads these two.
// Registering those workflows through the editor interface (a later slice) removes the names
// from this host. They are consulted only AFTER the descriptor has declared the control.
const KEY_COMMAND_NAME = 'commandName';
const KEY_COMMAND_LABEL = 'commandLabel';
const KEY_PARAMETER_SCHEMA = 'parameterSchema';
const KEY_TILE_URL = 'tileUrl';
const KEY_ATTRIBUTION = 'attribution';

// The one field rendered ABOVE the data-source block. A widget's title is what an author
// reaches for first; everything else reads as a detail of the binding and belongs after it.
// This is layout, not behaviour: an option called `title` on an extension leads too.
const LEADING_KEYS: ReadonlySet<string> = new Set(['title']);

// ---- The derived field list --------------------------------------------------

type OptionField =
  | { id: string; leading: boolean; kind: 'schema'; key: string; spec: OptionSpec }
  | { id: string; leading: false; kind: CustomControl; key: string };

// optionFieldsFor walks a descriptor's declared options in schema order and returns the
// fields that author them. Options sharing a custom control collapse into one field,
// emitted where the first of them appears.
function optionFieldsFor(descriptor: WidgetDescriptor): OptionField[] {
  const seen = new Set<CustomControl>();
  const fields: OptionField[] = [];
  for (const [key, spec] of Object.entries(descriptor.options)) {
    const custom = customControlOf(spec);
    if (custom) {
      if (seen.has(custom)) continue;
      seen.add(custom);
      fields.push({ id: FIELD_ID[custom], leading: false, kind: custom, key });
      continue;
    }
    fields.push({ id: key, leading: LEADING_KEYS.has(key), kind: 'schema', key, spec });
  }
  return fields;
}

export interface OptionFieldContext {
  widget: KnownWidgetInstance;
  // What this widget declares: the schema the fields are derived from.
  descriptor: WidgetDescriptor;
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
  const fields = optionFieldsFor(ctx.descriptor).filter((f) => f.leading === leading);
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
      return <SchemaField option={field.key} spec={field.spec} ctx={ctx} />;
    case 'command-vocabulary':
      return <CommandField ctx={ctx} />;
    case 'slot-target':
      return <SlotTargetField ctx={ctx} optionKey={field.key} />;
  }
}

// ---- Controls derived from the spec kind -------------------------------------

function SchemaField({
  option,
  spec,
  ctx,
}: {
  option: string;
  spec: OptionSpec;
  ctx: OptionFieldContext;
}) {
  const { t } = useTranslation('dashboards');
  const lookup = useMessageLookup();
  const label = fieldLabel(option, spec, lookup);
  const hint = fieldHelp(spec, lookup);

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
            spec={spec}
            value={optNumber(ctx.widget, option)}
            onChange={(v) => ctx.setOption(option, v)}
          />
        </FormField>
      );
    case 'enum':
      return (
        <FormField label={label} description={hint}>
          <Combobox
            options={spec.values.map(
              (value): ComboboxOption => ({ value, label: enumChoiceLabel(spec, value, lookup) }),
            )}
            value={optString(ctx.widget, option)}
            onChange={(v) => ctx.setOption(option, v || undefined)}
            placeholder={enumPlaceholder(spec, lookup, t)}
          />
        </FormField>
      );
  }
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
// Every widget whose descriptor declares the slot-target control gets it, under whatever
// option key it chose to store the target in.
function SlotTargetField({ ctx, optionKey }: { ctx: OptionFieldContext; optionKey: string }) {
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
            value={optString(ctx.widget, optionKey)}
            onChange={(v) => ctx.setOption(optionKey, v || undefined)}
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
// Which widget it applies to is asked of the DESCRIPTOR — "does this widget declare both
// halves of the tile pair, under the basemap control?" — rather than written as
// `type === 'map'`. Stated limit: declaring the pair is a PROXY for rendering tiles, not
// proof of it; a widget that declared both and did not discard the unattributed pair at
// render would be told something untrue here. Nothing declares them today but the map, and
// the discard rule lives with the widget that does.
export function WidgetOptionNotices({
  widget,
  descriptor,
}: {
  widget: KnownWidgetInstance;
  descriptor: WidgetDescriptor;
}) {
  const { t } = useTranslation('dashboards');
  if (!declaresTilePair(descriptor)) return null;
  if (optString(widget, KEY_TILE_URL).trim() === '') return null;
  if (optString(widget, KEY_ATTRIBUTION).trim() !== '') return null;
  return (
    <p className="text-destructive text-xs" data-testid="widget-tile-uncredited">
      {t('widgetTileUncredited')}
    </p>
  );
}

// declaresTilePair answers whether a descriptor reads BOTH halves of the basemap override,
// each under the basemap control. An own-property test rather than `in`, so a key reaching
// here from data ('constructor' and friends) is not mistaken for a declared option.
function declaresTilePair(descriptor: WidgetDescriptor): boolean {
  const declares = (key: string) =>
    Object.prototype.hasOwnProperty.call(descriptor.options, key) && descriptor.options[key].control === 'basemap';
  return declares(KEY_TILE_URL) && declares(KEY_ATTRIBUTION);
}

// ---- options readers (panel-local; strict-typed inputs) ---------------------

function optString(widget: KnownWidgetInstance, key: string): string {
  const value = widget.options?.[key];
  return typeof value === 'string' ? value : '';
}

function optNumber(widget: KnownWidgetInstance, key: string): number | undefined {
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
// The bounds come from the descriptor's own spec rather than a second copy of the same numbers
// here, so the panel and the validator cannot disagree about one option. They can
// still disagree about a RULE spanning two — a gauge min above its max is authorable
// here and rejected there — because that is not something a single input can see.
function NumberInput({
  spec,
  value,
  onChange,
}: {
  spec: NumberOptionSpec;
  value: number | undefined;
  onChange: (value: number | undefined) => void;
}) {
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
