// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { MeasurementSample } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import {
  FRESHNESS_STRINGS,
  ageSeconds,
  formatAge,
  freshnessStrings,
  isStale,
  newestSampleTime,
  resolveStaleAfterSeconds,
} from './freshness';

const sample = (name: string, occurredTime: string | null): MeasurementSample => ({
  id: name,
  deviceToken: 'd',
  eventType: 0,
  occurredTime,
  name,
  value: 1,
  classifier: null,
});

describe('resolveStaleAfterSeconds', () => {
  it('uses the widget option when it is a positive number', () => {
    expect(resolveStaleAfterSeconds({ staleAfterSeconds: 90 })).toBe(90);
  });

  it('is undefined with no option: there is no default threshold', () => {
    expect(resolveStaleAfterSeconds({})).toBeUndefined();
    expect(resolveStaleAfterSeconds(undefined)).toBeUndefined();
  });

  it('treats a non-positive or non-numeric option as no threshold', () => {
    expect(resolveStaleAfterSeconds({ staleAfterSeconds: 0 })).toBeUndefined();
    expect(resolveStaleAfterSeconds({ staleAfterSeconds: -5 })).toBeUndefined();
    expect(resolveStaleAfterSeconds({ staleAfterSeconds: '60' })).toBeUndefined();
  });
});

describe('isStale', () => {
  it('is never stale without a threshold, however old the sample', () => {
    expect(isStale(10 * 365 * 86400, undefined)).toBe(false);
  });

  it('is stale only past the threshold', () => {
    expect(isStale(60, 60)).toBe(false);
    expect(isStale(61, 60)).toBe(true);
  });
});

describe('newestSampleTime', () => {
  const latest = {
    a: sample('a', '2026-01-01T00:00:10Z'),
    b: sample('b', '2026-01-01T00:00:50Z'),
    c: sample('c', null),
  };
  it('picks the newest across all measurements, skipping ones with no time', () => {
    expect(newestSampleTime(latest)).toBe(Date.parse('2026-01-01T00:00:50Z'));
  });
  it('restricts to the named measurements', () => {
    expect(newestSampleTime(latest, ['a'])).toBe(Date.parse('2026-01-01T00:00:10Z'));
  });
  it('is undefined when nothing carries a time', () => {
    expect(newestSampleTime({ c: sample('c', null) })).toBeUndefined();
    expect(newestSampleTime({})).toBeUndefined();
  });
});

describe('ageSeconds / formatAge', () => {
  it('never reports a negative age for a clock ahead of the viewer', () => {
    expect(ageSeconds(10_000, 5_000)).toBe(0);
  });

  it('steps up units and localises', () => {
    expect(formatAge(12, 'en')).toMatch(/12/);
    expect(formatAge(12, 'en')).toMatch(/ago/);
    expect(formatAge(125, 'en')).toMatch(/2/);
    expect(formatAge(125, 'en')).toMatch(/min/);
    expect(formatAge(12, 'es')).toMatch(/hace/);
    expect(formatAge(0, 'en')).toBe('now');
  });

  it('survives an unparseable language tag', () => {
    expect(() => formatAge(5, 'not a tag!!')).not.toThrow();
  });
});

describe('freshnessStrings', () => {
  it('ships en and es with the same keys, and falls back to en', () => {
    expect(Object.keys(FRESHNESS_STRINGS.es).sort()).toEqual(Object.keys(FRESHNESS_STRINGS.en).sort());
    expect(freshnessStrings('es-MX').stale).toBe(FRESHNESS_STRINGS.es.stale);
    expect(freshnessStrings('fr').stale).toBe(FRESHNESS_STRINGS.en.stale);
  });
});
