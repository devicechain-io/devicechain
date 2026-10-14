// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The reviewed alert presets, pinned two ways. (1) Each preset emits EXACTLY the documented
// values. (2) With canonical parameters it emits exactly the golden file the backend compiler test
// (rules/presets_golden_test.go) decodes strictly and compiles — so a preset that stopped
// compiling, or a compiler change that invalidated one, fails a test on one side or the other.
// The goldens are imported ?raw: a .json import would make tsc read a file outside this package,
// and the console image builds without backend/ in its context.

import { describe, expect, it } from 'vitest';
import EN from '@/i18n/locales/en/deviceProfiles.json';
import DEVICE_SILENT from '../../../../../../backend/services/event-processing/internal/rules/testdata/presets/device-silent.json?raw';
import HELD_THRESHOLD from '../../../../../../backend/services/event-processing/internal/rules/testdata/presets/held-threshold.json?raw';
import LOW_BATTERY from '../../../../../../backend/services/event-processing/internal/rules/testdata/presets/low-battery.json?raw';
import { PRESET_IDS, buildPreset, pickBatteryMetric, type PresetId } from './rule-presets';
import { parseDefinition, rebuildFrom } from './rule-model';

const text = (id: PresetId) => {
  const k = `preset${id[0].toUpperCase()}${id.slice(1)}`;
  const en = EN as Record<string, string>;
  return { name: en[`${k}Name`], description: en[`${k}Description`] };
};
const build = (id: PresetId, metrics: string[] = [], threshold?: number) =>
  JSON.parse(buildPreset(id, { metrics, threshold }, text(id)).definition);

describe('the three presets', () => {
  it('has exactly the three documented presets', () => {
    expect([...PRESET_IDS]).toEqual(['deviceSilent', 'heldThreshold', 'lowBattery']);
  });

  it('Device silent: an absence rule, 15m, major, no condition', () => {
    const d = build('deviceSilent');
    expect(d).toMatchObject({ type: 'absence', timeout: '15m', severity: 'major' });
    expect(d.when).toBeUndefined();
    expect(d.actions).toEqual([{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'device-silent' } }]);
  });

  it('Held threshold: a duration rule held 10m, major, bound left for the author', () => {
    const d = build('heldThreshold', ['temperature', 'humidity']);
    expect(d).toMatchObject({ type: 'duration', hold: '10m', severity: 'major' });
    // Several metrics and no threshold: neither is guessed.
    expect(d.when).toEqual({ metric: '', op: 'gt' });
    expect(build('heldThreshold', ['temperature']).when).toEqual({ metric: 'temperature', op: 'gt' });
    expect(build('heldThreshold', ['temperature'], 80).when).toEqual({ metric: 'temperature', op: 'gt', threshold: 80 });
  });

  it('Held threshold: a threshold of 0 is a real bound, not "unset"', () => {
    expect(build('heldThreshold', ['t'], 0).when.threshold).toBe(0);
  });

  it('Low battery: a duration rule, level < 20 held 10m, warning', () => {
    const d = build('lowBattery', ['temperature', 'battery_level']);
    expect(d).toMatchObject({ type: 'duration', hold: '10m', severity: 'warning' });
    expect(d.when).toEqual({ metric: 'battery_level', op: 'lt', threshold: 20 });
    expect(d.actions).toEqual([{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'low-battery' } }]);
  });

  it('Low battery prefers battery|batt|soc by name and leaves the metric empty rather than guess', () => {
    expect(pickBatteryMetric(['temperature', 'SoC'])).toBe('SoC');
    expect(pickBatteryMetric(['temperature', 'batt_v'])).toBe('batt_v');
    expect(pickBatteryMetric(['battery_voltage', 'battery_level'])).toBe('battery_level');
    expect(pickBatteryMetric(['temperature', 'humidity'])).toBeNull();
    expect(build('lowBattery', ['temperature']).when.metric).toBe('');
    expect(buildPreset('lowBattery', { metrics: ['battery'] }, text('lowBattery')).metricToConfirm).toBe(true);
  });

  it('never emits a group scope (absence cannot carry one) and reopens losslessly in the form', () => {
    for (const id of PRESET_IDS) {
      const { definition } = buildPreset(id, { metrics: ['battery'], threshold: 5 }, text(id));
      const parsed = parseDefinition(definition);
      expect(parsed, id).not.toBeNull();
      expect(JSON.parse(rebuildFrom(parsed!)), id).toEqual(JSON.parse(definition));
      expect(Object.keys(JSON.parse(definition)).some((k) => /group|scope/i.test(k))).toBe(false);
    }
  });
});

describe('lockstep with the backend compiler goldens', () => {
  it.each([
    ['deviceSilent', DEVICE_SILENT, [] as string[], undefined],
    ['heldThreshold', HELD_THRESHOLD, ['temperature'], 80],
    ['lowBattery', LOW_BATTERY, ['battery_level'], undefined],
  ] as const)('%s emits exactly the file the Go compiler test compiles', (id, golden, metrics, threshold) => {
    expect(build(id, [...metrics], threshold)).toEqual(JSON.parse(golden));
  });
});
