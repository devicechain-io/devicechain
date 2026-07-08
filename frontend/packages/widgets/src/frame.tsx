// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The shared card frame every widget renders inside — a bordered, themed surface
// that fills its dashboard-canvas slot, with an optional title bar. Styled with
// inline CSS-variable references so it themes from the host without Tailwind.

import { createContext, useContext, type CSSProperties, type ReactNode } from 'react';

import type { SelectionTarget } from '@devicechain/dashboards';

import { css } from './theme';

// WidgetSubjectContext carries the resolved entity a widget shows (its datasource's
// bound device/anchor), computed once by the renderer and provided per widget. The
// frame reads it as a subtitle so every board answers "which asset is this?" — and,
// because it reads the RESOLVED binding, it updates for free once selection re-points a
// slot (ADR-039 selection-overlay amendment). Kept as ambient context, not a widget
// prop, so the pure widget contract (widget, data) stays untouched and no widget needs
// to know how a subject is resolved.
const WidgetSubjectContext = createContext<string | undefined>(undefined);

export function WidgetSubjectProvider({
  label,
  children,
}: {
  label: string | undefined;
  children: ReactNode;
}) {
  return <WidgetSubjectContext.Provider value={label}>{children}</WidgetSubjectContext.Provider>;
}

// WidgetSelect is a view-interaction callback (ADR-039 selection amendment): a widget
// drives a slot selection (an alarm-table originator drill, a context-selector pick) by
// naming its target slot + the entity to bind. Kept DISTINCT from WidgetActions (the
// write seam) — selection changes only the view's binding overlay, never the backend.
// Provided by the renderer from the host; absent (undefined) in edit mode and any host
// that wires no selection, so a widget feature-detects it before offering a drill.
export type WidgetSelect = (target: SelectionTarget) => void;

const WidgetSelectContext = createContext<WidgetSelect | undefined>(undefined);

export function WidgetSelectProvider({
  select,
  children,
}: {
  select: WidgetSelect | undefined;
  children: ReactNode;
}) {
  return <WidgetSelectContext.Provider value={select}>{children}</WidgetSelectContext.Provider>;
}

// useWidgetSelect returns the ambient selection callback, or undefined where selection is
// not wired (edit mode, a read-only embedder). A widget renders its drill affordance only
// when this is defined.
export function useWidgetSelect(): WidgetSelect | undefined {
  return useContext(WidgetSelectContext);
}

export interface WidgetFrameProps {
  title?: string;
  // An explicit subject line; when omitted the frame falls back to the resolved-entity
  // subject from WidgetSubjectContext.
  subtitle?: string;
  children: ReactNode;
  // Extra style for the body region (e.g. removing padding for a chart canvas).
  bodyStyle?: CSSProperties;
}

export function WidgetFrame({ title, subtitle, children, bodyStyle }: WidgetFrameProps) {
  const subject = useContext(WidgetSubjectContext);
  const sub = subtitle ?? subject;
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        width: '100%',
        height: '100%',
        boxSizing: 'border-box',
        background: css('card'),
        color: css('card-foreground'),
        border: `1px solid ${css('border')}`,
        borderRadius: 'var(--radius, 8px)',
        overflow: 'hidden',
      }}
    >
      {title || sub ? (
        <div
          style={{
            padding: '8px 12px',
            borderBottom: `1px solid ${css('border')}`,
          }}
        >
          {title ? (
            <div style={{ fontSize: 12, fontWeight: 600, color: css('muted-foreground') }}>{title}</div>
          ) : null}
          {sub ? (
            <div
              title={sub}
              style={{
                fontSize: 11,
                fontWeight: 400,
                color: css('muted-foreground'),
                opacity: 0.75,
                marginTop: title ? 2 : 0,
                whiteSpace: 'nowrap',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
              }}
            >
              {sub}
            </div>
          ) : null}
        </div>
      ) : null}
      <div style={{ flex: 1, minHeight: 0, ...bodyStyle }}>{children}</div>
    </div>
  );
}
