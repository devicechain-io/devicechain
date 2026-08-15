// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom (set globally in vite.config.ts). The real i18n catalogs are wired in
// by importing the config for its side effect, so the assertions below are on WHAT AN
// OPERATOR SEES and on WHAT GOES ON THE WIRE, never on a prop or a translation key.
//
// One seam is faked: the GraphQL transport (`gql`). The real API modules, the real query
// documents, the real vocabulary rule, the real payload serializer and the real form all
// run — so a request asserted here is the request that would be sent.
//
// 🔴 WHY THIS FILE EXISTS. Firing a batch is the one screen in the console where a
// mistake is a PHYSICAL ACTUATION across a fleet, and three of the four things it can get
// wrong are invisible on screen: a token reused between submits replays a previous batch
// and paints its old counts as fresh; an `allowPartial` defaulted rather than chosen
// sends an intent the operator never formed; a null `resolved` printed as 0 tells them a
// healthy group matched nothing. Each has a test below, and each was checked by breaking
// the code and watching the test fail.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock } = vi.hoisted(() => ({ gqlMock: vi.fn(), toastMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

// DeviceCommandsPanel (rendered in the payload-parity test below) reports through the
// toast. Faked so it cannot reach sonner from a test with no Toaster mounted.
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));

import { CreateBatchForm } from './CreateBatchForm';
import { DeviceCommandsPanel } from '@/routes/devices/DeviceCommandsPanel';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  firedBatches.length = 0;
});

const firedBatches: unknown[] = [];

// ── the wire ───────────────────────────────────────────────────────────────

// A published vocabulary with one typed command: a STRING and a DOUBLE, so the payload
// the form builds can only match the single-device path if BOTH ARE COERCED THE SAME WAY
// — a quoted string and an unquoted number.
const TYPED_SCHEMA = JSON.stringify([
  { name: 'mode', kind: 'SCALAR', dataType: 'STRING', required: true },
  { name: 'setpoint', kind: 'SCALAR', dataType: 'DOUBLE', required: true },
]);

const CONSTRAINED_VOCABULARY = {
  constrained: true,
  commands: [
    {
      commandKey: 'setpoint',
      name: 'Set point',
      description: null,
      parameterSchema: TYPED_SCHEMA,
    },
  ],
};

type BatchAnswer =
  | { kind: 'created'; accepted?: number; resolved?: number }
  | { kind: 'rejected'; code: string; reason: string; resolved: number | null }
  | { kind: 'neither' }
  | { kind: 'threw'; error: Error }
  | { kind: 'hangs' };

interface WireOptions {
  batch?: BatchAnswer;
  vocabulary?: { constrained: boolean; commands: unknown[] };
}

