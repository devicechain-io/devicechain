// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The editor for basemap.default (ADR-079): the tile source and fallback view
// every tenant inherits unless it sets its own.
//
// It is the SAME component the tenant editor uses — same fields, same rules, same
// provider catalog — with the storage wired differently: a tenant writes its
// override through a GraphQL mutation, an operator writes this one as a system
// setting. What is genuinely different is what an empty field means, and that is
// passed in rather than assumed: a tenant leaving the tile URL blank inherits
// THIS value, while leaving THIS blank means no map is drawn for anyone who has
// not configured one. So no inherited-from hint is offered here — there is no
// tier below it to inherit from.

import { Map } from 'lucide-react';
import {
  BasemapFields,
  basemapProblems,
  firstBasemapProblemKey,
  type BasemapFormState,
} from '@/components/basemap/BasemapFields';
import { defineSetting, onlyKnownKeys, parseJson, type SettingEditorProps } from './registry';

/**
 * The stored shape. Every field is optional — an unset one means "the console
 * falls back to its own behaviour", which is what an empty form field records.
 */
interface BasemapValue {
  tileUrl?: string | null;
  attribution?: string | null;
  // 🔴 `number | string`, because toJson is TOTAL: a camera field the operator is
  // part-way through typing ("9x", "33.") is written through AS TYPED so that
  // validate can see it and refuse it. Omitting it would drop the field silently,
  // and coercing it would write NaN — which JSON.stringify renders as null, a
  // value cleared without being asked. Neither reaches the server: validate
  // blocks the save while a string is present.
  centerLat?: number | string | null;
  centerLon?: number | string | null;
  zoom?: number | string | null;
}

/**
 * Every key this editor models. A stored value carrying anything else — including
 * a case variant like `tileURL`, which the server's decoder happily binds — drops
 * to the raw JSON editor rather than being partially loaded and then saved over.
 */
const KNOWN_KEYS = ['tileUrl', 'attribution', 'centerLat', 'centerLon', 'zoom'] as const;

/** A camera field as text, whether it arrived as a number or as typed-through
 *  text that is not one yet. */
function fieldText(v: unknown): string {
  if (typeof v === 'number' && Number.isFinite(v)) return String(v);
  return typeof v === 'string' ? v : '';
}

function strText(v: unknown): string {
  return typeof v === 'string' ? v : '';
}

function seed(json: string): BasemapFormState | null {
  const v = onlyKnownKeys<BasemapValue>(parseJson(json), KNOWN_KEYS);
  if (v === null) return null;
  return {
    tileUrl: strText(v.tileUrl),
    attribution: strText(v.attribution),
    centerLat: fieldText(v.centerLat),
    centerLon: fieldText(v.centerLon),
    zoom: fieldText(v.zoom),
  };
}

// 🔴 An empty field is OMITTED, not written as null: the value is decoded
// server-side with DisallowUnknownFields into a struct of pointers, and an absent
// key is how "not set at this tier" is expressed. Writing nulls would store a
// value whose every key is present and meaningless.
//
// Trims and normalizes freely — safe because the result never returns to the
// editor — but never DROPS anything the operator typed. See BasemapValue.
function toJson(form: BasemapFormState): string {
  const out: BasemapValue = {};
  if (form.tileUrl.trim() !== '') out.tileUrl = form.tileUrl.trim();
  if (form.attribution.trim() !== '') out.attribution = form.attribution.trim();
  for (const k of ['centerLat', 'centerLon', 'zoom'] as const) {
    const raw = form[k].trim();
    if (raw !== '') out[k] = Number.isFinite(Number(raw)) ? Number(raw) : raw;
  }
  return JSON.stringify(out);
}

// Validates the produced JSON rather than the form, so what is checked is what
// would be sent. The rules are the tenant editor's, reached through a form-shaped
// view of the same value — the shared checks are written against field strings,
// and re-deriving them here would be a second opinion about the same rules.
function validate(json: string) {
  const v = onlyKnownKeys<BasemapValue>(parseJson(json), KNOWN_KEYS);
  if (v === null) return { key: 'valueMustBeJsonError' };
  const form: BasemapFormState = {
    tileUrl: strText(v.tileUrl),
    attribution: strText(v.attribution),
    centerLat: fieldText(v.centerLat),
    centerLon: fieldText(v.centerLon),
    zoom: fieldText(v.zoom),
  };
  const key = firstBasemapProblemKey(form);
  if (!key) return null;
  // errNotANumber interpolates the offending fields; passing the values for every
  // message is harmless (i18next ignores unused ones) and keeps this a one-liner.
  return { key, values: { fields: basemapProblems(form).badNumbers.join(', ') } };
}

function BasemapDefaultEditor({ value, onChange }: SettingEditorProps<BasemapFormState>) {
  // No inheritedTileUrl: this IS the bottom tier, so there is nothing to inherit
  // from. showProblems=false because the settings frame renders the blocking
  // reason itself, in the same place for every setting.
  return <BasemapFields value={value} onChange={onChange} showProblems={false} />;
}

export const basemapDefaultSection = defineSetting<BasemapFormState>({
  key: 'basemap.default',
  labelKey: 'tabBasemap',
  icon: Map,
  seed,
  toJson,
  validate,
  Editor: BasemapDefaultEditor,
});
