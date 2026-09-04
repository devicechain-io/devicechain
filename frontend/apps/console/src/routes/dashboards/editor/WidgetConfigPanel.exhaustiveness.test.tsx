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
// field, drives the control inside it, and records what actually came back through
// onChange. The set of keys observed that way must equal the set the schema declares, in
// both directions and per widget type:
//
//   • a key declared and not observed  → a widget option nobody can author (the old defect)
//   • a key observed and not declared  → a control writing a key the renderer never reads
//
// 🔴 AND THE KEY IS ONLY HALF THE CONTRACT — THE OTHER HALF IS THE VALUE, IN BOTH
// DIRECTIONS. This file used to drive every control with the fixed inputs `'1'` and `'x'`,
// which makes a control storing the CONSTANT `1` or `'x'` indistinguishable from one
// storing what the author typed; and it never looked at what a control DISPLAYS, so a
// field reading the wrong key — showing a blank Text box for a widget whose `label` is
// set, which an author then "fixes" by overwriting the real content — passed untouched.
// Both were live survivors of #889's mutation run. So:
//
//   • every control is driven with a value NO OTHER OPTION OF THAT WIDGET can produce,
//     and the bag is asserted to hold that exact value under that exact key;
//   • every control is separately RENDERED from a bag of those values and asserted to
//     display its own key's value — the read side, which no write-side assertion reaches;
//   • the two passes deliberately DISAGREE about which dropdown entry they use, so no
//     single hard-coded choice can satisfy both (see `choiceIndex`); and
//   • a third pass renders an EMPTY bag, because a control displaying a constant is only
//     ever compared against that constant while every option is set.
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
import {
  WIDGET_OPTIONS,
  numberOptionSpec,
  validateWidgetOptions,
  type WidgetOptionSpecs,
} from '@devicechain/widgets';

import { WidgetConfigPanel } from './WidgetConfigPanel';

// The entity pickers are list queries over device-management and are not this file's
// subject: nothing they choose lands in the options bag (a datasource is reported through
// its own callback and stored as a slot). Stubbed so a render needs no GraphQL.
vi.mock('./EntityPicker', () => ({ EntityPicker: () => null }));

// The command vocabulary IS an input to an option this panel authors, so it is supplied
// rather than skipped: picking a command bakes its key, its label and its parameter
// descriptors, and a fixture with no parameterSchema would leave that third key unwritten
// and this gate would (correctly) fail for a reason that is about the fixture.
//
// 🔴 FOUR COMMANDS, AND THE COUNT IS DERIVED RATHER THAN CHOSEN. With ONE published
// command, "bakes the command the author picked" and "bakes the only command there is" are
// the same observation — a picker hard-coding `selectable[0]` authors the right three keys
// with the right three values and passes. With TWO, every entry is either the first or the
// last, so one of those two constants is still invisible whichever the driver picks. Three
// leaves exactly one interior entry, which the two passes then have to share.
//
// Four is the smallest list with TWO interior entries — one for the pass that drives, one
// for the pass that renders — so neither `selectable[0]` nor `selectable[length - 1]` can
// satisfy either. The same arithmetic sets the slot count below, and the instrument check
// at the end of the file asserts the property rather than trusting this comment.
const { COMMANDS } = vi.hoisted(() => ({
  COMMANDS: [
    { commandKey: 'reboot', name: 'Reboot', parameterSchema: '[]' },
    {
      commandKey: 'setpoint',
      name: 'Set point',
      parameterSchema: '[{"name":"target","type":"FLOAT","required":true}]',
    },
    { commandKey: 'calibrate', name: 'Calibrate', parameterSchema: '[{"name":"axis","type":"STRING"}]' },
    { commandKey: 'identify', name: 'Identify', parameterSchema: '[{"name":"seconds","type":"INT"}]' },
  ],
}));

vi.mock('@/lib/api/device-management', () => ({
  getDeviceCommandVocabulary: async () => ({ constrained: true, commands: COMMANDS }),
  listCommandDefinitionsForDevice: async () => [],
}));

afterEach(cleanup);

const FIELD_PREFIX = 'widget-option-field-';