// Answers dispatch on the DOCUMENT, not the service, because one render issues several
// operations against device-management and the outcome of the batch mutation has to be
// steerable independently of them.
//
// 🔴 CreateCommandBatch IS MATCHED FIRST. "mutation CreateCommandBatch" contains
// "mutation CreateCommand" as a prefix, so the looser test would swallow the batch
// mutation and answer it as a single-command enqueue.
function wire(opts: WireOptions = {}) {
  const batch = opts.batch ?? { kind: 'created' as const };
  const vocabulary = opts.vocabulary ?? { constrained: false, commands: [] };

  gqlMock.mockImplementation((_service: string, document: unknown) => {
    const doc = String(document);

    if (doc.includes('mutation CreateCommandBatch')) {
      switch (batch.kind) {
        case 'hangs':
          return new Promise(() => {});
        case 'threw':
          return Promise.reject(batch.error);
        case 'neither':
          return Promise.resolve({ createCommandBatch: { batch: null, rejection: null } });
        case 'rejected':
          return Promise.resolve({
            createCommandBatch: {
              batch: null,
              rejection: {
                code: batch.code,
                reason: batch.reason,
                resolved: batch.resolved,
                refusals: [],
                refusalCounts: [],
              },
            },
          });
        default:
          return Promise.resolve({
            createCommandBatch: {
              batch: {
                id: 'id-new',
                token: 'batch-new',
                name: 'reboot',
                targetKind: 'DEVICE_LIST',
                resolved: batch.resolved ?? 3,
                accepted: batch.accepted ?? 3,
              },
              rejection: null,
            },
          });
      }
    }
    if (doc.includes('mutation CreateCommand')) {
      return Promise.resolve({
        createCommand: {
          command: { id: 'id-1', token: 'tok-1', status: 'QUEUED' },
          rejection: null,
        },
      });
    }
    if (doc.includes('query DeviceCommandVocabulary')) {
      return Promise.resolve({ deviceCommandVocabulary: vocabulary });
    }
    // listCommandDefinitionsForDevice walks device -> type -> profile. An unresolved
    // device short-circuits it to no drafts, which is all these tests need.
    if (doc.includes('query DeviceByToken')) return Promise.resolve({ devicesByToken: [] });
    if (doc.includes('query Devices(')) {
      return Promise.resolve({
        devices: {
          results: [{ id: 'd1', token: 'therm-1', name: 'Thermostat 1', description: null, externalId: null, metadata: null, createdAt: null, deviceType: null }],
          pagination: { pageStart: 0, pageEnd: 1, totalRecords: 1 },
        },
      });
    }
    if (doc.includes('query DeviceGroupTargets')) {
      return Promise.resolve({
        entityGroups: {
          results: [
            { token: 'north-hvac', name: 'North HVAC', membershipMode: 'dynamic', activeVersion: 3 },
            { token: 'lobby', name: 'Lobby', membershipMode: 'static', activeVersion: null },
          ],
        },
      });
    }
    if (doc.includes('query EntityGroupVersions')) {
      return Promise.resolve({ entityGroupVersions: [] });
    }
    // The single-device panel's own history query.
    return Promise.resolve({
      commands: { results: [], pagination: { pageStart: 0, pageEnd: 0, totalRecords: 0 } },
    });
  });
}

/** Every request sent to createCommandBatch, in order. */
function batchRequests(): Record<string, unknown>[] {
  return gqlMock.mock.calls
    .filter((c) => String(c[1]).includes('mutation CreateCommandBatch'))
    .map((c) => (c[2] as { request: Record<string, unknown> }).request);
}

// ── driving the form ───────────────────────────────────────────────────────

function renderForm() {
  render(<CreateBatchForm onFired={(b) => firedBatches.push(b)} />);
}

function pasteDevices(text: string) {
  fireEvent.change(screen.getByLabelText('Paste device tokens'), { target: { value: text } });
}

// Awaits the field: with a reference device named, the form does not decide between a
// picker and free text until that device's published vocabulary has been read — showing
// free text first and swapping in the picker on arrival would discard whatever had been
// typed, mid-keystroke.
async function typeCommandKey(key: string) {
  const field = await screen.findByLabelText('Command key');
  fireEvent.change(field, { target: { value: key } });
}

function chooseAllOrNothing() {
  fireEvent.click(screen.getByRole('radio', { name: /Refuse the whole batch/ }));
}

function choosePartial() {
  fireEvent.click(screen.getByRole('radio', { name: /Send to the devices that can take it/ }));
}

function fire() {
  fireEvent.click(screen.getByRole('button', { name: /Fire batch/ }));
}

/** Open a Combobox by its trigger text and click the option whose label matches. */
function chooseFromCombobox(trigger: string | RegExp, option: string | RegExp) {
  fireEvent.click(screen.getByRole('button', { name: trigger }));
  fireEvent.click(screen.getByRole('button', { name: option }));
}

// ── the tests ──────────────────────────────────────────────────────────────

