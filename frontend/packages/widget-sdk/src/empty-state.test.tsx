// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { WidgetBodySizeProvider } from './context';
import { lastValueHint, WidgetEmptyState, widgetEmptyStrings, type WidgetEmptyKind } from './empty-state';

afterEach(cleanup);

const KINDS: WidgetEmptyKind[] = ['unbound', 'not-reported', 'no-data', 'forbidden', 'error'];

describe('the explained empty state', () => {
  it('says which of the five it is, in a title and a sentence', () => {
    const titles = new Set<string>();
    for (const kind of KINDS) {
      const { container, unmount } = render(<WidgetEmptyState kind={kind} measurement="temperature" locale="en" />);
      const root = container.querySelector(`[data-empty-state="${kind}"]`);
      expect(root, kind).not.toBeNull();
      titles.add(root!.querySelector('[data-empty-title]')!.textContent!);
      expect(root!.querySelector('[data-empty-detail]')!.textContent!.length).toBeGreaterThan(10);
      unmount();
    }
    expect(titles.size).toBe(KINDS.length);
  });

  it('names the measurement a profile does not declare, and the one with nothing in range', () => {
    const a = render(<WidgetEmptyState kind="not-reported" measurement="fuel_level" locale="en" />);
    expect(a.container.querySelector('[data-empty-detail]')!.textContent).toContain('"fuel_level"');
    a.unmount();
    const b = render(<WidgetEmptyState kind="no-data" measurement="fuel_level" hint="Last value 4 · 3 days ago" locale="en" />);
    expect(b.container.querySelector('[data-empty-detail]')!.textContent).toContain('"fuel_level"');
    expect(b.container.querySelector('[data-empty-hint]')!.textContent).toBe('Last value 4 · 3 days ago');
  });

  it('is neutral: no warning glyph and no severity colour', () => {
    for (const kind of KINDS) {
      const { container, unmount } = render(<WidgetEmptyState kind={kind} />);
      const root = container.firstElementChild as HTMLElement;
      expect(root.textContent).not.toMatch(/[⚠!]/);
      expect(root.style.color).toBe('hsl(var(--muted-foreground))');
      expect(root.getAttribute('role')).toBe('status');
      unmount();
    }
  });

  it('speaks Spanish for an es viewer', () => {
    const { container } = render(<WidgetEmptyState kind="no-data" measurement="t" locale="es-MX" />);
    expect(container.querySelector('[data-empty-title]')!.textContent).toBe('Sin datos en este rango');
    expect(widgetEmptyStrings('es').forbidden.title).toBe('Sin acceso');
    expect(lastValueHint('4', 'hace 3 d', 'es')).toBe('Último valor 4 · hace 3 d');
  });

  it('gives way to the title as the widget shrinks: the sentence, then the hint, then the glyph', () => {
    const at = (width: number, height: number) =>
      render(
        <WidgetBodySizeProvider size={{ width, height }}>
          <WidgetEmptyState kind="no-data" hint="h" locale="en" />
        </WidgetBodySizeProvider>,
      );
    const roomy = at(300, 200);
    expect(roomy.container.querySelector('[data-empty-hint]')).not.toBeNull();
    expect(roomy.container.querySelector('[aria-hidden]')).not.toBeNull();
    roomy.unmount();
    const medium = at(300, 90);
    expect(medium.container.querySelector('[data-empty-detail]')).not.toBeNull();
    expect(medium.container.querySelector('[data-empty-hint]')).toBeNull();
    medium.unmount();
    const tiny = at(120, 40);
    expect(tiny.container.querySelector('[data-empty-title]')).not.toBeNull();
    expect(tiny.container.querySelector('[data-empty-detail]')).toBeNull();
    expect(tiny.container.querySelector('[aria-hidden]')).toBeNull();
  });
});
