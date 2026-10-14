// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, describe, expect, it } from 'vitest';

import { SEVERITY_LEVELS, SEVERITY_TOKENS, severityTokenCss, severityVar } from '../severity';

import { resolveChartTheme } from './chart-theme';

afterEach(() => {
  document.documentElement.classList.remove('dark');
  for (const l of SEVERITY_LEVELS) document.documentElement.style.removeProperty(severityVar(l));
});

// 'h s% l%' -> 'hsl(h, s%, l%)', the form the theme hands a canvas.
const hsl = (channels: string) => `hsl(${channels.split(' ').join(', ')})`;

describe('severity colours in the chart theme', () => {
  it('defines all five levels, each with a light and a dark value that differ', () => {
    expect([...SEVERITY_LEVELS]).toEqual(['normal', 'warning', 'minor', 'major', 'critical']);
    for (const level of SEVERITY_LEVELS) {
      expect(SEVERITY_TOKENS[level].light).not.toBe(SEVERITY_TOKENS[level].dark);
    }
  });

  it('falls back to the SDK light values when the host defines no --sev-* token', () => {
    const theme = resolveChartTheme(document.documentElement);
    for (const level of SEVERITY_LEVELS) expect(theme.severity[level]).toBe(hsl(SEVERITY_TOKENS[level].light));
  });

  it('falls back to the dark values under .dark', () => {
    document.documentElement.classList.add('dark');
    const theme = resolveChartTheme(document.documentElement);
    for (const level of SEVERITY_LEVELS) expect(theme.severity[level]).toBe(hsl(SEVERITY_TOKENS[level].dark));
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
    for (const level of SEVERITY_LEVELS) {
      expect(root).toContain(`${severityVar(level)}: ${SEVERITY_TOKENS[level].light};`);
      expect(dark).toContain(`${severityVar(level)}: ${SEVERITY_TOKENS[level].dark};`);
    }
  });
});