// Four slots, for exactly the reason there are four commands — and this control is where
// a smaller fixture was actually caught being insufficient. With two slots the driver's
// entry was the LAST one at both of the call sites that declare `selectionTarget`, so a
// picker storing `names[names.length - 1]` passed every widget type.
const SLOTS: Record<string, SlotDefinition> = {
  primary: { type: 'device' },
  building: { type: 'anchor' },
  floor: { type: 'anchor' },
  spare: { type: 'device' },
};

const SLOT_NAMES = Object.keys(SLOTS);

function specsOf(type: WidgetType): WidgetOptionSpecs {
  return WIDGET_OPTIONS[type];
}

const schemaOrder = (type: WidgetType) => Object.keys(specsOf(type));

// ---- The distinguishable value per option key -------------------------------

// 🔴 THE TWO PASSES MUST NOT AGREE ON A CONSTANT. The write pass drives a control and
// checks what was stored; the read pass renders from a bag and checks what is shown. If
// both used the same entry of a dropdown — the last one, say — a control hard-coding THAT
// entry would satisfy both, and the suite would be green while an author who picked
// `MAJOR` got `INDETERMINATE` stored. "The last" is the same defect as "the first" with
// the sign flipped: it is a constant either way, and a second fixture entry only moves the
// ambiguity rather than closing it.
//
// 🔴 AND "VARY IT PER FIELD" IS NOT THE ANSWER EITHER — that was tried and it failed
// silently. An index derived from the option's schema position looks like it cannot be a
// constant, but the slot picker has exactly two call sites and both landed on the same
// entry, so `names[names.length - 1]` still passed every widget type. The property wanted
// is not "varies"; it is "is NEITHER THE FIRST NOR THE LAST", which is what a control can
// hard-code. That is a property of the INDEX, so it is stated as one, and the instrument
// check at the end of this file holds every choice control to it rather than trusting the
// arithmetic to have worked out.
//
// Index 1 written, index 2 shown: both interior wherever the list is long enough to have
// an interior, always distinct from each other, and folded back for the short lists
// (a two-value enum has no interior, and is covered by the group check instead).
type Side = 'written' | 'shown';
const SIDE_INDEX: Record<Side, number> = { written: 1, shown: 2 };

// Which entry of an `offered`-long dropdown this side uses. The `>= 2` is the assumption
// the whole scheme rests on: with one entry offered there is nothing to distinguish, and
// the two sides collapse onto the same choice. A control offering fewer fails here rather
// than being quietly measured against itself.
function choiceIndex(offered: number, side: Side): number {
  expect(
    offered,
    `a dropdown offering ${offered} entries cannot tell a choice from a constant`,
  ).toBeGreaterThanOrEqual(2);
  return SIDE_INDEX[side] % offered;
}

// The command a command-button is driven to, or rendered from. All three command keys
// resolve through one choice, because that is what they are: picking a command bakes its
// key, its label and its parameter descriptors together.
const commandFor = (side: Side) => COMMANDS[choiceIndex(COMMANDS.length, side)];

const slotFor = (side: Side) => SLOT_NAMES[choiceIndex(SLOT_NAMES.length, side)];

// optionValue is the ONE definition of "what this option is worth on this side", shared by
// the write pass (what the driver enters) and the read pass (what the panel is rendered
// from). Sharing the definition is what makes the round trip a round trip; the `side`
// argument is what stops it being a round trip a constant can fake.
function optionValue(type: WidgetType, key: string, side: Side): string | number | boolean {
  // 🔴 THE KEYS WHOSE VALUE IS NOT FREE. Their controls offer only real choices — a slot
  // that exists, a command the device publishes — so their value is whichever real choice
  // this side resolves to, not an invented string.
  if (key === 'selectionTarget') return slotFor(side);
  if (key === 'commandName') return commandFor(side).commandKey;
  if (key === 'commandLabel') return commandFor(side).name;
  if (key === 'parameterSchema') return commandFor(side).parameterSchema;

  const spec = specsOf(type)[key];
  switch (spec.kind) {
    case 'boolean':
      // A checkbox has one reachable value from unchecked, so there is nothing here for
      // the side to vary. The unset arm at the end of this file is where a boolean field
      // displaying a constant is caught instead.
      return true;
    case 'enum':
      return spec.values[choiceIndex(spec.values.length, side)];
    case 'number':
      return numberValue(type, key);
    case 'string':
      return `v-${key}`;
  }
}

