// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The one sentence every widget shows when the platform REFUSES what it reads — a member whose
// access is limited to part of the tenant viewing a widget bound to something outside it — in
// place of the error frame's "Data unavailable" and the server's raw "forbidden: …" text. It is
// a refusal working as designed, not an outage, so it reads as a state. Kept apart from the
// per-widget tables so the eager error frame and the lazily loaded fleet chunk share it without
// either pulling in the other's strings.

import { viewerLocale } from '@devicechain/widget-sdk';

export const ACCESS_STRINGS: Record<'en' | 'es', { forbidden: string }> = {
  en: { forbidden: 'You do not have access to this data' },
  es: { forbidden: 'No tiene acceso a estos datos' },
};

export function accessStrings(locale: string | undefined = viewerLocale()): { forbidden: string } {
  return locale?.toLowerCase().split('-')[0] === 'es' ? ACCESS_STRINGS.es : ACCESS_STRINGS.en;
}
