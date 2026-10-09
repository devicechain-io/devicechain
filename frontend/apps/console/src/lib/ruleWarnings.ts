// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Localised text for the rule compiler's advisory warnings. The server sends a stable `code` plus
// positional `params` and an English `message`; the console owns the wording, so a warning reads in
// the author's language and a new server-side code still shows (as the English fallback) in a console
// build that predates it.
import type { TFunction } from 'i18next';

export interface WarningLike {
  code?: string | null;
  params?: readonly string[] | null;
  message: string;
}

// ruleWarningText resolves a warning to display text. Params are exposed to the translation as
// {{p0}}, {{p1}}, … in the server's fixed per-code order. A code with no translation falls back to
// the server's message rather than showing a raw key.
export function ruleWarningText(t: TFunction, w: WarningLike): string {
  if (!w.code) return w.message;
  const params: Record<string, string> = {};
  (w.params ?? []).forEach((p, i) => {
    params[`p${i}`] = p;
  });
  return t(`deviceProfiles:ruleWarning.${w.code}`, { ...params, defaultValue: w.message });
}
