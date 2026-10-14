// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The built-ins' settings panels are what they were before the editor read the registry.
//
// The registry cutover moved every word and every control decision out of console tables
// keyed by option name / widget id and into the descriptors. Nothing about that move is
// meant to change what an author SEES for a built-in — and a descriptor is data, so a
// labelId pointing at the wrong sentence, a helpId dropped from one widget's `maxRows`, a
// `control` declared on the wrong key, or a datasource policy that disagrees with the old
// channel sets all compile and pass every structural test.
//
// So the tables the console used to hold are reproduced here VERBATIM from the code they
// replaced, and each built-in's rendered panel is held to them: the same fields in the same
// order, each worded by the same message, the same enum choices and unset text, and the same
// data-source block. A deliberate change to a built-in's panel edits this file — that is the
// point: it is a decision, not a side effect of moving the wording.

import '@/i18n/config';
import i18n from '@/i18n/config';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  WIDGET_TYPES,
  type ConcreteSelector,
  type EnumOptionSpec,
  type KnownWidgetInstance,
  type WidgetType,
} from '@devicechain/dashboards';
import { WIDGET_BINDS_DATASOURCE, WIDGET_CHANNEL, WIDGET_OPTIONS } from '@devicechain/widgets';

import { WidgetConfigPanel } from './WidgetConfigPanel';
import { enumChoiceLabel, enumPlaceholder, type MessageLookup } from './optionWording';
import { defaultWidgetRegistry } from './widgetRegistry';

vi.mock('./EntityPicker', () => ({ EntityPicker: () => null }));

afterEach(cleanup);

// ---- The legacy tables, as the console held them ------------------------------