// Whether an option is authored by PICKING FROM A LIST — the keys the two-sided scheme
// above actually varies, and so the keys the instrument check below holds it to.
function isChoiceKey(type: WidgetType, key: string): boolean {
  if (key === 'selectionTarget' || key === 'commandName') return true;
  if (key === 'commandLabel' || key === 'parameterSchema') return true;
  return specsOf(type)[key].kind === 'enum';
}

// A per-key number inside the option's own bounds. Spaced by the key's position in the
// schema, so no two numeric options of one widget can share a value — which is exactly
// what separates "stored what was typed" from "stored a constant" and from "stored the
// neighbouring key's value".
function numberValue(type: WidgetType, key: string): number {
  const value = 2 + 3 * schemaOrder(type).indexOf(key);
  const spec = numberOptionSpec(type, key);
  // Every numeric option in the table today is unbounded or has min ≤ 2 and max ≥ 23, so
  // the spacing above lands inside all of them. An option it does NOT fit fails here,
  // rather than being clamped on commit into a value the assertion then blames the panel
  // for.
  expect(value, `${type}.${key}: the driver's ${value} is below the option's min`).toBeGreaterThanOrEqual(
    spec?.min ?? value,
  );
  expect(value, `${type}.${key}: the driver's ${value} is above the option's max`).toBeLessThanOrEqual(
    spec?.max ?? value,
  );
  return value;
}

// A bag carrying a distinguishable value for EVERY option the type declares — what the
// read pass renders from.
function fullOptions(type: WidgetType): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const key of schemaOrder(type)) out[key] = optionValue(type, key, 'shown');
  return out;
}

// ---- Rendering ---------------------------------------------------------------

function widgetOf(type: WidgetType, options: Record<string, unknown> = {}): WidgetInstance {
  // A real layout rather than a cast: `as WidgetInstance` over a wrong shape compiles
  // happily and would leave this suite driving a widget the renderer could not place.
  return {
    id: 'w1',
    type,
    layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 4, z: 0 } },
    options,
  };
}

