// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs. Only the GraphQL transport, the toast and the
// confirm dialog are faked; the panel, its request shaping and the API modules all run.
//
// 🔴 WHAT THESE PIN. An unreadable signer pin must show as an error beside its assignment, not
// as "no signer" and not as a lost assignment; a read failure must be an error, never an empty
// state; the unverified-artifact rule and a disabled device must stop Assign BEFORE the round
// trip; and the server's refusal must reach the operator verbatim.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const { gqlMock, toastMock, confirmMock, authState } = vi.hoisted(() => ({
  authState: { scopeAnchors: [] as string[] },
  gqlMock: vi.fn(),
  toastMock: vi.fn(),
  confirmMock: vi.fn(),
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { username: 'u', authorities: [], scopeAnchors: authState.scopeAnchors } }),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => confirmMock }));

import { DeviceUpdatesPanel } from './DeviceUpdatesPanel';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  confirmMock.mockReset();
  authState.scopeAnchors = [];
});

const artifact = (over: Record<string, unknown> = {}) => ({
  id: '1',
  token: 'fw-1',
  kind: 'FIRMWARE',
  name: 'fw',
  version: '1.0.0',
  component: 'main',
  trustStatus: 'SIGNED',
  state: 'AVAILABLE',
  ...over,
});

const assignment = (over: Record<string, unknown> = {}, attempt: Record<string, unknown> = {}) => ({
  id: '10',
  createdAt: '2026-01-01T00:00:00Z',
  token: 'asg-1',
  deviceToken: 'dev-1',
  component: 'main',
  artifact: artifact(),
  actor: 'alice',
  supersededAt: null,
  current: true,
  attempt: {
    token: 'att-1',
    state: 'DOWNLOADING',
    targetVersion: '1.0.0',
    requiresReboot: true,
    lastReportAt: '2026-01-01T00:01:00Z',
    progress: { bytes: '512', total: '2048' },
    failure: null,
    cancelRequested: false,
    reconciled: false,
    ...attempt,
  },
  ...over,
});

const page = (rows: unknown[], totalRecords = rows.length) => ({
  results: rows,
  pagination: { pageStart: rows.length ? 1 : 0, pageEnd: rows.length, totalRecords },
});

interface Server {
  current?: unknown[] | Error;
  history?: unknown[] | Error;
  historyTotal?: number;
  signer?: unknown | Error;
  artifacts?: unknown[];
  allowUnverified?: boolean;
  mutation?: Record<string, unknown | Error>;
}

function serve(s: Server) {
  gqlMock.mockImplementation((_svc: string, doc: string, vars: Record<string, unknown>) => {
    const d = String(doc);
    const settle = (v: unknown, wrap: (x: unknown) => unknown) =>
      v instanceof Error ? Promise.reject(v) : Promise.resolve(wrap(v));
    if (d.includes('query DeviceUpdateAssignments')) {
      const criteria = vars.criteria as { currentOnly?: boolean };
      const rows = criteria.currentOnly ? (s.current ?? []) : (s.history ?? s.current ?? []);
      return settle(rows, (r) => ({
        updateAssignments: page(r as unknown[], criteria.currentOnly ? undefined : s.historyTotal),
      }));
    }
    if (d.includes('query AssignmentSigner')) {
      return settle(s.signer ?? null, (sg) => ({ updateAssignment: { token: 'asg-1', signedBy: sg } }));
    }
    if (d.includes('query UpdateManagementInfo')) {
      return Promise.resolve({
        updateManagementInfo: {
          area: 'update-management',
          allowUnverifiedArtifacts: s.allowUnverified ?? false,
          maxArtifactBytes: '1000',
        },
      });
    }
    if (d.includes('query Artifacts')) {
      return Promise.resolve({ artifacts: page(s.artifacts ?? []) });
    }
    for (const name of ['AssignArtifact', 'CancelUpdate', 'AbandonUnknownUpdate']) {
      if (d.includes(`mutation ${name}`)) {
        const out = s.mutation?.[name];
        return out instanceof Error ? Promise.reject(out) : Promise.resolve({ [name[0].toLowerCase() + name.slice(1)]: out ?? {} });
      }
    }
    return Promise.reject(new Error(`unexpected operation: ${d.slice(0, 80)}`));
  });
}

