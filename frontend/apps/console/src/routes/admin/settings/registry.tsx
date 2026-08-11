// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The system-settings editor registry (ADR-042 P2). Each setting key gets its own
// full editor — as simple or as elaborate as that key deserves — behind one small
// contract: load from JSON, save to JSON, validate. There is deliberately NO
// generic schema-to-form mapping here; three keys do not justify one, and a form
// generator would cap every editor at whatever the generator can express.
//
// The contract is the mirror of the server's (the settingsdefs Go package): there
// a Definition must carry a Validator, and omitting it does not compile. Here a
// section must carry parse + serialize + validate + Editor for the same reason,
// so a key cannot arrive with a typed UI and no rules, or rules and no UI.
//
// 🔴 T IS THE FORM STATE, NOT THE WIRE SHAPE. Keep every field a string, exactly
// as the tenant basemap and branding editors do. The host re-serializes on each
// change, so a T that holds parsed numbers would destroy what the user is halfway
// through typing: "1." serializes to 1 and the decimal point vanishes as they
// type it.
//
// 🔴 AND SERIALIZE MUST BE TOTAL AND LOSSLESS. The JSON it returns IS the draft —
// the host holds nothing else — so `parse(serialize(draft))` has to give the
// draft back for EVERY state the editor can reach, including invalid ones. It is
// tempting to have serialize omit or coerce a field the user has half-typed; that
// field then disappears from under the cursor on the next keystroke, and the
// error explaining it disappears with it. This is not hypothetical: the basemap
// editor was written that way first, and a test typing "9x" into Zoom watched it
// vanish and the save re-enable.
//
// Validity is validate's job, not serialize's. An unparseable number is written
// through AS TYPED and blocked by validate — never coerced, because
// `Number('12x')` is NaN and JSON.stringify writes NaN as null: a field the
// operator never meant to clear, cleared quietly on save.
//
// 🔴 Client validation must stay NO STRICTER than the server's. The server is the
// authority; a rule only the console knows refuses a value the platform accepts,
// and leaves the operator no way through. Weaker is fine — the save is rejected
// with the server's own message.

import type { ComponentType } from 'react';
import type { LucideIcon } from 'lucide-react';

/**
 * A reason a draft cannot be saved, as an i18n key plus interpolation values —
 * not a message. A section's validate runs outside React, so it cannot call `t`;
 * the host renders it.
 */
export interface SettingIssue {
  /** Key in the `adminSettings` i18n namespace. */
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
   * Stored JSON → form state. Returns null when the value cannot be read at all,
   * which drops the section to the raw-JSON editor rather than showing an empty
   * form over a value that is actually there.
   */
  parse: (json: string) => T | null;
  /** Form state → the JSON that gets saved. */
  serialize: (draft: T) => string;
  /** Form state → the reason it cannot be saved, or null. */
  validate: (draft: T) => SettingIssue | null;
  Editor: ComponentType<SettingEditorProps<T>>;
}

/**
 * A section with its form-state type erased, so sections of different shapes can
 * live in one list. Everything below the erasure speaks JSON.
 */
export interface SettingSection {
  key: string;
  labelKey: string | null;
  icon: LucideIcon;
  /** True when the stored JSON is beyond this section's editor. */
  unreadable: (json: string) => boolean;
  validateJson: (json: string) => SettingIssue | null;
  Body: ComponentType<{ json: string; onChange: (json: string) => void }>;
}

/**
 * defineSetting binds a spec's codec to its editor and erases the form-state
 * type. The round trip runs on every change: the host holds JSON (which is what
 * gets saved, compared for dirtiness, and shown in the raw view), and the editor
 * holds the form state it was written against.
 */
export function defineSetting<T>(spec: SettingSpec<T>): SettingSection {
  const { key, labelKey, icon, parse, serialize, validate, Editor } = spec;

  return {
    key,
    labelKey,
    icon,
    unreadable: (json) => parse(json) === null,
    validateJson: (json) => {
      const draft = parse(json);
      // An unreadable value is not "invalid" — the host has already fallen back
      // to the raw editor, which validates JSON syntax on its own. Reporting an
      // issue here too would stack two errors for one problem.
      return draft === null ? null : validate(draft);
    },
    Body: ({ json, onChange }) => {
      const draft = parse(json);
      if (draft === null) return null;
      return <Editor value={draft} onChange={(next) => onChange(serialize(next))} />;
    },
  };
}