const LEGACY_LABEL: Record<string, string> = {
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
  staleAfterSeconds: 'widgetLabelStaleAfter',
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

const LEGACY_HINT: Record<string, string | null> = {
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
  staleAfterSeconds: 'widgetHintStaleAfter',
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

const LEGACY_HINT_OVERRIDES: Partial<Record<WidgetType, Record<string, string>>> = {
  'alarm-table': { maxRows: 'widgetMaxRowsAlarmHint' },
  'command-button': { maxRows: 'widgetMaxRowsCommandHint' },
};

const LEGACY_ENUM_VALUE: Record<string, string> = {
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

const LEGACY_ENUM_PLACEHOLDER: Record<string, string> = {
  state: 'widgetPlaceholderAny',
  severity: 'widgetPlaceholderAny',
  acknowledged: 'widgetPlaceholderAny',
  align: 'widgetPlaceholderDefault',
  fit: 'widgetPlaceholderDefault',
};

const LEGACY_CUSTOM: Record<string, 'command' | 'slot'> = {
  selectionTarget: 'slot',
  commandName: 'command',
  commandLabel: 'command',
  parameterSchema: 'command',
};

// The legacy field list: schema order, custom-control keys collapsed into one field.
function legacyFieldIds(type: WidgetType): string[] {
  const seen = new Set<string>();
  const ids: string[] = [];
  for (const key of Object.keys(WIDGET_OPTIONS[type])) {
    const custom = LEGACY_CUSTOM[key];
    if (custom) {
      if (!seen.has(custom)) ids.push(custom);
      seen.add(custom);
    } else {
      ids.push(key);
    }
  }
  // title led, above the data-source block; everything else followed it.
  return [...ids.filter((id) => id === 'title'), ...ids.filter((id) => id !== 'title')];
}

// ---- Harness -----------------------------------------------------------------

const tr = (key: string) => i18n.t(key, { ns: 'dashboards' });
const lookup: MessageLookup = (id) => (i18n.exists(id, { ns: 'dashboards' }) ? tr(id) : undefined);

function renderPanel(type: WidgetType, datasource?: ConcreteSelector) {
  const widget: KnownWidgetInstance = {
    id: 'w1',
    type,
    layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 4, z: 0 } },
    options: {},
  };
  render(
    <WidgetConfigPanel
      widget={widget}
      datasource={datasource}
      onChange={vi.fn()}
      onDatasource={vi.fn()}
      onScope={vi.fn()}
      onClose={vi.fn()}
    />,
  );
}

// Removes each expected string once, in turn; what is left is text the legacy panel did not show.
function residue(text: string, expected: string[]): string {
  let rest = text;
  for (const e of expected) {
    const at = rest.indexOf(e);
    expect(at, `"${e}" missing from "${text}"`).toBeGreaterThanOrEqual(0);
    rest = rest.slice(0, at) + rest.slice(at + e.length);
  }
  return rest.trim();
}

describe.each([...WIDGET_TYPES])('the %s settings panel matches the pre-registry editor', (type) => {
  const descriptor = defaultWidgetRegistry().descriptor(type)!;

  it('shows the same fields, in the same order', () => {
    renderPanel(type);
    const ids = screen
      .queryAllByTestId(/^widget-option-field-/)
      .map((el) => el.getAttribute('data-testid')!.replace('widget-option-field-', ''));
    expect(ids).toEqual(legacyFieldIds(type));
  });

  it('words each generic field with the same label, hint and unset text — and nothing else', () => {
    renderPanel(type);
    for (const [key, spec] of Object.entries(WIDGET_OPTIONS[type])) {
      if (LEGACY_CUSTOM[key]) continue;
      // A list option post-dates the legacy editor, so there is no legacy wording to match;
      // WidgetConfigPanel.list.test.tsx owns its controls and their text.
      if (spec.kind === 'list') continue;
      const field = screen.getByTestId(`widget-option-field-${key}`);
      const hint = LEGACY_HINT_OVERRIDES[type]?.[key] ?? LEGACY_HINT[key];
      const expected = [tr(LEGACY_LABEL[key])];
      if (spec.kind === 'enum') expected.push(tr(LEGACY_ENUM_PLACEHOLDER[key]));
      if (hint) expected.push(tr(hint));
      expect(residue(field.textContent ?? '', expected), `${type}.${key}`).toBe('');
    }
  });

  it('labels each enum choice with the same message', () => {
    for (const [key, spec] of Object.entries(descriptor.options)) {
      if (spec.kind !== 'enum') continue;
      for (const value of spec.values) {
        expect(enumChoiceLabel(spec, value, lookup), `${type}.${key}:${value}`).toBe(tr(LEGACY_ENUM_VALUE[`${key}:${value}`]));
      }
      expect(enumPlaceholder(spec as EnumOptionSpec, lookup, i18n.getFixedT(null, 'dashboards'))).toBe(
        tr(LEGACY_ENUM_PLACEHOLDER[key]),
      );
    }
  });

  // The old panel's sets: a datasource block for the types WIDGET_BINDS_DATASOURCE marked,
  // labelled by channel; measurements hidden on alarm/control/location; device kind only on control.
  it('offers the same data-source block', () => {
    const channel = WIDGET_CHANNEL[type];
    renderPanel(type, { kind: 'device', deviceToken: '', measurements: [] });
    if (!WIDGET_BINDS_DATASOURCE[type]) {
      expect(screen.queryByText(tr('widgetLabelKind'))).toBeNull();
      return;
    }
    const heading =
      channel === 'control'
        ? 'widgetLabelTargetDevice'
        : channel === 'alarm'
          ? 'widgetLabelScope'
          : 'widgetLabelDataSource';
    expect(screen.getByText(tr(heading))).toBeTruthy();
    const showMeasurements = channel !== 'alarm' && channel !== 'control' && channel !== 'location';
    expect(screen.queryByText(tr('widgetLabelMeasurements')) !== null, 'measurements field').toBe(showMeasurements);

    // Open the kind picker and see which kinds it offers.
    const kindField = screen.getByText(tr('widgetLabelKind')).closest('div')!.parentElement!;
    fireEvent.click(within(kindField).getAllByRole('button')[0]);
    expect(screen.queryByText(tr('widgetKindAnchor')) !== null, 'anchor kind offered').toBe(channel !== 'control');
  });
});
