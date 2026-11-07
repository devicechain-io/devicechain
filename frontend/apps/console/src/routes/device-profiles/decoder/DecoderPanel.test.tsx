// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs; only the GraphQL transport is faked.
//
// What this file pins: a save sends exactly the document the form builds; a refusal is mapped by its
// wire code (INVALID_DECODER names the path and marks the row, UNSUPPORTED_DECODER_KIND is its own
// message); the tab NEVER shows one profile's decoder under another while the second loads;
// read-only callers cannot edit, save, clear or test over a stored sample; the draft-vs-active badge
// is right in all four states; and the test panel sends the input it was given.
import '@/i18n/config';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock, authState } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  toastMock: vi.fn(),
  authState: { authorities: ['device:read', 'device:write'] as string[] },
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: authState.authorities } }),
}));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));

import { GraphQLRequestError } from '@devicechain/client';
import { selectOption } from '@/test/select';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { DecoderPanel, draftStatus, mapRefusal } from './DecoderPanel';
import type { StoredDecoder } from '@/lib/api/payload-decoder';
import { buildDocument, blankField, emptyState, newId, type EditorState } from './model';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  authState.authorities = ['device:read', 'device:write'];
});

const METRICS = [
  { metricKey: 'temperature', dataType: 'DOUBLE', calculated: false },
  { metricKey: 'door', dataType: 'LONG', calculated: false },
  { metricKey: 'heat_index', dataType: 'DOUBLE', calculated: true },
  { metricKey: 'label', dataType: 'STRING', calculated: false },
];

function docFor(over: (st: EditorState) => void = () => {}): string {
  const st = emptyState();
  st.sections[0].rows = [
    { id: newId(), kind: 'field', field: { ...blankField(), key: 'temperature', type: 'i16', offset: '1', div: '10' } },
  ];
  over(st);
  return buildDocument(st).text as string;
}

const stored = (document: string) => ({ kind: 'CEL', envVersion: 1, input: 'FRAME', document, maxReadings: 1, estimatedCost: 12 });

interface Fixture {
  token?: string;
  draft?: string | null;
  active?: string | null;
  activeVersion?: number | null;
}

/** Routes each operation; records mutations and test calls. */
function install(fx: Fixture = {}, handlers: Record<string, (vars: any) => unknown> = {}) {
  const calls: { op: string; vars: any }[] = [];
  gqlMock.mockImplementation((_svc: string, doc: string, vars: any) => {
    const op = /(?:query|mutation) (\w+)/.exec(String(doc))?.[1] ?? '?';
    calls.push({ op, vars });
    if (handlers[op]) return Promise.resolve(handlers[op](vars));
    switch (op) {
      case 'ProfileDecoder': {
        const token = vars.tokens[0];
        const mine = token === (fx.token ?? 'thermostat');
        return Promise.resolve({
          deviceProfilesByToken: [
            {
              token,
              decoder: mine && fx.draft ? stored(fx.draft) : null,
              activeProfileVersion: fx.active ? { version: fx.activeVersion ?? 3, decoder: stored(fx.active) } : null,
            },
          ],
        });
      }
      case 'MetricDefinitions':
        return Promise.resolve({ metricDefinitions: { results: METRICS, pagination: { pageStart: 0, pageEnd: 0, totalRecords: 4 } } });
      case 'PayloadDecoderFunctions':
        return Promise.resolve({ payloadDecoderFunctions: [{ name: 'i16be', signature: 'i16be(b, off) -> int', description: 'Signed 16-bit big endian.' }] });
      case 'SetDeviceProfileDecoder':
        return Promise.resolve({ setDeviceProfileDecoder: { token: 'thermostat', decoder: stored(vars.document) } });
      case 'ClearDeviceProfileDecoder':
        return Promise.resolve({ clearDeviceProfileDecoder: { token: 'thermostat' } });
      case 'TestPayloadDecoder':
        return Promise.resolve({ testPayloadDecoder: okResult() });
      case 'PayloadDecoderHealth':
        return Promise.resolve({ payloadDecoderHealth: { decoded: 1234567, ignored: 8, failed: 3, suppressed: 0 } });
      case 'PayloadDecoderSamples':
        return Promise.resolve({ payloadDecoderSamples: SAMPLES });
      case 'Integrations':
        return Promise.resolve({
          integrations: { results: [{ token: 'ttn-1', name: 'TTN', type: 'TTS_V3' }], pagination: { pageStart: 0, pageEnd: 0, totalRecords: 1 } },
        });
      default:
        return Promise.reject(new Error(`unexpected operation ${op}`));
    }
  });
  return calls;
}

