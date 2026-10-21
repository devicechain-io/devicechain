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
  type ListItemFieldSpec,
  type ListOptionSpec,
  type NumberOptionSpec,
  type OptionSpec,
  type ResolvedWidgetInstance,
  type WidgetDescriptor,
} from '@devicechain/dashboards';
import type { WidgetEditorServices } from '@devicechain/widget-sdk';
import { ArrowDown, ArrowUp, Plus, X } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { IconButton } from '@/components/ui/icon-button';

import { Checkbox } from '@/components/ui/checkbox';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';

import { EntityPicker, type EntityKind } from './EntityPicker';
import { editorControlOf, hostControl, type ControlEditorProps } from './controlEditors';
import { enumChoiceLabel, enumPlaceholder, fieldHelp, fieldLabel, useMessageLookup } from './optionWording';

// ---- Which control authors which option -------------------------------------

// Options that declare a control this host has an EDITOR for collapse into ONE field, because
// they are one choice: picking a command bakes its key, its label and its parameter
// descriptors together (ADR-043 decision 3), and a free-text box for the parameter schema
// would invite an author to hand-write JSON the button silently ignores when it does not
// parse. The editors themselves are registered in ./controlEditors (the same interface a
// package's own editor is written against); this file only decides WHERE each one goes.

// The one field rendered ABOVE the data-source block. A widget's title is what an author
// reaches for first; everything else reads as a detail of the binding and belongs after it.
// This is layout, not behaviour: an option called `title` on an extension leads too.
const LEADING_KEYS: ReadonlySet<string> = new Set(['title']);

// ---- The derived field list --------------------------------------------------

type OptionField =
  | { id: string; leading: boolean; kind: 'schema'; key: string; spec: OptionSpec }
  | { id: string; leading: false; kind: 'editor'; control: string; keys: string[] };

// optionFieldsFor walks a descriptor's declared options in schema order and returns the
// fields that author them. Options sharing a registered control collapse into one field,
// emitted where the first of them appears.
function optionFieldsFor(descriptor: WidgetDescriptor): OptionField[] {
  const fields: OptionField[] = [];
  const editors = new Map<string, Extract<OptionField, { kind: 'editor' }>>();
  for (const [key, spec] of Object.entries(descriptor.options)) {
    const control = editorControlOf(descriptor, spec);
    if (control !== undefined) {
      const existing = editors.get(control);
      if (existing) {
        existing.keys.push(key);
      } else {
        const field = {
          id: hostControl(control)!.fieldId,
          leading: false as const,
          kind: 'editor' as const,
          control,
          keys: [key],
        };
        editors.set(control, field);
        fields.push(field);
      }
      continue;
    }
    fields.push({ id: key, leading: LEADING_KEYS.has(key), kind: 'schema', key, spec });
  }
  return fields;
}

export interface OptionFieldContext {
  widget: ResolvedWidgetInstance;
  // What this widget declares: the schema the fields are derived from.
  descriptor: WidgetDescriptor;
  // Write (or, with undefined/''/false, drop) a single option key.
  setOption: (key: string, value: string | number | boolean | undefined) => void;
  // Write several keys at once, for a control that authors more than one. Validated against
  // the descriptor's schema (an editor writing an undeclared key or a wrong-kind value throws).
  setOptions: (patch: Record<string, unknown>) => void;
  // The host services a registered editor is handed, passed explicitly.
  services: WidgetEditorServices;
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
  if (field.kind === 'schema') return <SchemaField option={field.key} spec={field.spec} ctx={ctx} />;
  const Editor = hostControl(field.control)!.Editor!;
  const props: ControlEditorProps = {
    options: ctx.widget.options ?? {},
    setOptions: ctx.setOptions,
    services: ctx.services,
    descriptor: ctx.descriptor,
    optionKeys: field.keys,
  };
  return <Editor {...props} />;
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
      if (spec.control === DEVICE_TYPE_CONTROL) {
        return (
          <FormField label={label} description={hint}>
            <EntityPicker
              kind={DEVICE_TYPE_KIND}
              ariaLabel={label}
              value={optString(ctx.widget, option)}
              // The picker's clear button hands back '' and setOption drops the key.
              onChange={(token) => ctx.setOption(option, token)}
            />
          </FormField>
        );
      }
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
    case 'list':
      return <ListField option={option} spec={spec} label={label} hint={hint} ctx={ctx} />;
  }
}

// ---- A list option: one row per entry, one control per declared item field -------

type Row = Record<string, unknown>;

// A row's controls are h-10 and its buttons size-6, and the row aligns to the top (a cell may grow
// below its control), so the buttons drop by half the difference to sit level with the controls.
const ROW_BUTTON = 'mt-2';

