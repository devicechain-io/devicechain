// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fixed words the command slider and toggle render themselves: why they will not send, what
// they last sent, and where that command is in its delivery. The widget packages carry no i18n
// runtime, so this is the one small per-language table, in the manner of level-strings.ts; option
// labels are translated in the console's catalogs, not here.
//
// The caption is worded as what was SENT. These controls know the history of the commands issued
// to the device, not what the device is doing, and no string here may read as the latter.

import { viewerLocale } from '@devicechain/widget-sdk';

export interface ControlStrings {
  noCommand: string;
  setParameter: string;
  parameterMissing: (name: string) => string;
  wrongType: (name: string, expected: string) => string;
  fixedChoices: (name: string) => string;
  otherRequired: (names: string[]) => string;
  badDefault: (names: string[]) => string;
  setRange: string;
  badRange: string;
  numberKind: string;
  booleanKind: string;
  // The caption.
  lastSent: string;
  nothingSent: string;
  // The history is longer than the page this control reads, and none of this command is on it.
  beyondHistory: string;
  noValue: string;
  unsent: string;
  // The slider's keyboard description: arrows choose, Enter sends.
  sliderHint: string;
  // The stepper: its keyboard description, the unsent caption, and its buttons.
  stepperHint: string;
  stepperUnsent: string;
  send: string;
  decrease: string;
  increase: string;
  pending: string;
  delivered: string;
  failed: string;
  sending: string;
  on: string;
  off: string;
  // Read-only reasons.
  // The short visible caption beside a read-only control; the full reason is its description.
  viewOnly: string;
  noPermission: string;
  noSeam: string;
  noDevice: string;
  sendFailed: string;
}

export const CONTROL_STRINGS: Record<'en' | 'es', ControlStrings> = {
  en: {
    noCommand: 'No command configured',
    setParameter: 'Set which parameter this control sends',
    parameterMissing: (n) => `The command has no parameter “${n}”`,
    wrongType: (n, e) => `Parameter “${n}” is not ${e}`,
    fixedChoices: (n) => `Parameter “${n}” takes fixed choices, not a range`,
    otherRequired: (ns) => `Not sent: the command also requires ${ns.map((n) => `“${n}”`).join(', ')}, which has no default value`,
    badDefault: (ns) => `Not sent: the default value of ${ns.map((n) => `“${n}”`).join(', ')} is not valid for the command`,
    setRange: 'Set min and max',
    badRange: 'Max must be above min',
    numberKind: 'a number',
    booleanKind: 'a boolean',
    lastSent: 'Last sent',
    nothingSent: 'Nothing sent yet',
    beyondHistory: 'Not among the recent commands',
    noValue: 'no value',
    unsent: 'Not sent yet. Release or press Enter to send',
    sliderHint: 'Arrow keys choose a value; press Enter to send it, or Escape to discard it.',
    stepperHint: 'Arrow keys and Page Up or Down change the value; press Enter to send it, or Escape to discard it.',
    stepperUnsent: 'Not sent yet. Press Send',
    send: 'Send',
    decrease: 'Decrease',
    increase: 'Increase',
    pending: 'Pending',
    delivered: 'Delivered',
    failed: 'Failed',
    sending: 'Sending…',
    on: 'On',
    off: 'Off',
    viewOnly: 'View only',
    noPermission: 'You don’t have permission to send commands.',
    noSeam: 'Commands aren’t available in this view.',
    noDevice: 'No device is selected for this command.',
    sendFailed: 'Couldn’t send the command. Try again.',
  },
  es: {
    noCommand: 'No hay un comando configurado',
    setParameter: 'Defina qué parámetro envía este control',
    parameterMissing: (n) => `El comando no tiene el parámetro “${n}”`,
    wrongType: (n, e) => `El parámetro “${n}” no es ${e}`,
    fixedChoices: (n) => `El parámetro “${n}” admite opciones fijas, no un rango`,
    otherRequired: (ns) => `No se envía: el comando también exige ${ns.map((n) => `“${n}”`).join(', ')}, que no tiene valor por defecto`,
    badDefault: (ns) => `No se envía: el valor por defecto de ${ns.map((n) => `“${n}”`).join(', ')} no es válido para el comando`,
    setRange: 'Defina mín. y máx.',
    badRange: 'El máx. debe superar el mín.',
    numberKind: 'un número',
    booleanKind: 'un booleano',
    lastSent: 'Último enviado',
    nothingSent: 'Aún no se ha enviado nada',
    beyondHistory: 'No figura entre los comandos recientes',
    noValue: 'sin valor',
    unsent: 'Aún no enviado. Suelte o pulse Intro para enviar',
    sliderHint: 'Las flechas eligen un valor; pulse Intro para enviarlo o Escape para descartarlo.',
    stepperHint: 'Las flechas y Re Pág o Av Pág cambian el valor; pulse Intro para enviarlo o Escape para descartarlo.',
    stepperUnsent: 'Aún no enviado. Pulse Enviar',
    send: 'Enviar',
    decrease: 'Disminuir',
    increase: 'Aumentar',
    pending: 'Pendiente',
    delivered: 'Entregado',
    failed: 'Fallido',
    sending: 'Enviando…',
    on: 'Encendido',
    off: 'Apagado',
    viewOnly: 'Solo lectura',
    noPermission: 'No tiene permiso para enviar comandos.',
    noSeam: 'Los comandos no están disponibles en esta vista.',
    noDevice: 'No hay un dispositivo seleccionado para este comando.',
    sendFailed: 'No se pudo enviar el comando. Inténtelo de nuevo.',
  },
};

export function controlStrings(locale: string | undefined = viewerLocale()): ControlStrings {
  return locale?.toLowerCase().split('-')[0] === 'es' ? CONTROL_STRINGS.es : CONTROL_STRINGS.en;
}
