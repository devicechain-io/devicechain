// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The frame's export menu: offered only when there is something to export, built when the viewer
// asks, and a CSV that arrives as a file. The encoder's own rules are in export-csv.test.ts.

import { resolveFrame } from '@devicechain/dashboards';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { WidgetFrameStyleProvider, useRegisterWidgetImage } from './context';
import { WidgetFrame } from './frame';
import type { WidgetExportTable } from './widget-export';

let saved: Array<{ blob: Blob }>;
let clicked: Array<{ href: string; download: string }>;

beforeEach(() => {
  document.documentElement.lang = 'en';
  saved = [];
  clicked = [];
  vi.spyOn(URL, 'createObjectURL').mockImplementation((blob) => {
    saved.push({ blob: blob as Blob });
    return 'blob:export';
  });
  vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined);
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    clicked.push({ href: this.href, download: this.download });
  });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const TABLE: WidgetExportTable = {
  name: 'Pump 3 history-avg per 5m',
  columns: ['Time', 'temp (°C)'],
  rows: [['2026-10-22T14:00:00.000Z', 21.5]],
};

const button = () => screen.findByRole('button', { name: 'Export data' });

describe('the export button', () => {
  it('is not drawn for a widget with nothing to export', () => {
    render(<WidgetFrame title="Plain">x</WidgetFrame>);
    expect(screen.queryByRole('button', { name: 'Export data' })).toBeNull();
  });

  it('is drawn in the title bar when the widget passes exportRows', async () => {
    const { container } = render(
      <WidgetFrame title="Pump 3" exportRows={() => TABLE}>
        x
      </WidgetFrame>,
    );
    const b = await button();
    expect(b.closest('[data-widget-export]')?.parentElement?.textContent).toContain('Pump 3');
    expect(container.querySelector('[data-widget-body]')?.contains(b)).toBe(false);
    expect(b.getAttribute('aria-haspopup')).toBe('menu');
    expect(b.getAttribute('aria-expanded')).toBe('false');
  });

  it('goes with the title bar: a board that hides headers has no export button', () => {
    render(
      <WidgetFrameStyleProvider frame={resolveFrame({ header: 'hide' }, undefined)}>
        <WidgetFrame title="Pump 3" exportRows={() => TABLE}>
          x
        </WidgetFrame>
      </WidgetFrameStyleProvider>,
    );
    expect(screen.queryByRole('button', { name: 'Export data' })).toBeNull();
  });

  it('says its name in the viewer’s language', async () => {
    document.documentElement.lang = 'es';
    render(
      <WidgetFrame title="Bomba" exportRows={() => TABLE}>
        x
      </WidgetFrame>,
    );
    expect(await screen.findByRole('button', { name: 'Exportar datos' })).toBeTruthy();
  });

  it('reserves its room beside the title only when it is drawn', async () => {
    const { container, rerender } = render(<WidgetFrame title="Pump 3">x</WidgetFrame>);
    const bar = () => container.querySelector('[data-widget-body]')!.previousElementSibling as HTMLElement;
    expect(bar().style.paddingRight).toBe('');
    rerender(
      <WidgetFrame title="Pump 3" exportRows={() => TABLE}>
        x
      </WidgetFrame>,
    );
    expect(bar().style.paddingRight).toContain('24px');
    await button();
  });
});

