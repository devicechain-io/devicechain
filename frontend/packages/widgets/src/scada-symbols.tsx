// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The process symbols, as SVG on a 100 x 100 viewBox: a pump, valve, tank, motor, fan, pipe segment,
// sensor, breaker and light. One drawing per symbol, painted by STATE from the theme's tokens, so a
// symbol follows light and dark with no colour of its own.
//
//   - running: the accent colour (or the severity colour of the band the value is in); a pump, motor
//     and fan TURN, a pipe's dashes MOVE along it (reversed for a negative value), a lamp glows;
//   - stopped: the muted foreground, still;
//   - fault: the severity colour, a "!" badge that pulses;
//   - unknown: hollow and dashed, so a symbol that has not reported is never read as a stopped one.
//
// 🔴 Motion is SMIL (<animateTransform>, <animate>), not CSS: a host under `style-src 'self'` (the
// strict-CSP board replay) refuses an inline <style>, and SMIL is not governed by it. Motion is also
// OFF for a viewer who asked for reduced motion; the state is then carried by shape, colour and the
// caption alone, and nothing in the symbol depends on seeing it move.

import { css } from '@devicechain/widget-sdk';
import { useId, type ComponentType, type ReactNode } from 'react';

import type { FlowDirection, ScadaState, ScadaSymbol } from './scada-model';

export interface SymbolPaint {
  state: ScadaState;
  // The colour of an active or fault part: the band's severity colour, else the accent.
  accent: string;
  flow: FlowDirection;
  // Motion allowed (the viewer has not asked for reduced motion).
  motion: boolean;
  // A tank's level, 0..1.
  fill?: number;
  horizontal?: boolean;
}

const STROKE = 3;

const ink = () => css('foreground');
const muted = () => css('muted-foreground');
const body = () => css('card');

// The colour of the symbol's working parts in a state.
function working(p: SymbolPaint): string {
  return p.state === 'running' || p.state === 'fault' ? p.accent : muted();
}

const spin = (dur: string, clockwise = true): ReactNode => (
  <animateTransform attributeName="transform" type="rotate" from={`0 50 50`} to={`${clockwise ? 360 : -360} 50 50`} dur={dur} repeatCount="indefinite" />
);

function Badge({ p }: { p: SymbolPaint }) {
  if (p.state !== 'fault') return null;
  return (
    <g data-fault-badge="">
      <circle cx="84" cy="16" r="12" fill={p.accent} stroke={ink()} strokeWidth={2} />
      <text x="84" y="22" textAnchor="middle" fontSize="18" fontWeight="700" fill={css('background')}>
        !
      </text>
      {p.motion && <animate attributeName="opacity" values="1;0.35;1" dur="1s" repeatCount="indefinite" />}
    </g>
  );
}

function Pump(p: SymbolPaint) {
  const on = p.state === 'running';
  return (
    <g>
      <path d="M8 62 H22 M78 38 V22 H92" stroke={ink()} strokeWidth={STROKE} fill="none" strokeLinecap="round" />
      <circle cx="50" cy="50" r="30" fill={body()} stroke={ink()} strokeWidth={STROKE} />
      <g stroke={working(p)} strokeWidth={5} strokeLinecap="round" fill="none">
        <g data-motion={on && p.motion ? 'spin' : undefined}>
          <path d="M50 50 L50 28 M50 50 L69 61 M50 50 L31 61" />
          {on && p.motion && spin('1.2s')}
        </g>
      </g>
      <circle cx="50" cy="50" r="5" fill={working(p)} />
      <Badge p={p} />
    </g>
  );
}

function Motor(p: SymbolPaint) {
  const on = p.state === 'running';
  return (
    <g>
      <path d="M4 50 H16 M84 50 H96" stroke={ink()} strokeWidth={STROKE} strokeLinecap="round" />
      <rect x="16" y="26" width="68" height="48" rx="8" fill={body()} stroke={ink()} strokeWidth={STROKE} />
      <path d="M26 26 V74 M34 26 V74" stroke={muted()} strokeWidth={2} />
      <g data-motion={on && p.motion ? 'spin' : undefined}>
        <circle cx="58" cy="50" r="16" fill="none" stroke={working(p)} strokeWidth={4} strokeDasharray="14 8" />
        {on && p.motion && (
          <animateTransform attributeName="transform" type="rotate" from="0 58 50" to="360 58 50" dur="1.4s" repeatCount="indefinite" />
        )}
      </g>
      <text x="58" y="56" textAnchor="middle" fontSize="16" fontWeight="700" fill={working(p)}>
        M
      </text>
      <Badge p={p} />
    </g>
  );
}