const SAMPLES = [
  { id: '41', profileVersion: 3, createdAt: '2026-11-02T12:00:00Z', deviceToken: 'dev-a', reason: 'missing_input', error: 'decode_failed: missing_input: frame too short' },
  { id: '40', profileVersion: 2, createdAt: '2026-11-02T11:00:00Z', deviceToken: 'dev-b', reason: 'invalid_output', error: 'decode_failed: invalid_output: temperature' },
];

function okResult(over: object = {}) {
  return {
    ok: true, ignored: false, section: 'telemetry', readings: [{ key: 'temperature', value: '21.5' }],
    skipped: [], error: null, cost: 7, vectors: [], uplinks: 0, ...over,
  };
}

function renderPanel(props: { profileToken?: string; activeVersion?: number | null } = {}) {
  const ui = (p: typeof props) => (
    <MemoryRouter>
      <ConfirmProvider>
        <DecoderPanel profileToken={p.profileToken ?? 'thermostat'} activeVersion={p.activeVersion ?? 3} />
      </ConfirmProvider>
    </MemoryRouter>
  );
  const r = render(ui(props));
  return { ...r, rerenderWith: (p: typeof props) => r.rerender(ui(p)) };
}

const savedButton = () => screen.getByRole('button', { name: 'Save decoder' });
const rowPath = (c: HTMLElement, p: string) => c.querySelector(`[data-row-path="${p}"]`) as HTMLElement;

