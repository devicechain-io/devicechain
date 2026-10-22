// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The frame's export control: one small button in the title bar that opens a menu (Download CSV,
// Download image). The button is a chunk of its own, fetched by a frame whose widget has something to
// export (frame.tsx), and the menu, the CSV encoder and the PNG download are another, fetched the first
// time the viewer opens the menu: a board's first download holds neither.

import { useEffect, useRef, useState, type ComponentType } from 'react';

import type { WidgetImageSource } from './context';
import type { ExportMenuProps } from './export-menu';
import { viewerLocale } from './freshness';
import { css } from './theme';
import type { WidgetExportResult } from './widget-export';

const LABEL = { en: 'Export data', es: 'Exportar datos' };

export interface ExportButtonProps {
  // The button's side (px): the title bar reserves this much beside the title (frame.tsx).
  size: number;
  // Called when the viewer picks CSV: builds the table from what the widget is showing right now.
  exportRows: (() => WidgetExportResult) | undefined;
  image: WidgetImageSource | undefined;
  // The frame's own title, the name of a file with nothing better to call it.
  title: string | undefined;
  // The element whose background an image is drawn on.
  frameElement: () => Element | null;
}

export function ExportButton({ size, exportRows, image, title, frameElement }: ExportButtonProps) {
  const [open, setOpen] = useState(false);
  const [Menu, setMenu] = useState<{ component: ComponentType<ExportMenuProps> }>();
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const label = viewerLocale()?.toLowerCase().startsWith('es') ? LABEL.es : LABEL.en;

  useEffect(() => {
    if (!open) return;
    const away = (e: Event): void => {
      if (e.target instanceof Node && !root.current?.contains(e.target)) setOpen(false);
    };
    document.addEventListener('pointerdown', away);
    return () => document.removeEventListener('pointerdown', away);
  }, [open]);

  const toggle = (): void => {
    if (open) return setOpen(false);
    setOpen(true);
    // A chunk that does not arrive closes the menu; the next click fetches it again.
    if (!Menu) import('./export-menu').then((m) => setMenu({ component: m.ExportMenu }), () => setOpen(false));
  };

  return (
    <div
      ref={root}
      data-widget-export=""
      onKeyDown={(e) => {
        if (e.key === 'Escape' && open) {
          e.stopPropagation();
          setOpen(false);
          trigger.current?.focus();
        }
      }}
      style={{ position: 'absolute', top: 0, right: 0, bottom: 0, display: 'flex', alignItems: 'center', paddingRight: 4 }}
    >
      <button
        ref={trigger}
        type="button"
        aria-label={label}
        title={label}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={toggle}
        style={{
          width: size,
          height: size,
          padding: 0,
          display: 'inline-flex',
          alignItems: 'center',
          justifyContent: 'center',
          border: 'none',
          borderRadius: 4,
          background: open ? css('muted') : 'transparent',
          color: css('muted-foreground'),
          cursor: 'pointer',
        }}
      >
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M8 2v8M4.5 6.5 8 10l3.5-3.5M2.5 13.5h11" />
        </svg>
      </button>
      {open && Menu ? (
        <Menu.component
          exportRows={exportRows}
          image={image}
          title={title}
          frameElement={frameElement}
          onDone={() => {
            setOpen(false);
            trigger.current?.focus();
          }}
        />
      ) : null}
    </div>
  );
}