function Fan(p: SymbolPaint) {
  const on = p.state === 'running';
  return (
    <g>
      <circle cx="50" cy="50" r="38" fill={body()} stroke={ink()} strokeWidth={STROKE} />
      <g data-motion={on && p.motion ? 'spin' : undefined}>
        <g fill={working(p)} opacity={0.9}>
          <path d="M50 50 C44 28 56 12 66 22 C72 30 60 44 50 50 Z" />
          <path d="M50 50 C72 44 88 56 78 66 C70 72 56 60 50 50 Z" transform="rotate(0 50 50)" />
          <path d="M50 50 C56 72 44 88 34 78 C28 70 40 56 50 50 Z" />
        </g>
        {on && p.motion && spin('0.9s')}
      </g>
      <circle cx="50" cy="50" r="6" fill={ink()} />
      <Badge p={p} />
    </g>
  );
}

function Valve(p: SymbolPaint) {
  const open = p.state === 'running';
  return (
    <g>
      <path d="M4 56 H30 M70 56 H96" stroke={ink()} strokeWidth={STROKE} strokeLinecap="round" />
      {/* the body: two opposed triangles; filled when the valve passes flow */}
      <path d="M30 36 L50 56 L30 76 Z M70 36 L50 56 L70 76 Z" fill={open ? working(p) : p.state === 'fault' ? working(p) : body()} stroke={ink()} strokeWidth={STROKE} strokeLinejoin="round" opacity={open || p.state === 'fault' ? 0.85 : 1} />
      {/* the stem and the handwheel */}
      <path d="M50 56 V30" stroke={ink()} strokeWidth={STROKE} />
      <path d="M36 28 H64" stroke={ink()} strokeWidth={5} strokeLinecap="round" />
      {/* closed: a bar across the bore */}
      {p.state === 'stopped' && <path d="M50 40 V72" stroke={muted()} strokeWidth={5} strokeLinecap="round" data-closed="" />}
      <Badge p={p} />
    </g>
  );
}

function Tank(p: SymbolPaint) {
  // One clip per tank on the board: a repeated id would clip a second tank by the first's geometry.
  const clip = `scada-tank-${useId().replace(/:/g, '')}`;
  const level = Math.min(1, Math.max(0, p.fill ?? 0));
  const top = 14;
  const bottom = 90;
  const h = (bottom - top) * level;
  return (
    <g>
      <clipPath id={clip}>
        <path d="M22 14 H78 V84 Q78 90 72 90 H28 Q22 90 22 84 Z" />
      </clipPath>
      <path d="M22 14 H78 V84 Q78 90 72 90 H28 Q22 90 22 84 Z" fill={body()} />
      {p.state !== 'unknown' && (
        <g clipPath={`url(#${clip})`}>
          <rect data-liquid="" x="22" y={bottom - h} width="56" height={h} fill={working(p)} opacity={0.6} />
          {level > 0 && level < 1 && <path d={`M22 ${bottom - h} H78`} stroke={p.accent} strokeWidth={2} />}
        </g>
      )}
      <path d="M22 14 H78 V84 Q78 90 72 90 H28 Q22 90 22 84 Z" fill="none" stroke={ink()} strokeWidth={STROKE} strokeDasharray={p.state === 'unknown' ? '6 4' : undefined} />
      <path d="M12 76 H22 M78 30 H90" stroke={ink()} strokeWidth={STROKE} strokeLinecap="round" />
      <Badge p={p} />
    </g>
  );
}

