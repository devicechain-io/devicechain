// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The frame's export button, its menu and what the menu's items do (Download CSV, Download image).
// Its own chunk, with the CSV encoder: fetched by a frame that has something to export (frame.tsx
// draws only the box the button sits in), so a board's first download holds none of it.
//
//   - Download CSV builds the table at the moment of the click, from what the widget is showing;
//   - Download image reads the chart's canvas on the card's own background (a canvas is transparent,
//     and a dark theme's light text on white is unreadable), at twice the on-screen resolution.
//
// 🔴 PLAIN DOM, AND IMPORTS NOTHING BUT THE ENCODER. Every chunk a lazy chunk reaches must be in the
// same group as the code a board always loads, or the app's bundler splits that shared code into a
// file of its own just for this chunk. A React menu here split React out of the shared vendor chunk,
// and the lost compression cost every board ~0.8 KB of its first download whether or not it held an
// exportable widget; an eager React button instead cost ~0.6 KB. So this builds its handful of elements by hand, reads the viewer's language the
// way freshness.ts does, and names theme colours as the `hsl(var(--token))` that theme.ts's css() writes.

import { downloadCsv, safeFileStem } from './export-csv';
import type { WidgetImageSource } from './context';
import type { WidgetExportResult } from './widget-export';

const WORDS = {
  en: { button: 'Export data', csv: 'Download CSV', image: 'Download image (PNG)' },
  es: { button: 'Exportar datos', csv: 'Descargar CSV', image: 'Descargar imagen (PNG)' },
};

export interface ExportMenuOptions {
  exportRows: (() => WidgetExportResult) | undefined;
  image: WidgetImageSource | undefined;
  // The frame's own title, the name of a file with nothing better to call it.
  title: string | undefined;
  // The element whose background an image is drawn on.
  frameElement: () => Element | null;
  // The menu has closed. `refocus` when the focus belongs back on the button (an item was picked,
  // Escape was pressed); not when the viewer clicked somewhere else.
  onClose: (refocus: boolean) => void;
}

// The first opaque background at or above `el`: what the card is drawn on. White when nothing in the
// page sets one.
export function opaqueBackground(el: Element | null): string {
  for (let node: Element | null = el; node; node = node.parentElement) {
    const color = getComputedStyle(node).backgroundColor;
    if (color && color !== 'transparent' && !/^rgba\(.*,\s*0\)$/.test(color) && !/\/\s*0\)$/.test(color)) return color;
  }
  return '#ffffff';
}

// `2026-10-22T14:05:09.123Z` -> `20261022T1405Z`: a file name's time, to the minute, in UTC.
export function fileStamp(at: Date): string {
  return at.toISOString().replace(/[-:]/g, '').replace(/\d\d\.\d+Z$/, 'Z');
}

// The viewer's language, as freshness.ts's viewerLocale reads it (see the top of this file).
function spanish(): boolean {
  const lang = (typeof document !== 'undefined' && document.documentElement.lang) || (typeof navigator !== 'undefined' && navigator.language) || '';
  return lang.toLowerCase().startsWith('es');
}

const MENU_STYLE =
  'position:absolute;top:100%;right:4px;z-index:5;margin-top:2px;padding:4px 0;min-width:160px;' +
  'background:hsl(var(--card));border:1px solid hsl(var(--border));border-radius:6px;box-shadow:0 4px 12px rgb(0 0 0 / 0.15)';
const ITEM_STYLE =
  'display:block;width:100%;padding:6px 12px;border:none;background:transparent;color:hsl(var(--card-foreground));' +
  'font:inherit;font-size:0.8125rem;text-align:left;white-space:nowrap;cursor:pointer';

/**
 * Opens the menu inside `host` (the positioned box around the button) and focuses its first item.
 * Returns a function that closes it without calling `onClose` (the frame unmounting, or the button
 * pressed again).
 */