describe('CreateBatchForm token', () => {
  // 🔴🔴 A REUSED BATCH TOKEN IS AN IDEMPOTENT REPLAY, NOT A RETRY. Re-issuing a token
  // that already names a batch returns THAT batch unchanged — it is never topped up — so
  // a form that minted its token once and fired twice would answer the second attempt
  // with the FIRST batch's stored counts and render them as the outcome of the write
  // just made. The operator reads "4900 of 5000 accepted" for a fan-out that never
  // happened.
  it('mints a fresh token for every attempt', async () => {
    wire({ batch: { kind: 'rejected', code: 'BATCH_PARTIAL_REFUSED', reason: 'nope', resolved: 2 } });
    renderForm();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    pasteDevices('therm-1, therm-2');
    await typeCommandKey('reboot');
    chooseAllOrNothing();

    fire();
    await waitFor(() => expect(batchRequests()).toHaveLength(1));
    fire();
    await waitFor(() => expect(batchRequests()).toHaveLength(2));

    const [first, second] = batchRequests();
    expect(first.token).toBeTruthy();
    expect(second.token).toBeTruthy();
    expect(second.token).not.toBe(first.token);
  });

  // The counterweight: a token is spent once per PRESS, not once per click. Three things
  // stop the second click — the `inFlight` ref, `disabled={busy}`, and Button's own
  // disable-while-loading — and this pins the composed behaviour, not any one of them.
  //
  // 🔑 KNOWN LIMIT OF THIS INSTRUMENT, measured rather than assumed: removing any SINGLE
  // one of the three leaves the test green, because RTL flushes a render between the two
  // fireEvent calls and whichever guard remains has already engaged. It goes red only
  // when all three are gone. So read it as "a double-click fires once", never as
  // "the ref guard works" — the ref covers the sub-render window, which jsdom cannot
  // produce.
  it('fires once when the button is double-clicked', async () => {
    wire({ batch: { kind: 'hangs' } });
    renderForm();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    pasteDevices('therm-1');
    await typeCommandKey('reboot');
    chooseAllOrNothing();

    fire();
    fire();
    await waitFor(() => expect(batchRequests().length).toBeGreaterThan(0));
    expect(batchRequests()).toHaveLength(1);
  });
});

describe('CreateBatchForm allowPartial', () => {
  // 🔴🔴 THE SCHEMA REFUSES TO SUPPLY A DEFAULT, AND SO DOES THIS FORM. `allowPartial` is
  // `Boolean!` with no SDL default because the safe answer depends on what is being
  // fired — and it decides whether a physical actuation may reach part of a fleet and
  // not the rest. A preselected value is an intent the operator never formed. Both
  // halves are asserted in one test on purpose: a form that had simply become
  // unsubmittable would satisfy the first assertion perfectly and fail the second.
  it('will not fire until the operator has chosen, then sends what they chose', async () => {
    wire();
    renderForm();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    pasteDevices('therm-1');
    await typeCommandKey('reboot');

    fire();
    // Nothing on the wire, and the reason is on screen rather than a dead button.
    expect(batchRequests()).toHaveLength(0);
    expect(screen.getByText(/There is no default/)).toBeTruthy();

    chooseAllOrNothing();
    fire();
    await waitFor(() => expect(batchRequests()).toHaveLength(1));
    expect(batchRequests()[0].allowPartial).toBe(false);
  });

  it('sends true when the operator accepts a partial fan-out', async () => {
    wire();
    renderForm();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    pasteDevices('therm-1');
    await typeCommandKey('reboot');
    choosePartial();
    fire();

    await waitFor(() => expect(batchRequests()).toHaveLength(1));
    expect(batchRequests()[0].allowPartial).toBe(true);
  });
});

describe('CreateBatchForm target', () => {
  // Order is part of the request: a partially-admitted batch admits in the order given,
  // so the pasted order is how an operator states priority. It reaches the wire intact,
  // with repeats collapsed.
  it('sends the pasted device tokens in order, deduped, and no group', async () => {
    wire();
    renderForm();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    pasteDevices('therm-9\ntherm-1, therm-9\ntherm-5');
    await typeCommandKey('reboot');
    chooseAllOrNothing();
    fire();

    await waitFor(() => expect(batchRequests()).toHaveLength(1));
    const request = batchRequests()[0];
    expect(request.deviceTokens).toEqual(['therm-9', 'therm-1', 'therm-5']);
    // 🔴 Both target fields on one request is REFUSED rather than resolved by precedence,
    // so a device-list batch must carry no group at all.
    expect(request.groupToken).toBeUndefined();
    expect(request.groupVersion).toBeUndefined();
  });

  // 🔴 A VERSION MAY ONLY BE NAMED FOR A DYNAMIC GROUP. Naming one for a static group is
  // refused by the service rather than ignored, so the input must not exist there — and
  // the mode is lowercase on the wire, which is where a case-sensitive comparison would
  // quietly get this backwards.
  it('offers a version only for a dynamic group', async () => {
    wire();
    renderForm();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    fireEvent.click(screen.getByRole('radio', { name: 'Group' }));

    expect(screen.queryByText('Group version')).toBeNull();

    chooseFromCombobox(/Choose a device group/, /North HVAC/);
    await waitFor(() => expect(screen.queryByText('Group version')).toBeTruthy());

    chooseFromCombobox(/North HVAC/, /Lobby/);
    await waitFor(() => expect(screen.queryByText('Group version')).toBeNull());
  });

  it('sends the chosen group and no device list', async () => {
    wire();
    renderForm();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    fireEvent.click(screen.getByRole('radio', { name: 'Group' }));
    chooseFromCombobox(/Choose a device group/, /Lobby/);
    await typeCommandKey('reboot');
    chooseAllOrNothing();
    fire();

    await waitFor(() => expect(batchRequests()).toHaveLength(1));
    const request = batchRequests()[0];
    expect(request.groupToken).toBe('lobby');
    expect(request.deviceTokens).toBeUndefined();
  });
});

