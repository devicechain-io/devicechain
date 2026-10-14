// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { WidgetSubjectProvider } from './context';
import { WidgetFrame } from './frame';

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