export function openExportMenu(host: HTMLElement, o: ExportMenuOptions): () => void {
  const words = spanish() ? WORDS.es : WORDS.en;
  const menu = document.createElement('div');
  menu.setAttribute('role', 'menu');
  menu.setAttribute('data-widget-export-menu', '');
  menu.style.cssText = MENU_STYLE;

  let open = true;
  const close = (): void => {
    if (!open) return;
    open = false;
    document.removeEventListener('pointerdown', away);
    menu.remove();
  };
  const done = (refocus: boolean): void => {
    if (!open) return;
    close();
    o.onClose(refocus);
  };
  // A press outside the button and the menu closes it, leaving the focus where the viewer put it.
  function away(e: Event): void {
    if (!(e.target instanceof Node) || !host.contains(e.target)) done(false);
  }

  const csv = async (): Promise<void> => {
    try {
      const table = await o.exportRows?.();
      if (table) downloadCsv(table);
    } catch (err) {
      // Loud, not silent: a click that produced no file says why in the console.
      console.error('Widget export failed', err);
    }
    done(true);
  };
  const png = async (): Promise<void> => {
    const url = o.image?.({ backgroundColor: opaqueBackground(o.frameElement()) });
    if (url) {
      // A chart that also exports rows names its image for the same span and bucket as its CSV.
      const named = await Promise.resolve()
        .then(() => o.exportRows?.())
        .then((t) => t?.name, () => undefined);
      const a = document.createElement('a');
      a.href = url;
      a.download = `${safeFileStem(named ?? `${o.title ?? ''}-${fileStamp(new Date())}`)}.png`;
      document.body.appendChild(a);
      a.click();
      a.remove();
    }
    done(true);
  };

  const items: HTMLButtonElement[] = [];
  const add = (label: string, run: () => Promise<void>): void => {
    const item = document.createElement('button');
    item.type = 'button';
    item.setAttribute('role', 'menuitem');
    item.style.cssText = ITEM_STYLE;
    item.textContent = label;
    item.addEventListener('click', () => void run());
    items.push(item);
    menu.appendChild(item);
  };
  if (o.exportRows) add(words.csv, csv);
  if (o.image) add(words.image, png);

  // Arrow keys move between the items (a menu's own convention), Escape closes it and gives the focus
  // back to the button, and Tab leaves it.
  menu.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      e.stopPropagation();
      done(true);
      return;
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const at = items.indexOf(document.activeElement as HTMLButtonElement);
    items[(at + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length]?.focus();
    e.preventDefault();
  });

  document.addEventListener('pointerdown', away);
  host.appendChild(menu);
  items[0]?.focus();
  return close;
}

// What the frame hands the button, read again each time the menu opens.
export type ExportSource = Omit<ExportMenuOptions, 'onClose'>;

// A down arrow onto a tray: a constant, never built from data.
const ICON =
  '<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" ' +
  'stroke-linejoin="round" aria-hidden="true"><path d="M8 2v8M4.5 6.5 8 10l3.5-3.5M2.5 13.5h11"/></svg>';

/**
 * Draws the export button (`size` px square) inside `host`, the box the frame reserves for it in the
 * title bar. The button opens and closes the menu. Returns a function that removes both (the frame
 * unmounting, or no longer having anything to export).
 */
export function mountExportButton(host: HTMLElement, size: number, source: () => ExportSource): () => void {
  const words = spanish() ? WORDS.es : WORDS.en;
  const button = document.createElement('button');
  button.type = 'button';
  button.setAttribute('aria-label', words.button);
  button.title = words.button;
  button.setAttribute('aria-haspopup', 'menu');
  button.innerHTML = ICON;
  const look = (open: boolean): void => {
    button.setAttribute('aria-expanded', String(open));
    button.style.cssText =
      `width:${size}px;height:${size}px;padding:0;display:inline-flex;align-items:center;justify-content:center;border:none;` +
      `border-radius:4px;color:hsl(var(--muted-foreground));cursor:pointer;background:${open ? 'hsl(var(--muted))' : 'transparent'}`;
  };
  look(false);

  let close: (() => void) | undefined;
  button.addEventListener('click', () => {
    if (close) {
      close();
      close = undefined;
      look(false);
      return;
    }
    look(true);
    const s = source();
    close = openExportMenu(host, {
      // Read at the click, so an item acts on the widget as it is then.
      exportRows: s.exportRows && (() => source().exportRows?.()),
      image: s.image && ((o) => source().image?.(o)),
      title: s.title,
      frameElement: s.frameElement,
      onClose: (refocus) => {
        close = undefined;
        look(false);
        if (refocus) button.focus();
      },
    });
  });
  host.appendChild(button);
  return () => {
    close?.();
    button.remove();
  };
}
