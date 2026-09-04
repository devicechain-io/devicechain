// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THE GATE THAT MAKES THE DERIVED CONFIG PANEL WORTH ANYTHING (ADR-076 half 1).
//
// The panel builds its fields by walking WIDGET_OPTIONS[type], so "a widget type with no
// config UI" cannot happen by forgetting a branch. That is a claim about a LIST, and a list
// is exactly what the compiler can be satisfied about while nothing works: a field entry
// that renders `null`, or a control that writes `windo` instead of `window`, walks the same
// list, typechecks, and leaves the author with an option the renderer reads and no way to
// set. `Record<WidgetType, Component>` filled with `() => null` is the same defect wearing
// the compiler's blessing.
//
// So nothing here asks the panel what it authors. It RENDERS the real panel, finds each
// field, drives the control inside it, and records the option keys that actually came back
// through onChange. The set of keys observed that way must equal the set the schema
// declares, in both directions and per widget type:
//
//   • a key declared and not observed  → a widget option nobody can author (the old defect)
//   • a key observed and not declared  → a control writing a key the renderer never reads
//
// And the last link — that the DECLARED keys are the keys the RENDERER reads — is not
// asserted here because it is asserted better elsewhere: options.test.ts in
// @devicechain/widgets scans the widget sources and requires WIDGET_OPTIONS to match the
// reads exactly, per type, with its own negative control. This file joins the panel to that
// table; that test joins the table to the renderer.

import '@/i18n/config';
import { cleanup, render, fireEvent, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { WIDGET_TYPES, type SlotDefinition, type WidgetInstance, type WidgetType } from '@devicechain/dashboards';
import { WIDGET_OPTIONS, validateWidgetOptions } from '@devicechain/widgets';

import { WidgetConfigPanel } from './WidgetConfigPanel';

// The entity pickers are list queries over device-management and are not this file's
// subject: nothing they choose lands in the options bag (a datasource is reported through
// its own callback and stored as a slot). Stubbed so a render needs no GraphQL.
vi.mock('./EntityPicker', () => ({ EntityPicker: () => null }));

// The command vocabulary IS an input to an option this panel authors, so it is supplied
// rather than skipped: picking a command bakes its key, its label and its parameter
// descriptors, and a fixture with no parameterSchema would leave that third key unwritten
// and this gate would (correctly) fail for a reason that is about the fixture.
vi.mock('@/lib/api/device-management', () => ({
  getDeviceCommandVocabulary: async () => ({
    constrained: true,
    commands: [{ commandKey: 'setpoint', name: 'Set point', parameterSchema: '[]' }],
  }),
  listCommandDefinitionsForDevice: async () => [],
}));

afterEach(cleanup);

const FIELD_PREFIX = 'widget-option-field-';

const SLOTS: Record<string, SlotDefinition> = {
  primary: { type: 'device' },
  building: { type: 'anchor' },
};

function widgetOf(type: WidgetType): WidgetInstance {
  // A real layout rather than a cast: `as WidgetInstance` over a wrong shape compiles
  // happily and would leave this suite driving a widget the renderer could not place.
  return {
    id: 'w1',
    type,
    layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 4, z: 0 } },
    options: {},
  };
}

// Rendered with everything a field could need already chosen — a bound device (the command
// vocabulary hangs off it) and a dashboard with slots (the selection-target picker offers
// them). A field with nothing to offer renders no control, which this gate reads as "cannot
// author that key"; that reading is right, so the fixture must not be the reason.
function renderPanel(type: WidgetType, onChange: (next: WidgetInstance) => void) {
  return render(
    <WidgetConfigPanel
      widget={widgetOf(type)}
      datasource={{ kind: 'device', deviceToken: 'dev-1', measurements: ['temp'] }}
      slots={SLOTS}
      onChange={onChange}
      onDatasource={vi.fn()}
      onScope={vi.fn()}
      onClose={vi.fn()}
    />,
  );
}

function fieldContainers(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(`[data-testid^="${FIELD_PREFIX}"]`)];
}

const fieldId = (el: HTMLElement) => el.dataset.testid!.slice(FIELD_PREFIX.length);

