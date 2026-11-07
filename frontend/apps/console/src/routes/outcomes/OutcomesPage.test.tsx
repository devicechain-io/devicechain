// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// EXPERIMENT: the outcomes page. The point it must keep: a city-wide figure that passes does not
// hide the district that fails, and an instance with the feature off says so instead of showing
// an empty or passing board.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

import i18n from '@/i18n/config';

const evaluateOutcome = vi.fn();
vi.mock('@/lib/api/outcomes', () => ({
  evaluateOutcome: (...a: unknown[]) => evaluateOutcome(...a),
}));

import OutcomesPage, { pct, statusVariant } from './OutcomesPage';
import { parseAreas, SEED_OUTCOMES } from './seed';

const area = (over: Record<string, unknown>) => ({
  area: 'cp-district-1',
  status: 'OK',
  goodBuckets: 24,
  badBuckets: 0,
  emptyBuckets: 0,
  totalBuckets: 24,
  compliance: 1,
  coverage: 1,
  budgetRemaining: 1,
  ...over,
});

const evaluation = (over: Record<string, unknown> = {}) => ({
  token: 'x',
  status: 'BREACHED',
  objectiveStatus: 'BREACHED',
  strayBuckets: 0,
  span: { start: '2026-11-06T12:00:00Z', end: '2026-11-07T12:00:00Z', intervalSeconds: 3600, bucketCount: 24 },
  areas: [
    area({ area: 'cp-district-1' }),
    area({ area: 'cp-district-2' }),
    area({ area: 'cp-district-3' }),
    area({ area: 'cp-district-4', status: 'BREACHED', goodBuckets: 12, badBuckets: 12, compliance: 0.5, budgetRemaining: -9 }),
  ],
  equity: {
    evaluatedAreas: 4,
    excludedAreas: 0,
    spreadUndefined: false,
    complete: true,
    worst: 0.5,
    worstArea: 'cp-district-4',
    median: 1,
    spread: 0.5,
    pooled: 0.875,
    breached: true,
  },
  ...over,
});

beforeEach(async () => {
  window.localStorage.clear();
  evaluateOutcome.mockReset();
  await i18n.changeLanguage('en');
});
afterEach(cleanup);

describe('OutcomesPage', () => {
  it('shows the failing district, its status and the spread beside the city-wide figure', async () => {
    evaluateOutcome.mockResolvedValue(evaluation());
    render(<OutcomesPage />);
    const lighting = await screen.findByRole('region', { name: 'Lighting uptime' });
    expect(within(lighting).getByText('cp-district-4 · 50.0%')).toBeTruthy();
    expect(within(lighting).getByText('87.5%')).toBeTruthy();
    expect(within(lighting).getAllByText('Breached').length).toBeGreaterThan(1);
    expect(within(lighting).getByText(/Over the allowed spread/)).toBeTruthy();
    expect(evaluateOutcome).toHaveBeenCalledTimes(SEED_OUTCOMES.length);
    expect(evaluateOutcome.mock.calls[0][0].areas).toEqual(['cp-district-1', 'cp-district-2', 'cp-district-3', 'cp-district-4']);
  });

  it('says the feature is off, and shows no board, when every outcome is refused as unavailable', async () => {
    evaluateOutcome.mockRejectedValue(
      new GraphQLRequestError('off', 200, [{ message: 'off', extensions: { code: 'UNAVAILABLE' } }]),
    );
    render(<OutcomesPage />);
    expect(await screen.findByText(/not enabled on this instance/)).toBeTruthy();
    expect(screen.queryByRole('region')).toBeNull();
  });

  it('shows another failure as a failure, never as a pass or as "off"', async () => {
    evaluateOutcome.mockRejectedValue(new GraphQLRequestError('store down', 200, [{ message: 'store down' }]));
    render(<OutcomesPage />);
    await waitFor(() => expect(screen.getAllByText('store down').length).toBe(SEED_OUTCOMES.length));
    expect(screen.queryByText(/not enabled/)).toBeNull();
    expect(screen.queryByText('On target')).toBeNull();
  });

  it('renders no spread when fewer than two areas reported', async () => {
    evaluateOutcome.mockResolvedValue(
      evaluation({
        status: 'NO_DATA',
        areas: [area({ status: 'NO_DATA', goodBuckets: 0, emptyBuckets: 24, compliance: 0, coverage: 0, budgetRemaining: -19 })],
        equity: {
          evaluatedAreas: 0, excludedAreas: 1, spreadUndefined: true, complete: false,
          worst: null, worstArea: null, median: null, spread: null, pooled: 0, breached: false,
        },
      }),
    );
    render(<OutcomesPage />);
    const air = await screen.findByRole('region', { name: 'Air quality' });
    expect(within(air).getAllByText('No data').length).toBeGreaterThan(0);
    expect(within(air).getAllByText(/Fewer than two areas/).length).toBeGreaterThan(0);
    expect(within(air).queryByText('On target')).toBeNull();
  });
});

describe('Evaluate', () => {
  it('asks again when pressed with the same areas, since the answer depends on the clock', async () => {
    evaluateOutcome.mockResolvedValue(evaluation());
    render(<OutcomesPage />);
    await screen.findByRole('region', { name: 'Lighting uptime' });
    expect(evaluateOutcome).toHaveBeenCalledTimes(SEED_OUTCOMES.length);
    fireEvent.click(screen.getByRole('button', { name: 'Evaluate' }));
    await waitFor(() => expect(evaluateOutcome).toHaveBeenCalledTimes(SEED_OUTCOMES.length * 2));
  });
  it('sends the areas it was given, in order, once edited', async () => {
    evaluateOutcome.mockResolvedValue(evaluation());
    render(<OutcomesPage />);
    await screen.findByRole('region', { name: 'Lighting uptime' });
    fireEvent.change(screen.getByLabelText(/Areas to judge/), { target: { value: 'north, south' } });
    fireEvent.click(screen.getByRole('button', { name: 'Evaluate' }));
    await waitFor(() => expect(evaluateOutcome.mock.calls[evaluateOutcome.mock.calls.length - 1]?.[0].areas).toEqual(['north', 'south']));
  });
});

describe('helpers', () => {
  it('pct renders a missing value as a dash, not 0%', () => {
    expect(pct(null)).toBe('—');
    expect(pct(0.875)).toBe('87.5%');
    expect(pct(0.95, 0)).toBe('95%');
  });
  it('NO_DATA is never painted as a pass', () => {
    expect(statusVariant('NO_DATA')).not.toBe('success');
    expect(statusVariant('OK')).toBe('success');
    expect(statusVariant('BREACHED')).toBe('destructive');
  });
  it('parseAreas splits on commas and spaces and drops empties and repeats', () => {
    expect(parseAreas(' a, b  c,,a ')).toEqual(['a', 'b', 'c']);
    expect(parseAreas('')).toEqual([]);
  });
  it('every seeded outcome is a definition the server accepts', () => {
    for (const s of SEED_OUTCOMES) {
      const d = s.definition;
      expect(d.objective).toBeGreaterThan(0);
      expect(d.objective).toBeLessThan(1);
      expect(86400 % d.intervalSeconds).toBe(0);
      expect(d.windowSeconds / d.intervalSeconds).toBeLessThanOrEqual(10000);
    }
  });
});