describe('CreateBatchForm outcome', () => {
  // 🔴🔴 NULL `resolved` IS NOT ZERO, and `?? 0` is the defect. Null means no target set
  // was ever established — the refusal came first — while zero means a target that
  // genuinely matched nothing. Printing "0 devices" for the null case sends an operator
  // off to debug a group that may be perfectly healthy.
  it('does not report an unestablished target set as zero devices', async () => {
    wire({
      batch: { kind: 'rejected', code: 'BATCH_TARGET_AMBIGUOUS', reason: 'both given', resolved: null },
    });
    renderForm();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    pasteDevices('therm-1');
    await typeCommandKey('reboot');
    chooseAllOrNothing();
    fire();

    expect(await screen.findByText(/No target set was ever established/)).toBeTruthy();
    expect(screen.queryByText(/resolved to no devices/)).toBeNull();
    expect(screen.queryByText(/had resolved to 0 devices/)).toBeNull();
    // The code and the reason are the backend's own, English by policy, verbatim.
    expect(screen.getByText('BATCH_TARGET_AMBIGUOUS')).toBeTruthy();
    expect(screen.getByText('both given')).toBeTruthy();
    // A refusal creates NOTHING, so there is no batch to navigate to.
    expect(firedBatches).toHaveLength(0);
  });

  // The counterweight to the test above. Zero is a real, distinct statement and must keep
  // its own wording — otherwise "not zero" could be satisfied by never saying either.
  it('reports a target that genuinely matched nothing as exactly that', async () => {
    wire({
      batch: { kind: 'rejected', code: 'BATCH_GROUP_UNUSABLE', reason: 'empty', resolved: 0 },
    });
    renderForm();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    pasteDevices('therm-1');
    await typeCommandKey('reboot');
    chooseAllOrNothing();
    fire();

    expect(await screen.findByText(/resolved to no devices/)).toBeTruthy();
    expect(screen.queryByText(/No target set was ever established/)).toBeNull();
  });

  // 🔴 `CreateCommandBatchResult` IS TWO NULLABLE FIELDS, NOT A UNION — "neither" is a
  // shape the type permits. It must not read as success (a fleet write nobody can audit)
  // and must not read as a transport failure either: a call that ANSWERED says nothing
  // about whether it created something first, so telling the operator to just try again
  // invites a second fan-out on top of an invisible first one.
  it('treats an answer carrying neither arm as unresolved, not as success', async () => {
    wire({ batch: { kind: 'neither' } });
    renderForm();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    pasteDevices('therm-1');
    await typeCommandKey('reboot');
    chooseAllOrNothing();
    fire();

    expect(await screen.findByText(/neither a batch nor a refusal/)).toBeTruthy();
    expect(screen.getByText(/Whether the batch was created is not known/)).toBeTruthy();
    expect(firedBatches).toHaveLength(0);
  });

  it('hands the created batch to its opener', async () => {
    wire({ batch: { kind: 'created', accepted: 2, resolved: 3 } });
    renderForm();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    pasteDevices('therm-1');
    await typeCommandKey('reboot');
    chooseAllOrNothing();
    fire();

    await waitFor(() => expect(firedBatches).toHaveLength(1));
    expect(firedBatches[0]).toMatchObject({ token: 'batch-new', accepted: 2, resolved: 3 });
  });

  // A thrown error is the OTHER failure, and the difference is load-bearing: the service
  // guarantees an undecided batch creates nothing, so here — and only here — a retry is
  // free and the screen says so.
  it('says a retry is safe when the batch could not be decided', async () => {
    wire({ batch: { kind: 'threw', error: new Error('device:read required') } });
    renderForm();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    pasteDevices('therm-1');
    await typeCommandKey('reboot');
    chooseAllOrNothing();
    fire();

    expect(await screen.findByText('device:read required')).toBeTruthy();
    expect(screen.getByText(/nothing was created/)).toBeTruthy();
    expect(firedBatches).toHaveLength(0);
  });
});