// The control shapes this driver knows how to work. Tracked across the whole run and
// asserted at the end: a selector that stops matching would otherwise silently reduce this
// suite to "the fields that still respond", which passes for the wrong reason.
type ControlShape = 'number' | 'checkbox' | 'choice' | 'text';
const shapesDriven = new Set<ControlShape>();

// drive works one field's control the way an author would and returns the shape it found.
// It THROWS on a container with no control rather than returning nothing: an empty field is
// the defect this gate exists for, and a driver that shrugged at one would report it as a
// missing key with no hint of why.
async function drive(container: HTMLElement): Promise<ControlShape> {
  // A control that has to load its choices (the command picker) shows a message first.
  await waitFor(() => {
    expect(
      container.querySelector('input, button'),
      `no control ever appeared in field ${fieldId(container)}`,
    ).toBeTruthy();
  });

  const number = container.querySelector<HTMLInputElement>('input[type="number"]');
  if (number) {
    // 1 satisfies every numeric option in the table at once (each is either unbounded or
    // integer with min ≤ 1 ≤ max), so the driver needs no per-option arithmetic — and a
    // future option that 1 does NOT satisfy fails here rather than being clamped away.
    fireEvent.change(number, { target: { value: '1' } });
    fireEvent.blur(number);
    shapesDriven.add('number');
    return 'number';
  }

  const checkbox = container.querySelector<HTMLElement>('button[role="checkbox"]');
  if (checkbox) {
    fireEvent.click(checkbox);
    shapesDriven.add('checkbox');
    return 'checkbox';
  }

  // Any remaining button is a Combobox trigger (its clear affordance is an svg with
  // role="button", not a <button>, so it cannot be picked up here).
  const trigger = container.querySelector<HTMLElement>('button');
  if (trigger) {
    fireEvent.click(trigger);
    // The options live in a portal outside the field, so they are found on the document.
    const popover = await waitFor(() => {
      const el = document.querySelector<HTMLElement>('[role="dialog"]');
      expect(el, `the dropdown in field ${fieldId(container)} did not open`).toBeTruthy();
      return el!;
    });
    const choices = [...popover.querySelectorAll<HTMLElement>('button')];
    expect(choices.length, `the dropdown in field ${fieldId(container)} offered nothing`).toBeGreaterThan(0);
    fireEvent.click(choices[0]);
    shapesDriven.add('choice');
    return 'choice';
  }

  const text = container.querySelector<HTMLInputElement>('input');
  if (text) {
    fireEvent.change(text, { target: { value: 'x' } });
    shapesDriven.add('text');
    return 'text';
  }

  throw new Error(`field ${fieldId(container)} renders no control an author could use`);
}

// keysWrittenBy drives ONE field on a freshly rendered panel and returns the option keys
// that reached onChange. Fresh each time because the panel is controlled: the widget it is
// given never changes, so every field starts from the same empty bag and the keys in the
// resulting options are exactly what THIS field wrote.
// 🔴 A KEY IS NOT THE WHOLE CONTRACT. A number field that stored "1" instead of 1 writes
// exactly the right key and is read back as `undefined` by the renderer's optNumber — the
// same invisible nothing as no field at all, arrived at through a spelling the key-level
// check calls correct. So every bag a field produces is put past the schema's own validator.
//
// Two codes are excluded, and neither is a shrug. `missing` fires because a field is driven
// on its own, so every other required option of that widget is legitimately absent.
// `invariant` fires because the only cross-field rule today wants a gauge's max above its
// min, and driving each of those two fields separately sets one of them to 1 with the other
// unset or equal. Everything the driver actually asserts about a value — its TYPE, its
// bounds, its enum, its JSON — is left in.
const VALUE_ISSUE_CODES = ['type', 'range', 'enum', 'json', 'unknown'];

