// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The board's frame, as WidgetFrame draws it: variant, border, padding, background and header,
// each from a token, each read from the context the renderer provides.

import { resolveFrame, type WidgetFrameSpec } from '@devicechain/dashboards';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { WidgetFrameStyleProvider } from './context';
import { FRAME_TITLE_MIN_HEIGHT, WidgetFrame } from './frame';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function framed(widget: WidgetFrameSpec | undefined, canvas?: WidgetFrameSpec) {
  const { container } = render(
    <WidgetFrameStyleProvider frame={resolveFrame(canvas, widget)}>
      <WidgetFrame title="Fuel" subtitle="sp-fuel-01">
        x
      </WidgetFrame>
    </WidgetFrameStyleProvider>,
  );
  const root = container.firstElementChild as HTMLElement;
  const column = container.querySelector('[data-widget-content]') as HTMLElement;
  return { root, column };
}

describe('WidgetFrame frame style', () => {
  it('draws the default card exactly as a frame outside any board does', () => {
    const inBoard = framed(undefined).root.getAttribute('style');
    cleanup();
    const { container } = render(
      <WidgetFrame title="Fuel" subtitle="sp-fuel-01">
        x
      </WidgetFrame>,
    );
    expect((container.firstElementChild as HTMLElement).getAttribute('style')).toBe(inBoard);
  });

  it('draws the border width, radius and colour tokens it is given', () => {
    const { root } = framed({ border: { width: 3, radius: 'lg', color: 'sev-critical' } });
    expect(root.style.border).toBe('3px solid hsl(var(--sev-critical))');
    expect(root.style.borderRadius).toContain('1.5');
  });

  it('draws no border, a transparent background and no header for a frameless widget', () => {
    const { root, column } = framed({ variant: 'none' });
    expect(root.style.borderStyle).toBe('none');
    expect(root.style.background).toBe('transparent');
    expect(screen.queryByText('Fuel')).toBeNull();
    expect(root.getAttribute('aria-label')).toBe('Fuel'); // still the accessible name
    expect(column.style.padding).toBe('0px');
  });

  it('takes the content padding from a spacing token, and a background token', () => {
    const { root, column } = framed({ padding: 'space-1', background: 'muted' }, { variant: 'flat' });
    expect(column.style.padding).toContain('--dc-space-1');
    expect(root.style.background).toBe('hsl(var(--muted))');
    expect(root.style.borderStyle).toBe('none'); // flat, from the canvas
  });

  it('keeps the header in a short frame when told to show it, and drops it from a tall one when told to hide it', () => {
    vi.stubGlobal(
      'ResizeObserver',
      class {
        constructor(private cb: ResizeObserverCallback) {}
        observe(el: Element) {
          const h = el.hasAttribute('data-widget-content') ? 10 : FRAME_TITLE_MIN_HEIGHT - 1;
          this.cb([{ target: el, contentRect: { width: 300, height: h } } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver);
        }
        unobserve() {}
        disconnect() {}
      },
    );
    framed({ header: 'show' });
    expect(screen.getByText('Fuel')).toBeTruthy();
    expect(screen.getByText('sp-fuel-01')).toBeTruthy();
    cleanup();
    vi.unstubAllGlobals();
    framed({ header: 'hide' });
    expect(screen.queryByText('Fuel')).toBeNull();
  });
});
