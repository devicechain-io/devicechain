// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The one place a LoadError becomes words.
//
// It lives beside the catalogs rather than inside load.ts so that load.ts can stay a
// module with no notion of language at all (see the header there), and beside them
// rather than inside App.tsx so that the code → key mapping is a plain function a test
// can enumerate. The mapping is a `switch` with an exhaustiveness check, so adding a
// LoadError variant is a TYPE ERROR here until it has a key — the compiler, not a
// reviewer, is what stops a new failure mode from rendering as a blank line.

import type { LoadError } from '../load';

/** A catalog key plus the values it interpolates. Never prose. */
export interface LoadErrorKey {
  key: string;
  params: Record<string, unknown>;
}

/**
 * Slot names come out of the pasted JSON, so they are quoted the way JSON quotes a
 * key — with `"`, in every locale. That is deliberate rather than an oversight of
 * Spanish's «»: the quoted thing is a literal key the viewer can search for in the
 * text they pasted, not a phrase in a sentence.
 */
function quoteSlots(slots: string[]): string {
  return slots.map((slot) => `"${slot}"`).join(', ');
}

export function loadErrorKey(error: LoadError): LoadErrorKey {
  switch (error.code) {
    case 'definitionTooLarge':
      return { key: 'load:errorDefinitionTooLarge', params: {} };
    case 'definitionInvalid':
      return { key: 'load:errorDefinitionInvalid', params: { detail: error.detail } };
    case 'manifestInvalid':
      return { key: 'load:errorManifestInvalid', params: { detail: error.detail } };
    case 'manifestNotObject':
      return { key: 'load:errorManifestNotObject', params: {} };
    case 'manifestDropped':
      // `count` drives the plural (was/were ignored); `slots` is the rendered list.
      return {
        key: 'load:errorManifestDropped',
        params: { count: error.slots.length, slots: quoteSlots(error.slots) },
      };
    default: {
      // Unreachable while the switch is exhaustive; a new variant reddens the
      // assignment below at compile time rather than falling through at runtime.
      const unhandled: never = error;
      throw new Error(`unhandled LoadError: ${JSON.stringify(unhandled)}`);
    }
  }
}

/**
 * Translate a LoadError. `t` is taken structurally rather than as i18next's TFunction
 * so this stays callable from a test with any translator, including one that returns
 * its own key — which is how the tests below assert on keys instead of on English.
 *
 * A `detail` of null means the thrown value was not an Error; it is filled with the
 * localized fallback here rather than in load.ts, which owns no copy.
 */
export function loadErrorMessage(
  error: LoadError,
  t: (key: string, params?: Record<string, unknown>) => string,
): string {
  const { key, params } = loadErrorKey(error);
  const filled =
    'detail' in params && params.detail == null
      ? { ...params, detail: t('common:unexpectedError') }
      : params;
  return t(key, filled);
}
