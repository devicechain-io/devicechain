// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs and the real API modules; only the GraphQL
// transport, the auth claims and the two report channels are faked.
//
// What this file pins, on the REQUEST THAT LEAVES and on what the screen says:
//   - the dialog opens on the active version with the metrics that are new in it selected, a metric
//     whose formula changed flagged but not selected, and a range of 30 days ending where the
//     version became active;
//   - "Try one device first" sends exactly that one device; the plain start sends none;
//   - UNAVAILABLE from the server turns the start controls off and says why, rather than hiding them;
//   - a customer-scoped caller sees nothing of this, a caller without device:write sees the jobs but
//     no start or cancel, and a profile with nothing calculated gets a disabled button with the reason;
//   - the jobs panel shows each counter, names the skips (ambiguous included) and cancels only a job
//     that is still active.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { DecodedClaims } from '@devicechain/client';

const { gqlMock, toastMock, authState } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  toastMock: vi.fn(),
  authState: { claims: null as unknown },
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => authState }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => () => Promise.resolve(true) }));

import { GraphQLRequestError } from '@devicechain/client';
import { BackfillDrawer } from './BackfillDrawer';
import { BackfillJobs } from './BackfillJobs';
import { BackfillSection } from './BackfillSection';

const DAY = 86_400_000;
const ACTIVE_SINCE = '2026-10-10T12:00:00Z';

function claims(authorities: string[], scopeAnchors: string[] = []): DecodedClaims {
  return { authorities, scopeAnchors } as unknown as DecodedClaims;
}

const plan = {
  calculatedMetricPlan: {
    version: 3,
    activeSince: ACTIVE_SINCE,
    calculated: [
      { key: 'power_kw', unit: 'kW', newInVersion: true, changedSinceEarlier: false },
      { key: 'energy_kwh', unit: 'kWh', newInVersion: false, changedSinceEarlier: true },
    ],
  },
};

function job(over: Record<string, unknown> = {}) {
  return {
    token: 'bf-1',
    status: 'RUNNING',
    profileToken: 'meter',
    profileVersion: 3,
    metricKeys: ['power_kw'],
    warnings: [],
    from: '2026-09-10T12:00:00Z',
    to: ACTIVE_SINCE,
    devicesTotal: 40,
    devicesDone: 10,
    progress: 0.25,
    samples: 5_000_000_000,
    computed: 4_000,
    written: 3_000,
    alreadyPresent: 1_000,
    windowsCompressed: 0,
    windowsBeyondRetention: 0,
    windowsRefreshed: 0,
    skipped: [],
    storedInputPrecision: '8 decimal places',
    failure: null,
    createdAt: '2026-10-11T00:00:00Z',
    createdBy: 'ops@example.com',
    startedAt: '2026-10-11T00:00:01Z',
    finishedAt: null,
    cancelRequestedAt: null,
    ...over,
  };
}

interface Install {
  jobs?: unknown[];
  start?: () => Promise<unknown>;
  plan?: unknown;
}

// Answers by operation name, so a changed document is noticed by name rather than by position.
function install(opts: Install = {}) {
  const calls: { op: string; vars: Record<string, unknown> }[] = [];
  gqlMock.mockImplementation((_svc: string, doc: string, vars: Record<string, unknown>) => {
    const d = String(doc);
    const op = /(?:query|mutation) (\w+)/.exec(d)?.[1] ?? '';
    calls.push({ op, vars });
    switch (op) {
      case 'CalculatedMetricPlan':
        return Promise.resolve(opts.plan ?? plan);
      case 'DeviceProfileVersions':
        return Promise.resolve({
          deviceProfileVersions: [
            { version: 3, label: null, publishedAt: ACTIVE_SINCE, publishedBy: 'a' },
            { version: 2, label: null, publishedAt: '2026-09-01T00:00:00Z', publishedBy: 'a' },
          ],
        });
      case 'CalculatedMetricBackfills':
        return Promise.resolve({ calculatedMetricBackfills: { nodes: opts.jobs ?? [], nextCursor: null } });
      case 'StartCalculatedMetricBackfill':
        return opts.start ? opts.start() : Promise.resolve({ startCalculatedMetricBackfill: { token: 'bf-new', status: 'QUEUED' } });
      case 'CancelCalculatedMetricBackfill':
        return Promise.resolve({ cancelCalculatedMetricBackfill: { token: vars.token, status: 'RUNNING', cancelRequestedAt: 'x' } });
      default:
        return Promise.reject(new Error(`unexpected operation ${op}`));
    }
  });
  return calls;
}

beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  authState.claims = claims(['device:write', 'device:read', 'event:read']);
});
afterEach(cleanup);

function drawer(over: Partial<React.ComponentProps<typeof BackfillDrawer>> = {}) {
  const props = {
    profileToken: 'meter',
    activeVersion: 3,
    open: true,
    onOpenChange: vi.fn(),
    unavailable: null,
    onUnavailable: vi.fn(),
    onStarted: vi.fn(),
    ...over,
  };
  render(<BackfillDrawer {...props} />);
  return props;
}

const startedWith = (calls: { op: string; vars: Record<string, unknown> }[]) =>
  calls.find((c) => c.op === 'StartCalculatedMetricBackfill')?.vars.input as Record<string, unknown> | undefined;

describe('the backfill dialog', () => {
  it('opens on the active version with the new metrics selected and a 30-day range ending at activation', async () => {
    install();
    drawer();

    const power = await screen.findByRole('checkbox', { name: 'power_kw' });
    expect(power).toHaveProperty("ariaChecked", "true");
    // A metric whose formula changed is offered, flagged, and NOT selected.
    expect(screen.getByRole('checkbox', { name: 'energy_kwh' })).toHaveProperty("ariaChecked", "false");
    expect(screen.getByTestId('bf-changed-energy_kwh')).toBeTruthy();
    expect(screen.queryByTestId('bf-changed-power_kw')).toBeNull();

    await waitFor(() => expect((screen.getByLabelText('To') as HTMLInputElement).value).not.toBe(''));
    const to = new Date((screen.getByLabelText('To') as HTMLInputElement).value).getTime();
    const from = new Date((screen.getByLabelText('From') as HTMLInputElement).value).getTime();
    // The inputs hold minutes, so the end is the activation instant to the minute.
    expect(Math.abs(to - new Date(ACTIVE_SINCE).getTime())).toBeLessThan(60_000);
    expect(to - from).toBe(30 * DAY);
    // The longest range is stated up front.
    expect(screen.getByText(/At most 90 days per backfill/)).toBeTruthy();
  });

  it('refuses a range longer than 90 days before asking the server', async () => {
    install();
    drawer();
    await screen.findByRole('checkbox', { name: 'power_kw' });
    await waitFor(() => expect((screen.getByLabelText('To') as HTMLInputElement).value).not.toBe(''));
    const to = new Date((screen.getByLabelText('To') as HTMLInputElement).value).getTime();
    const far = new Date(to - 100 * DAY);
    const p = (n: number) => String(n).padStart(2, '0');
    fireEvent.change(screen.getByLabelText('From'), {
      target: { value: `${far.getFullYear()}-${p(far.getMonth() + 1)}-${p(far.getDate())}T${p(far.getHours())}:${p(far.getMinutes())}` },
    });
    expect(await screen.findByText(/covers at most 90 days/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Backfill every device' })).toHaveProperty("disabled", true);
  });

  it('starts every device of the profile with the selected keys and no device list', async () => {
    const calls = install();
    const props = drawer();
    await screen.findByRole('checkbox', { name: 'power_kw' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Backfill every device' })).toHaveProperty("disabled", false));

    fireEvent.click(screen.getByRole('button', { name: 'Backfill every device' }));

    await waitFor(() => expect(props.onStarted).toHaveBeenCalledWith('bf-new'));
    const input = startedWith(calls)!;
    expect(input.profileToken).toBe('meter');
    expect(input.version).toBe(3);
    expect(input.metricKeys).toEqual(['power_kw']);
    expect(input.deviceTokens).toBeUndefined();
    const span = new Date(input.to as string).getTime() - new Date(input.from as string).getTime();
    expect(span).toBe(30 * DAY);
    expect(props.onOpenChange).toHaveBeenCalledWith(false);
  });

  it('tries one device first: exactly that device, and only once a token is typed', async () => {
    const calls = install();
    drawer();
    await screen.findByRole('checkbox', { name: 'power_kw' });
    const tryOne = screen.getByRole('button', { name: 'Try one device first' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Backfill every device' })).toHaveProperty("disabled", false));
    expect(tryOne).toHaveProperty("disabled", true);

    fireEvent.change(screen.getByLabelText('Device token'), { target: { value: ' meter-17 ' } });
    expect(tryOne).toHaveProperty("disabled", false);
    fireEvent.click(tryOne);

    await waitFor(() => expect(startedWith(calls)).toBeDefined());
    expect(startedWith(calls)!.deviceTokens).toEqual(['meter-17']);
  });

  it('sends the named devices when a list is typed', async () => {
    const calls = install();
    drawer();
    await screen.findByRole('checkbox', { name: 'power_kw' });
    fireEvent.change(screen.getByLabelText('Devices (optional)'), { target: { value: 'a, b\nc a' } });
    const go = await screen.findByRole('button', { name: 'Backfill 3 devices' });
    await waitFor(() => expect(go).toHaveProperty("disabled", false));
    fireEvent.click(go);
    await waitFor(() => expect(startedWith(calls)).toBeDefined());
    expect(startedWith(calls)!.deviceTokens).toEqual(['a', 'b', 'c']);
  });

  it('turns the start controls off and says why when the server answers UNAVAILABLE', async () => {
    install({
      start: () =>
        Promise.reject(
          new GraphQLRequestError('calculated-metric backfill is unavailable: the backfill worker is not running', 200, [
            { message: 'unavailable', extensions: { code: 'UNAVAILABLE' } },
          ]),
        ),
    });
    const onUnavailable = vi.fn();
    drawer({ onUnavailable });
    await screen.findByRole('checkbox', { name: 'power_kw' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Backfill every device' })).toHaveProperty("disabled", false));
    fireEvent.click(screen.getByRole('button', { name: 'Backfill every device' }));
    await waitFor(() => expect(onUnavailable).toHaveBeenCalled());
    expect(onUnavailable.mock.calls[0][0]).toMatch(/worker is not running/);
  });

  it('shows the reason and keeps both start buttons disabled while unavailable', async () => {
    install();
    drawer({ unavailable: 'the backfill worker is not running' });
    await screen.findByRole('checkbox', { name: 'power_kw' });
    expect(screen.getByText(/Backfill is not available right now: the backfill worker is not running/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Backfill every device' })).toHaveProperty("disabled", true);
    fireEvent.change(screen.getByLabelText('Device token'), { target: { value: 'meter-1' } });
    expect(screen.getByRole('button', { name: 'Try one device first' })).toHaveProperty("disabled", true);
  });

  it('shows a refusal such as a conflicting active job and stays open', async () => {
    install({
      start: () => Promise.reject(new GraphQLRequestError('tenant already has an active backfill bf-0', 200, [{ message: 'x', extensions: { code: 'CONFLICT' } }])),
    });
    const props = drawer();
    await screen.findByRole('checkbox', { name: 'power_kw' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Backfill every device' })).toHaveProperty("disabled", false));
    fireEvent.click(screen.getByRole('button', { name: 'Backfill every device' }));
    expect(await screen.findByText(/already has an active backfill bf-0/)).toBeTruthy();
    expect(props.onOpenChange).not.toHaveBeenCalledWith(false);
    expect(props.onUnavailable).not.toHaveBeenCalled();
  });
});

describe('the backfill section', () => {
  it('shows nothing to a customer-scoped caller', async () => {
    authState.claims = claims(['device:write', 'device:read', 'event:read'], ['customer:acme']);
    const calls = install();
    const { container } = render(<BackfillSection profileToken="meter" activeVersion={3} request={null} onRequestHandled={() => {}} />);
    expect(container.innerHTML).toBe("");
    // And it did not even ask.
    expect(calls).toEqual([]);
  });

  it('shows the jobs but no start or cancel to a caller without device:write', async () => {
    authState.claims = claims(['device:read', 'event:read']);
    install({ jobs: [job()] });
    render(<BackfillSection profileToken="meter" activeVersion={3} request={null} onRequestHandled={() => {}} />);
    expect(await screen.findByTestId('bf-job-bf-1')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Backfill history' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull();
  });

  it('disables the button, with the reason, when the version calculates nothing', async () => {
    install({ plan: { calculatedMetricPlan: { version: 3, activeSince: ACTIVE_SINCE, calculated: [] } } });
    render(<BackfillSection profileToken="meter" activeVersion={3} request={null} onRequestHandled={() => {}} />);
    expect(await screen.findByTestId('backfill-disabled-reason')).toHaveProperty('textContent', expect.stringContaining('no calculated metrics'));
    expect(screen.getByRole('button', { name: 'Backfill history' })).toHaveProperty("disabled", true);
  });

  it('disables the button, with the reason, before anything is published', async () => {
    install();
    render(<BackfillSection profileToken="meter" activeVersion={null} request={null} onRequestHandled={() => {}} />);
    expect(await screen.findByTestId('backfill-disabled-reason')).toHaveProperty('textContent', expect.stringContaining('Publish a version first'));
    expect(screen.getByRole('button', { name: 'Backfill history' })).toHaveProperty("disabled", true);
  });

  it('opens the dialog on the requested version, once', async () => {
    const calls = install();
    const handled = vi.fn();
    render(<BackfillSection profileToken="meter" activeVersion={3} request={2} onRequestHandled={handled} />);
    await waitFor(() => expect(handled).toHaveBeenCalledTimes(1));
    await screen.findByRole('checkbox', { name: 'power_kw' });
    expect(calls.some((c) => c.op === 'CalculatedMetricPlan' && c.vars.version === 2)).toBe(true);
  });

  it('remembers UNAVAILABLE after a failed start and disables the button with the reason', async () => {
    install({
      start: () =>
        Promise.reject(new GraphQLRequestError('worker off', 200, [{ message: 'x', extensions: { code: 'UNAVAILABLE' } }])),
    });
    render(<BackfillSection profileToken="meter" activeVersion={3} request={null} onRequestHandled={() => {}} />);
    const open = await screen.findByRole('button', { name: 'Backfill history' });
    await waitFor(() => expect(open).toHaveProperty("disabled", false));
    fireEvent.click(open);
    await screen.findByRole('checkbox', { name: 'power_kw' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Backfill every device' })).toHaveProperty("disabled", false));
    fireEvent.click(screen.getByRole('button', { name: 'Backfill every device' }));
    await waitFor(() => expect(screen.getAllByText(/Backfill is not available right now: worker off/).length).toBeGreaterThan(0));
    expect(screen.getByRole('button', { name: 'Backfill every device' })).toHaveProperty("disabled", true);
  });
});

describe('the backfill jobs panel', () => {
  it('renders a running job with its counters, progress and skips by reason', async () => {
    install({
      jobs: [
        job({
          skipped: [
            { reason: 'ambiguous', count: 12 },
            { reason: 'missing_input', count: 3 },
          ],
          windowsCompressed: 7,
          windowsBeyondRetention: 2,
          warnings: ['energy_kwh: its formula differs from an earlier version'],
        }),
      ],
    });
    render(<BackfillJobs profileToken="meter" canCancel reloadKey={0} />);
    const row = await screen.findByTestId('bf-job-bf-1');
    expect(within(row).getByText('Running')).toBeTruthy();
    expect(within(row).getByText('25%')).toBeTruthy();
    expect(within(row).getByText('10 of 40 devices')).toBeTruthy();
    expect(within(row).getByText(/3,000 values written/)).toBeTruthy();
    // A counter past 2^31 is shown whole, not wrapped.
    expect(within(row).getByText(/5,000,000,000 readings looked at, 1,000 already had a value/)).toBeTruthy();
    expect(within(row).getByText(/12 not computed: the reading holds an input twice/)).toBeTruthy();
    expect(within(row).getByText(/3 not computed: an input or device constant is missing/)).toBeTruthy();
    expect(within(row).getByText('7 stretches of compressed history skipped')).toBeTruthy();
    expect(within(row).getByText('2 stretches skipped because retention may remove them')).toBeTruthy();
    expect(within(row).getByText(/its formula differs from an earlier version/)).toBeTruthy();
  });

  it('cancels an active job and offers no cancel on a finished one', async () => {
    const calls = install({
      jobs: [job(), job({ token: 'bf-0', status: 'SUCCEEDED', progress: 1, devicesDone: 40, finishedAt: '2026-10-11T01:00:00Z' })],
    });
    render(<BackfillJobs profileToken="meter" canCancel reloadKey={0} />);
    await screen.findByTestId('bf-job-bf-1');
    expect(within(screen.getByTestId('bf-job-bf-0')).queryByRole('button', { name: 'Cancel' })).toBeNull();

    fireEvent.click(within(screen.getByTestId('bf-job-bf-1')).getByRole('button', { name: 'Cancel' }));

    await waitFor(() => expect(calls.some((c) => c.op === 'CancelCalculatedMetricBackfill')).toBe(true));
    expect(calls.find((c) => c.op === 'CancelCalculatedMetricBackfill')!.vars).toEqual({ token: 'bf-1' });
  });

  it('shows a requested cancel as cancelling and does not offer it twice', async () => {
    install({ jobs: [job({ cancelRequestedAt: '2026-10-11T00:05:00Z' })] });
    render(<BackfillJobs profileToken="meter" canCancel reloadKey={0} />);
    const row = await screen.findByTestId('bf-job-bf-1');
    expect(within(row).getByText('Cancelling…')).toBeTruthy();
    expect(within(row).getByRole('button', { name: 'Cancel' })).toHaveProperty("disabled", true);
  });

  it('says why a failed job failed', async () => {
    install({ jobs: [job({ status: 'FAILED', failure: 'device-management could not be reached' })] });
    render(<BackfillJobs profileToken="meter" canCancel reloadKey={0} />);
    const row = await screen.findByTestId('bf-job-bf-1');
    expect(within(row).getByText('Failed')).toBeTruthy();
    expect(within(row).getByText('device-management could not be reached')).toBeTruthy();
  });

  it('says so when there are no jobs', async () => {
    install({ jobs: [] });
    render(<BackfillJobs profileToken="meter" canCancel reloadKey={0} />);
    expect(await screen.findByText('No backfills yet.')).toBeTruthy();
  });

  it('keeps re-reading while a job is active', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const calls = install({ jobs: [job()] });
      render(<BackfillJobs profileToken="meter" canCancel reloadKey={0} />);
      await screen.findByTestId('bf-job-bf-1');
      const before = calls.filter((c) => c.op === 'CalculatedMetricBackfills').length;
      await vi.advanceTimersByTimeAsync(5100);
      await waitFor(() => expect(calls.filter((c) => c.op === 'CalculatedMetricBackfills').length).toBeGreaterThan(before));
    } finally {
      vi.useRealTimers();
    }
  });
});
