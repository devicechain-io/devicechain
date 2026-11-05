// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { configureStrictCsp, plainTooltipText } from './strict-csp';
import { ALARM_LEVELS, SEVERITY_TOKENS, severityTokenCss } from './severity';
import { FONT_TOKENS, SPACE_TOKENS, WidgetTokenStyles, densityScale, fontVar, spaceVar, tokenPx, tokenStyle } from './tokens';

describe('size tokens', () => {
  it('fontVar / spaceVar read the variable with the base size as the fallback', () => {
    expect(fontVar('md')).toBe(`var(--dc-font-md, ${FONT_TOKENS.md}px)`);
    expect(spaceVar(3)).toBe(`var(--dc-space-3, ${SPACE_TOKENS[3]}px)`);
  });

  it('tokenStyle defines every token as a multiple of --dc-scale', () => {
    const style = tokenStyle(1.5) as Record<string, string>;
    expect(style['--dc-scale']).toBe('1.5');
    for (const name of Object.keys(FONT_TOKENS)) expect(style[`--dc-font-${name}`]).toMatch(/var\(--dc-scale\)/);
    for (const name of Object.keys(SPACE_TOKENS)) expect(style[`--dc-space-${name}`]).toMatch(/var\(--dc-scale\)/);
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

describe('WidgetTokenStyles in strict-CSP host mode', () => {
  afterEach(() => {
    configureStrictCsp(false);
    vi.unstubAllGlobals();
    Reflect.deleteProperty(document, 'adoptedStyleSheets');
  });

  // jsdom has no constructed stylesheets: a minimal stand-in that records what was adopted.
  function stubSheets(replaceSync: (css: string) => void) {
    vi.stubGlobal(
      'CSSStyleSheet',
      class {
        css = '';
        replaceSync(css: string) {
          replaceSync(css);
          this.css = css;
        }
      },
    );
    Object.defineProperty(document, 'adoptedStyleSheets', { value: [], writable: true, configurable: true });
  }

  it('adopts the token CSS instead of rendering a <style> element', () => {
    stubSheets(() => {});
    configureStrictCsp(true);
    const { container } = render(<WidgetTokenStyles />);
    expect(container.querySelector('style')).toBeNull();
    const adopted = (document as unknown as { adoptedStyleSheets: Array<{ css: string }> }).adoptedStyleSheets;
    expect(adopted.map((s) => s.css)).toContain(severityTokenCss({ hostWins: true }));
  });

  it('falls back to the <style> element when adoption fails', () => {
    stubSheets(() => {
      throw new Error('nope');
    });
    configureStrictCsp(true);
    const { container } = render(<WidgetTokenStyles />);
    expect(container.querySelector('style[data-dc-tokens]')!.textContent).toBe(severityTokenCss({ hostWins: true }));
  });

  it('is the element by default, even where constructed stylesheets exist', () => {
    stubSheets(() => {});
    const { container } = render(<WidgetTokenStyles />);
    expect(container.querySelector('style[data-dc-tokens]')).not.toBeNull();
    expect((document as unknown as { adoptedStyleSheets: unknown[] }).adoptedStyleSheets).toHaveLength(0);
  });
});

describe('plainTooltipText', () => {
  it('neutralizes the rich-text delimiters and leaves other text alone', () => {
    expect(plainTooltipText('a {b|c} <img onerror=x>')).not.toMatch(/[{}]/);
    expect(plainTooltipText('plain')).toBe('plain');
  });
});
