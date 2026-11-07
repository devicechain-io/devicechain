// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a process symbol (a pump, a valve, a breaker...) is doing, decided apart from the drawing and
// from the hub. Pure, so each rule is a test.
//
// ONE measurement drives a symbol, and the threshold bands say what is wrong:
//
//   - no number                                  -> `unknown`  (drawn hollow; never `stopped`, which
//                                                    would be a claim about the machine)
//   - the value is in a major or critical band   -> `fault`
//   - a switching symbol, value at or above onAt -> `running` (a valve OPEN, a breaker CLOSED, a lamp ON)
//   - a switching symbol, value below onAt       -> `stopped`
//   - a pipe: |value| below onAt -> `stopped`, a positive value flows forward, a negative one reverse
//   - a tank or a sensor shows a LEVEL or a READING, so it is `running` whenever it has a number
//
// A signal that is 1 while the machine runs and 0 while it does not needs no options; a speed or a
// current reads the same way with `onAt` set to the lowest value that counts as running.

import type { Threshold } from '@devicechain/widget-sdk';

export const SCADA_SYMBOLS = ['pump', 'valve', 'tank', 'motor', 'fan', 'pipe', 'sensor', 'breaker', 'light'] as const;
export type ScadaSymbol = (typeof SCADA_SYMBOLS)[number];

export type ScadaState = 'unknown' | 'stopped' | 'running' | 'fault';

export function isScadaSymbol(v: unknown): v is ScadaSymbol {
  return typeof v === 'string' && (SCADA_SYMBOLS as readonly string[]).includes(v);
}

// The symbols that show a quantity rather than switch.
export function isContinuous(symbol: ScadaSymbol): boolean {
  return symbol === 'tank' || symbol === 'sensor';
}

// A symbol that turns or moves while it runs.
export function isRotating(symbol: ScadaSymbol): boolean {
  return symbol === 'pump' || symbol === 'motor' || symbol === 'fan';
}

// The value at or above which a switching symbol is on. Default 1: the 0/1 signal.
export const DEFAULT_ON_AT = 1;

export type FlowDirection = 'forward' | 'reverse' | 'none';

export interface ScadaReading {
  state: ScadaState;
  // Only a pipe has a direction; everything else is `none`.
  flow: FlowDirection;
}

export function isFaultBand(band: Threshold | undefined): boolean {
  return band !== undefined && (band.level === 'major' || band.level === 'critical');
}

export function readSymbol(symbol: ScadaSymbol, value: number | undefined, onAt: number | undefined, band: Threshold | undefined): ScadaReading {
  if (value === undefined || !Number.isFinite(value)) return { state: 'unknown', flow: 'none' };
  const threshold = onAt ?? DEFAULT_ON_AT;
  // Zero is never a flow, whatever onAt says (an onAt of 0 would otherwise make 0 >= 0 "flowing").
  const flow: FlowDirection = symbol === 'pipe' && value !== 0 && Math.abs(value) >= Math.abs(threshold) ? (value < 0 ? 'reverse' : 'forward') : 'none';
  if (isFaultBand(band)) return { state: 'fault', flow };
  if (isContinuous(symbol)) return { state: 'running', flow: 'none' };
  if (symbol === 'pipe') return { state: flow === 'none' ? 'stopped' : 'running', flow };
  return { state: value >= threshold ? 'running' : 'stopped', flow: 'none' };
}

// The word that says what the symbol is doing, per symbol: a valve is open, not running.
export type StateWords = Record<ScadaState, string>;
