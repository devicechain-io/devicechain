// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pure half of the device alias field: the names a sender other than the platform knows a
// device by (a LoRaWAN DevEUI, a network server's device id, a tenant's own scheme). No React and
// no network, so every rule is unit-tested.
//
// 🔴 THE SERVER IS THE AUTHORITY ON VALIDITY. The checks below only spare an operator a round
// trip for the mistakes the grammar makes obvious; the canonical spelling is the server's
// (`Normalize` in core/devicealias), and this file mirrors it only far enough to tell whether two
// spellings are the SAME alias, so an unchanged field is not sent. An update REPLACES the whole
// alias list, so `aliasesChanged` is what stops a save of an unrelated field from rewriting it.

import type { DeviceAliasInput } from '@/gql/device-management/graphql';

export const SCHEME_DEVEUI = 'lorawan.deveui';
export const SCHEME_TTS_DEVICE_ID = 'tts.device-id';
export const CUSTOM_PREFIX = 'custom.';

/** At most this many aliases per device, one per scheme. */
export const MAX_ALIASES = 8;
const MAX_VALUE_LEN = 128;

/** The three families a row can be; `custom` also needs a name. */
export const ALIAS_KINDS = [SCHEME_DEVEUI, SCHEME_TTS_DEVICE_ID, 'custom'] as const;
export type AliasKind = (typeof ALIAS_KINDS)[number];

/** What one row of the field holds. */
export interface AliasRow {
  kind: AliasKind;
  /** The name after `custom.`; used only when kind is `custom`. */
  customName: string;
  value: string;
}

/** A dot-free key for the kind, for the message catalog (i18next splits keys on dots). */
export const kindKey = (k: AliasKind): 'deveui' | 'ttsDeviceId' | 'custom' =>
  k === SCHEME_DEVEUI ? 'deveui' : k === SCHEME_TTS_DEVICE_ID ? 'ttsDeviceId' : 'custom';

export const emptyAliasRow = (): AliasRow => ({ kind: SCHEME_DEVEUI, customName: '', value: '' });

/** The scheme a row names, or '' while a custom row has no name yet. */
export function rowScheme(row: AliasRow): string {
  if (row.kind !== 'custom') return row.kind;
  const name = row.customName.trim();
  return name ? CUSTOM_PREFIX + name : '';
}

/** A row from a scheme and value as the server stores them (any scheme the server returned). */
export function rowOf(scheme: string, value: string): AliasRow {
  if (scheme === SCHEME_DEVEUI || scheme === SCHEME_TTS_DEVICE_ID) return { kind: scheme, customName: '', value };
  return { kind: 'custom', customName: scheme.startsWith(CUSTOM_PREFIX) ? scheme.slice(CUSTOM_PREFIX.length) : scheme, value };
}

/** The rows for the aliases a device holds. */
export function rowsOf(aliases: readonly { scheme: string; value: string }[] | null | undefined): AliasRow[] {
  return (aliases ?? []).map((a) => rowOf(a.scheme, a.value));
}

/** A row's value in the spelling the server would store, for comparing two rows. */
export function canonicalValue(scheme: string, value: string): string {
  const v = value.trim();
  if (scheme === SCHEME_DEVEUI) return v.toUpperCase();
  if (scheme === SCHEME_TTS_DEVICE_ID) return v.toLowerCase();
  return v;
}

const HEX16 = /^[0-9a-fA-F]{16}$/;
const TTS_ID = /^[a-z0-9]([a-z0-9-]{0,34}[a-z0-9])?$/;
const CUSTOM_NAME = /^[a-z0-9][a-z0-9_-]{0,62}$/;

export type AliasProblem = 'name' | 'value' | 'duplicate';

/** What is wrong with a row, if anything. A row with no value at all is not "wrong": it is blank. */
export function rowProblem(row: AliasRow): AliasProblem | null {
  if (row.kind === 'custom' && !CUSTOM_NAME.test(row.customName.trim())) return 'name';
  const v = row.value.trim();
  switch (row.kind) {
    case SCHEME_DEVEUI:
      return HEX16.test(v) ? null : 'value';
    case SCHEME_TTS_DEVICE_ID:
      return TTS_ID.test(v.toLowerCase()) ? null : 'value';
    default:
      // eslint-disable-next-line no-control-regex
      return v !== '' && [...v].length <= MAX_VALUE_LEN && !/[\u0000-\u001f\u007f]/.test(v) ? null : 'value';
  }
}

/** True for a row the operator has not touched, which is dropped rather than reported. */
export const isBlank = (row: AliasRow): boolean => row.value.trim() === '' && row.customName.trim() === '';

/**
 * The problem of each row, by index (null when it is fine or blank), plus `duplicate` for a
 * second row naming a scheme an earlier one already names: a device holds one value per scheme.
 */
export function aliasProblems(rows: readonly AliasRow[]): (AliasProblem | null)[] {
  const seen = new Set<string>();
  return rows.map((row) => {
    if (isBlank(row)) return null;
    const own = rowProblem(row);
    if (own) return own;
    const scheme = rowScheme(row);
    if (seen.has(scheme)) return 'duplicate';
    seen.add(scheme);
    return null;
  });
}

/** True when every non-blank row is sendable. */
export const aliasesValid = (rows: readonly AliasRow[]): boolean => aliasProblems(rows).every((p) => p === null);

/** The `aliases` input for the rows: blank rows dropped, values trimmed. */
export function aliasInputs(rows: readonly AliasRow[]): DeviceAliasInput[] {
  return rows.filter((r) => !isBlank(r)).map((r) => ({ scheme: rowScheme(r), value: r.value.trim() }));
}

const keyOf = (r: AliasRow) => `${rowScheme(r)}\u0000${canonicalValue(rowScheme(r), r.value)}`;

/** True when the two lists name different aliases (spelling aside, order aside). */
export function aliasesChanged(initial: readonly AliasRow[], current: readonly AliasRow[]): boolean {
  const a = initial.filter((r) => !isBlank(r)).map(keyOf).sort();
  const b = current.filter((r) => !isBlank(r)).map(keyOf).sort();
  return a.length !== b.length || a.some((k, i) => k !== b[i]);
}

/**
 * The device-create URL the unregistered list links to: it opens the new-device drawer with the
 * alias filled in (and the integration's only device type chosen, when it has just one).
 */
export const NEW_ALIAS_PARAM = 'newAlias';
export const NEW_TYPE_PARAM = 'newType';

export function newDeviceLink(scheme: string, value: string, deviceTypeToken?: string): string {
  const q = new URLSearchParams({ [NEW_ALIAS_PARAM]: `${scheme}|${value}` });
  if (deviceTypeToken) q.set(NEW_TYPE_PARAM, deviceTypeToken);
  return `/devices?${q.toString()}`;
}

/** The row a `newAlias` parameter carries, or null when it is absent or malformed. */
export function rowFromParam(param: string | null): AliasRow | null {
  if (!param) return null;
  const i = param.indexOf('|');
  if (i <= 0) return null;
  return rowOf(param.slice(0, i), param.slice(i + 1));
}
