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
  // The colours come from the SDK's severity tokens (one palette with the threshold bands), so
  // the test hands in a distinct colour per level and asserts the VOCABULARY mapping.
  const colors = { warning: 'W', minor: 'MI', major: 'MA', critical: 'C' };

  it('maps each alarm severity onto its token level colour', () => {
    expect(severityColor(colors, 'CRITICAL')).toBe('C');
    expect(severityColor(colors, 'MAJOR')).toBe('MA');
    expect(severityColor(colors, 'MINOR')).toBe('MI');
    expect(severityColor(colors, 'WARNING')).toBe('W');
  });

  it('gives INDETERMINATE and an unknown severity the muted foreground, not a hue', () => {
    const muted = severityColor(colors, 'INDETERMINATE');
    expect(muted).toBe('hsl(var(--muted-foreground))');
    expect(severityColor(colors, 'MYSTERY')).toBe(muted);
    expect(severityColor(colors, '')).toBe(muted);
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