// Rendered with everything a field could need already chosen — a bound device (the command
// vocabulary hangs off it) and a dashboard with slots (the selection-target picker offers
// them). A field with nothing to offer renders no control, which this gate reads as "cannot
// author that key"; that reading is right, so the fixture must not be the reason.
function renderPanel(
  type: WidgetType,
  onChange: (next: WidgetInstance) => void,
  options: Record<string, unknown> = {},
) {
  return render(
    <WidgetConfigPanel
      widget={widgetOf(type, options)}
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

// Waits for the field's control, which for the command picker appears only after its
// choices load (it shows a message first).
async function controlIn(container: HTMLElement): Promise<void> {
  await waitFor(() => {
    expect(
      container.querySelector('input, button'),
      `no control ever appeared in field ${fieldId(container)}`,
    ).toBeTruthy();
  });
}

// Opens a Combobox and returns its choice buttons, which live in a portal outside the
// field and so are found on the document.
async function openChoices(container: HTMLElement, trigger: HTMLElement): Promise<HTMLElement[]> {
  fireEvent.click(trigger);
  const popover = await waitFor(() => {
    const el = document.querySelector<HTMLElement>('[role="dialog"]');
    expect(el, `the dropdown in field ${fieldId(container)} did not open`).toBeTruthy();
    return el!;
  });
  const choices = [...popover.querySelectorAll<HTMLElement>('button')];
  expect(choices.length, `the dropdown in field ${fieldId(container)} offered nothing`).toBeGreaterThan(0);
  return choices;
}

// Which dropdown entry to pick, and what picking it must STORE. The entry is the one
// `choiceIndex` resolves for the WRITTEN side — never the first and never the last, both
// of which a control can hard-code.
function choiceFor(
  type: WidgetType,
  id: string,
  offered: number,
): { index: number; entered: Record<string, string> } {
  if (id === 'slot') {
    expect(offered, 'the selection-target picker did not offer every slot').toBe(SLOT_NAMES.length);
    return {
      index: choiceIndex(offered, 'written'),
      entered: { selectionTarget: slotFor('written') },
    };
  }
  if (id === 'command') {
    expect(offered, 'the command picker did not offer every published command').toBe(COMMANDS.length);
    const picked = commandFor('written');
    return {
      index: choiceIndex(offered, 'written'),
      entered: {
        commandName: picked.commandKey,
        commandLabel: picked.name,
        parameterSchema: picked.parameterSchema,
      },
    };
  }
  const spec = specsOf(type)[id];
  expect(spec.kind, `field ${id} on ${type} renders a dropdown for a non-enum option`).toBe('enum');
  const values = spec.kind === 'enum' ? spec.values : [];
  // 🔴 THE INDEX↔VALUE CORRESPONDENCE THIS RELIES ON. The dropdown is built by mapping the
  // spec's values in order, so entry i IS values[i]. Pinning the COUNT is what stops that
  // assumption drifting into a confidently wrong value assertion.
  expect(values.length, `the ${id} dropdown on ${type} offered ${offered} of ${values.length} values`).toBe(
    offered,
  );
  const index = choiceIndex(values.length, 'written');
  return { index, entered: { [id]: values[index] } };
}

// drive works one field's control the way an author would, and returns WHAT IT ENTERED per
// option key — not merely which shape it found. It THROWS on a container with no control
// rather than returning nothing: an empty field is the defect this gate exists for, and a
// driver that shrugged at one would report it as a missing key with no hint of why.
async function drive(
  type: WidgetType,
  container: HTMLElement,
): Promise<Record<string, string | number | boolean>> {
  const id = fieldId(container);
  await controlIn(container);

  const number = container.querySelector<HTMLInputElement>('input[type="number"]');
  if (number) {
    const value = optionValue(type, id, 'written') as number;
    fireEvent.change(number, { target: { value: String(value) } });
    fireEvent.blur(number);
    shapesDriven.add('number');
    return { [id]: value };
  }

  const checkbox = container.querySelector<HTMLElement>('button[role="checkbox"]');
  if (checkbox) {
    // A checkbox has one reachable value from unchecked, so `true` is not a choice the
    // driver makes. The read side is where a boolean field's key is distinguished.
    fireEvent.click(checkbox);
    shapesDriven.add('checkbox');
    return { [id]: true };
  }

  // Any remaining button is a Combobox trigger (its clear affordance is an svg with
  // role="button", not a <button>, so it cannot be picked up here).
  const trigger = container.querySelector<HTMLElement>('button');
  if (trigger) {
    const choices = await openChoices(container, trigger);
    const { index, entered } = choiceFor(type, id, choices.length);
    fireEvent.click(choices[index]);
    shapesDriven.add('choice');
    return entered;
  }

  const text = container.querySelector<HTMLInputElement>('input');
  if (text) {
    const value = optionValue(type, id, 'written') as string;
    fireEvent.change(text, { target: { value } });
    shapesDriven.add('text');
    return { [id]: value };
  }

  throw new Error(`field ${id} renders no control an author could use`);
}

// optionsWrittenBy drives ONE field on a freshly rendered panel and returns the option keys
// that reached onChange, having first asserted the VALUES under them. Fresh each time
// because the panel is controlled: the widget it is given never changes, so every field
// starts from the same empty bag and what lands in the resulting options is exactly what
// THIS field wrote.
//
// 🔴 A KEY IS NOT THE WHOLE CONTRACT, and there are two ways past it. A number field that
// stored "1" instead of 1 writes exactly the right key and is read back as `undefined` by
// the renderer's optNumber — the same invisible nothing as no field at all, arrived at
// through a spelling the key-level check calls correct; every bag is therefore put past the
// schema's own validator. And a field that stored a CONSTANT would satisfy both the key
// check and the validator, which is what the per-key value assertion below is for.
//
// Two validator codes are excluded, and neither is a shrug. `missing` fires because a field
// is driven on its own, so every other required option of that widget is legitimately
// absent. `invariant` fires because the only cross-field rule today wants a gauge's max
// above its min, and driving each of those two fields separately sets one of them with the
// other unset. Everything the driver actually asserts about a value — its TYPE, its bounds,
// its enum, its JSON — is left in.
const VALUE_ISSUE_CODES = ['type', 'range', 'enum', 'json', 'unknown'];

async function optionsWrittenBy(type: WidgetType, id: string): Promise<string[]> {
  const onChange = vi.fn();
  const { container } = renderPanel(type, onChange);
  const field = fieldContainers(container).find((el) => fieldId(el) === id);
  expect(field, `field ${id} vanished on a re-render of ${type}`).toBeTruthy();
  const entered = await drive(type, field!);
  await waitFor(() => expect(onChange, `driving field ${id} on ${type} changed nothing`).toHaveBeenCalled());
  const calls = onChange.mock.calls;
  const last = calls[calls.length - 1][0] as WidgetInstance;
  cleanup();

  const bad = validateWidgetOptions(type, last.options).filter((i) => VALUE_ISSUE_CODES.includes(i.code));
  expect(bad.map((i) => `${i.key}: ${i.message}`), `field ${id} on ${type} wrote a value the schema rejects`).toEqual(
    [],
  );

  const written = (last.options ?? {}) as Record<string, unknown>;
  for (const [key, value] of Object.entries(entered)) {
    expect(
      written[key],
      `field ${id} on ${type} stored ${JSON.stringify(written[key])} under ${key}, not the ` +
        `${JSON.stringify(value)} it was given`,
    ).toEqual(value);
  }
  // Exactly the keys it was driven to write — no more. A control quietly stamping an extra
  // key would otherwise be absorbed into "covered" by the union below.
  expect(Object.keys(written).sort(), `field ${id} on ${type} wrote keys it was not driven to write`).toEqual(
    Object.keys(entered).sort(),
  );

  return Object.keys(written);
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
      const keys = await optionsWrittenBy(type, id);
      expect(keys.length, `field ${id} on ${type} wrote no option at all`).toBeGreaterThan(0);
      for (const key of keys) written.add(key);
    }

    expect([...written].sort()).toEqual(declared);
  });
});

