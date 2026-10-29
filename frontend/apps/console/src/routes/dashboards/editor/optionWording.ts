// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// How a widget option is worded in the config panel.
//
// A descriptor can carry two things for each piece of text: a MESSAGE ID the host may know
// how to translate, and PORTABLE TEXT the package shipped. The lookup order is the point:
//
//   1. the message id, when the console's own catalog defines it (built-ins declare the
//      console's keys here, which is how they stay translated);
//   2. the portable text from the descriptor (a third-party option is understandable with
//      no console translation key at all);
//   3. for a label only, the option's key made readable — so a package that supplied
//      neither still renders a field a person can tell apart, not a raw translation key.
//
// 🔴 The id is consulted with `exists`, never by calling t() and comparing: i18next answers
// an unknown key with the key itself, which is an ordinary-looking string and would be
// shown as the label.

import type { EnumOptionSpec, OptionSpec } from '@devicechain/dashboards';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import '@/i18n/lazy/dashboards';

// A host catalog lookup: the translated text for an id, or undefined when the console has
// no such message.
export type MessageLookup = (id: string) => string | undefined;

export function useMessageLookup(): MessageLookup {
  const { t, i18n } = useTranslation('dashboards');
  return (id) => (i18n.exists(id, { ns: 'dashboards' }) ? t(id) : undefined);
}

// 'maxRows' / 'max_rows' / 'max-rows' -> 'Max rows'.
export function humanizeKey(key: string): string {
  const words = key
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/[_\-.]+/g, ' ')
    .trim()
    .toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

// Resolve one piece of descriptor text: the host's message for `id`, else the portable text.
export function resolveText(
  lookup: MessageLookup,
  id: string | undefined,
  portable: string | undefined,
): string | undefined {
  return (id !== undefined ? lookup(id) : undefined) ?? portable;
}

export function fieldLabel(key: string, spec: OptionSpec, lookup: MessageLookup): string {
  return resolveText(lookup, spec.labelId, spec.label) ?? humanizeKey(key);
}

export function fieldHelp(spec: OptionSpec, lookup: MessageLookup): string | undefined {
  return resolveText(lookup, spec.helpId, spec.help);
}

export function enumChoiceLabel(spec: EnumOptionSpec, value: string, lookup: MessageLookup): string {
  // Own-property reads: a value like 'constructor' must not resolve to an inherited member.
  const own = (table: Readonly<Record<string, string>> | undefined) =>
    table && Object.prototype.hasOwnProperty.call(table, value) ? table[value] : undefined;
  return resolveText(lookup, own(spec.valueLabelIds), own(spec.valueLabels)) ?? value;
}

// What an unset enum shows. The generic host sentence is the last resort, not a claim about
// what unset means for this widget — a descriptor that knows says so with its own text.
export function enumPlaceholder(spec: EnumOptionSpec, lookup: MessageLookup, t: TFunction): string {
  return resolveText(lookup, spec.placeholderId, spec.placeholder) ?? t('widgetPlaceholderDefault');
}
