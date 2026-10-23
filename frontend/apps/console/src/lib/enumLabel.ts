// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Localised display text for a wire enum value, with the raw value as the fallback.
//
// A badge shows a LABEL; the enum value stays what the data, the filters and the tests key
// on. An unknown value (a status a newer service adds before this console learns it) renders
// verbatim rather than as a missing-key placeholder or, worse, as a known value's label.
import type { TFunction } from 'i18next';

/** `prefix_VALUE` in the `common` catalog, or `value` itself when no such key exists. */
export function enumLabel(t: TFunction, prefix: string, value: string): string {
  return t(`common:${prefix}_${value}`, { defaultValue: value });
}

export const commandStatusLabel = (t: TFunction, status: string) => enumLabel(t, 'commandStatus', status);
export const updateStateLabel = (t: TFunction, state: string) => enumLabel(t, 'updateState', state);
export const signerStateLabel = (t: TFunction, state: string) => enumLabel(t, 'signerState', state);
export const artifactKindLabel = (t: TFunction, kind: string) => enumLabel(t, 'artifactKind', kind);
export const artifactStateLabel = (t: TFunction, state: string) => enumLabel(t, 'artifactState', state);
export const rolloutStateLabel = (t: TFunction, state: string) => enumLabel(t, 'rolloutState', state);
export const rolloutOutcomeLabel = (t: TFunction, outcome: string) => enumLabel(t, 'rolloutOutcome', outcome);
export const rolloutPauseReasonLabel = (t: TFunction, reason: string) => enumLabel(t, 'rolloutPauseReason', reason);
