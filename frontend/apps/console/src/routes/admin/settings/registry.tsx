// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The system-settings editor registry (ADR-042 P2). Each setting key gets its own
// full editor — as simple or as elaborate as that key deserves — behind one small
// contract: load from JSON, save to JSON, validate. There is deliberately NO
// generic schema-to-form mapping here; three keys do not justify one, and a form
// generator would cap every editor at whatever the generator can express.
//
// The contract mirrors the server's (the settingsdefs Go package): there a
// Definition must carry a Validator, and omitting it does not compile. Here a
// section must carry seed + toJson + validate + Editor for the same reason, so a
// key cannot arrive with a typed UI and no rules, or rules and no UI.
//
// # 🔴 The host holds the DRAFT, not JSON — and that is a correction
//
// The first version of this contract had the host hold serialized JSON and
// re-derive the form from it on every keystroke. That imposed a law — "serialize
// must be total and lossless over every reachable state" — which is easy to state
// and nearly impossible to keep, because the wire shape and the half-typed shape
// are genuinely different things. It was broken by the first three editors
// written against it, in three separate ways:
//
//   "33."  → Number("33.") is 33, so the decimal point vanished as the operator
//            typed it, making a non-integer latitude UNENTERABLE except by paste
//   "© A " → toJson trimmed, so a trailing space could not be typed
//   "9x"   → the field was dropped from the wire value entirely, so the character
//            disappeared and the save re-enabled
//
// The law is gone now rather than better documented. `toJson` is called only to
// SAVE and to compare against what is stored — never fed back into the editor —
// so it is free to trim, coerce and omit, which is exactly what a wire shape
// wants and exactly what a form state must not suffer.
//
// 🔴 What replaces it: SEED MUST MODEL THE WHOLE VALUE OR RETURN NULL. An editor
// that reads the keys it knows and ignores the rest renders empty fields over
// real data and then saves the emptiness — the full-replace field-loss shape.
// Not hypothetical either: the server decodes these values case-insensitively, so
// `{"tileURL": …}` is stored, valid, and serving traffic, while a reader looking
// up `v.tileUrl` sees nothing. See onlyKnownKeys.
//
// 🔴 Client validation must stay NO STRICTER than the server's. The server is the
// authority; a rule only the console knows refuses a value the platform accepts
// and leaves the operator no way through. Weaker is fine — the save is then
// rejected with the server's own message.

import type { ComponentType } from 'react';
import type { LucideIcon } from 'lucide-react';

/**
 * A reason a draft cannot be saved, as an i18n key plus interpolation values —
 * not a message. A section's validate runs outside React, so it cannot call `t`;
 * the host renders it. The key may be namespace-qualified (`'basemap:errHttps'`)
 * to reuse a shared form's existing messages.
 */
export interface SettingIssue {
  /** Key in the `adminSettings` i18n namespace, or `ns:key` for another. */
  key: string;
  values?: Record<string, string | number>;
}

/** What a section's editor component receives. */
export interface SettingEditorProps<T> {
  value: T;
  onChange: (next: T) => void;
}

/**
 * The per-key contract. Every member is required: a section that knows how to
 * render a value but not how to check it does not compile.
 */
