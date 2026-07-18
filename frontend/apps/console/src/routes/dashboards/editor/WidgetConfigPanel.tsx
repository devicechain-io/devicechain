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
import { X } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import {
  listCommandDefinitionsForDevice,
  getDeviceCommandVocabulary,
  type PublishedCommand,
} from '@/lib/api/device-management';
import { draftOnlyCommandKeys } from '@/routes/devices/commandVocabulary';
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

const KIND_OPTIONS: ComboboxOption[] = [
  { value: 'device', label: 'Device' },
  { value: 'anchor', label: 'Anchor' },
];

const ALARM_STATE_OPTIONS: ComboboxOption[] = [
  { value: 'ACTIVE', label: 'Active' },
  { value: 'CLEARED', label: 'Cleared' },
];

const ALARM_SEVERITY_OPTIONS: ComboboxOption[] = [
  { value: 'CRITICAL', label: 'Critical' },
  { value: 'MAJOR', label: 'Major' },
  { value: 'MINOR', label: 'Minor' },
  { value: 'WARNING', label: 'Warning' },
  { value: 'INDETERMINATE', label: 'Indeterminate' },
];

// Stored as the string 'true'/'false' (absent = any) — the widget maps it back to the
// boolean acknowledged filter.
const ALARM_ACK_OPTIONS: ComboboxOption[] = [
  { value: 'false', label: 'Unacknowledged' },
  { value: 'true', label: 'Acknowledged' },
];

const TARGET_TYPE_OPTIONS: ComboboxOption[] = [
  { value: 'customer', label: 'Customer' },
  { value: 'area', label: 'Area' },
  { value: 'asset', label: 'Asset' },
];

const CONTEXT_MODE_OPTIONS: ComboboxOption[] = [
  { value: 'root', label: 'Root context' },
  { value: 'scoped', label: 'Scoped to a parent' },
];

