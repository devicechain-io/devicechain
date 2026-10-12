// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import {
  WidgetFrame,
  css,
  defineWidget,
  formatValue,
  optNumber,
  optString,
  pickSample,
  primaryMeasurementName,
} from '@devicechain/widget-sdk';

// A DOM-only tank-level card: a number and a fill bar. It imports from the SDK root only.
export const tankLevelCardWidget = defineWidget({
  descriptor: {
    id: 'acme/tank-level-card',
    // Provenance: the release of this widget. A different version still renders, and the
    // editor shows a notice. Bump it when the rendering changes in a way worth surfacing.
    version: '1.0.0',
    sdkApiVersion: 1,
    // Bump ONLY when stored options stop meaning the same thing. A board saved with another
    // optionsVersion renders as a placeholder that keeps its JSON.
    optionsVersion: 1,
    display: {
      title: 'Tank level (card)',
      description: 'The latest level of a tank as a fill bar.',
      category: 'Telemetry',
    },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: { window: { option: 'window', min: 1, default: 1 } },
    options: {
      measurement: {
        kind: 'string',
        doc: 'The measurement to show; the first one the datasource selects when empty.',
        // Portable text: shown as-is by a host that has never heard of this widget. The
        // message ids are optional: a host that defines them in its own translations shows
        // its text instead, and a host that does not falls back to `label` and `help`.
        label: 'Measurement',
        labelId: 'acme.tankLevel.measurement',
        help: 'Name of the measurement that carries the level.',
        helpId: 'acme.tankLevel.measurementHelp',
      },
      unit: { kind: 'string', doc: 'Unit shown beside the level.', label: 'Unit' },
      capacity: {
        kind: 'number',
        min: 1,
        doc: 'The value that counts as a full tank.',
        label: 'Capacity',
        help: 'A level at or above this value fills the bar.',
        // A specialized authoring workflow, named in this package's own namespace. The
        // vocabulary is open: a host that does not implement `acme/capacity-presets` shows
        // the generic number field for this option instead.
        control: 'acme/capacity-presets',
      },
      show: {
        kind: 'enum',
        values: ['bar', 'number'],
        doc: 'Whether the card draws the fill bar under the number.',
        label: 'Show',
        // Portable text for each choice and for the unset entry.
        valueLabels: { bar: 'Number and fill bar', number: 'Number only' },
        placeholder: 'Number and fill bar',
      },
      window: {
        kind: 'number',
        integer: true,
        min: 1,
        doc: 'Samples the card keeps.',
        label: 'Samples kept',
      },
    },
    defaults: { unit: '%', capacity: 100, window: 1 },
    interactions: {},
  },
  implementation: {
    kind: 'trusted-react',
    component: function TankLevelCard({ widget, data }) {
      const sample = pickSample(data.latest, primaryMeasurementName(widget));
      const capacity = optNumber(widget.options, 'capacity') ?? 100;
      const unit = optString(widget.options, 'unit') ?? '';
      const value = sample?.value ?? null;
      const fill = value === null ? 0 : Math.max(0, Math.min(1, value / capacity));
      const showBar = optString(widget.options, 'show') !== 'number';
      return (
        <WidgetFrame title="Tank level">
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8, padding: 8 }}>
            <div style={{ fontSize: 28, color: css('foreground') }}>
              {formatValue(value, 1)} {unit}
            </div>
            {showBar && (
              <div style={{ height: 12, borderRadius: 6, background: css('muted'), overflow: 'hidden' }}>
                <div style={{ width: `${fill * 100}%`, height: '100%', background: css('primary') }} />
              </div>
            )}
          </div>
        </WidgetFrame>
      );
    },
    // An optional settings editor. A host that shows it does so IN PLACE of the form it would
    // generate from `options`, so it authors every option. It imports nothing from the host:
    // it reads `options`, writes through `setOptions` (which the host checks against the
    // schema above, refusing an undeclared key or a value of the wrong kind), and takes its
    // wording from its own `descriptor`.
    editor: function TankLevelCardEditor({ options, setOptions, descriptor }) {
      const spec = (key: string) => descriptor.options[key];
      const label = (key: string) => spec(key)?.label ?? key;
      const number = (text: string) => (text === '' ? undefined : Number(text));
      const show = spec('show');
      const choices = show?.kind === 'enum' ? show.valueLabels ?? {} : {};
      const field = { display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12 } as const;
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <label style={field}>
            {label('measurement')}
            <input value={options.measurement ?? ''} onChange={(e) => setOptions({ measurement: e.target.value })} />
          </label>
          <label style={field}>
            {label('unit')}
            <input value={options.unit ?? ''} onChange={(e) => setOptions({ unit: e.target.value })} />
          </label>
          <div style={field}>
            {label('capacity')}
            <input
              type="number"
              min={1}
              value={options.capacity ?? ''}
              onChange={(e) => setOptions({ capacity: number(e.target.value) })}
            />
            <div style={{ display: 'flex', gap: 4 }}>
              {[100, 1000, 5000].map((preset) => (
                <button key={preset} type="button" onClick={() => setOptions({ capacity: preset })}>
                  {preset}
                </button>
              ))}
            </div>
          </div>
          <label style={field}>
            {label('show')}
            <select
              value={options.show ?? ''}
              onChange={(e) => setOptions({ show: e.target.value === 'number' ? 'number' : undefined })}
            >
              <option value="">{show?.kind === 'enum' ? show.placeholder : ''}</option>
              <option value="number">{choices.number ?? 'number'}</option>
            </select>
          </label>
          <label style={field}>
            {label('window')}
            <input
              type="number"
              min={1}
              step={1}
              value={options.window ?? ''}
              onChange={(e) => setOptions({ window: number(e.target.value) })}
            />
          </label>
        </div>
      );
    },
  },
});
