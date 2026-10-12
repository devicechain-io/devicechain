// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Whether a stored widget can be rendered by an installed implementation.
//
// The compatibility key is (id, optionsVersion, sdkApiVersion). `version` is PROVENANCE: a
// differing release still renders, and the editor shows a non-blocking "authored with X,
// rendering with Y" notice (widgetVersionNotice). Only a settings-shape or SDK-generation
// difference turns a widget into a placeholder, because only those change what the stored
// options MEAN. "Exact replay" is claimed only when the versions are equal.
//
// React-free, and it reads a WidgetCatalog (descriptors), never an implementation, so the
// parser and the headless validators can use it.

import type { WidgetCatalog } from './widget-catalog';
import type { WidgetDescriptor } from './widget-descriptor';
import type {
  DashboardDefinition,
  ResolvedWidgetInstance,
  UnresolvedWidgetReason,
  WidgetImplementationRef,
  WidgetInstance,
} from './types';
import { UNKNOWN_WIDGET_TYPE, WIDGET_TYPES, type KnownWidgetInstance } from './types';

const BUILTIN_TYPES: ReadonlySet<string> = new Set(WIDGET_TYPES);

// isBuiltinWidget narrows a parsed widget to a built-in instance: not a placeholder and not
// an extension. For code (the built-in editor panels) that is typed against the closed
// built-in union and must not be handed an extension widget.
export function isBuiltinWidget(widget: WidgetInstance): widget is KnownWidgetInstance {
  return BUILTIN_TYPES.has(widget.type);
}

// isResolvedWidget narrows a parsed widget to one an installed implementation resolved (a
// built-in or a registered extension): everything that is not a placeholder.
export function isResolvedWidget(widget: WidgetInstance): widget is ResolvedWidgetInstance {
  return widget.type !== UNKNOWN_WIDGET_TYPE;
}

const SEMVER = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

// parseWidgetRef reads the stored `widget` metadata. `undefined` = absent (legacy / incomplete);
// `null` = present but malformed, which must not be mistaken for absent.
export function parseWidgetRef(raw: unknown): WidgetImplementationRef | undefined | null {
  if (raw === undefined) return undefined;
  if (!isRecord(raw)) return null;
  const { version, optionsVersion, sdkApiVersion } = raw;
  if (typeof version !== 'string' || !SEMVER.test(version)) return null;
  if (typeof optionsVersion !== 'number' || !Number.isInteger(optionsVersion) || optionsVersion < 1) return null;
  const ref: WidgetImplementationRef = { version, optionsVersion };
  if (sdkApiVersion !== undefined) {
    if (typeof sdkApiVersion !== 'number' || !Number.isInteger(sdkApiVersion) || sdkApiVersion < 1) return null;
    ref.sdkApiVersion = sdkApiVersion;
  }
  return ref;
}

// checkWidgetCompatibility returns why the stored reference cannot be rendered by this
// descriptor, or undefined when it can (a differing `version` alone is compatible).
export function checkWidgetCompatibility(
  id: string,
  ref: WidgetImplementationRef,
  descriptor: WidgetDescriptor,
): UnresolvedWidgetReason | undefined {
  if (ref.optionsVersion !== descriptor.optionsVersion) {
    return {
      code: 'options-version',
      message: `"${id}" was saved with settings version ${ref.optionsVersion}; the installed widget uses settings version ${descriptor.optionsVersion}`,
    };
  }
  if (ref.sdkApiVersion !== undefined && ref.sdkApiVersion !== descriptor.sdkApiVersion) {
    return {
      code: 'sdk-incompatible',
      message: `"${id}" was authored for widget SDK API ${ref.sdkApiVersion}; the installed widget uses API ${descriptor.sdkApiVersion}`,
    };
  }
  return undefined;
}

// widgetVersionNotice is the non-blocking provenance message for a widget rendered by a
// different release than the one it was authored with, or undefined when they match (or the
// widget carries no version).
export function widgetVersionNotice(
  id: string,
  ref: WidgetImplementationRef | undefined,
  descriptor: WidgetDescriptor,
): string | undefined {
  if (!ref || ref.version === descriptor.version) return undefined;
  return `"${id}" was authored with version ${ref.version}; rendering with version ${descriptor.version}`;
}

// classifyStoredWidget decides, for a stored widget object of a NON-reserved or versioned
// type, whether the catalog resolves it. Returns the reason it does not, or undefined.
// `builtin` selects the legacy rule: a built-in with no metadata is fine.
export function unresolvedReason(
  type: string,
  rawRef: unknown,
  catalog: WidgetCatalog | undefined,
  builtin: boolean,
): UnresolvedWidgetReason | undefined {
  const ref = parseWidgetRef(rawRef);
  if (ref === null) {
    return { code: 'invalid-metadata', message: `"${type}" carries widget version metadata that is not valid` };
  }
  if (!catalog) {
    return builtin
      ? undefined
      : { code: 'unregistered', message: `"${type}" is not available: no widget catalog was supplied` };
  }
  const descriptor = catalog.get(type);
  if (!descriptor) {
    return {
      code: 'unregistered',
      message: builtin
        ? `"${type}" is not registered in this host`
        : `"${type}" is not installed in this host; add its package to the host's widget registry`,
    };
  }
  if (ref === undefined) {
    return builtin
      ? undefined
      : { code: 'unversioned', message: `"${type}" is stored without version metadata, so the installed widget cannot be matched to it` };
  }
  return checkWidgetCompatibility(type, ref, descriptor);
}

export interface UnresolvedWidgetReport {
  widgetId: string;
  type: string;
  reason: UnresolvedWidgetReason;
}

// unresolvedWidgets lists every widget on the board that is a placeholder, with its reason.
// A publish gate uses it: loading, layout edits and draft saves stay possible, but a new
// version should not be published while a widget cannot be resolved. A placeholder built
// by an older parser (no reason) is reported as unregistered.
export function unresolvedWidgets(definition: Pick<DashboardDefinition, 'widgets'>): UnresolvedWidgetReport[] {
  const out: UnresolvedWidgetReport[] = [];
  for (const w of definition.widgets as readonly WidgetInstance[]) {
    if (w.type !== UNKNOWN_WIDGET_TYPE) continue;
    const storedType = typeof w.raw.type === 'string' ? w.raw.type : '';
    out.push({
      widgetId: w.id,
      type: storedType,
      reason: w.reason ?? { code: 'unregistered', message: `"${storedType}" is not available in this host` },
    });
  }
  return out;
}