describe('the menu', () => {
  it('builds the table only when the viewer picks CSV, and saves it as a UTF-8 file with a mark', async () => {
    const exportRows = vi.fn(() => TABLE);
    render(
      <WidgetFrame title="Pump 3" exportRows={exportRows}>
        x
      </WidgetFrame>,
    );
    expect(exportRows).not.toHaveBeenCalled();
    fireEvent.click(await button());
    const item = await screen.findByRole('menuitem', { name: 'Download CSV' });
    expect(exportRows).not.toHaveBeenCalled();
    expect(screen.queryByRole('menuitem', { name: /image/i })).toBeNull();

    fireEvent.click(item);
    await waitFor(() => expect(saved).toHaveLength(1));
    expect(exportRows).toHaveBeenCalledTimes(1);
    expect(clicked[0].download).toBe('Pump-3-history-avg-per-5m.csv');
    const bytes = new Uint8Array(await saved[0].blob.arrayBuffer());
    expect([...bytes.slice(0, 3)]).toEqual([0xef, 0xbb, 0xbf]);
    expect(new TextDecoder().decode(bytes.slice(3))).toBe('Time,temp (°C)\r\n2026-10-22T14:00:00.000Z,21.5\r\n');
    // The menu closes once it has done its work.
    await waitFor(() => expect(screen.queryByRole('menu')).toBeNull());
  });

  it('waits for a table built on demand', async () => {
    render(
      <WidgetFrame title="Pump 3" exportRows={() => Promise.resolve(TABLE)}>
        x
      </WidgetFrame>,
    );
    fireEvent.click(await button());
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Download CSV' }));
    await waitFor(() => expect(saved).toHaveLength(1));
  });

  it('saves nothing, and says so in the console, when the table cannot be built', async () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    render(
      <WidgetFrame title="Pump 3" exportRows={() => Promise.reject(new Error('chunk failed'))}>
        x
      </WidgetFrame>,
    );
    fireEvent.click(await button());
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Download CSV' }));
    await waitFor(() => expect(error).toHaveBeenCalled());
    expect(saved).toHaveLength(0);
    await waitFor(() => expect(screen.queryByRole('menu')).toBeNull());
  });

  it('saves nothing when the widget answers with no table', async () => {
    render(
      <WidgetFrame title="Pump 3" exportRows={() => null}>
        x
      </WidgetFrame>,
    );
    fireEvent.click(await button());
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Download CSV' }));
    await waitFor(() => expect(screen.queryByRole('menu')).toBeNull());
    expect(saved).toHaveLength(0);
  });

  it('closes on Escape, giving the focus back to the button, and on a click elsewhere', async () => {
    render(
      <div>
        <span data-testid="elsewhere">elsewhere</span>
        <WidgetFrame title="Pump 3" exportRows={() => TABLE}>
          x
        </WidgetFrame>
      </div>,
    );
    const b = await button();
    fireEvent.click(b);
    const item = await screen.findByRole('menuitem');
    expect(document.activeElement).toBe(item);
    fireEvent.keyDown(item, { key: 'Escape' });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(b);

    fireEvent.click(b);
    await screen.findByRole('menu');
    fireEvent.pointerDown(screen.getByTestId('elsewhere'));
    expect(screen.queryByRole('menu')).toBeNull();
  });
});

function Chart({ url }: { url: string | undefined }) {
  useRegisterWidgetImage(({ backgroundColor }) => (url ? `${url}#${backgroundColor}` : undefined));
  return <canvas />;
}

describe('the chart image', () => {
  it('offers "Download image" only while a chart inside has registered one', async () => {
    const { rerender } = render(
      <WidgetFrame title="Chart">
        <Chart url="data:image/png;base64,AAAA" />
      </WidgetFrame>,
    );
    fireEvent.click(await button());
    expect(await screen.findByRole('menuitem', { name: 'Download image (PNG)' })).toBeTruthy();
    expect(screen.queryByRole('menuitem', { name: 'Download CSV' })).toBeNull();

    // The chart goes away: nothing is left to save, so the button goes with it.
    rerender(<WidgetFrame title="Chart">x</WidgetFrame>);
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Export data' })).toBeNull());
  });

  it('saves the image the chart gives, on the frame’s own background, named for the table when there is one', async () => {
    document.body.style.background = 'rgb(10, 20, 30)';
    render(
      <WidgetFrame title="Chart" exportRows={() => ({ ...TABLE, name: 'Chart-1h-20261022T1300Z-20261022T1400Z' })}>
        <Chart url="data:image/png;base64,AAAA" />
      </WidgetFrame>,
    );
    fireEvent.click(await button());
    expect(await screen.findAllByRole('menuitem')).toHaveLength(2);
    fireEvent.click(screen.getByRole('menuitem', { name: 'Download image (PNG)' }));
    await waitFor(() => expect(clicked).toHaveLength(1));
    expect(clicked[0].download).toBe('Chart-1h-20261022T1300Z-20261022T1400Z.png');
    // The canvas is transparent; the frame supplies what is behind it (here, the page's colour).
    expect(decodeURIComponent(clicked[0].href)).toBe('data:image/png;base64,AAAA#rgb(10, 20, 30)');
    document.body.style.background = '';
  });

  it('names an image after the title and the minute when the widget exports no rows', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-10-22T14:05:09Z'));
    render(
      <WidgetFrame title="Tank level">
        <Chart url="data:image/png;base64,AAAA" />
      </WidgetFrame>,
    );
    fireEvent.click(await button());
    fireEvent.click(await screen.findByRole('menuitem'));
    await waitFor(() => expect(clicked).toHaveLength(1));
    expect(clicked[0].download).toBe('Tank-level-20261022T1405Z.png');
    vi.useRealTimers();
  });

  it('saves nothing when the chart has no image yet', async () => {
    render(
      <WidgetFrame title="Chart">
        <Chart url={undefined} />
      </WidgetFrame>,
    );
    fireEvent.click(await button());
    fireEvent.click(await screen.findByRole('menuitem'));
    await act(async () => undefined);
    expect(clicked).toHaveLength(0);
  });
});