describe('DecoderPanel: loading and editing', () => {
  it('shows the stored decoder as a field row with its generated expression', async () => {
    install({ draft: docFor() });
    const { container } = renderPanel();
    await waitFor(() => expect(rowPath(container, 'sections[0].outputs[0]')).toBeTruthy());
    expect(screen.getByTestId('cel-preview').textContent).toContain('double(i16be(frame, 1)) / 10.0');
    expect(screen.getByTestId('max-readings').textContent).toContain('Up to 1 of 64');
  });

  it('lists only declared, measured, non-calculated metrics as outputs', async () => {
    install({ draft: docFor() });
    renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.click(screen.getByRole('button', { name: /Metric/ }));
    expect(await screen.findByText('door')).toBeTruthy();
    expect(screen.queryByText('heat_index')).toBeNull();
    expect(screen.queryByText('label')).toBeNull();
  });

  it('starts an empty form for a profile with no decoder, with nothing to save or clear yet', async () => {
    install({ draft: null });
    renderPanel();
    await screen.findByRole('button', { name: 'Add field' });
    expect((savedButton() as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: 'Clear decoder' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('converting a row to an expression keeps what it computed and is one way', async () => {
    install({ draft: docFor() });
    const { container } = renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.click(screen.getByRole('button', { name: 'Convert to expression' }));
    const expr = (await screen.findByLabelText('Expression')) as HTMLTextAreaElement;
    expect(expr.value).toBe('double(i16be(frame, 1)) / 10.0');
    expect(screen.queryByRole('button', { name: 'Convert to expression' })).toBeNull();
    expect(within(rowPath(container, 'sections[0].outputs[0]')).queryByTestId('cel-preview')).toBeNull();
  });
});

describe('DecoderPanel: save and error mapping', () => {
  it('saves exactly the document the form builds', async () => {
    const calls = install({ draft: docFor() });
    renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.change(screen.getByLabelText('Divide by'), { target: { value: '100' } });
    expect((savedButton() as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(savedButton());
    await waitFor(() => expect(calls.some((c) => c.op === 'SetDeviceProfileDecoder')).toBe(true));
    const set = calls.find((c) => c.op === 'SetDeviceProfileDecoder');
    expect(set?.vars.profileToken).toBe('thermostat');
    const doc = JSON.parse(set?.vars.document);
    expect(doc.sections[0].outputs[0]).toEqual({ key: 'temperature', expr: 'double(i16be(frame, 1)) / 100.0' });
    expect(doc.table.sections[0].rows[0].field.div).toBe('100');
    await waitFor(() => expect(toastMock).toHaveBeenCalledWith('Decoder saved to the draft.'));
    // Saved: nothing left to save.
    await waitFor(() => expect((savedButton() as HTMLButtonElement).disabled).toBe(true));
  });

  it('INVALID_DECODER shows the message, marks the row it names and moves focus to the alert', async () => {
    const msg = 'cannot save decoder: sections[0].outputs[0].expr: unknown function foo';
    install({ draft: docFor() }, {
      SetDeviceProfileDecoder: () => {
        throw new GraphQLRequestError(msg, 200, [{ message: msg, extensions: { code: 'INVALID_DECODER' } }]);
      },
    });
    const { container } = renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.change(screen.getByLabelText('Divide by'), { target: { value: '5' } });
    fireEvent.click(savedButton());
    const alert = await screen.findByText(/It does not compile:/);
    expect(alert.closest('[role=alert]')?.textContent).toContain('unknown function foo');
    expect(alert.closest('[role=alert]')?.textContent).toContain('sections[0].outputs[0].expr');
    const row = rowPath(container, 'sections[0].outputs[0]');
    expect(row.getAttribute('aria-invalid')).toBe('true');
    expect(within(row).getByText('Named in the error')).toBeTruthy();
    await waitFor(() => expect(document.activeElement?.getAttribute('aria-live')).toBe('assertive'));
    // Nothing was saved, so the form is still dirty and can be fixed and retried.
    expect((savedButton() as HTMLButtonElement).disabled).toBe(false);
    // The mark clears on the next edit.
    fireEvent.change(screen.getByLabelText('Divide by'), { target: { value: '6' } });
    await waitFor(() => expect(row.getAttribute('aria-invalid')).toBeNull());
  });

  it('UNSUPPORTED_DECODER_KIND is its own message, not the compile failure', async () => {
    install({ draft: docFor() }, {
      SetDeviceProfileDecoder: () => {
        throw new GraphQLRequestError('decoder kind WASM is not executable in this build', 200, [
          { message: 'decoder kind WASM is not executable in this build', extensions: { code: 'UNSUPPORTED_DECODER_KIND' } },
        ]);
      },
    });
    renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.change(screen.getByLabelText('Divide by'), { target: { value: '5' } });
    fireEvent.click(savedButton());
    expect(await screen.findByText(/Its engine is not available in this build/)).toBeTruthy();
    expect(screen.queryByText(/It does not compile:/)).toBeNull();
  });

  it('maps by wire code, never by message text', () => {
    const e = (message: string, code?: string) => new GraphQLRequestError(message, 200, [{ message, extensions: code ? { code } : {} }]);
    expect(mapRefusal(e('x sections[2].outputs[1].expr y; let[0].expr', 'INVALID_DECODER'))).toEqual({
      kind: 'invalid', message: 'x sections[2].outputs[1].expr y; let[0].expr', paths: ['sections[2].outputs[1].expr', 'let[0].expr'],
    });
    expect(mapRefusal(e('anything', 'UNSUPPORTED_DECODER_KIND')).kind).toBe('unsupported');
    expect(mapRefusal(e('anything', 'RATE_LIMITED')).kind).toBe('rateLimited');
    // A message that merely MENTIONS the code is not the code.
    expect(mapRefusal(e('INVALID_DECODER UNSUPPORTED_DECODER_KIND')).kind).toBe('other');
    expect(mapRefusal(new Error('boom'))).toEqual({ kind: 'other', message: 'boom' });
  });

  it('clearing asks first, then withdraws the draft and empties the form', async () => {
    const calls = install({ draft: docFor() });
    renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.click(screen.getByRole('button', { name: 'Clear decoder' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Clear decoder', hidden: false }));
    await waitFor(() => expect(calls.some((c) => c.op === 'ClearDeviceProfileDecoder')).toBe(true));
    await waitFor(() => expect(screen.queryByTestId('cel-preview')).toBeNull());
  });

  it('shows a decoder it cannot edit as stored, without an editor to corrupt it', async () => {
    install({ draft: JSON.stringify({ kind: 'WASM', envVersion: 1, input: 'FRAME', sections: [] }) });
    renderPanel();
    expect(await screen.findByText(/uses the WASM engine/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Add field' })).toBeNull();
  });
});

describe('DecoderPanel: stale data', () => {
  it('never shows the previous profile decoder while the next one loads', async () => {
    let releaseB: (v: unknown) => void = () => {};
    const calls = install({ draft: docFor() }, {
      ProfileDecoder: (vars: any) => {
        if (vars.tokens[0] === 'thermostat') {
          return { deviceProfilesByToken: [{ token: 'thermostat', decoder: stored(docFor()), activeProfileVersion: null }] };
        }
        return new Promise((r) => { releaseB = r; });
      },
    });
    const { rerenderWith } = renderPanel();
    await screen.findByTestId('cel-preview');

    // Without the keyed remount the panel's own guard must hold: profile B has not answered yet.
    rerenderWith({ profileToken: 'pump' });
    await waitFor(() => expect(screen.getByText('Loading the decoder…')).toBeTruthy());
    expect(screen.queryByTestId('cel-preview')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Add field' })).toBeNull();

    // B answers with NO decoder: the form must be B's (empty), not A's.
    await act(async () => {
      releaseB({ deviceProfilesByToken: [{ token: 'pump', decoder: null, activeProfileVersion: null }] });
    });
    await screen.findByRole('button', { name: 'Add field' });
    expect(screen.queryByTestId('cel-preview')).toBeNull();
    expect(calls.filter((c) => c.op === 'ProfileDecoder').map((c) => c.vars.tokens[0])).toEqual(['thermostat', 'pump']);
  });

  it('refuses an answer for a different profile than the one asked about', async () => {
    install({ draft: docFor() }, {
      ProfileDecoder: () => ({ deviceProfilesByToken: [{ token: 'someone-else', decoder: stored(docFor()), activeProfileVersion: null }] }),
    });
    renderPanel();
    expect(await screen.findByText('This profile could not be found.')).toBeTruthy();
    expect(screen.queryByTestId('cel-preview')).toBeNull();
  });

  it('re-reads both sides when the active version changes (a publish)', async () => {
    const calls = install({ draft: docFor(), active: docFor(), activeVersion: 3 });
    const { rerenderWith } = renderPanel({ activeVersion: 3 });
    await screen.findByTestId('cel-preview');
    rerenderWith({ activeVersion: 4 });
    await waitFor(() => expect(calls.filter((c) => c.op === 'ProfileDecoder').length).toBe(2));
  });
});

describe('DecoderPanel: permissions', () => {
  it('a read-only caller sees the decoder but cannot edit, save, clear or test a stored sample', async () => {
    authState.authorities = ['device:read'];
    install({ draft: docFor() });
    renderPanel();
    await screen.findByTestId('cel-preview');
    expect(screen.queryByRole('button', { name: 'Save decoder' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Clear decoder' })).toBeNull();
    // Every editing control sits inside one disabled fieldset.
    const fieldset = screen.getByRole('group', { name: 'Decoder editor' }) as HTMLFieldSetElement;
    expect(fieldset.disabled).toBe(true);
    expect((screen.getByLabelText('Divide by') as HTMLInputElement).matches(':disabled')).toBe(true);
    expect((screen.getByRole('button', { name: 'Add field' }) as HTMLButtonElement).matches(':disabled')).toBe(true);
    // The test door is device:read, but a stored sample is not offered.
    expect(screen.getByRole('radio', { name: 'Frame' })).toBeTruthy();
    expect(screen.queryByRole('radio', { name: 'Stored sample' })).toBeNull();
  });

  it('a writer is offered the stored-sample input', async () => {
    install({ draft: docFor() });
    renderPanel();
    await screen.findByTestId('cel-preview');
    expect(screen.getByRole('radio', { name: 'Stored sample' })).toBeTruthy();
  });
});

describe('draft against the active version', () => {
  const d = (doc: string | null) => (doc === null ? null : (stored(doc) as StoredDecoder));
  it('classifies all four states, comparing documents rather than spellings', () => {
    const a = docFor();
    const reordered = JSON.stringify(Object.fromEntries(Object.entries(JSON.parse(a)).reverse()));
    expect(draftStatus(null, null, null)).toBe('none');
    expect(draftStatus(a, d(a), 3)).toBe('matches');
    expect(draftStatus(a, d(reordered), 3)).toBe('matches');
    expect(draftStatus(a, d(docFor((s) => { s.sections[0].name = 'other'; })), 3)).toBe('differs');
    expect(draftStatus(a, null, 3)).toBe('unpublished');
    expect(draftStatus(a, null, null)).toBe('unpublished');
    expect(draftStatus(null, d(a), 3)).toBe('draftCleared');
  });

  it('badges the tab', async () => {
    install({ draft: docFor(), active: docFor((s) => { s.sections[0].name = 'older'; }), activeVersion: 3 });
    renderPanel();
    expect(await screen.findByText('Draft differs from the active version (v3)')).toBeTruthy();
  });

  it('says so when the draft matches', async () => {
    install({ draft: docFor(), active: docFor(), activeVersion: 3 });
    renderPanel();
    expect(await screen.findByText('Draft matches the active version (v3)')).toBeTruthy();
  });

  it('moves to "differs" as soon as the draft is saved with a change', async () => {
    install({ draft: docFor(), active: docFor(), activeVersion: 3 });
    renderPanel();
    await screen.findByText('Draft matches the active version (v3)');
    fireEvent.change(screen.getByLabelText('Divide by'), { target: { value: '100' } });
    fireEvent.click(savedButton());
    expect(await screen.findByText('Draft differs from the active version (v3)')).toBeTruthy();
  });
});

describe('DecoderPanel: test panel', () => {
  it('sends the editor document and the pasted frame, and shows readings, cost and maxReadings', async () => {
    const calls = install({ draft: docFor() });
    renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.change(screen.getByLabelText('Frame', { selector: 'textarea' }), { target: { value: '01 00 D7' } });
    fireEvent.change(screen.getByLabelText('fPort', { selector: '#dec-test-fport' }), { target: { value: '2' } });
    fireEvent.click(screen.getByRole('button', { name: 'Run test' }));
    const result = await screen.findByTestId('test-result');
    expect(result.textContent).toContain('21.5');
    expect(result.textContent).toContain('Cost 7');
    expect(result.textContent).toContain('1 readings of up to 1');
    const call = calls.filter((c) => c.op === 'TestPayloadDecoder').pop();
    expect(call?.vars.input.profileToken).toBe('thermostat');
    expect(call?.vars.input.frameHex).toBe('01 00 D7');
    expect(call?.vars.input.fPort).toBe(2);
    expect(JSON.parse(call?.vars.input.document).sections[0].outputs[0].key).toBe('temperature');
    expect(call?.vars.input.json).toBeUndefined();
  });

  it('tests the UNSAVED edit, not the saved draft', async () => {
    const calls = install({ draft: docFor() });
    renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.change(screen.getByLabelText('Divide by'), { target: { value: '100' } });
    fireEvent.change(screen.getByLabelText('Frame', { selector: 'textarea' }), { target: { value: '0100D7' } });
    fireEvent.click(screen.getByRole('button', { name: 'Run test' }));
    await screen.findByTestId('test-result');
    const doc = JSON.parse(calls.filter((c) => c.op === 'TestPayloadDecoder').pop()?.vars.input.document);
    expect(doc.sections[0].outputs[0].expr).toBe('double(i16be(frame, 1)) / 100.0');
  });

  it('shows an error with its expression path, the expression, and marks the row', async () => {
    install({ draft: docFor() }, {
      TestPayloadDecoder: () => ({
        testPayloadDecoder: okResult({
          ok: false, readings: [], section: 'telemetry',
          error: { reason: 'eval_error', path: 'sections[0].outputs[0].expr', message: 'i16be: offset 1 is outside the 1-byte frame', offset: 1 },
        }),
      }),
    });
    const { container } = renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.change(screen.getByLabelText('Frame', { selector: 'textarea' }), { target: { value: '01' } });
    fireEvent.click(screen.getByRole('button', { name: 'Run test' }));
    const result = await screen.findByTestId('test-result');
    expect(result.textContent).toContain('i16be: offset 1 is outside the 1-byte frame');
    expect(result.textContent).toContain('sections[0].outputs[0].expr');
    expect(result.textContent).toContain('Byte offset 1');
    expect(screen.getByTestId('error-expression').textContent).toBe('double(i16be(frame, 1)) / 10.0');
    expect(rowPath(container, 'sections[0].outputs[0]').getAttribute('aria-invalid')).toBe('true');
  });

  it('sends a whole webhook body with its integration, headers and query', async () => {
    const calls = install({ draft: docFor() });
    renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.click(screen.getByRole('radio', { name: 'Webhook body' }));
    fireEvent.change(await screen.findByLabelText('Whole request body'), { target: { value: '{"data":"AQDX"}' } });
    fireEvent.change(screen.getByLabelText('Headers'), { target: { value: 'X-Id: abc\n\n' } });
    fireEvent.change(screen.getByLabelText('Query parameters'), { target: { value: 'event=up' } });
    // Run is not offered until an integration is chosen.
    expect((screen.getByRole('button', { name: 'Run test' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(await screen.findByRole('button', { name: /Integration|Choose an integration/ }));
    fireEvent.click(await screen.findByText('TTN'));
    fireEvent.click(screen.getByRole('button', { name: 'Run test' }));
    await screen.findByTestId('test-result');
    const input = calls.filter((c) => c.op === 'TestPayloadDecoder').pop()?.vars.input;
    expect(input.webhookBody).toBe('{"data":"AQDX"}');
    expect(input.integrationToken).toBe('ttn-1');
    expect(input.headers).toEqual([{ name: 'X-Id', value: 'abc' }]);
    expect(input.query).toEqual([{ name: 'event', value: 'up' }]);
    expect(input.frameHex).toBeUndefined();
  });

  it('a writer can send a stored sample id; it is sent as the only input', async () => {
    const calls = install({ draft: docFor() });
    renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.click(screen.getByRole('radio', { name: 'Stored sample' }));
    fireEvent.change(await screen.findByLabelText('Sample id'), { target: { value: '42' } });
    fireEvent.click(screen.getByRole('button', { name: 'Run test' }));
    await screen.findByTestId('test-result');
    const input = calls.filter((c) => c.op === 'TestPayloadDecoder').pop()?.vars.input;
    expect(input.sampleId).toBe('42');
    expect(input.frameHex).toBeUndefined();
    expect(input.webhookBody).toBeUndefined();
  });

  it('says so when the test door is rate limited', async () => {
    install({ draft: docFor() }, {
      TestPayloadDecoder: () => {
        throw new GraphQLRequestError('too many requests', 200, [{ message: 'too many requests', extensions: { code: 'RATE_LIMITED' } }]);
      },
    });
    renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.change(screen.getByLabelText('Frame', { selector: 'textarea' }), { target: { value: '0100D7' } });
    fireEvent.click(screen.getByRole('button', { name: 'Run test' }));
    expect(await screen.findByText(/Too many tests too quickly/)).toBeTruthy();
  });

  it('will not run a form that has problems, and says why', async () => {
    install({ draft: docFor() });
    renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.change(screen.getByLabelText('Divide by'), { target: { value: 'ten' } });
    fireEvent.change(screen.getByLabelText('Frame', { selector: 'textarea' }), { target: { value: '0100D7' } });
    expect((screen.getByRole('button', { name: 'Run test' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText('Fix the problems listed above first.')).toBeTruthy();
    expect(screen.getByTestId('issue-count').textContent).toContain('1 thing to fix');
    expect(screen.getAllByText('Enter a plain number, such as 0.1 or -40.').length).toBeGreaterThan(0);
    expect((savedButton() as HTMLButtonElement).disabled).toBe(true);
  });
});

describe('DecoderPanel: vectors', () => {
  it('saving a test as a vector adds it with the readings as expectations, saved with the decoder', async () => {
    const calls = install({ draft: docFor() });
    renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.change(screen.getByLabelText('Frame', { selector: 'textarea' }), { target: { value: '0100D7' } });
    fireEvent.change(screen.getByLabelText('fPort', { selector: '#dec-test-fport' }), { target: { value: '2' } });
    fireEvent.click(screen.getByRole('button', { name: 'Run test' }));
    await screen.findByTestId('test-result');
    fireEvent.click(screen.getByRole('button', { name: 'Save as vector' }));
    expect((await screen.findByLabelText('Payload', { selector: 'textarea' })) as HTMLTextAreaElement).toHaveProperty('value', '0100D7');
    fireEvent.click(savedButton());
    await waitFor(() => expect(calls.some((c) => c.op === 'SetDeviceProfileDecoder')).toBe(true));
    const doc = JSON.parse(calls.find((c) => c.op === 'SetDeviceProfileDecoder')?.vars.document);
    expect(doc.vectors).toEqual([{ name: 'vector 1', input: { frameHex: '0100D7', fPort: 2 }, expect: { temperature: 21.5 } }]);
  });

  it('runs saved vectors as the form changes and shows each outcome', async () => {
    const calls = install(
      { draft: docFor((s) => {
        s.vectors = [{ id: newId(), name: 'room', kind: 'frameHex', text: '0100D7', fPort: '', fCnt: '', inputExtra: {}, expect: [{ id: newId(), key: 'temperature', value: '21.5' }] }];
      }) },
      { TestPayloadDecoder: () => ({ testPayloadDecoder: okResult({ vectors: [{ name: 'room', ok: false, error: 'temperature: got 21.5, expected 99', cost: 5 }] }) }) },
    );
    renderPanel();
    await screen.findByTestId('cel-preview');
    const outcome = await screen.findByTestId('vector-outcome', {}, { timeout: 3000 });
    expect(outcome.textContent).toContain('Fails');
    expect(outcome.textContent).toContain('temperature: got 21.5, expected 99');
    expect(screen.getByTestId('vector-summary').textContent).toContain('0 of 1 pass');
    expect(calls.filter((c) => c.op === 'TestPayloadDecoder').pop()?.vars.input.frameHex).toBeUndefined();
  });

  it('a vector that is not valid is a problem on the form, not a silent drop', async () => {
    install({ draft: docFor() });
    renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.click(screen.getByRole('button', { name: 'Add vector' }));
    fireEvent.change(await screen.findByLabelText('Payload', { selector: 'textarea' }), { target: { value: 'zz' } });
    expect(await screen.findByText(/must be whole bytes of hex/)).toBeTruthy();
    expect((savedButton() as HTMLButtonElement).disabled).toBe(true);
  });

  it('a JSON vector has no frame port or counter to fill in', async () => {
    install({ draft: docFor() });
    const { container } = renderPanel();
    await screen.findByTestId('cel-preview');
    fireEvent.click(screen.getByRole('button', { name: 'Add vector' }));
    const vec = () => rowPath(container, 'vectors[0]');
    await waitFor(() => expect(vec()).toBeTruthy());
    expect(within(vec()).getByLabelText('fPort')).toBeTruthy();
    await selectOption(within(vec()).getByRole('combobox', { name: 'Input' }), 'JSON');
    await waitFor(() => expect(within(vec()).queryByLabelText('fPort')).toBeNull());
    expect(within(vec()).getByLabelText('Payload')).toBeTruthy();
  });
});

describe('DecoderPanel: function reference', () => {
  it('lists the functions the server serves and filters them', async () => {
    install({ draft: docFor() });
    renderPanel();
    expect(await screen.findByText('i16be(b, off) -> int')).toBeTruthy();
    fireEvent.change(screen.getByLabelText('Filter functions'), { target: { value: 'nothing-matches' } });
    expect(screen.getByText('No function matches.')).toBeTruthy();
  });
});

describe('DecoderPanel: health and recent failures', () => {
  it('shows the last day of counts, whole, with the profile and span asked for', async () => {
    const calls = install({ draft: docFor() });
    renderPanel();
    const strip = await screen.findByTestId('decoder-health');
    expect(within(strip).getByText('1,234,567')).toBeTruthy();
    expect(strip.querySelector('[data-figure="failed"]')?.textContent).toContain('3');
    const ask = calls.find((c) => c.op === 'PayloadDecoderHealth')?.vars;
    expect(ask).toEqual({ profileToken: 'thermostat', minutes: 1440 });
    // Nothing was suppressed, so no warning.
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('says so when failures were counted but not recorded', async () => {
    install({ draft: docFor() }, {
      PayloadDecoderHealth: () => ({ payloadDecoderHealth: { decoded: 10, ignored: 0, failed: 20, suppressed: 5 } }),
    });
    renderPanel();
    expect((await screen.findByRole('status')).textContent).toContain('not recorded');
  });

  it('lists the kept failures and links to the failed events filtered to decoder failures', async () => {
    install({ draft: docFor() });
    renderPanel();
    const list = await screen.findByTestId('decoder-failures');
    expect(within(list).getAllByRole('listitem')).toHaveLength(2);
    expect(within(list).getByText(/frame too short/)).toBeTruthy();
    const link = screen.getByRole('link', { name: /failed events/i }) as HTMLAnchorElement;
    expect(link.getAttribute('href')).toBe('/ingest-failures?reason=DECODE_FAILED');
  });

  it('says so when no failure is kept', async () => {
    install({ draft: docFor() }, { PayloadDecoderSamples: () => ({ payloadDecoderSamples: [] }) });
    renderPanel();
    expect(await screen.findByTestId('no-failures')).toBeTruthy();
  });

  it('"load into test panel" selects the stored-sample input with that id, and a second pick replaces it', async () => {
    const calls = install({ draft: docFor() });
    renderPanel();
    const list = await screen.findByTestId('decoder-failures');
    fireEvent.click(within(list).getByRole('button', { name: 'Load sample 40 into the test panel' }));
    expect((await screen.findByLabelText('Sample id') as HTMLInputElement).value).toBe('40');
    fireEvent.click(within(list).getByRole('button', { name: 'Load sample 41 into the test panel' }));
    await waitFor(() => expect((screen.getByLabelText('Sample id') as HTMLInputElement).value).toBe('41'));
    fireEvent.click(screen.getByRole('button', { name: 'Run test' }));
    await screen.findByTestId('test-result');
    expect(calls.filter((c) => c.op === 'TestPayloadDecoder').pop()?.vars.input.sampleId).toBe('41');
  });

  it('a hand edit after a pick is not overwritten by a re-render', async () => {
    install({ draft: docFor() });
    renderPanel();
    const list = await screen.findByTestId('decoder-failures');
    fireEvent.click(within(list).getByRole('button', { name: 'Load sample 40 into the test panel' }));
    fireEvent.change(await screen.findByLabelText('Sample id'), { target: { value: '7' } });
    fireEvent.change(screen.getByLabelText('Divide by'), { target: { value: '5' } });
    expect((screen.getByLabelText('Sample id') as HTMLInputElement).value).toBe('7');
  });

  it('a read-only caller neither sees the health nor asks for it', async () => {
    authState.authorities = ['device:read'];
    const calls = install({ draft: docFor() });
    renderPanel();
    await screen.findByTestId('cel-preview');
    expect(screen.queryByTestId('decoder-health')).toBeNull();
    expect(calls.some((c) => c.op === 'PayloadDecoderHealth' || c.op === 'PayloadDecoderSamples')).toBe(false);
  });

  it('a failed read of the health leaves the editor usable', async () => {
    install({ draft: docFor() }, { PayloadDecoderHealth: () => { throw new Error('boom'); } });
    renderPanel();
    await screen.findByTestId('cel-preview');
    await waitFor(() => expect(screen.queryByTestId('decoder-health')).toBeNull());
    expect(screen.getByRole('button', { name: 'Save decoder' })).toBeTruthy();
  });
});