const callsTo = (name: string) => gqlMock.mock.calls.filter((c) => String(c[1]).includes(name));

const renderPanel = (over: Partial<{ deviceEnabled: boolean; canWrite: boolean }> = {}) =>
  render(<DeviceUpdatesPanel deviceToken="dev-1" deviceEnabled canWrite {...over} />);

const SIGNER = { token: 'sg-1', name: 'release key', keyId: 'abcd1234', algorithm: 'ED25519', state: 'ACTIVE' };

describe('a member limited to part of the tenant', () => {
  it('reads the assignments without the field the server refuses them, and still shows them', async () => {
    authState.scopeAnchors = ['customer:acme'];
    serve({ current: [assignment()], signer: SIGNER });
    renderPanel({ canWrite: false });
    expect(await screen.findByTestId('current-assignment')).toBeTruthy();
    const reads = callsTo('query DeviceUpdateAssignments');
    expect(reads.length).toBeGreaterThan(0);
    for (const c of reads) expect(String(c[1])).not.toMatch(/actor/);
  });

  it('keeps asking for the actor when the caller is not limited', async () => {
    serve({ current: [assignment()], signer: SIGNER });
    renderPanel();
    await screen.findByTestId('current-assignment');
    for (const c of callsTo('query DeviceUpdateAssignments')) expect(String(c[1])).toMatch(/actor/);
  });
});

describe('current assignment', () => {
  it('shows the artifact, trust, state, progress and the pinned signer', async () => {
    serve({ current: [assignment()], signer: SIGNER });
    renderPanel();
    const card = await screen.findByTestId('current-assignment');
    expect(within(card).getByText('1.0.0')).toBeTruthy();
    expect(within(card).getByText('Signed')).toBeTruthy();
    expect(within(card).getByText('Downloading')).toBeTruthy();
    expect(within(card).getByText(/512 \/ 2048 B \(25\.0%\)/)).toBeTruthy();
    const signer = await within(card).findByTestId('pinned-signer');
    await waitFor(() => expect(signer.textContent).toContain('abcd1234'));
    expect(signer.textContent).toContain('release key');
    expect(signer.textContent).toContain('Active');
  });

  it('shows an unreadable signer pin as an error beside the assignment, not as none', async () => {
    serve({ current: [assignment()], signer: new GraphQLRequestError('pinned to a signature that cannot be read', 200) });
    renderPanel();
    const card = await screen.findByTestId('current-assignment');
    // The assignment is still there: the pin failing must not take the row with it.
    expect(within(card).getByText('Downloading')).toBeTruthy();
    const alert = await within(card).findByText(/pinned to a signature that cannot be read/);
    expect(alert.getAttribute('role')).toBe('alert');
    expect(within(card).queryByText(/pins no signature/)).toBeNull();
  });

  it('says plainly when the assignment pins no signature', async () => {
    serve({ current: [assignment()], signer: null });
    renderPanel();
    expect(await screen.findByText(/pins no signature/)).toBeTruthy();
  });

  it.each([
    ['SIGNATURE_INVALID', /did not verify/],
    ['UNKNOWN_SIGNER', /does not hold the key/],
    ['VERIFICATION_FAILED', /do not match the artifact digest/],
  ])('explains %s with its stage and the device detail', async (code, explanation) => {
    serve({
      current: [
        assignment(
          {},
          { state: 'FAILED', failure: { stage: 'DOWNLOADED', code, detail: 'device said so', platform: false } },
        ),
      ],
      signer: SIGNER,
    });
    renderPanel();
    const card = await screen.findByTestId('current-assignment');
    expect(within(card).getAllByText(code).length).toBeGreaterThan(0);
    expect(within(card).getByText(explanation)).toBeTruthy();
    expect(within(card).getByText(/DOWNLOADED/)).toBeTruthy();
    expect(within(card).getByText('device said so')).toBeTruthy();
  });

  it('shows an unexplained failure code as reported', async () => {
    serve({
      current: [assignment({}, { state: 'FAILED', failure: { stage: '', code: 'FLASH_WRITE', detail: 'bad sector', platform: false } })],
    });
    renderPanel();
    const card = await screen.findByTestId('current-assignment');
    expect(within(card).getByText('FLASH_WRITE')).toBeTruthy();
    expect(within(card).getByText('bad sector')).toBeTruthy();
  });

  it('shows a read failure as an error, never as no update', async () => {
    serve({ current: new GraphQLRequestError('update-management is down', 200), history: [] });
    renderPanel();
    expect(await screen.findByText('update-management is down')).toBeTruthy();
    expect(screen.queryByText('No update is assigned to this device.')).toBeNull();
  });

  it('shows the empty states', async () => {
    serve({ current: [], history: [] });
    renderPanel();
    expect(await screen.findByText('No update is assigned to this device.')).toBeTruthy();
    expect(await screen.findByText('This device has no update history.')).toBeTruthy();
  });

  it('shows a loading state first', () => {
    gqlMock.mockImplementation(() => new Promise(() => {}));
    renderPanel();
    expect(screen.getAllByText('Loading…').length).toBeGreaterThan(0);
  });
});