function Pipe(p: SymbolPaint) {
  const flowing = p.flow !== 'none' && p.state !== 'unknown';
  const dash = '8 8';
  const to = p.flow === 'reverse' ? 16 : -16;
  const v = p.horizontal === false;
  const d = v ? 'M50 4 V96' : 'M4 50 H96';
  return (
    <g>
      <path d={d} stroke={ink()} strokeWidth={22} strokeLinecap="butt" fill="none" />
      <path d={d} stroke={body()} strokeWidth={16} strokeLinecap="butt" fill="none" />
      <path
        d={d}
        data-flow={flowing ? p.flow : 'none'}
        data-motion={flowing && p.motion ? 'flow' : undefined}
        stroke={flowing || p.state === 'fault' ? p.accent : muted()}
        strokeWidth={8}
        strokeLinecap="butt"
        fill="none"
        strokeDasharray={flowing || p.state === 'fault' ? dash : '2 10'}
      >
        {flowing && p.motion && <animate attributeName="stroke-dashoffset" from="0" to={String(to)} dur="0.8s" repeatCount="indefinite" />}
      </path>
      <Badge p={p} />
    </g>
  );
}

function Sensor(p: SymbolPaint) {
  return (
    <g>
      <circle cx="50" cy="50" r="34" fill={body()} stroke={ink()} strokeWidth={STROKE} strokeDasharray={p.state === 'unknown' ? '6 4' : undefined} />
      <circle cx="50" cy="50" r="18" fill="none" stroke={working(p)} strokeWidth={5} />
      <circle cx="50" cy="50" r="5" fill={working(p)} />
      <path d="M50 16 V4 M50 84 V96" stroke={ink()} strokeWidth={STROKE} strokeLinecap="round" />
      <Badge p={p} />
    </g>
  );
}

// A single-line-diagram breaker: a contact arm between two terminals. Closed: the arm meets the
// lower terminal. Open: it swings clear. Tripped (fault): clear, with the badge.
function Breaker(p: SymbolPaint) {
  const closed = p.state === 'running';
  return (
    <g>
      <path d="M50 4 V30 M50 76 V96" stroke={ink()} strokeWidth={STROKE} strokeLinecap="round" />
      <circle cx="50" cy="30" r="5" fill={body()} stroke={ink()} strokeWidth={STROKE} />
      <circle cx="50" cy="76" r="5" fill={body()} stroke={ink()} strokeWidth={STROKE} />
      <path data-contact={closed ? 'closed' : 'open'} d={closed ? 'M50 30 V76' : 'M50 30 L74 66'} stroke={working(p)} strokeWidth={7} strokeLinecap="round" />
      <Badge p={p} />
    </g>
  );
}

function Light(p: SymbolPaint) {
  const lit = p.state === 'running';
  return (
    <g>
      {lit && (
        <g data-glow="" opacity={0.35}>
          <circle cx="50" cy="46" r="42" fill={p.accent} />
          {p.motion && <animate attributeName="opacity" values="0.35;0.5;0.35" dur="2.4s" repeatCount="indefinite" />}
        </g>
      )}
      <circle cx="50" cy="46" r="26" fill={lit ? p.accent : body()} stroke={ink()} strokeWidth={STROKE} strokeDasharray={p.state === 'unknown' ? '6 4' : undefined} />
      <path d="M38 84 H62 M42 92 H58" stroke={ink()} strokeWidth={STROKE} strokeLinecap="round" />
      <path d="M40 72 H60" stroke={ink()} strokeWidth={STROKE} />
      <Badge p={p} />
    </g>
  );
}

const DRAW: Record<ScadaSymbol, ComponentType<SymbolPaint>> = {
  pump: Pump,
  valve: Valve,
  tank: Tank,
  motor: Motor,
  fan: Fan,
  pipe: Pipe,
  sensor: Sensor,
  breaker: Breaker,
  light: Light,
};

export function SymbolArt({ symbol, paint, label }: { symbol: ScadaSymbol; paint: SymbolPaint; label: string }) {
  const Draw = DRAW[symbol];
  return (
    <svg
      viewBox="0 0 100 100"
      preserveAspectRatio="xMidYMid meet"
      role="img"
      aria-label={label}
      data-symbol={symbol}
      data-state={paint.state}
      data-flow={paint.flow}
      data-motion-on={paint.motion ? '' : undefined}
      style={{ width: '100%', height: '100%', display: 'block', overflow: 'visible' }}
    >
      {/* No reading: the symbol is drawn dashed and faint, so it is never read as a stopped one. */}
      <g data-unknown={paint.state === 'unknown' ? '' : undefined} strokeDasharray={paint.state === 'unknown' ? '5 4' : undefined} opacity={paint.state === 'unknown' ? 0.55 : undefined}>
        <Draw {...paint} />
      </g>
    </svg>
  );
}
