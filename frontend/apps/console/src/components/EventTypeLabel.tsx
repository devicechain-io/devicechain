// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The ONE place an event-type ordinal becomes words.
//
// The wire carries the platform's event type as an integer (backend
// event-sources model: NewRelationship=0, Location=1, Measurement=2, Alert=3,
// StateChange=4, CommandInvocation=5, CommandResponse=6), and every event list
// in the console used to print it raw — a location fix read as "#1". The mapping
// lives here and only here, so a second screen cannot grow a second, drifting
// copy of it.
//
// 🔴 An ordinal this table does not know renders as UNKNOWN, naming the number.
// The tempting alternative — clamping into range, or falling back to the last
// known label — would display a future seventh event type as
// "Command response", which is not a rendering gap but a lie: the operator has
// no way to tell it apart from a real command response. An honest "Unknown
// type (#7)" says exactly what the console knows and prompts an upgrade.
import { useTranslation } from 'react-i18next';

// Ordinal → key in the `common` catalog. The labels live in `common` rather than
// in a screen namespace because event types are platform vocabulary: the device
// page, browse, and anything else listing raw events all name the same seven.
const EVENT_TYPE_LABEL_KEYS: Readonly<Record<number, string>> = {
  0: 'eventTypeNewRelationship',
  1: 'eventTypeLocation',
  2: 'eventTypeMeasurement',
  3: 'eventTypeAlert',
  4: 'eventTypeStateChange',
  5: 'eventTypeCommandInvocation',
  6: 'eventTypeCommandResponse',
};

/**
 * The catalog key naming an event-type ordinal, or null when the ordinal is one
 * this console does not know. Null is deliberately not a key: there is no
 * "default" event type, and returning one would make an unknown ordinal
 * indistinguishable from a known one at every call site.
 */
export function eventTypeLabelKey(eventType: number): string | null {
  return EVENT_TYPE_LABEL_KEYS[eventType] ?? null;
}

/** Renders an event-type ordinal as its translated name. */
export function EventTypeLabel({ eventType }: { eventType: number }) {
  const { t } = useTranslation('common');
  const key = eventTypeLabelKey(eventType);
  return <span>{key ? t(key) : t('eventTypeUnknown', { ordinal: eventType })}</span>;
}