describe('history', () => {
  it('lists the device history with the failure code, asking by device', async () => {
    serve({
      current: [],
      history: [
        assignment(
          { token: 'asg-0', current: false },
          { state: 'FAILED', failure: { stage: 'DOWNLOADED', code: 'UNKNOWN_SIGNER', detail: '', platform: false } },
        ),
      ],
    });
    renderPanel();
    expect(await screen.findByText('UNKNOWN_SIGNER')).toBeTruthy();
    const asks = callsTo('query DeviceUpdateAssignments').map((c) => c[2].criteria);
    expect(asks.every((c) => c.deviceToken === 'dev-1')).toBe(true);
    expect(asks.some((c) => c.currentOnly === true)).toBe(true);
  });

  it('reads the pinned signer for current assignments only, never per history row', async () => {
    const rows = Array.from({ length: 10 }, (_, i) => assignment({ token: `asg-h${i}`, current: false }));
    serve({ current: [assignment()], history: rows, signer: SIGNER });
    renderPanel();
    await screen.findByTestId('current-assignment');
    await waitFor(() => expect(screen.getAllByText('Downloading').length).toBe(11));
    expect(callsTo('query AssignmentSigner').map((c) => c[2])).toEqual([{ token: 'asg-1' }]);
  });

  it('asks for the page it shows', async () => {
    serve({ current: [], history: [assignment({ current: false })], historyTotal: 25 });
    renderPanel();
    fireEvent.click(await screen.findByRole('button', { name: /Next/ }));
    await waitFor(() =>
      expect(
        callsTo('query DeviceUpdateAssignments')
          .map((c) => c[2].criteria)
          .some((c) => !c.currentOnly && c.pageNumber === 2),
      ).toBe(true),
    );
  });
});

// Every state against what core/ota offers for it: Cancel (ota.Cancel accepts QUEUED..VERIFIED,
// refuses UPDATING/REBOOTING/UNKNOWN as beyond recall and terminal states as finished) and
// Abandon (ota.Abandon accepts UNKNOWN only).
describe('the actions each state offers', () => {
  it.each([
    ['QUEUED', true, false],
    ['INITIATED', true, false],
    ['DOWNLOADING', true, false],
    ['DOWNLOADED', true, false],
    ['VERIFIED', true, false],
    ['UPDATING', false, false],
    ['REBOOTING', false, false],
    ['UNKNOWN', false, true],
    ['UPDATED', false, false],
    ['FAILED', false, false],
    ['TIMED_OUT', false, false],
    ['CANCELLED', false, false],
  ])('%s: cancel=%s abandon=%s', async (state, cancel, abandon) => {
    serve({ current: [assignment({}, { state })], signer: SIGNER });
    renderPanel();
    await screen.findByTestId('current-assignment');
    expect(screen.queryByRole('button', { name: 'Cancel update' }) !== null).toBe(cancel);
    expect(screen.queryByRole('button', { name: 'Abandon update' }) !== null).toBe(abandon);
  });
});