// ---- The read side -----------------------------------------------------------

// 🔴 NOTHING ABOVE LOOKS AT WHAT A CONTROL SHOWS, and a config panel is read at least as
// often as it is written: an author opens a saved widget to change one thing. A field
// wired to the wrong key displays the wrong value — a blank Text box on a label that has
// text, a gauge minimum showing its maximum — and every write-side assertion still passes,
// because the field writes its own key correctly. Worse than invisible: the author "fixes"
// the blank box by typing over content that was never lost until they did.
//
// The panel is rendered from a bag holding a DIFFERENT value for every option of the
// widget, so a field showing any key but its own shows something demonstrably wrong.
async function assertDisplaysItsOwnValue(type: WidgetType, container: HTMLElement): Promise<void> {
  const id = fieldId(container);
  await controlIn(container);
  const where = `field ${id} on ${type}`;

  const number = container.querySelector<HTMLInputElement>('input[type="number"]');
  if (number) {
    expect(number.value, `${where} displays another option's number`).toBe(String(optionValue(type, id, 'shown')));
    return;
  }

  const checkbox = container.querySelector<HTMLElement>('button[role="checkbox"]');
  if (checkbox) {
    // The bag sets every boolean option of the widget, so an unchecked box means the field
    // is reading a key that is not a boolean — or not reading one at all.
    expect(checkbox.getAttribute('aria-checked'), `${where} is unchecked for a set boolean`).toBe('true');
    return;
  }

  const trigger = container.querySelector<HTMLElement>('button');
  if (trigger) {
    // The Combobox renders its clear affordance (an svg with role="button") only when an
    // option is actually SELECTED, so its presence is a placeholder-independent read of
    // "something is chosen". Asserted here so the unset arm below, which reads the same
    // signal in the negative, is a real observation rather than a vacuous one.
    expect(container.querySelector('[role="button"]'), `${where} shows no chosen option at all`).not.toBeNull();
    // Read BEFORE the dropdown is opened; opening does not change the trigger, but the
    // order says which observation is the subject.
    const shown = (trigger.textContent ?? '').trim();
    expect(shown, `${where} shows the wrong choice`).toBe(await expectedChoiceLabel(type, id, container, trigger));
    return;
  }

  const text = container.querySelector<HTMLInputElement>('input');
  if (text) {
    expect(text.value, `${where} displays another option's text`).toBe(String(optionValue(type, id, 'shown')));
    return;
  }

  throw new Error(`field ${id} renders no control an author could read`);
}

