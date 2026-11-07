// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// EXPERIMENT: outcome objects. The three outcomes the citypulse demo scenario is built to
// exercise. Definitions are not stored by the platform in this prototype: they live here, and the
// area list is the one thing a viewer edits (kept in this browser only).
import type { OutcomeDefinitionInput } from '@/lib/api/outcomes';

export const DEFAULT_AREAS = ['cp-district-1', 'cp-district-2', 'cp-district-3', 'cp-district-4'];

export interface SeedOutcome {
  /** Catalog key of the outcome's name. */
  nameKey: string;
  definition: Omit<OutcomeDefinitionInput, 'areas'>;
}

// One hour in five-minute buckets: twelve per area, enough to fill within an hour of a demo run.
const WINDOW = 60 * 60;
const INTERVAL = 5 * 60;

export const SEED_OUTCOMES: SeedOutcome[] = [
  {
    nameKey: 'lighting',
    definition: {
      token: 'lighting-uptime',
      metric: 'lamp_ok',
      aggregation: 'AVG',
      comparator: 'GTE',
      threshold: 0.9,
      windowSeconds: WINDOW,
      intervalSeconds: INTERVAL,
      objective: 0.9,
      maxEquitySpread: 0.1,
    },
  },
  {
    nameKey: 'airQuality',
    definition: {
      token: 'air-quality',
      metric: 'pm25',
      aggregation: 'AVG',
      comparator: 'LTE',
      threshold: 15,
      windowSeconds: WINDOW,
      intervalSeconds: INTERVAL,
      objective: 0.8,
      maxEquitySpread: 0.1,
    },
  },
  {
    nameKey: 'parking',
    definition: {
      token: 'parking-availability',
      metric: 'parking_free_pct',
      aggregation: 'AVG',
      comparator: 'GTE',
      threshold: 10,
      windowSeconds: WINDOW,
      intervalSeconds: INTERVAL,
      objective: 0.9,
      maxEquitySpread: 0.15,
    },
  },
];

/** Parses the area box: comma or whitespace separated tokens, empties and repeats dropped. */
export function parseAreas(text: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of text.split(/[\s,]+/)) {
    const token = raw.trim();
    if (token && !seen.has(token)) {
      seen.add(token);
      out.push(token);
    }
  }
  return out;
}