export interface SettingSpec<T> {
  /** The server-side setting key. Must match a key in the settingsdefs registry. */
  key: string;
  /**
   * i18n key for the tab label, in the `adminSettings` namespace. Null for the
   * raw-JSON fallback, whose tab is labelled with the setting key itself — a key
   * with no editor of its own has no name of ours to show.
   */
  labelKey: string | null;
  icon: LucideIcon;
  /**
   * Stored JSON → form state, or null when this editor cannot model the WHOLE
   * value. Null drops the setting to the raw-JSON editor, which is the honest
   * answer: better to show an operator JSON they can read than a form that
   * quietly omits part of what is stored.
   */
  seed: (json: string) => T | null;
  /**
   * Form state → the JSON that is saved, validated and compared for dirtiness.
   * Never fed back into the editor, so it may trim and normalize freely.
   *
   * 🔴 It must be TOTAL: everything the operator has entered has to appear, even
   * when it is not yet valid. Omitting a field the editor could not make sense of
   * is how a value gets dropped without anyone being told — validate would never
   * see it, and the save would succeed having silently discarded it.
   */
  toJson: (draft: T) => string;
  /**
   * The produced JSON → the reason it cannot be saved, or null.
   *
   * 🔴 It validates the JSON, not the draft, so the thing checked IS the thing
   * sent. Validating the draft instead leaves two independent descriptions of
   * what is acceptable — one in validate, one in whatever toJson chose to emit —
   * and a disagreement between them is silent data loss rather than an error.
   * It is also what makes "no stricter than the server" a checkable claim: both
   * planes now judge the same artifact.
   *
   * Evaluated on every render, not only on save, so the operator sees the reason
   * as they type rather than after clicking.
   */
  validate: (json: string) => SettingIssue | null;
  Editor: ComponentType<SettingEditorProps<T>>;
}

/**
 * A section with its form-state type erased, so sections of different shapes can
 * live in one list. The host treats a draft as opaque and hands it straight back.
 */
export interface SettingSection {
  key: string;
  labelKey: string | null;
  icon: LucideIcon;
  seed: (json: string) => unknown | null;
  toJson: (draft: unknown) => string;
  validate: (json: string) => SettingIssue | null;
  Body: ComponentType<{ draft: unknown; onChange: (next: unknown) => void }>;
}

/**
 * defineSetting erases the form-state type. The casts are the erasure itself: the
 * host only ever hands back a draft this same section produced, so the type is
 * recovered exactly where it was lost.
 *
 * 🔴 The returned object must be IDENTITY-STABLE — build it once at module scope,
 * never during a render. `Body` is a component TYPE, so a fresh section object
 * per render is a fresh component type per render, which remounts the subtree and
 * drops focus and cursor position on every keystroke. That bug shipped once, on
 * the raw-JSON fallback: the one screen whose whole purpose is letting an
 * operator repair a value needed a mouse click per character.
 */
export function defineSetting<T>(spec: SettingSpec<T>): SettingSection {
  const { key, labelKey, icon, seed, toJson, validate, Editor } = spec;
  const Body = ({ draft, onChange }: { draft: unknown; onChange: (next: unknown) => void }) => (
    <Editor value={draft as T} onChange={onChange} />
  );
  return {
    key,
    labelKey,
    icon,
    seed,
    toJson: (draft) => toJson(draft as T),
    validate,
    Body,
  };
}

/**
 * onlyKnownKeys returns the parsed object when EVERY one of its keys is modelled,
 * and null otherwise — the guard behind seed's "model the whole value or return
 * null" rule.
 *
 * 🔴 The comparison is exact and case-SENSITIVE, deliberately, even though the
 * server's decoder is case-insensitive. Go binds `tileURL` to the TileURL field,
 * so such a value is stored, valid, and serving traffic — while a JS reader
 * looking up `v.tileUrl` sees nothing. Treating it as unmodelled sends it to the
 * raw editor, where the operator can see what is actually there. Matching
 * case-insensitively here instead would let the form load it and then save
 * `tileUrl` ALONGSIDE the original `tileURL`, two keys the Go decoder resolves
 * last-wins — a coin flip over which tile server every tenant gets.
 *
 * It also covers version skew: a field added to the server shape that this
 * console build predates would otherwise be dropped on the next save.
 */
export function onlyKnownKeys<T extends object>(value: unknown, known: readonly string[]): T | null {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return null;
  return Object.keys(value as Record<string, unknown>).every((k) => known.includes(k))
    ? (value as T)
    : null;
}

/** parseJson is the shared "does this even parse" step of a seed. */
export function parseJson(json: string): unknown {
  try {
    return JSON.parse(json);
  } catch {
    return undefined;
  }
}