describe('CreateBatchForm payload', () => {
  // 🔴🔴 THE PAYLOAD MUST BE THE ONE THE SINGLE-DEVICE PATH BUILDS. Both screens send a
  // command to the same enqueue gate against the same published schema, so a second
  // serializer here — one that stringified the form's value map, say, or quoted the
  // number — would make one command behave differently depending on where the operator
  // clicked. This asserts the identity directly rather than pinning a literal, by
  // driving BOTH forms with the same vocabulary and the same keystrokes and comparing
  // what each put on the wire.
  it('is byte-identical to the one the single-device form builds', async () => {
    wire({ vocabulary: CONSTRAINED_VOCABULARY });

    // ── the batch drawer ──
    const batchForm = render(<CreateBatchForm onFired={(b) => firedBatches.push(b)} />);
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());
    pasteDevices('therm-1');
    // The picker appears once the reference device's published vocabulary lands.
    await waitFor(() => expect(screen.queryByRole('button', { name: /Choose a command/ })).toBeTruthy());
    chooseFromCombobox(/Choose a command/, /Set point/);
    await waitFor(() => expect(batchForm.container.querySelectorAll('input').length).toBeGreaterThan(1));
    fillTypedParams(batchForm.container);
    chooseAllOrNothing();
    fire();
    await waitFor(() => expect(batchRequests()).toHaveLength(1));
    const batchPayload = batchRequests()[0].payload;
    cleanup();

    // ── the single-device panel ──
    gqlMock.mockClear();
    const panel = render(<DeviceCommandsPanel deviceToken="therm-1" />);
    await waitFor(() => expect(screen.queryByRole('button', { name: /Set point|Choose|Select/ })).toBeTruthy());
    chooseFromCombobox(/Select a command|Choose|Select/, /Set point/);
    await waitFor(() => expect(panel.container.querySelectorAll('input').length).toBeGreaterThan(1));
    fillTypedParams(panel.container);
    fireEvent.click(screen.getByRole('button', { name: /Issue command/ }));
    await waitFor(() => expect(singleCommandRequests()).toHaveLength(1));
    const singlePayload = singleCommandRequests()[0].payload;

    expect(batchPayload).toBeTruthy();
    expect(batchPayload).toBe(singlePayload);
    // Named explicitly too, so a shared regression in BOTH paths cannot pass by making
    // the two equally wrong.
    expect(batchPayload).toBe(JSON.stringify({ mode: 'eco', setpoint: 21.5 }));
  });
});

/** Fill the generated argument form: the STRING param, then the DOUBLE one. */
function fillTypedParams(container: HTMLElement) {
  const text = container.querySelector('input[type="text"]');
  const numeric = container.querySelector('input[type="number"]');
  if (!text || !numeric) throw new Error('the typed parameter form did not render both inputs');
  fireEvent.change(text, { target: { value: 'eco' } });
  fireEvent.change(numeric, { target: { value: '21.5' } });
}

/** Every request sent to the single-device createCommand mutation. */
function singleCommandRequests(): Record<string, unknown>[] {
  return gqlMock.mock.calls
    .filter((c) => {
      const doc = String(c[1]);
      return doc.includes('mutation CreateCommand') && !doc.includes('mutation CreateCommandBatch');
    })
    .map((c) => (c[2] as { request: Record<string, unknown> }).request);
}
