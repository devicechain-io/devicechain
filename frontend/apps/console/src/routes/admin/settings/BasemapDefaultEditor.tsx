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
import { defineSetting, type SettingEditorProps } from './registry';

// The camera fields are numbers on the wire — but serialize has to be lossless
// over what the operator can TYPE, so a half-written one passes through as the
// string it is and validate blocks the save. The server never sees it.
type CameraValue = number | string;

/**
 * The stored shape. Every field is optional — an unset one means "the console
 * falls back to its own behaviour", which is what an empty form field records.
 */
interface BasemapValue {
  tileUrl?: string | null;
  attribution?: string | null;
  centerLat?: CameraValue | null;
  centerLon?: CameraValue | null;
  zoom?: CameraValue | null;
}

function numText(v: unknown): string {
  if (typeof v === 'number' && Number.isFinite(v)) return String(v);
  // A string here is either a draft mid-edit or a value written through the API
  // as one. Either way, showing it beats blanking the field.
  return typeof v === 'string' ? v : '';
}

function strText(v: unknown): string {
  return typeof v === 'string' ? v : '';
}

function parse(json: string): BasemapFormState | null {
  let value: unknown;
  try {
    value = JSON.parse(json);
  } catch {
    return null;
  }
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return null;
  const v = value as BasemapValue;
  return {
    tileUrl: strText(v.tileUrl),
    attribution: strText(v.attribution),
    centerLat: numText(v.centerLat),
    centerLon: numText(v.centerLon),
    zoom: numText(v.zoom),
  };
}

// 🔴 An empty field is OMITTED, not written as null: the value is decoded
// server-side with DisallowUnknownFields into a struct of pointers, and an absent
// key is how "not set at this tier" is expressed. Writing nulls would store a
// value whose every key is present and meaningless.
//
// A field with CONTENT is always written, even when that content is not yet a
// number — see CameraValue. Dropping it would delete what the operator is typing.
function serialize(form: BasemapFormState): string {
  const out: BasemapValue = {};
  if (form.tileUrl.trim() !== '') out.tileUrl = form.tileUrl.trim();
  if (form.attribution.trim() !== '') out.attribution = form.attribution.trim();
  for (const k of ['centerLat', 'centerLon', 'zoom'] as const) {
    const raw = form[k].trim();
    if (raw === '') continue;
    out[k] = Number.isFinite(Number(raw)) ? Number(raw) : raw;
  }
  return JSON.stringify(out);
}

function validate(form: BasemapFormState) {
  const key = firstBasemapProblemKey(form);
  if (!key) return null;
  // errNotANumber interpolates the offending fields; passing the values for every
  // message is harmless (i18next ignores unused ones) and keeps this a one-liner.
  return { key, values: { fields: basemapProblems(form).badNumbers.join(', ') } };
}

function BasemapDefaultEditor({ value, onChange }: SettingEditorProps<BasemapFormState>) {
  // No inheritedTileUrl: this IS the bottom tier.
  return <BasemapFields value={value} onChange={onChange} />;
}

export const basemapDefaultSection = defineSetting<BasemapFormState>({
  key: 'basemap.default',
  labelKey: 'tabBasemap',
  icon: Map,
  parse,
  serialize,
  validate,
  Editor: BasemapDefaultEditor,
});
