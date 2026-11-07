// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// scada-symbol -- a process symbol (pump, valve, tank, motor, fan, pipe segment, sensor, breaker or
// light) bound to ONE measurement of a device, drawn in the state that measurement puts it in. The
// states are running, stopped and fault, a pipe's flow has a direction, and the symbol is painted from
// the theme's tokens, so it follows light and dark with no colour of its own.
//
// The binding is the board's ordinary one: the datasource names a device or a slot (so a board of
// symbols re-points at another site with a binding manifest, like any other widget), `measurement`
// names the signal, `onAt` says what counts as on, and the threshold bands say what is a fault.
// What the symbol is doing is ALSO printed as text under it, so colour and motion are never the only
// cue. See scada-model.ts for the rules.

import { useEffect, useMemo, useState } from 'react';

import {
  css,
  fontVar,
  formatValue,
  optNumber,
  optString,
  optThresholds,
  WidgetFrame,
  type WidgetProps,
  type WidgetScale,
} from '@devicechain/widget-sdk';

import { emptyReason, useLevelReading, type LevelReading } from '../level-reading';
import { LevelEmptyFor } from '../no-reading';
import { isContinuous, isScadaSymbol, readSymbol, type ScadaSymbol } from '../scada-model';
import { scadaStrings } from '../scada-strings';
import { SymbolArt } from '../scada-symbols';
import { readRange } from '../value-scale';

// Whether the viewer asked the system for reduced motion. Read once, and followed if it changes.
export function usePrefersReducedMotion(): boolean {
  const query = typeof window !== 'undefined' && typeof window.matchMedia === 'function' ? window.matchMedia('(prefers-reduced-motion: reduce)') : undefined;
  const [reduced, setReduced] = useState(query?.matches === true);
  useEffect(() => {
    if (!query) return;
    const on = () => setReduced(query.matches);
    on();
    query.addEventListener?.('change', on);
    return () => query.removeEventListener?.('change', on);
  }, [query]);
  return reduced;
}

export function ScadaSymbol({ widget, data }: WidgetProps) {
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const symbolOption = optString(widget.options, 'symbol');
  const symbol: ScadaSymbol = isScadaSymbol(symbolOption) ? symbolOption : 'pump';
  const reading = useLevelReading(widget, data, {
    thresholds,
    unit: optString(widget.options, 'unit'),
    precision: optNumber(widget.options, 'precision'),
    // A tank is the one symbol with a scale; both ends are needed, none is invented.
    range: symbol === 'tank' ? readRange(optNumber(widget.options, 'min'), optNumber(widget.options, 'max')) : undefined,
  });
  const title = optString(widget.options, 'title') ?? reading.name;
  const onAt = optNumber(widget.options, 'onAt');
  const vertical = optString(widget.options, 'orientation') === 'vertical';
  return (
    <WidgetFrame title={title} sampleTime={reading.sample?.occurredTime ?? null}>
      {(scale) => <SymbolBody symbol={symbol} reading={reading} onAt={onAt} vertical={vertical} scale={scale} />}
    </WidgetFrame>
  );
}

function SymbolBody({ symbol, reading: r, onAt, vertical, scale: ws }: { symbol: ScadaSymbol; reading: LevelReading; onAt: number | undefined; vertical: boolean; scale: WidgetScale }) {
  const motion = !usePrefersReducedMotion();
  const words = scadaStrings();
  // A tank without a usable scale is a configuration problem, said before the absence of data.
  const reason = symbol === 'tank' ? emptyReason(r) : undefined;
  if (reason !== undefined) return <LevelEmptyFor reading={r} reason={reason} />;

  const { state, flow } = readSymbol(symbol, r.value, onAt, r.band);
  const name = words.symbols[symbol];
  const stateWord = words.states[symbol][state];
  // What is printed under the symbol: the value of a tank or sensor, the state of a switching one.
  const showValue = isContinuous(symbol) && r.value !== undefined;
  const fill = symbol === 'tank' && r.fill ? r.fill.fraction : undefined;
  const reading = showValue ? `${formatValue(r.value, r.precision)}${r.unit ? ` ${r.unit}` : ''}` : undefined;
  const flowWord = symbol === 'pipe' && flow !== 'none' ? ` (${flow === 'reverse' ? words.reverse : words.forward})` : '';
  const caption = reading !== undefined ? `${reading}${state === 'fault' ? ` · ${stateWord}` : ''}` : `${stateWord}${flowWord}`;
  // The image's accessible name is the whole caption, alarm included.
  const label = words.label(name, caption);
  const captionRoom = ws.px('font-md') * 1.8;
  // The symbol is held to the aspect it is drawn at, from the room the body measures. An unmeasured
  // body (no layout yet, or a test renderer) sets no limit: a limit read off a 0 would shrink the
  // symbol to a pixel and leave it there until something re-rendered it.
  const room = ws.size.height - captionRoom;
  const measuredWidth = room > 0 ? `${room * (symbol === 'pipe' && !vertical ? 4 : 1.1)}px` : undefined;

  return (
    <div
      data-scada-state={state}
      data-threshold-level={r.band?.level}
      style={{ height: '100%', boxSizing: 'border-box', display: 'flex', flexDirection: 'column', alignItems: 'center', gap: ws.px('space-1'), minHeight: 0 }}
    >
      <div style={{ flex: '1 1 0', minHeight: ws.px('space-4'), width: '100%', maxWidth: measuredWidth }}>
        <SymbolArt
          symbol={symbol}
          label={label}
          paint={{ state, accent: r.fillColor, flow, motion, fill, horizontal: !vertical }}
        />
      </div>
      <div
        data-scada-caption=""
        style={{
          fontSize: fontVar('md'),
          fontWeight: 600,
          lineHeight: 1.2,
          textAlign: 'center',
          overflowWrap: 'anywhere',
          maxWidth: '100%',
          color: state === 'unknown' ? css('muted-foreground') : css('foreground'),
        }}
      >
        {caption}
      </div>
    </div>
  );
}
