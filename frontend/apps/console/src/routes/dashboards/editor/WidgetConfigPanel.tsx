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

import type {
  AnchorSelector,
  AnchorTarget,
  ConcreteSelector,
  DatasourceSelector,
  DeviceSelector,
  SlotDefinition,
  SlotScope,
  WidgetInstance,
  WidgetType,
} from '@devicechain/dashboards';
import { useEffect, useState } from 'react';
import { Trans, useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { X } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import {
  listCommandDefinitionsForDevice,
  getDeviceCommandVocabulary,
} from '@/lib/api/device-management';
import { commandChoices, type PickableCommand } from '@/routes/devices/commandVocabulary';
import { EntityPicker, type EntityKind } from './EntityPicker';

// Widgets that carry a datasource (label/image do not). Alarm widgets carry one too —
// as SCOPE (which entity's alarms), where "None" means tenant-wide (all alarms). The
// command-button carries one as its single TARGET device (device-only, no measurements).
const ALARM_WIDGETS = new Set<WidgetType>(['alarm-table', 'alarm-count']);
const CONTROL_WIDGETS = new Set<WidgetType>(['command-button']);
const DATA_WIDGETS = new Set<WidgetType>([
  'latest-card',
  'gauge',
  'timeseries-chart',
  'table',
  ...ALARM_WIDGETS,
  ...CONTROL_WIDGETS,
]);

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

// eslint-disable-next-line i18next/no-literal-string -- 'ACTIVE'/'CLEARED' are the
// alarm-state API values, not user text.
function alarmStateOptions(t: TFunction): ComboboxOption[] {
  return [
    { value: 'ACTIVE', label: t('widgetAlarmStateActive') },
    { value: 'CLEARED', label: t('widgetAlarmStateCleared') },
  ];
}

// eslint-disable-next-line i18next/no-literal-string -- severity API values.
function alarmSeverityOptions(t: TFunction): ComboboxOption[] {
  return [
    { value: 'CRITICAL', label: t('widgetAlarmSeverityCritical') },
    { value: 'MAJOR', label: t('widgetAlarmSeverityMajor') },
    { value: 'MINOR', label: t('widgetAlarmSeverityMinor') },
    { value: 'WARNING', label: t('widgetAlarmSeverityWarning') },
    { value: 'INDETERMINATE', label: t('widgetAlarmSeverityIndeterminate') },
  ];
}

// Stored as the string 'true'/'false' (absent = any) — the widget maps it back to the
// boolean acknowledged filter.
// eslint-disable-next-line i18next/no-literal-string -- 'true'/'false' are the stored
// option values, not user text.
function alarmAckOptions(t: TFunction): ComboboxOption[] {
  return [
    { value: 'false', label: t('widgetAlarmAckUnacknowledged') },
    { value: 'true', label: t('widgetAlarmAckAcknowledged') },
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

  return (
    <aside className="w-80 shrink-0 overflow-auto border-l bg-card p-4">
      <div className="mb-4 flex items-center justify-between">
        <div className="text-sm font-semibold">{widget.type}</div>
        <button
          onClick={onClose}
          aria-label={t('widgetClosePanel')}
          className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          <X size={14} />
        </button>
      </div>

      <div className="space-y-4">
        <TitleFields widget={widget} setOption={setOption} />

        {widget.type === 'entity-selector' && (
          <EntitySelectorFields
            slots={slots}
            // 'selectionTarget' is the widget.options field key, not user text.
            // eslint-disable-next-line i18next/no-literal-string
            selectionTarget={optString(widget, 'selectionTarget')}
            // eslint-disable-next-line i18next/no-literal-string
            onSelect={(target) => setOption('selectionTarget', target)}
          />
        )}

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
                  showMeasurements={!ALARM_WIDGETS.has(widget.type) && !CONTROL_WIDGETS.has(widget.type)}
                  deviceOnly={CONTROL_WIDGETS.has(widget.type)}
                  onChange={(ds) => onDatasource(ds as ConcreteSelector | undefined)}
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

        {widget.type === 'command-button' && (
          <CommandFields
            deviceToken={datasource?.kind === 'device' ? datasource.deviceToken : undefined}
            // 'commandName' is the widget.options field key, not user text.
            // eslint-disable-next-line i18next/no-literal-string
            commandName={optString(widget, 'commandName')}
            onSelect={(def) =>
              setOptions({
                commandName: def?.commandKey,
                commandLabel: def?.name ?? def?.commandKey,
                parameterSchema: def?.parameterSchema ?? undefined,
              })
            }
          />
        )}

        <TypeOptions widget={widget} setOption={setOption} />
      </div>
    </aside>
  );
}

// ---- Title / text -----------------------------------------------------------

function TitleFields({
  widget,
  setOption,
}: {
  widget: WidgetInstance;
  setOption: (key: string, value: string | undefined) => void;
}) {
  const { t } = useTranslation('dashboards');
  if (widget.type === 'label') {
    return (
      <FormField label={t('widgetLabelText')}>
        {/* 'text' is the widget.options field key, not user text. */}
        {/* eslint-disable-next-line i18next/no-literal-string */}
        <Input value={optString(widget, 'text')} onChange={(e) => setOption('text', e.target.value)} />
      </FormField>
    );
  }
  if (widget.type === 'image') {
    return (
      <>
        <FormField label={t('widgetLabelImageUrl')}>
          {/* eslint-disable-next-line i18next/no-literal-string */}
          <Input value={optString(widget, 'url')} onChange={(e) => setOption('url', e.target.value)} />
        </FormField>
        <FormField label={t('widgetLabelAltText')}>
          {/* eslint-disable-next-line i18next/no-literal-string */}
          <Input value={optString(widget, 'alt')} onChange={(e) => setOption('alt', e.target.value)} />
        </FormField>
      </>
    );
  }
  return (
    <FormField label={t('widgetLabelTitle')}>
      {/* eslint-disable-next-line i18next/no-literal-string */}
      <Input value={optString(widget, 'title')} onChange={(e) => setOption('title', e.target.value)} />
    </FormField>
  );
}

// ---- Type-specific options --------------------------------------------------

function TypeOptions({
  widget,
  setOption,
}: {
  widget: WidgetInstance;
  setOption: (key: string, value: string | number | boolean | undefined) => void;
}) {
  const { t } = useTranslation('dashboards');
  if (widget.type === 'gauge') {
    return (
      <>
        <FormField label={t('widgetLabelMin')}>
          {/* eslint-disable-next-line i18next/no-literal-string */}
          <NumberInput value={optNumber(widget, 'min')} onChange={(v) => setOption('min', v)} />
        </FormField>
        <FormField label={t('widgetLabelMax')}>
          {/* eslint-disable-next-line i18next/no-literal-string */}
          <NumberInput value={optNumber(widget, 'max')} onChange={(v) => setOption('max', v)} />
        </FormField>
        <FormField label={t('widgetLabelUnit')}>
          {/* eslint-disable-next-line i18next/no-literal-string */}
          <Input value={optString(widget, 'unit')} onChange={(e) => setOption('unit', e.target.value)} />
        </FormField>
        <FlashToggle widget={widget} setOption={setOption} />
      </>
    );
  }
  if (widget.type === 'latest-card') {
    return (
      <>
        <FormField label={t('widgetLabelUnit')}>
          {/* eslint-disable-next-line i18next/no-literal-string */}
          <Input value={optString(widget, 'unit')} onChange={(e) => setOption('unit', e.target.value)} />
        </FormField>
        <FormField label={t('widgetLabelPrecision')}>
          {/* eslint-disable-next-line i18next/no-literal-string */}
          <NumberInput value={optNumber(widget, 'precision')} onChange={(v) => setOption('precision', v)} />
        </FormField>
        <FlashToggle widget={widget} setOption={setOption} />
      </>
    );
  }
  if (widget.type === 'alarm-table' || widget.type === 'alarm-count') {
    return (
      <>
        <FormField label={t('widgetLabelState')} description={t('widgetStateHint')}>
          <Combobox
            options={alarmStateOptions(t)}
            value={optString(widget, 'state')}
            // eslint-disable-next-line i18next/no-literal-string
            onChange={(v) => setOption('state', v || undefined)}
            placeholder={t('widgetPlaceholderAny')}
          />
        </FormField>
        <FormField label={t('widgetLabelSeverity')} description={t('widgetSeverityHint')}>
          <Combobox
            options={alarmSeverityOptions(t)}
            value={optString(widget, 'severity')}
            // eslint-disable-next-line i18next/no-literal-string
            onChange={(v) => setOption('severity', v || undefined)}
            placeholder={t('widgetPlaceholderAny')}
          />
        </FormField>
        <FormField label={t('widgetLabelAcknowledged')} description={t('widgetAcknowledgedHint')}>
          <Combobox
            options={alarmAckOptions(t)}
            value={optString(widget, 'acknowledged')}
            // eslint-disable-next-line i18next/no-literal-string
            onChange={(v) => setOption('acknowledged', v || undefined)}
            placeholder={t('widgetPlaceholderAny')}
          />
        </FormField>
        {widget.type === 'alarm-table' && (
          <>
            <FormField label={t('widgetLabelMaxRows')} description={t('widgetMaxRowsAlarmHint')}>
              {/* eslint-disable-next-line i18next/no-literal-string */}
              <NumberInput value={optNumber(widget, 'maxRows')} onChange={(v) => setOption('maxRows', v)} />
            </FormField>
            <FlashToggle widget={widget} setOption={setOption} />
          </>
        )}
      </>
    );
  }
  if (widget.type === 'command-button') {
    return (
      <FormField label={t('widgetLabelMaxRows')} description={t('widgetMaxRowsCommandHint')}>
        {/* eslint-disable-next-line i18next/no-literal-string */}
        <NumberInput value={optNumber(widget, 'maxRows')} onChange={(v) => setOption('maxRows', v)} />
      </FormField>
    );
  }
  if (widget.type === 'table') {
    return (
      <>
        <FormField label={t('widgetLabelPrecision')} description={t('widgetPrecisionTableHint')}>
          {/* eslint-disable-next-line i18next/no-literal-string */}
          <NumberInput value={optNumber(widget, 'precision')} onChange={(v) => setOption('precision', v)} />
        </FormField>
        <FlashToggle widget={widget} setOption={setOption} />
      </>
    );
  }
  return null; // chart/label/image have no extra options beyond above
}

// FlashToggle exposes the opt-in directional change-flash (widget option `flashOnChange`,
// read by the widgets package's optBoolean): a value briefly tints green/red on a
// rise/fall, then fades. Off by default; the option is dropped when unchecked.
function FlashToggle({
  widget,
  setOption,
}: {
  widget: WidgetInstance;
  setOption: (key: string, value: string | number | boolean | undefined) => void;
}) {
  const { t } = useTranslation('dashboards');
  const checked = widget.options?.flashOnChange === true;
  return (
    <label className="flex cursor-pointer items-start gap-2 text-sm text-foreground">
      <input
        type="checkbox"
        className="mt-0.5"
        checked={checked}
        // eslint-disable-next-line i18next/no-literal-string
        onChange={(e) => setOption('flashOnChange', e.target.checked)}
      />
      <span>
        <span className="font-medium">{t('widgetFlashOnChange')}</span>
        <span className="mt-0.5 block text-xs text-muted-foreground">{t('widgetFlashOnChangeHint')}</span>
      </span>
    </label>
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

// ---- Command selection (command-button) -------------------------------------

// CommandFields lets the author pick which command the button issues, from the target
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
function CommandFields({
  deviceToken,
  commandName,
  onSelect,
}: {
  deviceToken: string | undefined;
  commandName: string;
  onSelect: (def: PickableCommand | undefined) => void;
}) {
  const { t } = useTranslation('dashboards');
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
                onChange={(key) => onSelect(selectable.find((d) => d.commandKey === key))}
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

// EntitySelectorFields picks which slot an entity-selector widget re-points. A scoped slot
// becomes a member picker (which device within the parent context); a root slot a context
// picker (which building/customer the dashboard shows). The target is stored in the widget's
// options; the runtime resolves the candidate set from the target slot's scope.
function EntitySelectorFields({
  slots,
  selectionTarget,
  onSelect,
}: {
  slots: Record<string, SlotDefinition> | undefined;
  selectionTarget: string;
  onSelect: (target: string | undefined) => void;
}) {
  const { t } = useTranslation('dashboards');
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
            value={selectionTarget}
            onChange={(v) => onSelect(v || undefined)}
            placeholder={t('widgetPlaceholderSelectSlot')}
          />
        </FormField>
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

// ---- options readers (panel-local; strict-typed inputs) ---------------------

function optString(widget: WidgetInstance, key: string): string {
  const value = widget.options?.[key];
  return typeof value === 'string' ? value : '';
}

function optNumber(widget: WidgetInstance, key: string): number | undefined {
  const value = widget.options?.[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined;
}

function NumberInput({
  value,
  onChange,
}: {
  value: number | undefined;
  onChange: (value: number | undefined) => void;
}) {
  return (
    <Input
      type="number"
      value={value ?? ''}
      onChange={(e) => {
        const raw = e.target.value;
        if (raw === '') return onChange(undefined);
        const n = Number(raw);
        onChange(Number.isFinite(n) ? n : undefined);
      }}
    />
  );
}
