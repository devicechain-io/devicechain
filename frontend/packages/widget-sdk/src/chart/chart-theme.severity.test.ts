// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, describe, expect, it } from 'vitest';

import { ALARM_LEVELS, SEVERITY_LEVELS, SEVERITY_TOKENS, levelColor, severityTokenCss, severityVar } from '../severity';

import { resolveChartTheme } from './chart-theme';

afterEach(() => {
  document.documentElement.classList.remove('dark');
  for (const l of ALARM_LEVELS) document.documentElement.style.removeProperty(severityVar(l));
});

// 'h s% l%' -> 'hsl(h, s%, l%)', the form the theme hands a canvas.
const hsl = (channels: string) => `hsl(${channels.split(' ').join(', ')})`;

describe('severity colours in the chart theme', () => {
  it('keeps five levels but colours only the four abnormal ones, each light and dark', () => {
    expect([...SEVERITY_LEVELS]).toEqual(['normal', 'warning', 'minor', 'major', 'critical']);
    expect([...ALARM_LEVELS]).toEqual(['warning', 'minor', 'major', 'critical']);
    expect(Object.keys(SEVERITY_TOKENS).sort()).toEqual([...ALARM_LEVELS].sort());
    for (const level of ALARM_LEVELS) {
      expect(SEVERITY_TOKENS[level].light).not.toBe(SEVERITY_TOKENS[level].dark);
    }
  });

  it('falls back to the SDK light values when the host defines no --sev-* token', () => {
    const theme = resolveChartTheme(document.documentElement);
    for (const level of ALARM_LEVELS) expect(theme.severity[level]).toBe(hsl(SEVERITY_TOKENS[level].light));
  });

  it('falls back to the dark values under .dark', () => {
    document.documentElement.classList.add('dark');
    const theme = resolveChartTheme(document.documentElement);
    for (const level of ALARM_LEVELS) expect(theme.severity[level]).toBe(hsl(SEVERITY_TOKENS[level].dark));
  });

  it('normal has no colour: levelColor answers undefined, so the widget uses its own neutral', () => {
    const theme = resolveChartTheme(document.documentElement);
    expect(levelColor(theme.severity, 'normal')).toBeUndefined();
    expect(levelColor(theme.severity, undefined)).toBeUndefined();
    expect(levelColor(theme.severity, 'critical')).toBe(theme.severity.critical);
    expect('normal' in theme.severity).toBe(false);
  });

  it('a host token wins over the SDK value (this is how the theme re-skins a level)', () => {
    document.documentElement.style.setProperty(severityVar('critical'), '300 50% 40%');
    const theme = resolveChartTheme(document.documentElement);
    expect(theme.severity.critical).toBe('hsl(300, 50%, 40%)');
    expect(theme.severity.major).toBe(hsl(SEVERITY_TOKENS.major.light));
  });

  it('emits the same values as a stylesheet, light on :root and dark on .dark', () => {
    const css = severityTokenCss();
    const [root, dark] = css.split('.dark');
    expect(css).not.toContain('--sev-normal');
    for (const level of ALARM_LEVELS) {
      expect(root).toContain(`${severityVar(level)}: ${SEVERITY_TOKENS[level].light};`);
      expect(dark).toContain(`${severityVar(level)}: ${SEVERITY_TOKENS[level].dark};`);
    }
  });
});