async function keysWrittenBy(type: WidgetType, id: string): Promise<string[]> {
  const onChange = vi.fn();
  const { container } = renderPanel(type, onChange);
  const field = fieldContainers(container).find((el) => fieldId(el) === id);
  expect(field, `field ${id} vanished on a re-render of ${type}`).toBeTruthy();
  await drive(field!);
  await waitFor(() => expect(onChange, `driving field ${id} on ${type} changed nothing`).toHaveBeenCalled());
  const calls = onChange.mock.calls;
  const last = calls[calls.length - 1][0] as WidgetInstance;
  cleanup();

  const bad = validateWidgetOptions(type, last.options).filter((i) => VALUE_ISSUE_CODES.includes(i.code));
  expect(bad.map((i) => `${i.key}: ${i.message}`), `field ${id} on ${type} wrote a value the schema rejects`).toEqual(
    [],
  );

  return Object.keys(last.options ?? {});
}

describe('the config panel authors exactly what each widget reads', () => {
  beforeEach(() => shapesDriven.clear());

  // Iterated over WIDGET_TYPES — the vocabulary itself — rather than over the option table's
  // own keys. A table missing a whole widget type would otherwise be invisible here, which
  // is the failure this file is named after.
  it.each([...WIDGET_TYPES])('%s', async (type) => {
    const declared = Object.keys(WIDGET_OPTIONS[type]).sort();
    expect(declared.length, `${type} declares no options at all`).toBeGreaterThan(0);

    const { container } = renderPanel(type, vi.fn());
    const ids = fieldContainers(container).map(fieldId);
    cleanup();
    expect(new Set(ids).size, `${type} renders two fields under one id`).toBe(ids.length);

    const written = new Set<string>();
    for (const id of ids) {
      const keys = await keysWrittenBy(type, id);
      expect(keys.length, `field ${id} on ${type} wrote no option at all`).toBeGreaterThan(0);
      for (const key of keys) written.add(key);
    }

    expect([...written].sort()).toEqual(declared);
  });
});

describe('the gate itself', () => {
  beforeEach(() => shapesDriven.clear());

  // 🔴 THE INSTRUMENT CHECK. Every assertion above is "the keys the driver could reach",
  // so a selector that stopped matching would quietly shrink what is measured — and the
  // shrinkage shows up as a MISSING key, which reads like a product defect and would be
  // "fixed" in the panel. Pinning that all four control shapes are still reachable makes a
  // rotted selector fail as itself.
  it('still knows how to work all four control shapes', async () => {
    // gauge covers number+text, table covers the checkbox, alarm-table the dropdown.
    for (const type of ['gauge', 'table', 'alarm-table'] as const) {
      const { container } = renderPanel(type, vi.fn());
      const ids = fieldContainers(container).map(fieldId);
      cleanup();
      for (const id of ids) await keysWrittenBy(type, id);
    }
    expect([...shapesDriven].sort()).toEqual(['checkbox', 'choice', 'number', 'text']);
  });

  // 🔴 THE CLOSED-WORLD HALF. Everything above only looks inside the derived field
  // containers, so a control that wrote an option from somewhere else in the panel — the
  // datasource block, the scope picker, a stray handler — would be authoring an option this
  // gate never measures, and the key it wrote would look "covered" while nothing checked it.
  // Driving every OTHER control in the panel and requiring the options bag to stay untouched
  // is what makes the field containers the whole authoring surface rather than most of it.
  it('has no control outside those fields that writes an option', async () => {
    for (const type of WIDGET_TYPES) {
      const { container } = renderPanel(type, vi.fn());
      const outside = [...container.querySelectorAll<HTMLElement>('input, button')].filter(
        (el) => !el.closest(`[data-testid^="${FIELD_PREFIX}"]`),
      );
      cleanup();

      for (let i = 0; i < outside.length; i += 1) {
        const onChange = vi.fn();
        const fresh = renderPanel(type, onChange);
        const control = [...fresh.container.querySelectorAll<HTMLElement>('input, button')].filter(
          (el) => !el.closest(`[data-testid^="${FIELD_PREFIX}"]`),
        )[i];
        if (control) {
          if (control instanceof HTMLInputElement) fireEvent.change(control, { target: { value: 'x' } });
          else fireEvent.click(control);
        }
        expect(
          onChange,
          `a control outside the derived fields on ${type} wrote to the options bag`,
        ).not.toHaveBeenCalled();
        cleanup();
      }
    }
  });
});
