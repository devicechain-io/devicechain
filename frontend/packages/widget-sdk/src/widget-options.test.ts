// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import type { MeasurementSample } from '@devicechain/dashboards';

import { pickSample } from './widget-options';

function sample(name: string, value: number | null, textValue?: string): MeasurementSample {
  return { id: name, deviceToken: 'd1', eventType: 0, occurredTime: 't', name, value, textValue, classifier: null };
}

describe('pickSample', () => {
  it('skips a text reading that arrived first when no measurement is pinned', () => {
    const latest = { mode: sample('mode', null, 'eco'), temp: sample('temp', 21.5) };
    expect(pickSample(latest, undefined)?.name).toBe('temp');
  });

  it('takes the first numeric reading in arrival order', () => {
    const latest = { a: sample('a', 1), mode: sample('mode', null, 'eco'), b: sample('b', 2) };
    expect(pickSample(latest, undefined)?.name).toBe('a');
  });

  it('falls back to the first reading when every reading is text', () => {
    const latest = { mode: sample('mode', null, 'eco'), state: sample('state', null, 'on') };
    expect(pickSample(latest, undefined)?.name).toBe('mode');
  });

  it('returns a pinned measurement as it is, text included', () => {
    const latest = { mode: sample('mode', null, 'eco'), temp: sample('temp', 21.5) };
    expect(pickSample(latest, 'mode')?.name).toBe('mode');
    expect(pickSample(latest, 'missing')).toBeUndefined();
    expect(pickSample({}, undefined)).toBeUndefined();
  });

  it('takes a zero reading as a reading', () => {
    const latest = { mode: sample('mode', null, 'eco'), level: sample('level', 0) };
    expect(pickSample(latest, undefined)?.name).toBe('level');
  });
});
