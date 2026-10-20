// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The calculated-metric health block, driven through the real component: only the GraphQL
// transport and the auth claims are faked.
//
// What this file pins:
//   - the three statuses (computing, not computing, no input readings) and the unknown-inputs fallback;
//   - counts are floored at the activation time, in the query sent and in the buckets summed;
//   - a profile with no activation row says so and is not floored;
//   - readings that arrive in separate messages read as healthy (the count is never "samples seen");
//   - the shared-name caveat, the scoped-caller exclusion, and "Why?" opening the formula history.
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
  measuredInputs?: string[];
  inputs?: string[];
  compileError?: string | null;
  shared?: boolean;
  activation?: { version: number; kind: string; activatedAt: string } | null;
  counts: Record<string, { bucketStart: string; count: number }[]>;
};

type Call = { op: string; vars: Record<string, any> };

function install(s: Setup) {
  const calls: Call[] = [];
  gqlMock.mockImplementation((_svc: string, doc: string, vars: Record<string, any>) => {
    const op = /(?:query|mutation) (\w+)/.exec(String(doc))?.[1] ?? '';
    calls.push({ op, vars });
    switch (op) {
      case 'CalculatedMetricPlan':
        return Promise.resolve({
          calculatedMetricPlan: {
            version: 3,
            activeSince: null,
            constantKeys: [],
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
              },
            ],
          },
        });
      case 'DeviceProfileLatestActivation':
        return Promise.resolve({
          deviceProfilesByToken: [{ activations: { entries: s.activation ? [s.activation] : [] } }],
        });
      case 'TestMetricFormula':
        return Promise.resolve({
          testMetricFormula: {
            compileError: s.compileError ?? null,
            value: null,
            skippedReason: 'missing_input',
            inputs: s.inputs ?? ['volts', 'amps'],
            chain: [],
            measuredInputs: s.measuredInputs ?? ['volts', 'amps'],
            constants: [],
            warnings: [],
            estimatedCost: 3,
          },
        });
      case 'HourlyMeasurementCounts':
        return Promise.resolve({ bucketedMeasurements: s.counts[vars.criteria.name] ?? [] });
      case 'MetricDefinitions':
        return Promise.resolve({ metricDefinitions: { results: [] } });
      default:
        return Promise.reject(new Error(`unexpected operation ${op}`));
    }
  });
  return calls;
}

const ACTIVE_3H_AGO = { version: 3, kind: 'PUBLISH', activatedAt: new Date(NOW.getTime() - 3 * HOUR).toISOString() };

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
    install({ activation: ACTIVE_3H_AGO, counts: { volts: hourly(60, 24), amps: hourly(60, 24) } });
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
      activation: ACTIVE_3H_AGO,
      counts: { volts: hourly(60, 10, 4), amps: hourly(60, 10, 4) },
    });
    mount();
    expect(await statusOf()).toBe('no_input_readings');
    const counts = calls.filter((c) => c.op === 'HourlyMeasurementCounts');
    expect(counts.length).toBe(3);
    for (const c of counts) expect(c.vars.criteria.startTime).toBe(ACTIVE_3H_AGO.activatedAt);
    expect(screen.getByTestId('calc-health-basis').textContent).toContain('since version 3 became active');
  });

  it('says so and does not floor when the activation log has no entry for the version', async () => {
    const calls = install({ activation: null, counts: { volts: hourly(60, 24), amps: hourly(60, 24) } });
    mount();
    expect(await statusOf()).toBe('not_computing');
    expect(screen.getByTestId('calc-health-basis').textContent).toContain('No activation time is recorded for version 3');
    const first = calls.find((c) => c.op === 'HourlyMeasurementCounts')!;
    expect(first.vars.criteria.startTime).toBe(new Date(NOW.getTime() - 168 * HOUR).toISOString());
  });

  it('treats a log entry for another version as no evidence', async () => {
    install({ activation: { ...ACTIVE_3H_AGO, version: 2 }, counts: {} });
    mount();
    await statusOf();
    expect(screen.getByTestId('calc-health-basis').textContent).toContain('No activation time is recorded');
  });

  it('reads inputs that arrive in separate messages as healthy', async () => {
    // volts at 1 Hz and amps once an hour, in separate messages. A per-sample denominator would
    // call 3599 of every 3600 samples a skip; counting values against input readings does not.
    install({ counts: { power_kw: hourly(1, 24), volts: hourly(3600, 24), amps: hourly(1, 24) } });
    mount();
    expect(await statusOf()).toBe('computing');
    expect(screen.queryByText('Not computing')).toBeNull();
  });

  it('shows the shared-name caveat only when the plan flags it', async () => {
    install({ shared: true, counts: { power_kw: hourly(1, 24), volts: hourly(1, 24), amps: hourly(1, 24) } });
    mount();
    await statusOf();
    expect(screen.getByTestId('calc-health-shared').textContent).toContain('Another profile also declares a metric named power_kw');
    cleanup();
    install({ shared: false, counts: {} });
    mount();
    await statusOf();
    expect(screen.queryByTestId('calc-health-shared')).toBeNull();
  });

  it('reads Inputs unknown, not a status, when the formula no longer compiles', async () => {
    install({ compileError: 'undeclared reference to volts', counts: { power_kw: hourly(5, 24) } });
    mount();
    expect(await statusOf()).toBe('unknown');
    expect(screen.getByText(/no longer compiles/)).toBeTruthy();
  });

  it('switches the window to 7 days', async () => {
    // Inputs only older than 24 h: No input readings over 24 h, Not computing over 7 d.
    install({ counts: { volts: hourly(60, 24, 30), amps: hourly(60, 24, 30) } });
    mount();
    expect(await statusOf()).toBe('no_input_readings');
    fireEvent.click(screen.getByRole('radio', { name: 'Last 168 h' }));
    await waitFor(() => expect(screen.getByTestId('calc-health-status').getAttribute('data-status')).toBe('not_computing'));
  });

  it('opens the formula history from Why?', async () => {
    install({ counts: { volts: hourly(60, 24), amps: hourly(60, 24) } });
    mount();
    await statusOf();
    fireEvent.click(screen.getByRole('button', { name: 'Why?' }));
    const hist = await screen.findByTestId('formula-history');
    expect(within(hist).getByText('Try it on recent history')).toBeTruthy();
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
