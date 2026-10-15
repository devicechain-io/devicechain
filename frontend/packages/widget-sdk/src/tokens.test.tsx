// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { ALARM_LEVELS, SEVERITY_TOKENS, severityTokenCss } from './severity';
import { FONT_TOKENS, SPACE_TOKENS, WidgetTokenStyles, densityScale, fontVar, spaceVar, tokenPx, tokenStyle } from './tokens';

describe('size tokens', () => {
  it('fontVar / spaceVar read the variable with the base size as the fallback', () => {
    expect(fontVar('md')).toBe(`var(--font-md, ${FONT_TOKENS.md}px)`);
    expect(spaceVar(3)).toBe(`var(--space-3, ${SPACE_TOKENS[3]}px)`);
  });

  it('tokenStyle defines every token as a multiple of --dc-scale', () => {
    const style = tokenStyle(1.5) as Record<string, string>;
    expect(style['--dc-scale']).toBe('1.5');
    for (const name of Object.keys(FONT_TOKENS)) expect(style[`--font-${name}`]).toMatch(/var\(--dc-scale\)/);
    for (const name of Object.keys(SPACE_TOKENS)) expect(style[`--space-${name}`]).toMatch(/var\(--dc-scale\)/);
  });

  it('tokenPx is the numeric twin of the variable at a scale', () => {
    expect(tokenPx('font-md', 1)).toBe(FONT_TOKENS.md);
    expect(tokenPx('font-md', 1.5)).toBeCloseTo(FONT_TOKENS.md * 1.5);
    expect(tokenPx('space-2', 0.9)).toBeCloseTo(SPACE_TOKENS[2] * 0.9);
  });

  it('densities map to 0.9 / 1 / 1.5, comfortable by default', () => {
    expect(densityScale('compact')).toBe(0.9);
    expect(densityScale('comfortable')).toBe(1);
    expect(densityScale('wall')).toBe(1.5);
    expect(densityScale(undefined)).toBe(1);
  });
});

describe('severity tokens', () => {
  it('have no normal hue: only the four alarm levels carry a token', () => {
    expect(Object.keys(SEVERITY_TOKENS).sort()).toEqual([...ALARM_LEVELS].sort());
    expect(severityTokenCss()).not.toMatch(/sev-normal/);
  });

  it('WidgetTokenStyles injects the SAME table at zero specificity so a host token wins', () => {
    const { container } = render(<WidgetTokenStyles />);
    const css = container.querySelector('style[data-dc-tokens]')!.textContent!;
    expect(css).toBe(severityTokenCss({ hostWins: true }));
    expect(css).toContain(':where(:root)');
    expect(css).toContain(':where(.dark)');
    for (const level of ALARM_LEVELS) {
      expect(css).toContain(`--sev-${level}: ${SEVERITY_TOKENS[level].light};`);
      expect(css).toContain(`--sev-${level}: ${SEVERITY_TOKENS[level].dark};`);
    }
  });
});