describe('cancel and abandon', () => {
  it('cancels an in-flight update after confirmation', async () => {
    serve({ current: [assignment()], signer: SIGNER, mutation: { CancelUpdate: assignment() } });
    confirmMock.mockResolvedValue(true);
    renderPanel();
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel update' }));
    await waitFor(() => expect(callsTo('mutation CancelUpdate')).toHaveLength(1));
    expect(callsTo('mutation CancelUpdate')[0][2]).toEqual({ assignmentToken: 'asg-1' });
    expect(confirmMock.mock.calls[0][0].description).toMatch(/device confirms/);
    // The tab re-reads what it shows: the cancel moved the attempt.
    await waitFor(() => expect(callsTo('query DeviceUpdateAssignments').length).toBe(4));
  });

  it('does not cancel when the confirmation is declined', async () => {
    serve({ current: [assignment()], signer: SIGNER });
    confirmMock.mockResolvedValue(false);
    renderPanel();
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel update' }));
    await waitFor(() => expect(confirmMock).toHaveBeenCalled());
    expect(callsTo('mutation CancelUpdate')).toHaveLength(0);
  });

  it('shows the server refusal when a cancel fails', async () => {
    serve({
      current: [assignment()],
      signer: SIGNER,
      mutation: { CancelUpdate: new GraphQLRequestError('update is beyond recall', 200) },
    });
    confirmMock.mockResolvedValue(true);
    renderPanel();
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel update' }));
    await waitFor(() => expect(toastMock).toHaveBeenCalledWith('update is beyond recall', 'error'));
  });

  it('offers no cancel once installation has begun, and says why', async () => {
    serve({ current: [assignment({}, { state: 'UPDATING' })], signer: SIGNER });
    renderPanel();
    await screen.findByTestId('current-assignment');
    expect(screen.queryByRole('button', { name: 'Cancel update' })).toBeNull();
    expect(screen.getByText(/can no longer be cancelled/)).toBeTruthy();
  });

  it('offers no second cancel while one is pending', async () => {
    serve({ current: [assignment({}, { cancelRequested: true })], signer: SIGNER });
    renderPanel();
    await screen.findByTestId('current-assignment');
    expect(screen.queryByRole('button', { name: 'Cancel update' })).toBeNull();
    expect(screen.getByText('Cancel pending')).toBeTruthy();
  });

  it('abandons an UNKNOWN update with a reason and a confirmation naming the consequence', async () => {
    serve({ current: [assignment({}, { state: 'UNKNOWN' })], signer: SIGNER, mutation: { AbandonUnknownUpdate: assignment() } });
    confirmMock.mockResolvedValue(true);
    renderPanel();
    // UNKNOWN offers Abandon, not Cancel: the device may already have installed it.
    expect(screen.queryByRole('button', { name: 'Cancel update' })).toBeNull();
    fireEvent.click(await screen.findByRole('button', { name: 'Abandon update' }));
    fireEvent.click(screen.getByRole('button', { name: 'Abandon' }));
    // No reason: refused here, nothing sent.
    expect(toastMock).toHaveBeenCalledWith('A reason is required to abandon an update.', 'error');
    expect(confirmMock).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText('Reason'), { target: { value: '  device is in the lab  ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Abandon' }));
    await waitFor(() => expect(callsTo('mutation AbandonUnknownUpdate')).toHaveLength(1));
    expect(callsTo('mutation AbandonUnknownUpdate')[0][2]).toEqual({
      assignmentToken: 'asg-1',
      reason: 'device is in the lab',
    });
    await waitFor(() => expect(callsTo('query DeviceUpdateAssignments').length).toBe(4));
    expect(confirmMock.mock.calls[0][0].description).toMatch(/may be on either version/);
  });

  it('hides Cancel and Abandon without write authority', async () => {
    serve({ current: [assignment(), assignment({ token: 'asg-2' }, { state: 'UNKNOWN' })], signer: SIGNER });
    renderPanel({ canWrite: false });
    await screen.findAllByTestId('current-assignment');
    expect(screen.queryByRole('button', { name: 'Cancel update' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Abandon update' })).toBeNull();
    expect(screen.queryByText('Assign an update')).toBeNull();
  });
});

describe('assign', () => {
  const pick = async (label: RegExp) => {
    fireEvent.click(await screen.findByLabelText('Artifact'));
    fireEvent.click(await screen.findByText(label));
  };

  it('assigns the chosen artifact to this device', async () => {
    serve({ current: [], artifacts: [artifact()], mutation: { AssignArtifact: assignment() } });
    renderPanel();
    await pick(/fw 1\.0\.0/);
    fireEvent.click(screen.getByRole('button', { name: 'Assign' }));
    await waitFor(() => expect(callsTo('mutation AssignArtifact')).toHaveLength(1));
    expect(callsTo('mutation AssignArtifact')[0][2]).toEqual({ deviceToken: 'dev-1', artifactToken: 'fw-1' });
    // Only AVAILABLE artifacts are offered.
    expect(callsTo('query Artifacts')[0][2].criteria.state).toBe('AVAILABLE');
    await waitFor(() => expect(callsTo('query DeviceUpdateAssignments').length).toBe(4));
  });

  it('shows the server refusal verbatim', async () => {
    serve({
      current: [],
      artifacts: [artifact()],
      mutation: { AssignArtifact: new GraphQLRequestError('INCOMPATIBLE_PROFILE: profile "x" is not on the list', 200) },
    });
    renderPanel();
    await pick(/fw 1\.0\.0/);
    fireEvent.click(screen.getByRole('button', { name: 'Assign' }));
    expect((await screen.findByTestId('assign-refusal')).textContent).toBe(
      'INCOMPATIBLE_PROFILE: profile "x" is not on the list',
    );
  });

  it('marks an unverified artifact not assignable when the instance refuses them', async () => {
    serve({
      current: [],
      artifacts: [artifact({ token: 'fw-u', version: '2.0.0', trustStatus: 'UNVERIFIED' })],
      allowUnverified: false,
    });
    renderPanel();
    // The info read settles after the first paint: wait for the option to carry the mark.
    await waitFor(() => expect(callsTo('query UpdateManagementInfo')).toHaveLength(1));
    await pick(/fw 2\.0\.0/);
    expect(await screen.findByTestId('assign-unverified-blocked')).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Assign' }) as HTMLButtonElement).disabled).toBe(true);
    expect(callsTo('mutation AssignArtifact')).toHaveLength(0);
  });

  it('allows an unverified artifact when the instance allows them', async () => {
    serve({
      current: [],
      artifacts: [artifact({ token: 'fw-u', version: '2.0.0', trustStatus: 'UNVERIFIED' })],
      allowUnverified: true,
    });
    renderPanel();
    await waitFor(() => expect(callsTo('query UpdateManagementInfo')).toHaveLength(1));
    await pick(/fw 2\.0\.0/);
    expect(screen.queryByTestId('assign-unverified-blocked')).toBeNull();
    expect((screen.getByRole('button', { name: 'Assign' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('is not offered without write authority', async () => {
    serve({ current: [], artifacts: [artifact()] });
    renderPanel({ canWrite: false });
    await screen.findByText('No update is assigned to this device.');
    expect(screen.queryByRole('button', { name: 'Assign' })).toBeNull();
    expect(callsTo('query Artifacts')).toHaveLength(0);
  });
});

describe('a disabled device', () => {
  it('shows the notice and disables Assign, and leaves Cancel available', async () => {
    serve({ current: [assignment()], artifacts: [artifact()], signer: SIGNER });
    renderPanel({ deviceEnabled: false });
    expect((await screen.findByTestId('updates-device-disabled')).textContent).toMatch(/cut off from updates/);
    // Try to pick one anyway: whichever guard stops it, Assign must stay unusable.
    fireEvent.click(screen.getByLabelText('Artifact'));
    const option = screen.queryByText(/fw 1\.0\.0/);
    if (option) fireEvent.click(option);
    expect((screen.getByRole('button', { name: 'Assign' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole('button', { name: 'Cancel update' })).toBeTruthy();
  });

  it('shows no notice for an enabled device', async () => {
    serve({ current: [], artifacts: [artifact()] });
    renderPanel();
    await screen.findByText('No update is assigned to this device.');
    expect(screen.queryByTestId('updates-device-disabled')).toBeNull();
  });
});
