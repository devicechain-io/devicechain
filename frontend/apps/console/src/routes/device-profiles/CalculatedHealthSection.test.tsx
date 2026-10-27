// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The calculated-metric health block, driven through the real component: only the GraphQL
// transport and the auth claims are faked.
//
// What this file pins:
//   - the three statuses (computing, not computing, no input readings), Unconfirmed for values
//     under a shared name, and the unknown-inputs fallback;
//   - the inputs are the ACTIVE version's (the plan's), never the draft's;
//   - counts are floored at the version's activation time, in the query sent and in the buckets summed;
//   - a plan with no activation time says so and is not floored;
//   - readings that arrive in separate messages read as healthy (the count is never "samples seen");
//   - the shared-name caveat, the scoped-caller exclusion, and "Why?" opening the formula history;
//   - the window arithmetic: a single value, the hour that straddles the activation, a bucket after now.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { DecodedClaims } from '@devicechain/client';

const { gqlMock, authState } = vi.hoisted(() => ({ gqlMock: vi.fn(), authState: { claims: null as unknown } }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => authState }));

import { CalculatedHealthSection } from './CalculatedHealthSection';
import { countInWindow, statusOf as statusOfCounts } from './calcHealth';

const NOW = new Date('2026-10-20T12:30:00Z');
const HOUR = 3_600_000;

function claims(authorities: string[], scopeAnchors: string[] = []): DecodedClaims {
  return { authorities, scopeAnchors } as unknown as DecodedClaims;
}

/** `perHour` readings in each of `hours` whole hours, the newest ending `endOffsetHours` before the current hour. */
function hourly(perHour: number, hours: number, endOffsetHours = 0) {
  const top = Math.floor(NOW.getTime() / HOUR) * HOUR - endOffsetHours * HOUR;
  return Array.from({ length: hours }, (_, i) => ({
    bucketStart: new Date(top - i * HOUR).toISOString(),
    count: perHour,
  }));
}

type Setup = {
  expression?: string;
  /** null: the metric cannot run in the version. */
  measuredInputs?: string[] | null;
  inputs?: string[] | null;
  shared?: boolean;
  activeSince?: string | null;
  counts: Record<string, { bucketStart: string; count: number }[]>;
  samples?: unknown[];
};

type Call = { op: string; vars: Record<string, any> };

function install(s: Setup) {
  const calls: Call[] = [];
  gqlMock.mockImplementation((_svc: string, doc: string, vars: Record<string, any>) => {
    const op = /(?:query|mutation) (\w+)/.exec(String(doc))?.[1] ?? '';
    calls.push({ op, vars });
    switch (op) {
      case 'CalculatedMetricPlan': {
        const unknown = s.measuredInputs === null;
        return Promise.resolve({
          calculatedMetricPlan: {
            version: 3,
            activeSince: s.activeSince ?? null,
            constantKeys: [],
            measured: [
              { key: 'volts', dataType: 'DOUBLE' },
              { key: 'amps', dataType: 'INT' },
            ],
            calculated: [
              {
                key: 'power_kw',
                unit: 'kW',
                expression: s.expression ?? 'volts * amps / 1000.0',
                dataType: 'DOUBLE',
                minValue: null,
                maxValue: null,
                newInVersion: false,
                changedSinceEarlier: false,
                nameSharedWithOtherProfiles: s.shared ?? false,
                inputs: unknown ? null : (s.inputs ?? ['volts', 'amps']),
                measuredInputs: unknown ? null : (s.measuredInputs ?? ['volts', 'amps']),
                constants: unknown ? null : [],
              },
            ],
          },
        });
      }
      case 'CalculatedEvents':
        return Promise.resolve({
          calculatedEvents: { computed: 0, skipped: [], samples: s.samples ?? [], truncated: false, sampleCount: 0 },
        });
      case 'HourlyMeasurementCounts':
        return Promise.resolve({ bucketedMeasurements: s.counts[vars.criteria.name] ?? [] });
      default:
        return Promise.reject(new Error(`unexpected operation ${op}`));
    }
  });
  return calls;
}