// The `control` that picks a device type from the catalogue instead of asking for its token to be
// typed. Open vocabulary: a descriptor that names it on a string option or a string list field
// gets the picker, and the stored value is still the bare token.
const DEVICE_TYPE_CONTROL = 'device-type';
const DEVICE_TYPE_KIND: EntityKind = 'deviceType';

// The stored rows, read defensively: a hand-edited bag may hold anything, and a row that is not
// an object is shown as an empty row rather than crashing the panel.
function readRows(value: unknown): Row[] {
  if (!Array.isArray(value)) return [];
  return value.map((r) => (typeof r === 'object' && r !== null && !Array.isArray(r) ? (r as Row) : {}));
}

// A row an author gets from "Add": every REQUIRED field at the first thing its spec offers, so
// the new row is already valid (the validated write refuses one with a required field absent).
function newRow(item: ListOptionSpec['item']): Row {
  const row: Row = {};
  for (const [field, spec] of Object.entries(item)) {
    if (!spec.required) continue;
    if (spec.kind === 'number') row[field] = clampNumberOption(spec, 0) ?? 0;
    else if (spec.kind === 'enum') row[field] = spec.values[0];
    else if (spec.kind === 'boolean') row[field] = false;
  }
  return row;
}

function ListField({
  option,
  spec,
  label,
  hint,
  ctx,
}: {
  option: string;
  spec: ListOptionSpec;
  label: string;
  hint: string | undefined;
  ctx: OptionFieldContext;
}) {
  const { t } = useTranslation('dashboards');
  const lookup = useMessageLookup();
  const rows = readRows(ctx.widget.options?.[option]);
  const full = spec.maxItems !== undefined && rows.length >= spec.maxItems;

  // An empty list drops the key (applyOptionPatch), so removing the last row unsets the option.
  const write = (next: Row[]) => ctx.setOptions({ [option]: next });
  // Rows are keyed by position, so after a move the focused button would stay where it was and act
  // on the row that slid INTO that place. Focus follows the moved row instead: the same arrow on
  // its new position, or the other one when it has reached an end and that arrow is disabled.
  const listRef = useRef<HTMLDivElement>(null);
  const [moved, setMoved] = useState<{ to: number; dir: 'up' | 'down' } | null>(null);
  useEffect(() => {
    if (!moved) return;
    const at = (d: 'up' | 'down') =>
      listRef.current?.querySelector<HTMLButtonElement>(`[data-testid="list-${d}-${option}-${moved.to}"]`);
    const same = at(moved.dir);
    (same && !same.disabled ? same : at(moved.dir === 'up' ? 'down' : 'up'))?.focus();
    setMoved(null);
  }, [moved, option]);
  const move = (from: number, to: number) => {
    const next = [...rows];
    [next[from], next[to]] = [next[to], next[from]];
    write(next);
    setMoved({ to, dir: to < from ? 'up' : 'down' });
  };
  const setCell = (index: number, field: string, value: unknown) =>
    write(
      rows.map((row, i) => {
        if (i !== index) return row;
        const next = { ...row };
        if (value === undefined || value === '') delete next[field];
        else next[field] = value;
        return next;
      }),
    );

  return (
    <FormField label={label} description={hint}>
      <div ref={listRef} className="flex flex-col gap-2">
        {rows.length > 0 && (
          // Visible column headings: a row of bare controls (a number, a select, a text box)
          // says nothing about which is which to a sighted author. Each cell also carries its
          // own accessible name, so this row is hidden from a screen reader.
          <div
            aria-hidden
            data-testid={`list-head-${option}`}
            className="flex items-end gap-2 text-xs font-medium text-muted-foreground"
          >
            {Object.entries(spec.item).map(([field, fieldSpec]) => (
              <div key={field} className="min-w-0 flex-1 truncate">
                {fieldLabel(field, fieldSpec, lookup)}
              </div>
            ))}
            {/* the move/remove buttons' columns (IconButton size sm) */}
            {spec.ordered && <div className="size-6 shrink-0" />}
            {spec.ordered && <div className="size-6 shrink-0" />}
            <div className="size-6 shrink-0" />
          </div>
        )}
        {rows.map((row, index) => (
          <div
            // Rows are positional (no identity of their own), so the index is the key.
            key={index}
            data-testid={`list-row-${option}-${index}`}
            className="flex items-start gap-2"
          >
            {Object.entries(spec.item).map(([field, fieldSpec]) => (
              <div key={field} className="min-w-0 flex-1" data-testid={`list-cell-${option}-${index}-${field}`}>
                <ListCell
                  fieldSpec={fieldSpec}
                  value={row[field]}
                  onChange={(v) => {
                    // A required field cannot be cleared: the row would be invalid and the
                    // validated write would refuse it, so a blank keeps the last good value.
                    if (v === undefined && fieldSpec.required) return;
                    setCell(index, field, v);
                  }}
                  lookup={lookup}
                  ariaLabel={`${fieldLabel(field, fieldSpec, lookup)} ${index + 1}`}
                />
              </div>
            ))}
            {spec.ordered && (
              <>
                <IconButton
                  data-testid={`list-up-${option}-${index}`}
                  className={ROW_BUTTON}
                  label={t('widgetListMoveUp', { n: index + 1 })}
                  disabled={index === 0}
                  onClick={() => move(index, index - 1)}
                >
                  <ArrowUp className="size-4" />
                </IconButton>
                <IconButton
                  data-testid={`list-down-${option}-${index}`}
                  className={ROW_BUTTON}
                  label={t('widgetListMoveDown', { n: index + 1 })}
                  disabled={index === rows.length - 1}
                  onClick={() => move(index, index + 1)}
                >
                  <ArrowDown className="size-4" />
                </IconButton>
              </>
            )}
            <IconButton
              className={ROW_BUTTON}
              label={t('widgetListRemove', { n: index + 1 })}
              onClick={() => write(rows.filter((_, i) => i !== index))}
            >
              <X className="size-4" />
            </IconButton>
          </div>
        ))}
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={full}
            data-testid={`list-add-${option}`}
            onClick={() => write([...rows, newRow(spec.item)])}
          >
            <Plus className="mr-1 size-4" />
            {t('widgetListAdd')}
          </Button>
        </div>
      </div>
    </FormField>
  );
}

