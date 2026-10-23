// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// An EXPLAINED empty state: the body a widget shows when it has nothing to draw, saying which of the
// few reasons it is. A bare dash or a blank card cannot tell "nobody picked a device" from "this
// device never reports that" from "the device is quiet right now", and a board that does not say
// looks broken. The kinds are:
//
//   unbound       no device (or slot binding) is chosen: the author's job
//   not-reported  the device's active profile does not declare this measurement: it will never report it
//   no-data       declared, but nothing arrived in the window; `hint` can say what the last value was
//   forbidden     the platform refused the read for this viewer
//   error         the source failed to answer
//
// Styling is deliberately NEUTRAL (ISA-101): the muted foreground, a quiet glyph, no severity hue and no
// warning symbol. Alarm colour means an alarm on a board, so a state that is not one never borrows it.
// It sizes by the frame's content column: a small widget keeps the title, a roomy one adds the
// explanation and the hint.

import type { CSSProperties } from 'react';

import { useWidgetBodySize } from './context';
import { viewerLocale } from './freshness';
import { css } from './theme';
import { fontVar, spaceVar } from './tokens';

export type WidgetEmptyKind = 'unbound' | 'not-reported' | 'no-data' | 'forbidden' | 'error';

export interface WidgetEmptyStrings {
  unbound: { title: string; detail: string };
  notReported: { title: string; detail: (measurement: string | undefined) => string };
  noData: { title: string; detail: (measurement: string | undefined) => string };
  forbidden: { title: string; detail: string };
  error: { title: string; detail: string };
}

export const WIDGET_EMPTY_STRINGS: Record<'en' | 'es', WidgetEmptyStrings> = {
  en: {
    unbound: { title: 'No device chosen', detail: 'Choose a device for this widget to show its data.' },
    notReported: {
      title: 'Not reported by this device',
      detail: (m) => `This device's profile does not include ${m ? `"${m}"` : 'this measurement'}, so it will not report it.`,
    },
    noData: {
      title: 'No data in this range',
      detail: (m) => `Nothing was reported${m ? ` for "${m}"` : ''} in the selected time range.`,
    },
    forbidden: { title: 'No access', detail: 'You do not have access to this data.' },
    error: { title: 'Data unavailable', detail: 'The data source did not answer.' },
  },
  es: {
    unbound: { title: 'Sin dispositivo elegido', detail: 'Elija un dispositivo para que este widget muestre sus datos.' },
    notReported: {
      title: 'Este dispositivo no lo informa',
      detail: (m) => `El perfil de este dispositivo no incluye ${m ? `"${m}"` : 'esta medición'}, por lo que no la informará.`,
    },
    noData: {
      title: 'Sin datos en este rango',
      detail: (m) => `No se informó nada${m ? ` de "${m}"` : ''} en el rango de tiempo elegido.`,
    },
    forbidden: { title: 'Sin acceso', detail: 'No tiene acceso a estos datos.' },
    error: { title: 'Datos no disponibles', detail: 'El origen de datos no respondió.' },
  },
};

export function widgetEmptyStrings(locale: string | undefined = viewerLocale()): WidgetEmptyStrings {
  return locale?.toLowerCase().split('-')[0] === 'es' ? WIDGET_EMPTY_STRINGS.es : WIDGET_EMPTY_STRINGS.en;
}

// The glyph per kind. Quiet and non-alarming; the access refusal keeps the circle-slash the board's
// other settled panes use.
const GLYPH: Record<WidgetEmptyKind, string> = {
  unbound: '○',
  'not-reported': '∅',
  'no-data': '∅',
  forbidden: '⊘',
  error: '…',
};

// The sizes (px of the content column) at which the explanation and the hint make room for the title.
const DETAIL_MIN = { width: 150, height: 84 };
const HINT_MIN = { width: 150, height: 100 };
const GLYPH_MIN_HEIGHT = 56;

export interface WidgetEmptyStateProps {
  kind: WidgetEmptyKind;
  // The measurement the widget was asked for, named in the explanation.
  measurement?: string;
  // One line after the explanation: for no-data, the last value known and when ("Last value 21.4 °C · 3 hr. ago").
  hint?: string;
  // Replaces the stock explanation (an error's own message, say).
  detail?: string;
  locale?: string;
}

export function WidgetEmptyState({ kind, measurement, hint, detail, locale }: WidgetEmptyStateProps) {
  const size = useWidgetBodySize();
  const words = widgetEmptyStrings(locale);
  const copy =
    kind === 'unbound'
      ? { title: words.unbound.title, detail: words.unbound.detail }
      : kind === 'not-reported'
        ? { title: words.notReported.title, detail: words.notReported.detail(measurement) }
        : kind === 'no-data'
          ? { title: words.noData.title, detail: words.noData.detail(measurement) }
          : kind === 'forbidden'
            ? words.forbidden
            : words.error;
  // Unmeasured (first frame, jsdom) shows everything.
  const measured = size.width > 0 && size.height > 0;
  const room = (min: { width: number; height: number }) => !measured || (size.width >= min.width && size.height >= min.height);
  const text = detail ?? copy.detail;
  const lines: CSSProperties = { overflowWrap: 'anywhere' };
  return (
    <div
      role="status"
      data-empty-state={kind}
      title={`${copy.title}. ${text}`}
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        gap: spaceVar(1),
        width: '100%',
        height: '100%',
        boxSizing: 'border-box',
        padding: spaceVar(2),
        textAlign: 'center',
        overflow: 'hidden',
        color: css('muted-foreground'),
      }}
    >
      {!measured || size.height >= GLYPH_MIN_HEIGHT ? (
        <div aria-hidden="true" style={{ fontSize: fontVar('lg'), lineHeight: 1 }}>
          {GLYPH[kind]}
        </div>
      ) : null}
      <div data-empty-title="" style={{ ...lines, fontSize: fontVar('sm'), fontWeight: 600 }}>
        {copy.title}
      </div>
      {room(DETAIL_MIN) ? (
        <div data-empty-detail="" style={{ ...lines, fontSize: fontVar('xs'), opacity: 0.85 }}>
          {text}
        </div>
      ) : null}
      {hint && room(HINT_MIN) ? (
        <div data-empty-hint="" style={{ ...lines, fontSize: fontVar('xs'), fontVariantNumeric: 'tabular-nums' }}>
          {hint}
        </div>
      ) : null}
    </div>
  );
}

// The words of the hint, in the widget packages' two languages.
export interface LastValueStrings {
  lastValue: (value: string, age: string) => string;
}

export const LAST_VALUE_STRINGS: Record<'en' | 'es', LastValueStrings> = {
  en: { lastValue: (value, age) => `Last value ${value} · ${age}` },
  es: { lastValue: (value, age) => `Último valor ${value} · ${age}` },
};

export function lastValueHint(value: string, age: string, locale: string | undefined = viewerLocale()): string {
  return (locale?.toLowerCase().split('-')[0] === 'es' ? LAST_VALUE_STRINGS.es : LAST_VALUE_STRINGS.en).lastValue(value, age);
}
