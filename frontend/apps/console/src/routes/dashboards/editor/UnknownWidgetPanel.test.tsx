// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The side panel for a widget whose type this console does not know (a newer release
// added it). There is nothing to configure, so the panel says what the widget is and
// that it will be saved unchanged; it offers no option or data-source control that
// could write into a widget this console cannot read.

import '@/i18n/config';
import { parseDashboardDefinition, type UnknownWidgetInstance } from '@devicechain/dashboards';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { UnknownWidgetPanel } from './WidgetConfigPanel';

afterEach(cleanup);

function placeholder(): UnknownWidgetInstance {
  const def = parseDashboardDefinition({
    widgets: [
      {
        id: 'deck-1',
        type: 'card-deck',
        layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 4, z: 0 } },
        options: { title: 'Fleet' },
      },
    ],
  });
  const widget = def.widgets[0];
  if (widget.type !== 'unknown-widget') throw new Error('expected a placeholder');
  return widget;
}

describe('UnknownWidgetPanel', () => {
  it('names the stored type and says the widget is kept unchanged', () => {
    render(<UnknownWidgetPanel widget={placeholder()} onClose={vi.fn()} />);
    expect(screen.getByText('Unsupported widget')).toBeTruthy();
    expect(screen.getByText('Type: card-deck')).toBeTruthy();
    expect(screen.getByText(/saved back unchanged/)).toBeTruthy();
  });

  it('offers no input that could write into the widget', () => {
    const { container } = render(<UnknownWidgetPanel widget={placeholder()} onClose={vi.fn()} />);
    expect(container.querySelectorAll('input, select, textarea')).toHaveLength(0);
  });

  it('closes', () => {
    const onClose = vi.fn();
    render(<UnknownWidgetPanel widget={placeholder()} onClose={onClose} />);
    fireEvent.click(screen.getByRole('button'));
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
