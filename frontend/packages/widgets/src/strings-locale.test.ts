// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { accessStrings } from './access-strings';
import { aggregateStrings } from './aggregate-strings';
import { alarmStrings } from './alarm-strings';
import { controlStrings } from './command-control-strings';
import { commandStrings } from './command-strings';
import { compareStrings } from './compare-strings';
import { eventMarkerStrings } from './event-markers-strings';
import { fleetStrings } from './fleet-strings';
import { historyStrings } from './history-strings';
import { levelStrings } from './level-strings';
import { presenceStrings } from './presence-strings';
import { scadaStrings } from './scada-strings';
import { sharedStrings, unresolvedWidgetDetail } from './shared-strings';
import { stateTimelineStrings } from './state-timeline-strings';
import { statusGridStrings } from './status-grid-strings';
import { tableStrings } from './table-strings';
import { timeRangeStrings } from './time-range-strings';
import { cardinalOf } from './heading';

let lang: string | null;
beforeEach(() => { lang = document.documentElement.getAttribute('lang'); });
afterEach(() => {
  if (lang === null) document.documentElement.removeAttribute('lang');
  else document.documentElement.lang = lang;
});

function shape(value: unknown): unknown {
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, shape(v)]));
  return typeof value;
}

function literals(value: unknown): string[] {
  if (typeof value === 'string') return [value];
  return value && typeof value === 'object' ? Object.values(value).flatMap(literals) : [];
}

describe.each([
  accessStrings, aggregateStrings, alarmStrings, controlStrings, commandStrings, compareStrings,
  eventMarkerStrings, fleetStrings, historyStrings, levelStrings, presenceStrings, scadaStrings,
  sharedStrings, stateTimelineStrings, statusGridStrings, tableStrings, timeRangeStrings,
])('%s locale coverage', (read) => {
  it('selects complete Spanish and Chinese tables from the document language, with English fallback', () => {
    document.documentElement.lang = 'en';
    const english = read();
    document.documentElement.lang = 'fr-CA';
    expect(read()).toBe(english);
    document.documentElement.lang = 'es';
    const spanish = read();
    expect(shape(spanish)).toEqual(shape(english));
    document.documentElement.lang = 'es-MX';
    expect(read()).toBe(spanish);
    document.documentElement.lang = 'zh-CN';
    const chinese = read();
    expect(chinese).not.toBe(english);
    expect(shape(chinese)).toEqual(shape(english));
    expect(literals(chinese).length).toBeGreaterThan(0);
    for (const text of literals(chinese)) {
      expect(text).toMatch(/[\u3400-\u9fff]/u);
      expect(text).not.toMatch(/ADR-\d+/u);
    }
    document.documentElement.lang = 'zh-Hans-CN';
    expect(read()).toBe(chinese);
  });
});

it('translates structured compatibility diagnostics and retains the authored and installed versions', () => {
  const reason = {
    code: 'options-version' as const, message: 'saved with settings version 7; installed version 1',
    versions: { authored: 7, installed: 1 },
  };
  expect(unresolvedWidgetDetail(reason, 'zh-CN')).toBe('已保存的设置版本与已安装的组件不匹配。 保存的版本：7；已安装的版本：1。');
  expect(unresolvedWidgetDetail(reason, 'es')).toContain('Versión guardada: 7; versión instalada: 1.');
  expect(unresolvedWidgetDetail(reason, 'fr')).toBe(reason.message);
});

it('preserves authored names, counts and values in translated functions', () => {
  expect(commandStrings('zh-CN').send('Pump Three')).toBe('发送 Pump Three');
  expect(commandStrings('zh-CN').minimum(12)).toBe('必须 ≥ 12');
  expect(controlStrings('zh-CN').otherRequired(['pressure', 'speed'])).toContain('“pressure”、“speed”');
  expect(alarmStrings('zh-CN').rows.shown(3, 8)).toBe('显示 8 条中的 3 条');
  expect(fleetStrings('zh-CN').range(1, 10, 25)).toBe('第 1–10 项，共 25 项');
  expect(eventMarkerStrings('zh-CN').aboutMeasurement('temperature')).toBe('关于 temperature');
});

it('uses natural Chinese names for all eight compass directions', () => {
  expect([0, 45, 90, 135, 180, 225, 270, 315].map((heading) => cardinalOf(heading, levelStrings('zh-CN').cardinals)))
    .toEqual(['北', '东北', '东', '东南', '南', '西南', '西', '西北']);
});