// What a dropdown must be SHOWING for the value the bag carries.
//
// For an enum the label is taken from the dropdown the panel itself renders, not from the
// wording table — so this asserts "the field shows its own value" and says nothing about
// the catalogs, which widgetOptionFields.test.ts owns. The slot and command pickers label
// their entries from data this file supplies, so their expected text is known directly.
async function expectedChoiceLabel(
  type: WidgetType,
  id: string,
  container: HTMLElement,
  trigger: HTMLElement,
): Promise<string> {
  if (id === 'slot') return slotFor('shown');
  if (id === 'command') {
    const cmd = commandFor('shown');
    return `${cmd.name} (${cmd.commandKey})`;
  }

  const spec = specsOf(type)[id];
  expect(spec.kind, `field ${id} on ${type} renders a dropdown for a non-enum option`).toBe('enum');
  const values = spec.kind === 'enum' ? spec.values : [];
  const choices = await openChoices(container, trigger);
  expect(choices.length, `the ${id} dropdown on ${type} offered ${choices.length} of ${values.length}`).toBe(
    values.length,
  );
  // The label of the entry the bag's value names — at the SHOWN offset, which is not the
  // entry the write pass drives. A control displaying any fixed entry fails here for at
  // least one field, whichever entry it fixes on.
  return (choices[choiceIndex(values.length, 'shown')].textContent ?? '').trim();
}

describe('the config panel shows each option the widget already carries', () => {
  it.each([...WIDGET_TYPES])('%s', async (type) => {
    const options = fullOptions(type);
    const { container } = renderPanel(type, vi.fn(), options);
    const ids = fieldContainers(container).map(fieldId);
    cleanup();
    expect(ids.length, `${type} rendered no fields at all`).toBeGreaterThan(0);

    // Fresh per field, like the write side: an enum read leaves a portal open, and a
    // shared render would let one field's dropdown be found while inspecting the next.
    for (const id of ids) {
      const fresh = renderPanel(type, vi.fn(), options);
      const field = fieldContainers(fresh.container).find((el) => fieldId(el) === id);
      expect(field, `field ${id} vanished on a re-render of ${type}`).toBeTruthy();
      await assertDisplaysItsOwnValue(type, field!);
      cleanup();
    }
  });
});

// 🔴 THE DEFAULT-STATE ARM, and it is not symmetry for its own sake. Every assertion in
// the pass above is made against a bag where every option is SET, so a control that
// displays a CONSTANT is only ever compared against the one value it happens to show. The
// two-sided choice index catches that for a dropdown, because the two passes disagree — but
// a checkbox has one reachable value, so `checked` hard-coded to true is invisible to every
// assertion in this file that renders a full bag. Rendering the same panel with an EMPTY
// bag is the other arm: nothing is set, so every control must show nothing.
//
// The same absence let a mutation survive on the geofence editor's warning, which is why
// WidgetConfigPanel.test.tsx carries a default-state control of its own.
async function assertDisplaysNothing(type: WidgetType, container: HTMLElement): Promise<void> {
  const id = fieldId(container);
  await controlIn(container);
  const where = `field ${id} on ${type}`;

  const number = container.querySelector<HTMLInputElement>('input[type="number"]');
  if (number) {
    expect(number.value, `${where} shows a number for an option nothing set`).toBe('');
    return;
  }

  const checkbox = container.querySelector<HTMLElement>('button[role="checkbox"]');
  if (checkbox) {
    expect(checkbox.getAttribute('aria-checked'), `${where} is checked for an option nothing set`).toBe('false');
    return;
  }

  if (container.querySelector('button')) {
    // The clear affordance again, read in the negative — see the positive assertion above.
    // Comparing the trigger's text against the placeholder instead would need this file to
    // know each option's placeholder key, which is the wording table's business.
    expect(
      container.querySelector('[role="button"]'),
      `${where} shows a chosen option for one nothing set`,
    ).toBeNull();
    return;
  }

  const text = container.querySelector<HTMLInputElement>('input');
  if (text) {
    expect(text.value, `${where} shows text for an option nothing set`).toBe('');
    return;
  }

  throw new Error(`field ${id} renders no control an author could read`);
}