// One cell of a list row, chosen by the item field's spec kind exactly as a top-level option's
// control is chosen by its own.
function ListCell({
  fieldSpec,
  value,
  onChange,
  lookup,
  ariaLabel,
}: {
  fieldSpec: ListItemFieldSpec;
  value: unknown;
  onChange: (value: string | number | boolean | undefined) => void;
  lookup: ReturnType<typeof useMessageLookup>;
  ariaLabel: string;
}) {
  const { t } = useTranslation('dashboards');
  switch (fieldSpec.kind) {
    case 'number':
      return (
        <NumberInput
          spec={fieldSpec}
          value={typeof value === 'number' && Number.isFinite(value) ? value : undefined}
          onChange={onChange}
          ariaLabel={ariaLabel}
        />
      );
    case 'enum':
      return (
        <Combobox
          ariaLabel={ariaLabel}
          options={fieldSpec.values.map(
            (v): ComboboxOption => ({ value: v, label: enumChoiceLabel(fieldSpec, v, lookup) }),
          )}
          value={typeof value === 'string' ? value : ''}
          onChange={(v) => onChange(v || undefined)}
          placeholder={enumPlaceholder(fieldSpec, lookup, t)}
        />
      );
    case 'boolean':
      return (
        <Checkbox checked={value === true} aria-label={ariaLabel} onCheckedChange={(c) => onChange(c === true)} />
      );
    case 'string':
      if (fieldSpec.control === DEVICE_TYPE_CONTROL) {
        return <EntityPicker kind={DEVICE_TYPE_KIND} ariaLabel={ariaLabel} value={typeof value === 'string' ? value : ''} onChange={(v) => onChange(v || undefined)} />;
      }
      return (
        <Input
          aria-label={ariaLabel}
          value={typeof value === 'string' ? value : ''}
          onChange={(e) => onChange(e.target.value)}
        />
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

// ---- Cross-field notices ------------------------------------------------------

// Notices live outside the derived field list because they are not fields: they read the
// options and write nothing, which is exactly what the exhaustiveness gate would (correctly)
// call a decorative container if one were emitted as a field. Which notice applies is asked
// of the DESCRIPTOR through the controls it declares -- never of the widget's id.
export function WidgetOptionNotices({
  widget,
  descriptor,
}: {
  widget: ResolvedWidgetInstance;
  descriptor: WidgetDescriptor;
}) {
  const controls = new Set<string>();
  for (const spec of Object.values(descriptor.options)) if (spec.control !== undefined) controls.add(spec.control);
  return (
    <>
      {[...controls].map((name) => {
        const Notice = hostControl(name)?.Notice;
        return Notice ? <Notice key={name} options={widget.options ?? {}} descriptor={descriptor} /> : null;
      })}
    </>
  );
}

// ---- options readers (panel-local; strict-typed inputs) ---------------------

function optString(widget: ResolvedWidgetInstance, key: string): string {
  const value = widget.options?.[key];
  return typeof value === 'string' ? value : '';
}

function optNumber(widget: ResolvedWidgetInstance, key: string): number | undefined {
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
  ariaLabel,
}: {
  spec: NumberOptionSpec;
  value: number | undefined;
  onChange: (value: number | undefined) => void;
  ariaLabel?: string;
}) {
  const [draft, setDraft] = useState<string | null>(null);

  const commit = (raw: string) => {
    setDraft(null);
    onChange(raw === '' ? undefined : clampNumberOption(spec, Number(raw)));
  };

  return (
    <Input
      type="number"
      aria-label={ariaLabel}
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