const SCOPE_STRATEGY_OPTIONS: ComboboxOption[] = [
  { value: 'first', label: 'First member' },
  { value: 'manual', label: 'Manual pick' },
];

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
          aria-label="Close panel"
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
            selectionTarget={optString(widget, 'selectionTarget')}
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
                  Data source
                </div>
                <p className="text-xs text-muted-foreground">
                  Derived from the parent context
                  {slotName && (
                    <>
                      {' '}
                      (slot <span className="font-mono">{slotName}</span>)
                    </>
                  )}
                  . This widget follows the selected context and any drill-down.
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
                      ? 'Target device'
                      : ALARM_WIDGETS.has(widget.type)
                        ? 'Scope'
                        : 'Data source'
                  }
                  showMeasurements={!ALARM_WIDGETS.has(widget.type) && !CONTROL_WIDGETS.has(widget.type)}
                  deviceOnly={CONTROL_WIDGETS.has(widget.type)}
                  onChange={(ds) => onDatasource(ds as ConcreteSelector | undefined)}
                />
                {datasource && slotName && (
                  <p className="text-xs text-muted-foreground">
                    Bound via slot <span className="font-mono">{slotName}</span>
                  </p>
                )}
                {ALARM_WIDGETS.has(widget.type) && !datasource && (
                  <p className="text-xs text-muted-foreground">
                    No scope selected — showing all alarms (tenant-wide).
                  </p>
                )}
              </>
            )}
          </>
        )}

        {widget.type === 'command-button' && (
          <CommandFields
            deviceToken={datasource?.kind === 'device' ? datasource.deviceToken : undefined}
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
  if (widget.type === 'label') {
    return (
      <FormField label="Text">
        <Input value={optString(widget, 'text')} onChange={(e) => setOption('text', e.target.value)} />
      </FormField>
    );
  }
  if (widget.type === 'image') {
    return (
      <>
        <FormField label="Image URL">
          <Input value={optString(widget, 'url')} onChange={(e) => setOption('url', e.target.value)} />
        </FormField>
        <FormField label="Alt text">
          <Input value={optString(widget, 'alt')} onChange={(e) => setOption('alt', e.target.value)} />
        </FormField>
      </>
    );
  }
  return (
    <FormField label="Title">
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
  if (widget.type === 'gauge') {
    return (
      <>
        <FormField label="Min">
          <NumberInput value={optNumber(widget, 'min')} onChange={(v) => setOption('min', v)} />
        </FormField>
        <FormField label="Max">
          <NumberInput value={optNumber(widget, 'max')} onChange={(v) => setOption('max', v)} />
        </FormField>
        <FormField label="Unit">
          <Input value={optString(widget, 'unit')} onChange={(e) => setOption('unit', e.target.value)} />
        </FormField>
        <FlashToggle widget={widget} setOption={setOption} />
      </>
    );
  }
  if (widget.type === 'latest-card') {
    return (
      <>
        <FormField label="Unit">
          <Input value={optString(widget, 'unit')} onChange={(e) => setOption('unit', e.target.value)} />
        </FormField>
        <FormField label="Precision">
          <NumberInput value={optNumber(widget, 'precision')} onChange={(v) => setOption('precision', v)} />
        </FormField>
        <FlashToggle widget={widget} setOption={setOption} />
      </>
    );
  }
  if (widget.type === 'alarm-table' || widget.type === 'alarm-count') {
    return (
      <>
        <FormField label="State" description="Which alarm states to include.">
          <Combobox
            options={ALARM_STATE_OPTIONS}
            value={optString(widget, 'state')}
            onChange={(v) => setOption('state', v || undefined)}
            placeholder="Any"
          />
        </FormField>
        <FormField label="Severity" description="Limit to one severity.">
          <Combobox
            options={ALARM_SEVERITY_OPTIONS}
            value={optString(widget, 'severity')}
            onChange={(v) => setOption('severity', v || undefined)}
            placeholder="Any"
          />
        </FormField>
        <FormField label="Acknowledged" description="Filter by acknowledgement.">
          <Combobox
            options={ALARM_ACK_OPTIONS}
            value={optString(widget, 'acknowledged')}
            onChange={(v) => setOption('acknowledged', v || undefined)}
            placeholder="Any"
          />
        </FormField>
        {widget.type === 'alarm-table' && (
          <>
            <FormField label="Max rows" description="Newest alarms shown before scrolling.">
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
      <FormField label="Max rows" description="Recent commands shown in the history before scrolling.">
        <NumberInput value={optNumber(widget, 'maxRows')} onChange={(v) => setOption('maxRows', v)} />
      </FormField>
    );
  }
  if (widget.type === 'table') {
    return (
      <>
        <FormField label="Precision" description="Decimal places for numeric values (blank = full precision).">
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
  const checked = widget.options?.flashOnChange === true;
  return (
    <label className="flex cursor-pointer items-start gap-2 text-sm text-foreground">
      <input
        type="checkbox"
        className="mt-0.5"
        checked={checked}
        onChange={(e) => setOption('flashOnChange', e.target.checked)}
      />
      <span>
        <span className="font-medium">Flash on change</span>
        <span className="mt-0.5 block text-xs text-muted-foreground">
          Briefly highlight a value green when it rises and red when it falls.
        </span>
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
  label = 'Data source',
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
  // Only device/anchor are offered (device only when deviceOnly); a stored reserved kind
  // (devices, slot, …) reads as unset here rather than being shown in a form that can't
  // edit it.
  const kind = datasource?.kind === 'anchor' ? 'anchor' : datasource?.kind === 'device' ? 'device' : '';
  const kindOptions = deviceOnly ? KIND_OPTIONS.filter((o) => o.value === 'device') : KIND_OPTIONS;

  const onKind = (next: string) => {
    if (next === 'device') onChange(EMPTY_DEVICE);
    else if (next === 'anchor') onChange(EMPTY_ANCHOR);
    else onChange(undefined);
  };

  return (
    <div className="space-y-3 rounded-md border border-border p-3">
      <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">{label}</div>
      <FormField label="Kind">
        <Combobox options={kindOptions} value={kind} onChange={onKind} placeholder="None" />
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
  return (
    <>
      <FormField label="Device">
        <EntityPicker
          kind="device"
          value={selector.deviceToken}
          onChange={(token) => onChange({ ...selector, deviceToken: token })}
        />
      </FormField>
      {showMeasurements && (
        <FormField label="Measurements" description="Comma-separated; leave blank for all.">
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
  const setAnchor = (patch: Partial<AnchorTarget>) =>
    onChange({ ...selector, anchor: { ...selector.anchor, ...patch } });

  return (
    <>
      <FormField label="Relationship">
        <Input
          value={selector.anchor.relationship}
          onChange={(e) => setAnchor({ relationship: e.target.value })}
        />
      </FormField>
      <FormField label="Target type">
        <Combobox
          options={TARGET_TYPE_OPTIONS}
          value={selector.anchor.targetType}
          onChange={(v) =>
            // Changing the target type clears the now-mismatched token.
            setAnchor({ targetType: v as AnchorTarget['targetType'], targetToken: '' })
          }
          allowClear={false}
        />
      </FormField>
      <FormField label="Target">
        <EntityPicker
          kind={selector.anchor.targetType as EntityKind}
          value={selector.anchor.targetToken}
          onChange={(token) => setAnchor({ targetToken: token })}
        />
      </FormField>
      {showMeasurements && (
        <FormField label="Measurements" description="Comma-separated; leave blank for all.">
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
// The picker offers published commands only, because published is what the enqueue gate
// accepts. Baking a draft would produce a button that looks correct in the editor, renders
// correctly on the dashboard, and fails only when an operator presses it.
//
// Draft-only commands are still NAMED below the picker rather than omitted: an author who
// just wrote a command definition and can't find it needs to be told it is unpublished,
// not left to conclude the editor is broken.
function CommandFields({
  deviceToken,
  commandName,
  onSelect,
}: {
  deviceToken: string | undefined;
  commandName: string;
  onSelect: (def: PublishedCommand | undefined) => void;
}) {
  const { data, loading, error } = useQuery(
    async () => {
      if (!deviceToken) return { published: [], draftOnly: [] };
      const [vocabulary, drafts] = await Promise.all([
        getDeviceCommandVocabulary(deviceToken),
        listCommandDefinitionsForDevice(deviceToken),
      ]);
      return {
        published: vocabulary.commands,
        draftOnly: draftOnlyCommandKeys(vocabulary.commands, drafts),
      };
    },
    [deviceToken],
  );
  const published = data?.published ?? [];
  const draftOnly = data?.draftOnly ?? [];

  const options: ComboboxOption[] = published.map((def) => ({
    value: def.commandKey,
    label: def.name ? `${def.name} (${def.commandKey})` : def.commandKey,
  }));

  // A command baked from a previous target device may not exist on the current one (the
  // author repointed the device without re-picking), or may have been unpublished since.
  // Flag it — non-destructively, since the datasource and options update through separate
  // handlers — so the author re-picks rather than silently shipping a button that fails.
  const staleSelection =
    commandName !== '' && !loading && !error && !published.some((d) => d.commandKey === commandName);

  return (
    <div className="space-y-3 rounded-md border border-border p-3">
      <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Command</div>
      {!deviceToken ? (
        <p className="text-xs text-muted-foreground">Select a target device to choose a command.</p>
      ) : loading ? (
        <p className="text-xs text-muted-foreground">Loading commands…</p>
      ) : error ? (
        <p className="text-xs text-muted-foreground">Couldn’t load commands: {error}</p>
      ) : (
        <>
          {staleSelection && (
            <p className="text-xs text-destructive">
              “{commandName}” isn’t published on this device. Pick a command below.
            </p>
          )}
          {published.length === 0 ? (
            <p className="text-xs text-muted-foreground">
              This device’s profile publishes no commands. Add a command definition to its device
              profile, then publish the profile.
            </p>
          ) : (
            <FormField label="Command" description="The command this button issues.">
              <Combobox
                options={options}
                value={commandName}
                onChange={(key) => onSelect(published.find((d) => d.commandKey === key))}
                placeholder="Select a command"
              />
            </FormField>
          )}
          {draftOnly.length > 0 && (
            <div className="space-y-1 rounded-md bg-muted/50 p-2">
              <p className="text-xs text-muted-foreground">
                Authored but not published, so not selectable yet:
              </p>
              <p className="text-xs font-medium text-muted-foreground">
                {draftOnly.join(', ')}
              </p>
              <p className="text-xs text-muted-foreground">
                Publish the device profile to use {draftOnly.length === 1 ? 'it' : 'them'} here.
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
      <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Context</div>
      {parents.length === 0 && mode === 'root' ? (
        <p className="text-xs text-muted-foreground">
          Add a widget bound to a customer, area, or asset to use as a parent context — then this
          slot can follow it.
        </p>
      ) : (
        <>
          <FormField
            label="Binds to"
            description="Root: chosen directly or by a context selector. Scoped: follows a parent context (e.g. a device within the selected building)."
          >
            <Combobox options={CONTEXT_MODE_OPTIONS} value={mode} onChange={setMode} allowClear={false} />
          </FormField>
          {mode === 'scoped' && (
            <>
              <FormField label="Parent slot">
                <Combobox
                  options={parents.map((p) => ({ value: p, label: p }))}
                  value={parent}
                  onChange={(p) => p && onScope({ parent: p, strategy })}
                  allowClear={false}
                  placeholder="Select a parent"
                />
              </FormField>
              <FormField
                label="Strategy"
                description="First: auto-follow the parent's first member. Manual: keep a chosen member (a picker offers the parent's members)."
              >
                <Combobox
                  options={SCOPE_STRATEGY_OPTIONS}
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
  const names = Object.keys(slots ?? {});
  const options: ComboboxOption[] = names.map((name) => {
    const slot = slots![name];
    const role = slot.scope
      ? `member of ${slot.scope.parent}`
      : slot.type === 'anchor'
        ? 'context'
        : 'device';
    return { value: name, label: name, description: role };
  });

  return (
    <div className="space-y-3 rounded-md border border-border p-3">
      <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Selection target
      </div>
      {options.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          This dashboard has no slots yet. Bind a widget to a device or anchor first, then this
          picker can re-point it.
        </p>
      ) : (
        <FormField
          label="Target slot"
          description="The slot this picker re-points. A scoped slot becomes a member picker; a root slot a context picker."
        >
          <Combobox
            options={options}
            value={selectionTarget}
            onChange={(v) => onSelect(v || undefined)}
            placeholder="Select a slot"
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