describe('the config panel shows nothing for an option the widget does not carry', () => {
  it.each([...WIDGET_TYPES])('%s', async (type) => {
    const { container } = renderPanel(type, vi.fn());
    const ids = fieldContainers(container).map(fieldId);
    cleanup();
    expect(ids.length, `${type} rendered no fields at all`).toBeGreaterThan(0);

    for (const id of ids) {
      const fresh = renderPanel(type, vi.fn());
      const field = fieldContainers(fresh.container).find((el) => fieldId(el) === id);
      expect(field, `field ${id} vanished on a re-render of ${type}`).toBeTruthy();
      await assertDisplaysNothing(type, field!);
      cleanup();
    }
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
      for (const id of ids) await optionsWrittenBy(type, id);
    }
    expect([...shapesDriven].sort()).toEqual(['checkbox', 'choice', 'number', 'text']);
  });

  // 🔴 THE SECOND INSTRUMENT CHECK, AND THE ONE THIS FILE'S REWRITE TURNS ON. Every value
  // assertion above — written and displayed — only distinguishes a right key from a wrong
  // one WHILE THE VALUES DIFFER. Two options of one widget driven with the same value
  // would silently restore the old blind spot for that pair, with every test still green.
  it('gives every option of a widget a value no other option of that widget has', () => {
    for (const type of WIDGET_TYPES) {
      const keys = schemaOrder(type);
      for (const side of ['written', 'shown'] as const) {
        const values = keys.map((k) => JSON.stringify(optionValue(type, k, side)));
        const collisions = keys.filter((_k, i) => values.indexOf(values[i]) !== i);
        expect(collisions, `${type} (${side}): these options share a value with an earlier one`).toEqual([]);
      }
    }
  });

  // 🔴 THE OTHER AXIS OF THE SAME PROPERTY, AND THE ONE THIS FILE ALREADY GOT WRONG ONCE.
  // Distinct values ACROSS KEYS stop a control reading its neighbour. What stops a control
  // reading NOTHING — storing or showing a fixed entry of its own dropdown — is which entry
  // the suite exercises, and that is a claim about an INDEX. Two earlier versions asserted
  // it in prose and were false: "always the last entry" is a constant, and "an index derived
  // from the option's schema position" collapsed to the same entry at both of the slot
  // picker's two call sites, so `names[names.length - 1]` passed every widget type.
  //
  // So it is checked, per CONTROL — the code path one mutation would change — and in both
  // directions. A control is only safe from `x[0]` if some field it serves uses an index
  // other than 0, and only safe from `x[length - 1]` if some field uses an index other than
  // the last. This is what forces four slots and four commands rather than two.
  it('never lets one hard-coded dropdown entry satisfy a whole control', () => {
    const groups = new Map<string, { label: string; offered: number }[]>();
    for (const type of WIDGET_TYPES) {
      for (const key of schemaOrder(type)) {
        if (!isChoiceKey(type, key)) continue;
        // The three command keys share one control and one pick, so they are counted once.
        if (key === 'commandLabel' || key === 'parameterSchema') continue;
        const control = key === 'selectionTarget' ? 'slot' : key === 'commandName' ? 'command' : 'enum';
        const offered =
          control === 'slot'
            ? SLOT_NAMES.length
            : control === 'command'
              ? COMMANDS.length
              : (specsOf(type)[key] as { kind: 'enum'; values: readonly string[] }).values.length;
        const bucket = groups.get(control) ?? [];
        bucket.push({ label: `${type}.${key}`, offered });
        groups.set(control, bucket);
      }
    }
    // The groups themselves must exist, or the loop above quietly checks nothing.
    expect([...groups.keys()].sort()).toEqual(['command', 'enum', 'slot']);

    for (const [control, fields] of groups) {
      for (const side of ['written', 'shown'] as const) {
        const used = fields.map((f) => ({ ...f, index: choiceIndex(f.offered, side) }));
        expect(
          used.some((f) => f.index !== 0),
          `${control}/${side}: every field uses the FIRST entry, so a control fixed on it is invisible`,
        ).toBe(true);
        expect(
          used.some((f) => f.index !== f.offered - 1),
          `${control}/${side}: every field uses the LAST entry, so a control fixed on it is invisible`,
        ).toBe(true);
      }
    }
  });

  // And the two passes must not land on the same entry, or a single constant satisfies the
  // write assertion and the display assertion together.
  it('reads back a different choice than it wrote', () => {
    for (const type of WIDGET_TYPES) {
      for (const key of schemaOrder(type)) {
        if (!isChoiceKey(type, key)) continue;
        expect(
          optionValue(type, key, 'shown'),
          `${type}.${key}: both passes use the same choice, so a control fixed on it is invisible`,
        ).not.toEqual(optionValue(type, key, 'written'));
      }
    }
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
