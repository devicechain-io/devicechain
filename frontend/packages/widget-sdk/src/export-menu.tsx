// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The frame's export menu and what its items do. Its own chunk: loaded by export-button.tsx the first
// time a viewer opens the menu, together with the CSV encoder it uses.
//
//   - Download CSV builds the table at the moment of the click, from what the widget is showing;
//   - Download image reads the chart's canvas on the card's own background (a canvas is transparent,
//     and a dark theme's light text on white is unreadable), at twice the on-screen resolution.

import { useEffect, useRef, type CSSProperties, type KeyboardEvent } from 'react';

import type { WidgetImageSource } from './context';
import { downloadCsv, safeFileStem } from './export-csv';
import { viewerLocale } from './freshness';
import { css } from './theme';
import type { WidgetExportResult } from './widget-export';

const WORDS = {
  en: { csv: 'Download CSV', image: 'Download image (PNG)' },
  es: { csv: 'Descargar CSV', image: 'Descargar imagen (PNG)' },
};

export interface ExportMenuProps {
  exportRows: (() => WidgetExportResult) | undefined;
  image: WidgetImageSource | undefined;
  title: string | undefined;
  frameElement: () => Element | null;
  onDone: () => void;
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

const itemStyle: CSSProperties = {
  display: 'block',
  width: '100%',
  padding: '6px 12px',
  border: 'none',
  background: 'transparent',
  color: css('card-foreground'),
  font: 'inherit',
  fontSize: '0.8125rem',
  textAlign: 'left',
  whiteSpace: 'nowrap',
  cursor: 'pointer',
};

export function ExportMenu({ exportRows, image, title, frameElement, onDone }: ExportMenuProps) {
  const words = viewerLocale()?.toLowerCase().startsWith('es') ? WORDS.es : WORDS.en;
  const first = useRef<HTMLButtonElement>(null);
  useEffect(() => first.current?.focus(), []);

  const csv = async (): Promise<void> => {
    try {
      const table = await exportRows?.();
      if (table) downloadCsv(table);
    } catch (err) {
      // Loud, not silent: a click that produced no file says why in the console.
      console.error('Widget export failed', err);
    }
    onDone();
  };
  const png = async (): Promise<void> => {
    const url = image?.({ backgroundColor: opaqueBackground(frameElement()) });
    if (url) {
      // A chart that also exports rows names its image for the same span and bucket as its CSV.
      const named = await Promise.resolve(exportRows?.()).then((t) => t?.name, () => undefined);
      const stem = safeFileStem(named ?? `${title ?? ''}-${fileStamp(new Date())}`);
      const a = document.createElement('a');
      a.href = url;
      a.download = `${stem}.png`;
      document.body.appendChild(a);
      a.click();
      a.remove();
    }
    onDone();
  };

  // Arrow keys move between the items (a menu's own convention); Tab leaves it.
  const move = (e: KeyboardEvent<HTMLDivElement>): void => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const items = [...e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')];
    const at = items.indexOf(document.activeElement as HTMLButtonElement);
    items[(at + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length]?.focus();
    e.preventDefault();
  };

  const items: Array<{ key: string; label: string; run: () => Promise<void> }> = [];
  if (exportRows) items.push({ key: 'csv', label: words.csv, run: csv });
  if (image) items.push({ key: 'png', label: words.image, run: png });

  return (
    <div
      role="menu"
      data-widget-export-menu=""
      onKeyDown={move}
      style={{
        position: 'absolute',
        top: '100%',
        right: 4,
        zIndex: 5,
        marginTop: 2,
        padding: '4px 0',
        minWidth: 160,
        background: css('card'),
        border: `1px solid ${css('border')}`,
        borderRadius: 6,
        boxShadow: '0 4px 12px rgb(0 0 0 / 0.15)',
      }}
    >
      {items.map((item, i) => (
        <button key={item.key} ref={i === 0 ? first : undefined} type="button" role="menuitem" style={itemStyle} onClick={item.run}>
          {item.label}
        </button>
      ))}
    </div>
  );
}
