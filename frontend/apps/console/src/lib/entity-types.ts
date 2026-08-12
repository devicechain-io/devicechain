// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The entity-type vocabulary that keys token masks (ADR-042 P3).
//
// These strings are the KEYS of the entity.token_masks setting: an operator
// writing a mask for devices writes `"device"`, and TokenField looks its mask up
// under whatever string its call site passes. That made the vocabulary an
// unwritten agreement between twenty call sites and an operator typing free text
// into a JSON blob, with nothing checking either side — and it had already
// drifted: one call site passed "tenant tier" WITH A SPACE while every other used
// kebab-case, so a mask written the obvious way (`"tenant-tier"`) silently never
// applied to that form.
//
// Making it a union type fixes that by construction: a call site with a typo does
// not compile, and the settings editor can offer the real list instead of a text
// box.
//
// 🔴 This vocabulary is CONSOLE-side only, and deliberately not enforced by the
// server. The console gains entity types as it gains screens, so a backend that
// refused an unrecognised key would reject a mask written for a newer console —
// a version-skew failure in exchange for catching a typo the type system already
// catches here. See validateTokenMasks in the settingsdefs Go package.

/** Every entity type that mints tokens through a TokenField. */
export const ENTITY_TYPES = [
  'ai-provider',
  'area',
  'area-group',
  'area-type',
  'asset',
  'asset-group',
  'asset-type',
  'command-definition',
  'connector',
  'customer',
  'customer-group',
  'customer-type',
  'dashboard',
  'detection-rule',
  'device',
  'device-group',
  'device-profile',
  'device-type',
  'geofence',
  'group',
  'metric-definition',
  'role',
  'tenant',
  'tenant-tier',
] as const;

export type EntityType = (typeof ENTITY_TYPES)[number];

/**
 * The mask key that applies to any entity type with no entry of its own. Not an
 * EntityType — it is the fallback, and offering it as a "type" in a picker would
 * invite an operator to read it as one.
 */
export const DEFAULT_MASK_KEY = 'default';

/**
 * Entity types named by a value rather than a literal, because a bare string
 * literal inside a JSX attribute expression trips the i18n literal-string lint
 * even when it is a technical identifier. Naming them here is clearer than the
 * `normalizeToken('detection rule')` dance the call sites used to do — that
 * computed the right string, but hid WHICH string from anyone reading it.
 */
export const ENTITY_TYPE = {
  detectionRule: 'detection-rule',
  deviceProfile: 'device-profile',
} as const satisfies Record<string, EntityType>;
