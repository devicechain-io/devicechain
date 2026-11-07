// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fixed words the process symbols render themselves: what each symbol is doing, in its own
// vocabulary (a valve is open, a breaker closed, a pump running). The widget packages carry no i18n
// runtime, so this is the one small per-language table (modelled on level-strings.ts); option labels
// are translated in the console's catalogs, not here.

import { viewerLocale } from '@devicechain/widget-sdk';

import type { ScadaState, ScadaSymbol } from './scada-model';

export interface ScadaStrings {
  // The name of each symbol, for the accessible label.
  symbols: Record<ScadaSymbol, string>;
  // What the symbol is doing, per symbol.
  states: Record<ScadaSymbol, Record<ScadaState, string>>;
  // A pipe's flow, in words (the colour and the motion are never the only cue).
  forward: string;
  reverse: string;
  // "Pump: Running"
  label: (symbol: string, state: string) => string;
}

const SWITCH = (stopped: string, running: string, fault: string, unknown: string): Record<ScadaState, string> => ({ stopped, running, fault, unknown });

export const SCADA_STRINGS: Record<'en' | 'es', ScadaStrings> = {
  en: {
    symbols: { pump: 'Pump', valve: 'Valve', tank: 'Tank', motor: 'Motor', fan: 'Fan', pipe: 'Pipe', sensor: 'Sensor', breaker: 'Breaker', light: 'Light' },
    states: {
      pump: SWITCH('Stopped', 'Running', 'Fault', 'No data'),
      valve: SWITCH('Closed', 'Open', 'Fault', 'No data'),
      tank: SWITCH('Level', 'Level', 'Alarm', 'No data'),
      motor: SWITCH('Stopped', 'Running', 'Fault', 'No data'),
      fan: SWITCH('Stopped', 'Running', 'Fault', 'No data'),
      pipe: SWITCH('No flow', 'Flowing', 'Fault', 'No data'),
      sensor: SWITCH('Reading', 'Reading', 'Alarm', 'No data'),
      breaker: SWITCH('Open', 'Closed', 'Tripped', 'No data'),
      light: SWITCH('Off', 'On', 'Fault', 'No data'),
    },
    forward: 'forward',
    reverse: 'reverse',
    label: (symbol, state) => `${symbol}: ${state}`,
  },
  es: {
    symbols: { pump: 'Bomba', valve: 'Válvula', tank: 'Depósito', motor: 'Motor', fan: 'Ventilador', pipe: 'Tubería', sensor: 'Sensor', breaker: 'Interruptor', light: 'Luz' },
    states: {
      pump: SWITCH('Detenida', 'En marcha', 'Falla', 'Sin datos'),
      valve: SWITCH('Cerrada', 'Abierta', 'Falla', 'Sin datos'),
      tank: SWITCH('Nivel', 'Nivel', 'Alarma', 'Sin datos'),
      motor: SWITCH('Detenido', 'En marcha', 'Falla', 'Sin datos'),
      fan: SWITCH('Detenido', 'En marcha', 'Falla', 'Sin datos'),
      pipe: SWITCH('Sin flujo', 'Con flujo', 'Falla', 'Sin datos'),
      sensor: SWITCH('Lectura', 'Lectura', 'Alarma', 'Sin datos'),
      breaker: SWITCH('Abierto', 'Cerrado', 'Disparado', 'Sin datos'),
      light: SWITCH('Apagada', 'Encendida', 'Falla', 'Sin datos'),
    },
    forward: 'directo',
    reverse: 'inverso',
    label: (symbol, state) => `${symbol}: ${state}`,
  },
};

export function scadaStrings(locale: string | undefined = viewerLocale()): ScadaStrings {
  return locale?.toLowerCase().startsWith('es') ? SCADA_STRINGS.es : SCADA_STRINGS.en;
}
