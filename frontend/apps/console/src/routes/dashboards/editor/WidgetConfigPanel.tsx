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
  WidgetInstance,
  WidgetType,
} from '@devicechain/dashboards';
import { useEffect, useState } from 'react';
import { X } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { EntityPicker, type EntityKind } from './EntityPicker';

// Widgets that carry a datasource (label/image do not).
const DATA_WIDGETS = new Set<WidgetType>(['latest-card', 'gauge', 'timeseries-chart', 'table']);

const KIND_OPTIONS: ComboboxOption[] = [
  { value: 'device', label: 'Device' },
  { value: 'anchor', label: 'Anchor' },
];

const TARGET_TYPE_OPTIONS: ComboboxOption[] = [
  { value: 'customer', label: 'Customer' },
  { value: 'area', label: 'Area' },
  { value: 'asset', label: 'Asset' },
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
  // Title/text/type-specific option edits (widget-only; datasource is separate).
  onChange: (next: WidgetInstance) => void;
  // Data-source edits: the new entity view, or undefined for "None".
  onDatasource: (next: ConcreteSelector | undefined) => void;
  onClose: () => void;
}

export function WidgetConfigPanel({
  widget,
  datasource,
  slotName,
  onChange,
  onDatasource,
  onClose,
}: WidgetConfigPanelProps) {
  // Read/write a single options key, dropping it when cleared so the widget falls
  // back to its default rather than reading an empty string/NaN.
  const setOption = (key: string, value: string | number | undefined) => {
    const options = { ...(widget.options ?? {}) };
    if (value === undefined || value === '') delete options[key];
    else options[key] = value;
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

        {DATA_WIDGETS.has(widget.type) && (
          <>
            {/* DatasourceFields edits a device/anchor selector, which is exactly a
                ConcreteSelector — the workspace re-stores it as a slot. */}
            <DatasourceFields
              datasource={datasource}
              onChange={(ds) => onDatasource(ds as ConcreteSelector | undefined)}
            />
            {datasource && slotName && (
              <p className="text-xs text-muted-foreground">
                Bound via slot <span className="font-mono">{slotName}</span>
              </p>
            )}
          </>
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
  setOption: (key: string, value: string | number | undefined) => void;
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
      </>
    );
  }
  return null; // chart/table/label/image have no extra options beyond above
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
  onChange,
}: {
  datasource: DatasourceSelector | undefined;
  onChange: (next: DatasourceSelector | undefined) => void;
}) {
  // Only device/anchor are offered; a stored reserved kind (devices, slot, …)
  // reads as unset here rather than being shown in a form that can't edit it.
  const kind = datasource?.kind === 'anchor' ? 'anchor' : datasource?.kind === 'device' ? 'device' : '';

  const onKind = (next: string) => {
    if (next === 'device') onChange(EMPTY_DEVICE);
    else if (next === 'anchor') onChange(EMPTY_ANCHOR);
    else onChange(undefined);
  };

  return (
    <div className="space-y-3 rounded-md border border-border p-3">
      <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Data source</div>
      <FormField label="Kind">
        <Combobox options={KIND_OPTIONS} value={kind} onChange={onKind} placeholder="None" />
      </FormField>

      {datasource?.kind === 'device' && <DeviceFields selector={datasource} onChange={onChange} />}
      {datasource?.kind === 'anchor' && <AnchorFields selector={datasource} onChange={onChange} />}
    </div>
  );
}

function DeviceFields({
  selector,
  onChange,
}: {
  selector: DeviceSelector;
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
      <FormField label="Measurements" description="Comma-separated; leave blank for all.">
        <MeasurementsInput
          measurements={selector.measurements}
          onChange={(m) => onChange({ ...selector, measurements: m })}
        />
      </FormField>
    </>
  );
}

function AnchorFields({
  selector,
  onChange,
}: {
  selector: AnchorSelector;
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
      <FormField label="Measurements" description="Comma-separated; leave blank for all.">
        <MeasurementsInput
          measurements={selector.measurements}
          onChange={(m) => onChange({ ...selector, measurements: m })}
        />
      </FormField>
    </>
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
