// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// WidgetConfigPanel — the edit-mode side panel for the selected widget (ADR-039,
// authoring in the console). It edits a widget's title/text, its datasource
// selector (device or anchor — the only Hub-supported kinds), and the
// type-specific options each widget reads. It is CONTROLLED: the workspace owns
// the working definition; this panel receives the selected widget and reports a
// replacement via onChange, which the workspace applies with the updateWidget
// transform.
//
// Unlike the standalone /dash editor's paste-a-token inputs, the datasource fields
// here use real entity pickers (EntityPicker) over the console's list queries.
//
// The type-specific fields are NOT written out here. They are derived from the widget
// option schema by widgetOptionFields.tsx (ADR-076 half 1), which is where the reasoning
// for that lives; what remains in this file is the part that is not a widget option — the
// datasource selector and the scoped-slot context, both of which are stored outside the
// options bag and reported through their own callbacks.

import type {
  AnchorSelector,
  AnchorTarget,
  ConcreteSelector,
  DatasourceSelector,
  DeviceSelector,
  LocationSelection,
  SlotDefinition,
  SlotScope,
  WidgetInstance,
  WidgetType,
} from '@devicechain/dashboards';
import {
  WIDGET_BINDS_DATASOURCE,
  WIDGET_CHANNEL,
  type WidgetChannel,
} from '@devicechain/widgets';
import { useEffect, useState } from 'react';
import { Trans, useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { X } from 'lucide-react';
import { IconButton } from '@/components/ui/icon-button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { EntityPicker, type EntityKind } from './EntityPicker';
import {
  WidgetOptionFields,
  WidgetOptionNotices,
  type OptionFieldContext,
} from './widgetOptionFields';

// Which widgets carry a datasource, and on which channel — DERIVED from the widget
// package's own classifiers rather than listed here.
//
// These were three hand-written sets, and a hand-written set of widget types is a
// list that silently omits whatever nobody remembered to add: a new alarm widget
// would have been offered no scope picker, a new data widget no datasource picker at
// all, and nothing would have failed. WIDGET_CHANNEL and WIDGET_BINDS_DATASOURCE are
// exhaustive over WidgetType by construction, so a new type does not compile until it
// answers both questions.
//
// Alarm widgets carry a datasource as SCOPE (which entity's alarms; "None" means
// tenant-wide); the command-button carries one as its single TARGET device.
const widgetTypesOn = (channel: WidgetChannel): Set<WidgetType> =>
  new Set((Object.keys(WIDGET_CHANNEL) as WidgetType[]).filter((type) => WIDGET_CHANNEL[type] === channel));

const ALARM_WIDGETS = widgetTypesOn('alarm');
const CONTROL_WIDGETS = widgetTypesOn('control');
const LOCATION_WIDGETS = widgetTypesOn('location');

// The location series a map widget reads. Stamped onto the selector the panel emits
// rather than offered as a choice: the vocabulary has exactly one member today, so a
// dropdown would be a control with nothing to decide.
//
// It must be stamped SOMEWHERE, though, and here is the only place that knows both the
// widget type and the entity the author just picked. The hub reads positions only for a
// selector that NAMES a location series — deliberately, so the field cannot be
// decorative — which means a map bound through a panel that forgot this would show its
// empty state forever with nothing failing to say why.
const LATEST_LOCATION: LocationSelection = { series: 'latest' };

// withLocationSeries stamps (or leaves off) the location series on the selector the
// datasource form produced. Applied at the panel's edge so every path through the form —
// picking a kind, picking an entity, editing an anchor — carries it, rather than each
// field component having to remember.
function withLocationSeries(
  ds: DatasourceSelector | undefined,
  isLocationWidget: boolean,
): DatasourceSelector | undefined {
  if (!ds || !isLocationWidget) return ds;
  if (ds.kind !== 'device' && ds.kind !== 'anchor') return ds;
  return { ...ds, location: LATEST_LOCATION };
}
const DATA_WIDGETS = new Set<WidgetType>(
  (Object.keys(WIDGET_BINDS_DATASOURCE) as WidgetType[]).filter((type) => WIDGET_BINDS_DATASOURCE[type]),
);

// The option arrays below build Combobox dropdown text, so each is a FUNCTION of the
// caller's `t` (module scope has no hook access) rather than a plain constant — a
// plain constant here would render correct-looking but permanently-English option
// text with no lint signal, since eslint-plugin-i18next only walks literals actually
// nested inside a JSX tree (mode: jsx-only) and this array sits above the return.

// eslint-disable-next-line i18next/no-literal-string -- 'device'/'anchor' are the
// DatasourceSelector kind discriminants, not user text; only the labels are shown.
function kindOptions(t: TFunction): ComboboxOption[] {
  return [
    { value: 'device', label: t('common:familyDevice') },
    { value: 'anchor', label: t('widgetKindAnchor') },
  ];
}

// eslint-disable-next-line i18next/no-literal-string -- target-type API values.
function targetTypeOptions(t: TFunction): ComboboxOption[] {
  return [
    { value: 'customer', label: t('common:familyCustomer') },
    { value: 'area', label: t('common:familyArea') },
    { value: 'asset', label: t('common:familyAsset') },
  ];
}

// eslint-disable-next-line i18next/no-literal-string -- 'root'/'scoped' are the
// SlotScope mode discriminants, not user text.
function contextModeOptions(t: TFunction): ComboboxOption[] {
  return [
    { value: 'root', label: t('widgetContextModeRoot') },
    { value: 'scoped', label: t('widgetContextModeScoped') },
  ];
}

// eslint-disable-next-line i18next/no-literal-string -- 'first'/'manual' are the
// SlotScope strategy discriminants, not user text.
function scopeStrategyOptions(t: TFunction): ComboboxOption[] {
  return [
    { value: 'first', label: t('widgetScopeStrategyFirst') },
    { value: 'manual', label: t('widgetScopeStrategyManual') },
  ];
}

export interface WidgetConfigPanelProps {
  widget: WidgetInstance;
  // The widget's data source resolved to a slot-free entity view (device/anchor +
  // measurements), or undefined when unbound. The panel edits THIS; the workspace
  // maps changes back to slot storage (find-or-create slot, prune).
  datasource: ConcreteSelector | undefined;
  // The slot this widget is bound through — shown as a muted hint so the author can
  // see it's slot-backed (matters for export). undefined when unbound.
  slotName?: string;
  // Whether that slot is SCOPED (context-driven). A scoped slot's entity is derived by the
  // cascade from its parent, not chosen here, so the entity is shown read-only; the scope
  // itself (parent + strategy) IS authored here via onScope.
  slotScoped?: boolean;
  // The dashboard's slots — the candidate PARENTS for scope authoring and the candidate
  // TARGETS for an entity-selector widget. Undefined on a slot-free dashboard.
  slots?: Record<string, SlotDefinition>;
  // Title/text/type-specific option edits (widget-only; datasource is separate).
  onChange: (next: WidgetInstance) => void;
  // Data-source edits: the new entity view, or undefined for "None".
  onDatasource: (next: ConcreteSelector | undefined) => void;
  // Set (or, with undefined, clear) the scope of the selected widget's slot — the scoped-slot
  // context authoring (ADR-039). Undefined when the widget has no slot to scope.
  onScope?: (scope: SlotScope | undefined) => void;
  onClose: () => void;
}

export function WidgetConfigPanel({
  widget,
  datasource,
  slotName,
  slotScoped,
  slots,
  onChange,
  onDatasource,
  onScope,
  onClose,
}: WidgetConfigPanelProps) {
  const { t } = useTranslation(['dashboards', 'common']);
  // Read/write a single options key, dropping it when cleared so the widget falls
  // back to its default rather than reading an empty string/NaN/false. Dropping a false
  // boolean keeps opt-in flags absent-when-off (matching optBoolean's absent = false).
  const setOption = (key: string, value: string | number | boolean | undefined) => {
    const options = { ...(widget.options ?? {}) };
    if (value === undefined || value === '' || value === false) delete options[key];
    else options[key] = value;
    onChange({ ...widget, options });
  };

  // Write several option keys atomically (a value of undefined/'' drops that key). Used
  // where one choice bakes multiple options at once (e.g. picking a command sets its
  // name, label, and parameter schema together).
  const setOptions = (patch: Record<string, string | number | undefined>) => {
    const options = { ...(widget.options ?? {}) };
    for (const [key, value] of Object.entries(patch)) {
      if (value === undefined || value === '') delete options[key];
      else options[key] = value;
    }
    onChange({ ...widget, options });
  };

  const fieldContext: OptionFieldContext = {
    widget,
    setOption,
    setOptions,
    slots,
    deviceToken: datasource?.kind === 'device' ? datasource.deviceToken : undefined,
  };

  return (
    <aside className="w-80 shrink-0 overflow-auto border-l bg-card p-4">
      <div className="mb-4 flex items-center justify-between">
        <div className="text-sm font-semibold">{widget.type}</div>
        <IconButton label={t('widgetClosePanel')} onClick={onClose}>
          <X size={14} />
        </IconButton>
      </div>

      <div className="space-y-4">
        <WidgetOptionFields ctx={fieldContext} leading />

        {DATA_WIDGETS.has(widget.type) && (
          <>
            {/* Scope authoring (ADR-039): a DEVICE slot can FOLLOW a parent anchor context
                (a thermostat within the selected building). Offered only for a device-typed
                slot — the cascade's strategies bind devices, so scoping an anchor slot has no
                meaning; an anchor slot stays a root context a selector can re-point. */}
            {onScope && slotName && slots && slots[slotName]?.type === 'device' && (
              <SlotScopeFields
                slotName={slotName}
                slots={slots}
                scope={slots[slotName]?.scope}
                onScope={onScope}
              />
            )}

            {slotScoped ? (
              // A scoped slot's ENTITY is derived by the cascade from its parent (plus any
              // in-context pick), not chosen here — show it read-only. The scope itself is
              // authored above.
              <div className="space-y-2 rounded-md border border-border p-3">
                <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                  {t('widgetLabelDataSource')}
                </div>
                <p className="text-xs text-muted-foreground">
                  {slotName ? (
                    <Trans
                      t={t}
                      i18nKey="widgetDerivedFromParentWithSlot"
                      values={{ slotName }}
                      components={{ mono: <span className="font-mono" /> }}
                    />
                  ) : (
                    t('widgetDerivedFromParentNoSlot')
                  )}
                </p>
              </div>
            ) : (
              <>
                {/* DatasourceFields edits a device/anchor selector, which is exactly a
                    ConcreteSelector — the workspace re-stores it as a slot. Alarm widgets
                    use it as scope and don't carry measurement names; the command-button
                    targets a single device (device-only, no measurements). */}
                <DatasourceFields
                  datasource={datasource}
                  label={
                    CONTROL_WIDGETS.has(widget.type)
                      ? t('widgetLabelTargetDevice')
                      : ALARM_WIDGETS.has(widget.type)
                        ? t('widgetLabelScope')
                        : t('widgetLabelDataSource')
                  }
                  // A map reads a LOCATION series, not named scalar series, so it hides
                  // the measurements field for the same reason the alarm widgets do —
                  // measurement names on a map selector would be read by nothing.
                  showMeasurements={
                    !ALARM_WIDGETS.has(widget.type) &&
                    !CONTROL_WIDGETS.has(widget.type) &&
                    !LOCATION_WIDGETS.has(widget.type)
                  }
                  deviceOnly={CONTROL_WIDGETS.has(widget.type)}
                  onChange={(ds) =>
                    onDatasource(
                      withLocationSeries(ds, LOCATION_WIDGETS.has(widget.type)) as ConcreteSelector | undefined,
                    )
                  }
                />
                {datasource && slotName && (
                  <p className="text-xs text-muted-foreground">
                    <Trans
                      t={t}
                      i18nKey="widgetBoundViaSlot"
                      values={{ slotName }}
                      components={{ mono: <span className="font-mono" /> }}
                    />
                  </p>
                )}
                {ALARM_WIDGETS.has(widget.type) && !datasource && (
                  <p className="text-xs text-muted-foreground">{t('widgetNoScopeSelected')}</p>
                )}
              </>
            )}
          </>
        )}

        <WidgetOptionFields ctx={fieldContext} leading={false} />
        <WidgetOptionNotices widget={widget} />
      </div>
    </aside>
  );
}

// ---- Datasource -------------------------------------------------------------

const EMPTY_DEVICE: DeviceSelector = { kind: 'device', deviceToken: '', measurements: [] };
const EMPTY_ANCHOR: AnchorSelector = {
  kind: 'anchor',
  anchor: { relationship: '', targetType: 'customer', targetToken: '' },
  measurements: [],
};

function DatasourceFields({
  datasource,
  label,
  showMeasurements = true,
  deviceOnly = false,
  onChange,
}: {
  datasource: DatasourceSelector | undefined;
  label?: string;
  // Alarm widgets scope by entity but carry no measurement names, so they hide the
  // measurements field.
  showMeasurements?: boolean;
  // The command-button targets a single device, so it offers only the device kind.
  deviceOnly?: boolean;
  onChange: (next: DatasourceSelector | undefined) => void;
}) {
  const { t } = useTranslation(['dashboards', 'common']);
  // Every current caller passes an explicit label; this default only guards a future
  // caller that omits it, so it must still be translated rather than hard-coded English.
  const resolvedLabel = label ?? t('widgetLabelDataSource');
  // Only device/anchor are offered (device only when deviceOnly); a stored reserved kind
  // (devices, slot, …) reads as unset here rather than being shown in a form that can't
  // edit it.
  const kind = datasource?.kind === 'anchor' ? 'anchor' : datasource?.kind === 'device' ? 'device' : '';
  const options = deviceOnly ? kindOptions(t).filter((o) => o.value === 'device') : kindOptions(t);

  const onKind = (next: string) => {
    if (next === 'device') onChange(EMPTY_DEVICE);
    else if (next === 'anchor') onChange(EMPTY_ANCHOR);
    else onChange(undefined);
  };

  return (
    <div className="space-y-3 rounded-md border border-border p-3">
      <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {resolvedLabel}
      </div>
      <FormField label={t('widgetLabelKind')}>
        <Combobox options={options} value={kind} onChange={onKind} placeholder={t('widgetPlaceholderNone')} />
      </FormField>

      {datasource?.kind === 'device' && (
        <DeviceFields selector={datasource} showMeasurements={showMeasurements} onChange={onChange} />
      )}
      {datasource?.kind === 'anchor' && (
        <AnchorFields selector={datasource} showMeasurements={showMeasurements} onChange={onChange} />
      )}
    </div>
  );
}

function DeviceFields({
  selector,
  showMeasurements,
  onChange,
}: {
  selector: DeviceSelector;
  showMeasurements: boolean;
  onChange: (next: DatasourceSelector) => void;
}) {
  const { t } = useTranslation(['dashboards', 'common']);
  return (
    <>
      <FormField label={t('common:familyDevice')}>
        <EntityPicker
          // 'device' is the EntityKind discriminant, not user text.
          // eslint-disable-next-line i18next/no-literal-string
          kind="device"
          value={selector.deviceToken}
          onChange={(token) => onChange({ ...selector, deviceToken: token })}
        />
      </FormField>
      {showMeasurements && (
        <FormField label={t('widgetLabelMeasurements')} description={t('widgetMeasurementsHint')}>
          <MeasurementsInput
            measurements={selector.measurements}
            onChange={(m) => onChange({ ...selector, measurements: m })}
          />
        </FormField>
      )}
    </>
  );
}

function AnchorFields({
  selector,
  showMeasurements,
  onChange,
}: {
  selector: AnchorSelector;
  showMeasurements: boolean;
  onChange: (next: DatasourceSelector) => void;
}) {
  const { t } = useTranslation(['dashboards', 'common']);
  const setAnchor = (patch: Partial<AnchorTarget>) =>
    onChange({ ...selector, anchor: { ...selector.anchor, ...patch } });

  return (
    <>
      <FormField label={t('widgetLabelRelationship')}>
        <Input
          value={selector.anchor.relationship}
          onChange={(e) => setAnchor({ relationship: e.target.value })}
        />
      </FormField>
      <FormField label={t('widgetLabelTargetType')}>
        <Combobox
          options={targetTypeOptions(t)}
          value={selector.anchor.targetType}
          onChange={(v) =>
            // Changing the target type clears the now-mismatched token.
            setAnchor({ targetType: v as AnchorTarget['targetType'], targetToken: '' })
          }
          allowClear={false}
        />
      </FormField>
      <FormField label={t('widgetLabelTarget')}>
        <EntityPicker
          kind={selector.anchor.targetType as EntityKind}
          value={selector.anchor.targetToken}
          onChange={(token) => setAnchor({ targetToken: token })}
        />
      </FormField>
      {showMeasurements && (
        <FormField label={t('widgetLabelMeasurements')} description={t('widgetMeasurementsHint')}>
          <MeasurementsInput
            measurements={selector.measurements}
            onChange={(m) => onChange({ ...selector, measurements: m })}
          />
        </FormField>
      )}
    </>
  );
}

// ---- scoped-slot context authoring (ADR-039) --------------------------------

// SlotScopeFields authors a device slot's CONTEXT: a root context (bound directly / by a
// context-selector) or scoped to a parent anchor slot (following the selected building, say)
// with a strategy — 'first' auto-follows the parent's first member, 'manual' keeps a chosen
// member. The parent choices are the dashboard's OTHER anchor slots. Emitting undefined
// clears the scope back to root. The workspace validates + applies (cycle/self/non-anchor
// parents are rejected there), so this only offers structurally valid choices.
function SlotScopeFields({
  slotName,
  slots,
  scope,
  onScope,
}: {
  slotName: string;
  slots: Record<string, SlotDefinition>;
  scope: SlotScope | undefined;
  onScope: (scope: SlotScope | undefined) => void;
}) {
  const { t } = useTranslation('dashboards');
  const parents = Object.keys(slots).filter((n) => slots[n].type === 'anchor' && n !== slotName);
  const mode = scope ? 'scoped' : 'root';
  const strategy = scope?.strategy ?? 'first';
  const parent = scope?.parent ?? '';

  const setMode = (next: string) => {
    if (next === 'root') return onScope(undefined);
    const p = parent || parents[0];
    if (p) onScope({ parent: p, strategy });
  };

  return (
    <div className="space-y-3 rounded-md border border-border p-3">
      <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {t('widgetContextHeading')}
      </div>
      {parents.length === 0 && mode === 'root' ? (
        <p className="text-xs text-muted-foreground">{t('widgetContextEmptyHint')}</p>
      ) : (
        <>
          <FormField label={t('widgetLabelBindsTo')} description={t('widgetBindsToHint')}>
            <Combobox options={contextModeOptions(t)} value={mode} onChange={setMode} allowClear={false} />
          </FormField>
          {mode === 'scoped' && (
            <>
              <FormField label={t('widgetLabelParentSlot')}>
                <Combobox
                  options={parents.map((p) => ({ value: p, label: p }))}
                  value={parent}
                  onChange={(p) => p && onScope({ parent: p, strategy })}
                  allowClear={false}
                  placeholder={t('widgetPlaceholderSelectParent')}
                />
              </FormField>
              <FormField label={t('widgetLabelStrategy')} description={t('widgetStrategyHint')}>
                <Combobox
                  options={scopeStrategyOptions(t)}
                  value={strategy}
                  onChange={(s) => onScope({ parent: parent || parents[0], strategy: s as SlotScope['strategy'] })}
                  allowClear={false}
                />
              </FormField>
            </>
          )}
        </>
      )}
    </div>
  );
}

// ---- measurement <-> comma-string helpers -----------------------------------

// MeasurementsInput keeps its own text buffer so the controlled join→split→join
// round-trip doesn't eat a comma (or trailing space) the user is mid-typing. It
// reconciles down to the parent only when the parsed measurements actually differ.
function MeasurementsInput({
  measurements,
  onChange,
}: {
  measurements: string[];
  onChange: (m: string[]) => void;
}) {
  const [text, setText] = useState(() => measurements.join(', '));
  useEffect(() => {
    if (splitMeasurements(text).join(',') !== measurements.join(',')) setText(measurements.join(', '));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [measurements]);
  return (
    <Input
      value={text}
      onChange={(e) => {
        setText(e.target.value);
        onChange(splitMeasurements(e.target.value));
      }}
    />
  );
}

function splitMeasurements(value: string): string[] {
  return value
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}
