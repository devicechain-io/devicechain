// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { WidgetDensityProvider, WidgetSubjectProvider } from './context';
import { FRAME_SUBTITLE_MIN_HEIGHT, FRAME_TITLE_MIN_HEIGHT, WidgetFrame } from './frame';
import { useWidgetScale } from './scale';

afterEach(cleanup);

describe('WidgetFrame overflow safety', () => {
  it('clips the body so an oversized widget cannot paint over its neighbours', () => {
    const { container } = render(<WidgetFrame title="t">content</WidgetFrame>);
    const body = container.querySelector('[data-widget-body]') as HTMLElement;
    expect(body.style.overflow).toBe('hidden');
  });

  it('renders the title on one line with an ellipsis and the full text as a tooltip', () => {
    render(<WidgetFrame title="A very long widget title that cannot fit">x</WidgetFrame>);
    const el = screen.getByText('A very long widget title that cannot fit');
    expect(el.style.whiteSpace).toBe('nowrap');
    expect(el.style.overflow).toBe('hidden');
    expect(el.style.textOverflow).toBe('ellipsis');
    expect(el.getAttribute('title')).toBe('A very long widget title that cannot fit');
  });

  it('suppresses a subject line that repeats the title', () => {
    render(
      <WidgetSubjectProvider label="sp-fuel-01">
        <WidgetFrame title="sp-fuel-01">x</WidgetFrame>
      </WidgetSubjectProvider>,
    );
    expect(screen.getAllByText('sp-fuel-01')).toHaveLength(1);
  });

  it('suppresses an explicit subtitle that repeats the title', () => {
    render(
      <WidgetFrame title="Same" subtitle=" Same ">
        x
      </WidgetFrame>,
    );
    expect(screen.getAllByText(/Same/)).toHaveLength(1);
  });

  it('keeps a subject line that differs from the title', () => {
    render(
      <WidgetSubjectProvider label="sp-fuel-01">
        <WidgetFrame title="Fuel level">x</WidgetFrame>
      </WidgetSubjectProvider>,
    );
    expect(screen.getByText('sp-fuel-01')).toBeTruthy();
    expect(screen.getByText('Fuel level')).toBeTruthy();
  });
});

// A ResizeObserver whose reported size is chosen per element, so the frame root and its content
// column can be given different sizes.
function stubSizes(sizes: { root: [number, number]; column: [number, number] }) {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      constructor(private cb: (entries: unknown[]) => void) {}
      observe(el: Element) {
        const [width, height] = el.hasAttribute('data-widget-content') ? sizes.column : sizes.root;
        this.cb([{ contentRect: { width, height } }]);
      }
      disconnect() {}
    },
  );
}

describe('WidgetFrame size awareness', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('makes the padded content column the size container, and the body does not', () => {
    const { container } = render(<WidgetFrame title="t">x</WidgetFrame>);
    const column = container.querySelector('[data-widget-content]') as HTMLElement;
    const body = container.querySelector('[data-widget-body]') as HTMLElement;
    expect(column.style.containerType).toBe('size');
    expect(body.style.containerType).toBe('');
    expect(body.contains(column)).toBe(true);
    expect(column.style.padding).toContain('--space-3');
  });

  it('flush removes the column padding', () => {
    const { container } = render(
      <WidgetFrame title="t" flush>
        x
      </WidgetFrame>,
    );
    expect((container.querySelector('[data-widget-content]') as HTMLElement).style.padding).toBe('0px');
  });

  it('shows title and subtitle in a tall frame', () => {
    stubSizes({ root: [300, 200], column: [276, 140] });
    render(
      <WidgetFrame title="Fuel" subtitle="sp-fuel-01">
        x
      </WidgetFrame>,
    );
    expect(screen.getByText('Fuel')).toBeTruthy();
    expect(screen.getByText('sp-fuel-01')).toBeTruthy();
  });

  it('drops the subtitle first, below its height threshold', () => {
    stubSizes({ root: [300, FRAME_SUBTITLE_MIN_HEIGHT - 1], column: [276, 60] });
    render(
      <WidgetFrame title="Fuel" subtitle="sp-fuel-01">
        x
      </WidgetFrame>,
    );
    expect(screen.getByText('Fuel')).toBeTruthy();
    expect(screen.queryByText('sp-fuel-01')).toBeNull();
  });

  it('then the title, below the lower threshold, keeping it as the accessible name', () => {
    stubSizes({ root: [300, FRAME_TITLE_MIN_HEIGHT - 1], column: [276, 30] });
    const { container } = render(
      <WidgetFrame title="Fuel" subtitle="sp-fuel-01">
        x
      </WidgetFrame>,
    );
    expect(screen.queryByText('Fuel')).toBeNull();
    expect(screen.queryByText('sp-fuel-01')).toBeNull();
    expect((container.firstElementChild as HTMLElement).getAttribute('aria-label')).toBe('Fuel');
  });

  it('scales the thresholds with density: a wall board needs more height to keep its header', () => {
    stubSizes({ root: [300, FRAME_SUBTITLE_MIN_HEIGHT + 10], column: [276, 100] });
    render(
      <WidgetDensityProvider scale={1.5}>
        <WidgetFrame title="Fuel" subtitle="sp-fuel-01">
          x
        </WidgetFrame>
      </WidgetDensityProvider>,
    );
    expect(screen.queryByText('sp-fuel-01')).toBeNull();
  });

  it('exposes the content column (quantised to 4 px) to a function child and to useWidgetScale', () => {
    stubSizes({ root: [300, 200], column: [277, 141] });
    function Inner() {
      const s = useWidgetScale();
      return <i data-testid="inner">{`${s.size.width}x${s.size.height}`}</i>;
    }
    render(
      <WidgetFrame title="t">
        {(scale) => (
          <>
            <b data-testid="fn">{`${scale.size.width}x${scale.size.height}:${scale.sizeClass}`}</b>
            <Inner />
          </>
        )}
      </WidgetFrame>,
    );
    expect(screen.getByTestId('fn').textContent).toBe('276x140:regular');
    expect(screen.getByTestId('inner').textContent).toBe('276x140');
  });
});
