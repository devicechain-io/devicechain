// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { highestSeverity, severityColor, severityLabel, severityRank } from './severity';

describe('severityRank', () => {
  it('orders severities most-severe-first', () => {
    const order = ['CRITICAL', 'MAJOR', 'MINOR', 'WARNING', 'INDETERMINATE'];
    const ranks = order.map(severityRank);
    // Strictly increasing rank = increasing (worsening→milder) order.
    expect(ranks).toEqual([...ranks].sort((a, b) => a - b));
    expect(new Set(ranks).size).toBe(ranks.length); // all distinct
    expect(severityRank('CRITICAL')).toBeLessThan(severityRank('MAJOR'));
    expect(severityRank('WARNING')).toBeLessThan(severityRank('INDETERMINATE'));
  });

  it('sorts an unknown severity last', () => {
    expect(severityRank('WHO-KNOWS')).toBeGreaterThan(severityRank('INDETERMINATE'));
  });
});

describe('severityColor', () => {
  it('maps a known severity to its fixed color', () => {
    expect(severityColor('CRITICAL')).toBe('#dc2626');
    expect(severityColor('MAJOR')).toBe('#ea580c');
  });

  it('falls back to a muted slate for an unknown severity', () => {
    expect(severityColor('MYSTERY')).toBe('#64748b');
    expect(severityColor('')).toBe('#64748b');
  });
});

describe('severityLabel', () => {
  it('title-cases a severity token', () => {
    expect(severityLabel('CRITICAL')).toBe('Critical');
    expect(severityLabel('INDETERMINATE')).toBe('Indeterminate');
  });

  it('returns empty for an empty severity', () => {
    expect(severityLabel('')).toBe('');
  });
});

describe('highestSeverity', () => {
  it('picks the most severe severity present', () => {
    expect(highestSeverity(['WARNING', 'CRITICAL', 'MINOR'])).toBe('CRITICAL');
    expect(highestSeverity(['INDETERMINATE', 'MAJOR', 'MINOR'])).toBe('MAJOR');
  });

  it('returns undefined for an empty list', () => {
    expect(highestSeverity([])).toBeUndefined();
  });
});
