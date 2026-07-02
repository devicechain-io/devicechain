// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// WidgetConfigPanel — the edit-mode side panel for the selected widget (ADR-039
// PR D2b). It edits a widget's title/text, its datasource selector (device or
// anchor — the only supported kinds), and the type-specific options each widget
// reads. It is CONTROLLED: the workspace owns the working definition; this panel
// receives the selected widget and reports a replacement via onChange, which the
// workspace applies with the updateWidget transform.

import type {
  AnchorSelector,
  AnchorTarget,
  DatasourceSelector,
  DeviceSelector,
  WidgetInstance,
  WidgetType,
} from '@devicechain/dashboards';
import { useEffect, useState, type CSSProperties } from 'react';

// Widgets that carry a datasource (label/image do not).
const DATA_WIDGETS = new Set<WidgetType>(['latest-card', 'gauge', 'timeseries-chart', 'table']);

export interface WidgetConfigPanelProps {
  widget: WidgetInstance;
  onChange: (next: WidgetInstance) => void;
  onClose: () => void;
}

export function WidgetConfigPanel({ widget, onChange, onClose }: WidgetConfigPanelProps) {
  // Read/write a single options key, dropping it when cleared so the widget falls
  // back to its default rather than reading an empty string/NaN.
  const setOption = (key: string, value: string | number | undefined) => {
    const options = { ...(widget.options ?? {}) };
    if (value === undefined || value === '') delete options[key];
    else options[key] = value;
    onChange({ ...widget, options });
  };

  const setDatasource = (datasource: DatasourceSelector | undefined) => {
    onChange({ ...widget, datasource });
  };

  return (
    <aside
      style={{
        width: 300,
        flex: '0 0 300px',
        height: '100%',
        overflow: 'auto',
        borderLeft: '1px solid hsl(var(--border))',
        background: 'hsl(var(--card))',
        color: 'hsl(var(--foreground))',
        padding: 16,
        boxSizing: 'border-box',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 12 }}>
        <div style={{ fontWeight: 600, fontSize: 14 }}>{widget.type}</div>
        <button onClick={onClose} aria-label="Close panel" style={closeButtonStyle}>
          ✕
        </button>
      </div>

      <TitleFields widget={widget} setOption={setOption} />

      {DATA_WIDGETS.has(widget.type) && (
        <DatasourceFields datasource={widget.datasource} onChange={setDatasource} />
      )}

      <TypeOptions widget={widget} setOption={setOption} />
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
      <Section title="Content">
        <Field label="Text">
          <TextInput value={optString(widget, 'text')} onChange={(v) => setOption('text', v)} />
        </Field>
      </Section>
    );
  }
  if (widget.type === 'image') {
    return (
      <Section title="Image">
        <Field label="URL">
          <TextInput value={optString(widget, 'url')} onChange={(v) => setOption('url', v)} />
        </Field>
        <Field label="Alt text">
          <TextInput value={optString(widget, 'alt')} onChange={(v) => setOption('alt', v)} />
        </Field>
      </Section>
    );
  }
  return (
    <Section title="Title">
      <Field label="Title">
        <TextInput value={optString(widget, 'title')} onChange={(v) => setOption('title', v)} />
      </Field>
    </Section>
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
      <Section title="Gauge">
        <Field label="Min">
          <NumberInput value={optNumber(widget, 'min')} onChange={(v) => setOption('min', v)} />
        </Field>
        <Field label="Max">
          <NumberInput value={optNumber(widget, 'max')} onChange={(v) => setOption('max', v)} />
        </Field>
        <Field label="Unit">
          <TextInput value={optString(widget, 'unit')} onChange={(v) => setOption('unit', v)} />
        </Field>
      </Section>
    );
  }
  if (widget.type === 'latest-card') {
    return (
      <Section title="Card">
        <Field label="Unit">
          <TextInput value={optString(widget, 'unit')} onChange={(v) => setOption('unit', v)} />
        </Field>
        <Field label="Precision">
          <NumberInput value={optNumber(widget, 'precision')} onChange={(v) => setOption('precision', v)} />
        </Field>
      </Section>
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
  // Only device/anchor are offered; a stored reserved kind reads as unset here.
  const kind = datasource?.kind === 'anchor' ? 'anchor' : datasource?.kind === 'device' ? 'device' : '';

  const onKind = (next: string) => {
    if (next === 'device') onChange(EMPTY_DEVICE);
    else if (next === 'anchor') onChange(EMPTY_ANCHOR);
    else onChange(undefined);
  };

  return (
    <Section title="Data source">
      <Field label="Kind">
        <select value={kind} onChange={(e) => onKind(e.target.value)} style={inputStyle}>
          <option value="">None</option>
          <option value="device">Device</option>
          <option value="anchor">Anchor</option>
        </select>
      </Field>

      {datasource?.kind === 'device' && (
        <DeviceFields selector={datasource} onChange={onChange} />
      )}
      {datasource?.kind === 'anchor' && (
        <AnchorFields selector={datasource} onChange={onChange} />
      )}
    </Section>
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
      <Field label="Device token">
        <TextInput
          value={selector.deviceToken}
          onChange={(v) => onChange({ ...selector, deviceToken: v })}
        />
      </Field>
      <Field label="Measurements (comma-separated)">
        <MeasurementsInput
          measurements={selector.measurements}
          onChange={(m) => onChange({ ...selector, measurements: m })}
        />
      </Field>
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
      <Field label="Relationship">
        <TextInput value={selector.anchor.relationship} onChange={(v) => setAnchor({ relationship: v })} />
      </Field>
      <Field label="Target type">
        <select
          value={selector.anchor.targetType}
          onChange={(e) => setAnchor({ targetType: e.target.value as AnchorTarget['targetType'] })}
          style={inputStyle}
        >
          <option value="customer">Customer</option>
          <option value="area">Area</option>
          <option value="asset">Asset</option>
        </select>
      </Field>
      <Field label="Target token">
        <TextInput value={selector.anchor.targetToken} onChange={(v) => setAnchor({ targetToken: v })} />
      </Field>
      <Field label="Measurements (comma-separated)">
        <MeasurementsInput
          measurements={selector.measurements}
          onChange={(m) => onChange({ ...selector, measurements: m })}
        />
      </Field>
    </>
  );
}

// ---- measurement <-> comma-string helpers -----------------------------------

// MeasurementsInput keeps its own text buffer so the controlled join→split→join
// round-trip doesn't eat a trailing comma the user is mid-typing. It reconciles
// down to the parent only when the parsed measurements actually differ.
function MeasurementsInput({ measurements, onChange }: { measurements: string[]; onChange: (m: string[]) => void }) {
  const [text, setText] = useState(() => measurements.join(', '));
  useEffect(() => {
    if (splitMeasurements(text).join(',') !== measurements.join(',')) setText(measurements.join(', '));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [measurements]);
  return <TextInput value={text} onChange={(v) => { setText(v); onChange(splitMeasurements(v)); }} />;
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

// ---- small presentational primitives ----------------------------------------

const inputStyle: CSSProperties = {
  width: '100%',
  boxSizing: 'border-box',
  fontSize: 13,
  padding: '4px 8px',
  borderRadius: 4,
  border: '1px solid hsl(var(--border))',
  background: 'hsl(var(--card))',
  color: 'hsl(var(--foreground))',
};

const closeButtonStyle: CSSProperties = {
  fontSize: 12,
  lineHeight: 1,
  padding: '2px 6px',
  borderRadius: 4,
  border: '1px solid hsl(var(--border))',
  cursor: 'pointer',
  color: 'hsl(var(--muted-foreground))',
  background: 'hsl(var(--card))',
};

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 16 }}>
      <div
        style={{
          fontSize: 11,
          fontWeight: 600,
          textTransform: 'uppercase',
          letterSpacing: '0.04em',
          color: 'hsl(var(--muted-foreground))',
          marginBottom: 8,
        }}
      >
        {title}
      </div>
      {children}
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 8 }}>
      <span style={{ display: 'block', fontSize: 12, color: 'hsl(var(--muted-foreground))', marginBottom: 3 }}>
        {label}
      </span>
      {children}
    </label>
  );
}

function TextInput({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return <input type="text" value={value} onChange={(e) => onChange(e.target.value)} style={inputStyle} />;
}

function NumberInput({
  value,
  onChange,
}: {
  value: number | undefined;
  onChange: (value: number | undefined) => void;
}) {
  return (
    <input
      type="number"
      value={value ?? ''}
      onChange={(e) => {
        const raw = e.target.value;
        if (raw === '') return onChange(undefined);
        const n = Number(raw);
        onChange(Number.isFinite(n) ? n : undefined);
      }}
      style={inputStyle}
    />
  );
}