const ACTIVE_3H_AGO = new Date(NOW.getTime() - 3 * HOUR).toISOString();

function mount(activeVersion: number | null = 3) {
  return render(<CalculatedHealthSection profileToken="meter" activeVersion={activeVersion} />);
}

async function statusOf() {
  const badge = await screen.findByTestId('calc-health-status');
  return badge.getAttribute('data-status');
}

beforeEach(() => {
  gqlMock.mockReset();
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(NOW);
  authState.claims = claims(['device:read', 'event:read']);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe('calculated-metric health', () => {
  it('reads Computing when values are stored for the metric', async () => {
    install({ counts: { power_kw: hourly(60, 24), volts: hourly(60, 24), amps: hourly(60, 24) } });
    mount();
    expect(await statusOf()).toBe('computing');
    expect(screen.getByText('Computing')).toBeTruthy();
    expect(screen.getByText('1440 values computed')).toBeTruthy();
  });

  it('reads Not computing when the inputs were stored and no value was', async () => {
    install({ activeSince: ACTIVE_3H_AGO, counts: { volts: hourly(60, 24), amps: hourly(60, 24) } });
    mount();
    expect(await statusOf()).toBe('not_computing');
    expect(screen.getByText('Not computing')).toBeTruthy();
  });

  it('reads No input readings when neither the metric nor its inputs have readings', async () => {
    install({ counts: {} });
    mount();
    expect(await statusOf()).toBe('no_input_readings');
    expect(screen.getByText('No input readings')).toBeTruthy();
  });

  it('floors the counts at the activation time, in the query and in the buckets summed', async () => {
    // Inputs were stored in the 10 hours BEFORE activation (3 h ago) and none since.
    const calls = install({
      activeSince: ACTIVE_3H_AGO,
      counts: { volts: hourly(60, 10, 4), amps: hourly(60, 10, 4) },
    });
    mount();
    expect(await statusOf()).toBe('no_input_readings');
    const counts = calls.filter((c) => c.op === 'HourlyMeasurementCounts');
    expect(counts.length).toBe(3);
    for (const c of counts) expect(c.vars.criteria.startTime).toBe(ACTIVE_3H_AGO);
    expect(screen.getByTestId('calc-health-basis').textContent).toContain('since version 3 became active');
  });

  it('says so and does not floor when the plan carries no activation time', async () => {
    const calls = install({ activeSince: null, counts: { volts: hourly(60, 24), amps: hourly(60, 24) } });
    mount();
    expect(await statusOf()).toBe('not_computing');
    expect(screen.getByTestId('calc-health-basis').textContent).toContain('No activation time was returned for version 3');
    const first = calls.find((c) => c.op === 'HourlyMeasurementCounts')!;
    expect(first.vars.criteria.startTime).toBe(new Date(NOW.getTime() - 168 * HOUR).toISOString());
  });

  it('counts the inputs the active version reads, and never asks the draft', async () => {
    // The plan says the active formula reads volts only (the draft may say otherwise).
    const calls = install({ inputs: ['volts'], measuredInputs: ['volts'], counts: { volts: hourly(60, 24), amps: hourly(60, 24) } });
    mount();
    expect(await statusOf()).toBe('not_computing');
    const names = calls.filter((c) => c.op === 'HourlyMeasurementCounts').map((c) => c.vars.criteria.name).sort();
    expect(names).toEqual(['power_kw', 'volts']);
    expect(calls.some((c) => c.op === 'TestMetricFormula' || c.op === 'MetricDefinitions')).toBe(false);
  });

  it('reads inputs that arrive in separate messages as healthy', async () => {
    // volts at 1 Hz and amps once an hour, in separate messages. A per-sample denominator would
    // call 3599 of every 3600 samples a skip; counting values against input readings does not.
    install({ counts: { power_kw: hourly(1, 24), volts: hourly(3600, 24), amps: hourly(1, 24) } });
    mount();
    expect(await statusOf()).toBe('computing');
    expect(screen.queryByText('Not computing')).toBeNull();
  });

  it('reads Unconfirmed, with the caveat, when values are stored under a name another profile shares', async () => {
    install({ shared: true, counts: { power_kw: hourly(1, 24), volts: hourly(1, 24), amps: hourly(1, 24) } });
    mount();
    expect(await statusOf()).toBe('unconfirmed');
    expect(screen.queryByText('Computing')).toBeNull();
    expect(screen.getByTestId('calc-health-shared').textContent).toContain('Another profile also declares a metric named power_kw');
    cleanup();
    install({ shared: false, counts: { power_kw: hourly(1, 24), volts: hourly(1, 24), amps: hourly(1, 24) } });
    mount();
    expect(await statusOf()).toBe('computing');
    expect(screen.queryByTestId('calc-health-shared')).toBeNull();
    cleanup();
    // No values under a shared name is still Not computing: no profile produced any.
    install({ shared: true, counts: { volts: hourly(1, 24), amps: hourly(1, 24) } });
    mount();
    expect(await statusOf()).toBe('not_computing');
  });

  it('reads Inputs unknown, not a status, when the metric cannot run in the active version', async () => {
    install({ measuredInputs: null, counts: { power_kw: hourly(5, 24) } });
    mount();
    expect(await statusOf()).toBe('unknown');
    expect(screen.getByText(/cannot run on this release/)).toBeTruthy();
  });

  it('switches the window to 7 days', async () => {
    // Inputs only older than 24 h: No input readings over 24 h, Not computing over 7 d.
    install({ counts: { volts: hourly(60, 24, 30), amps: hourly(60, 24, 30) } });
    mount();
    expect(await statusOf()).toBe('no_input_readings');
    fireEvent.click(screen.getByRole('radio', { name: 'Last 168 h' }));
    await waitFor(() => expect(screen.getByTestId('calc-health-status').getAttribute('data-status')).toBe('not_computing'));
  });

  it('opens the formula history from Why?, typed from the active version', async () => {
    const calls = install({ counts: { volts: hourly(60, 24), amps: hourly(60, 24) } });
    mount();
    await statusOf();
    fireEvent.click(screen.getByRole('button', { name: 'Why?' }));
    const hist = await screen.findByTestId('formula-history');
    expect(within(hist).getByText('Try it on recent history')).toBeTruthy();
    fireEvent.change(within(hist).getByLabelText('Device token'), { target: { value: 'dev-1' } });
    fireEvent.click(within(hist).getByRole('button', { name: 'Run on history' }));
    await waitFor(() => expect(calls.some((c) => c.op === 'CalculatedEvents')).toBe(true));
    const run = calls.find((c) => c.op === 'CalculatedEvents')!;
    expect(run.vars.inputs).toEqual([
      { key: 'volts', dataType: 'DOUBLE' },
      { key: 'amps', dataType: 'INT' },
    ]);
    expect(calls.some((c) => c.op === 'MetricDefinitions')).toBe(false);
  });

  it('explains separate messages under Not computing when the formula reads two reported inputs', async () => {
    install({ counts: { volts: hourly(60, 24), amps: hourly(60, 24) } });
    mount();
    expect(await statusOf()).toBe('not_computing');
    const hint = screen.getByTestId('calc-health-separate');
    expect(hint.textContent).toContain('A formula reads one message at one instant.');
    expect(within(hint).getByRole('link').getAttribute('href')).toContain('calculated-metrics#inputs-that-arrive-separately');
  });

  it('shows no separate-messages hint when the metric is computing, or reads fewer than two reported inputs', async () => {
    // Control: the same two-input fixture, but values are stored, so the status is Computing.
    install({ counts: { power_kw: hourly(60, 24), volts: hourly(60, 24), amps: hourly(60, 24) } });
    mount();
    expect(await statusOf()).toBe('computing');
    expect(screen.queryByTestId('calc-health-separate')).toBeNull();
    cleanup();
    // One reported input cannot be split across messages.
    install({ inputs: ['volts'], measuredInputs: ['volts'], counts: { volts: hourly(60, 24) } });
    mount();
    expect(await statusOf()).toBe('not_computing');
    expect(screen.queryByTestId('calc-health-separate')).toBeNull();
    cleanup();
    // Not computing, nothing stored for the inputs: Not computing is not the status, so no hint.
    install({ counts: {} });
    mount();
    expect(await statusOf()).toBe('no_input_readings');
    expect(screen.queryByTestId('calc-health-separate')).toBeNull();
  });

  it('names the inputs a skipped sample lacked in Why?, and none for a sample that held them all', async () => {
    const samples = [
      { occurredTime: '2026-10-20T12:00:00Z', eventId: 'a', inputs: [{ key: 'volts', value: '230' }], value: null, skippedReason: 'missing_input' },
      { occurredTime: '2026-10-20T12:01:00Z', eventId: 'b', inputs: [{ key: 'volts', value: '230' }, { key: 'amps', value: '4' }], value: '0.92', skippedReason: null },
      { occurredTime: '2026-10-20T12:02:00Z', eventId: 'c', inputs: [{ key: 'volts', value: '230' }, { key: 'amps', value: '4' }], value: null, skippedReason: 'invalid_input' },
      // A skip that is not missing_input keeps today's rendering even when an input is absent (a has()-guarded one).
      { occurredTime: '2026-10-20T12:03:00Z', eventId: 'd', inputs: [{ key: 'volts', value: '230' }], value: null, skippedReason: 'out_of_range' },
    ];
    install({ counts: { volts: hourly(60, 24), amps: hourly(60, 24) }, samples });
    mount();
    await statusOf();
    fireEvent.click(screen.getByRole('button', { name: 'Why?' }));
    const hist = await screen.findByTestId('formula-history');
    fireEvent.change(within(hist).getByLabelText('Device token'), { target: { value: 'dev-1' } });
    fireEvent.click(within(hist).getByRole('button', { name: 'Run on history' }));
    const lacked = await screen.findAllByTestId('formula-history-lacked');
    // Only the missing_input sample, and only for the input it did not carry.
    expect(lacked.map((e) => e.textContent)).toEqual(['lacked: amps']);
  });

  it('renders nothing for a customer-scoped caller, without the read authorities, or with no active version', () => {
    const calls = install({ counts: {} });
    authState.claims = claims(['device:read', 'event:read'], ['customer-1']);
    const a = mount();
    expect(a.container.innerHTML).toBe('');
    cleanup();
    authState.claims = claims(['device:read']);
    const b = mount();
    expect(b.container.innerHTML).toBe('');
    cleanup();
    authState.claims = claims(['device:read', 'event:read']);
    const c = mount(null);
    expect(c.container.innerHTML).toBe('');
    expect(calls).toEqual([]);
  });

  it('renders nothing when the active version calculates no metric', async () => {
    gqlMock.mockImplementation((_s: string, doc: string) =>
      /CalculatedMetricPlan/.test(String(doc))
        ? Promise.resolve({ calculatedMetricPlan: { version: 3, activeSince: null, constantKeys: [], calculated: [] } })
        : Promise.reject(new Error('unexpected')),
    );
    const { container } = mount();
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());
    await waitFor(() => expect(container.querySelector('[data-testid="calc-health-section"]')).toBeNull());
  });
});

describe('calculated-metric health counting', () => {
  const at = (iso: string) => ({ bucketStart: iso, count: 5 });

  it('one stored value is enough to read Computing', () => {
    expect(statusOfCounts({ computed: 1, inputs: [], inputTotal: 0 }, false)).toBe('computing');
    expect(statusOfCounts({ computed: 1, inputs: [], inputTotal: 0 }, true)).toBe('unconfirmed');
  });

  it('counts the hour the version became active in, and no bucket after now', () => {
    const floor = new Date('2026-10-20T09:30:00Z');
    // 09:00 straddles the floor and counts; 08:00 ends before it; 13:00 is after now (12:30).
    const buckets = [at('2026-10-20T08:00:00Z'), at('2026-10-20T09:00:00Z'), at('2026-10-20T12:00:00Z'), at('2026-10-20T13:00:00Z')];
    expect(countInWindow(buckets, 24, NOW, floor)).toBe(10);
    expect(countInWindow(buckets, 24, NOW, null)).toBe(15);
  });
});
