// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pure half of the integration form: the draft the inputs edit, the problems worth showing
// before a round trip, the generic-source settings document, and the two requests built from it.
// No React and no network, so every rule here is unit-tested.
//
// 🔴 THE SERVER IS THE AUTHORITY. These checks mirror the grammar the server enforces
// (`ValidateGenericSettings` in device-management) only far enough to spare an operator a round
// trip for an obvious mistake; anything they let through the server still judges.
//
// 🔴 AN EDIT SENDS ONLY WHAT THE OPERATOR CHANGED. updateIntegration is a PARTIAL update (omit =
// leave alone, null = clear, value = set), so `integrationEditRequest` compares each field with
// the value the form was seeded from and names only the ones that differ. The type is not an
// input of an update at all: it is fixed at creation. The bearer token is not an input of create
// or update either: the server generates it (see IntegrationTokenReveal.tsx).

import type {
  Integration,
  IntegrationCreateRequest,
  IntegrationType,
  IntegrationUpdateRequest,
} from '@/lib/api/integrations';

export const IDENTITY_DEV_EUI = 'DEV_EUI';
export const IDENTITY_DEVICE_ID = 'DEVICE_ID';
export const IDENTITY_ALIAS_PREFIX = 'ALIAS:';
const CUSTOM_PREFIX = 'custom.';

/** Whether the type is a generic source (one the operator describes, rather than a known server). */
export const isGeneric = (type: string): boolean => type === 'GENERIC_JSON' || type === 'GENERIC_BINARY';

/** The identities a type can name its devices by, in the order offered. Empty for a generic source, whose scheme is typed. */
export function identityChoices(type: string): string[] {
  switch (type) {
    case 'CHIRPSTACK_V4':
      return [IDENTITY_DEV_EUI];
    case 'TTS_V3':
      return [IDENTITY_DEV_EUI, IDENTITY_DEVICE_ID];
    default:
      return [];
  }
}

export const FRAME_ENCODINGS = ['HEX', 'BASE64'] as const;

/** What the form's inputs hold. Every field is a string or boolean, as an input has it. */
export interface IntegrationDraft {
  type: IntegrationType;
  name: string;
  description: string;
  enabled: boolean;
  deviceTypeTokens: string[];
  /** DEV_EUI or DEVICE_ID for a network server; for a generic source see `aliasName`. */
  identity: string;
  /** Generic only: the name after `custom.` of the alias scheme the source's identifier is stored under. */
  aliasName: string;
  linkMetrics: boolean;
  // Generic settings (all text; the optional ones are '' when unset).
  /** `pointer`, `header` or `query`: where the device identifier is. */
  idKind: 'pointer' | 'header' | 'query';
  idValue: string;
  itemsPointer: string;
  timePointer: string;
  framePointer: string;
  /** HEX or BASE64, or '' while no frame pointer is set. */
  frameEncoding: string;
}

export const emptyDraft = (type: IntegrationType = 'CHIRPSTACK_V4'): IntegrationDraft => ({
  type,
  name: '',
  description: '',
  enabled: true,
  deviceTypeTokens: [],
  identity: identityChoices(type)[0] ?? '',
  aliasName: '',
  linkMetrics: false,
  idKind: type === 'GENERIC_BINARY' ? 'header' : 'pointer',
  idValue: '',
  itemsPointer: '',
  timePointer: '',
  framePointer: '',
  frameEncoding: '',
});

/** Switching type keeps what still applies and resets the rest to that type's defaults. */
export function withType(d: IntegrationDraft, type: IntegrationType): IntegrationDraft {
  const fresh = emptyDraft(type);
  return {
    ...fresh,
    name: d.name,
    description: d.description,
    enabled: d.enabled,
    deviceTypeTokens: d.deviceTypeTokens,
    linkMetrics: d.linkMetrics,
    // A binary body has no fields to point at, so a pointer choice does not carry over to it.
    ...(isGeneric(type) && isGeneric(d.type)
      ? {
          aliasName: d.aliasName,
          idKind: type === 'GENERIC_BINARY' && d.idKind === 'pointer' ? 'header' : d.idKind,
          idValue: type === 'GENERIC_BINARY' && d.idKind === 'pointer' ? '' : d.idValue,
        }
      : {}),
  };
}

/** The `generic` settings text of a stored integration parsed into draft fields (blank on a bad document). */
export function parseGeneric(text: string | null | undefined): Partial<IntegrationDraft> {
  if (!text) return {};
  let o: Record<string, unknown>;
  try {
    o = JSON.parse(text) as Record<string, unknown>;
  } catch {
    return {};
  }
  const str = (k: string) => (typeof o[k] === 'string' ? (o[k] as string) : '');
  const idFrom = str('idFrom');
  const colon = idFrom.indexOf(':');
  const kind = colon > 0 ? idFrom.slice(0, colon) : '';
  return {
    idKind: kind === 'header' || kind === 'query' ? kind : 'pointer',
    idValue: colon > 0 ? idFrom.slice(colon + 1) : '',
    itemsPointer: str('itemsPointer'),
    timePointer: str('timePointer'),
    framePointer: str('framePointer'),
    frameEncoding: str('frameEncoding'),
  };
}

export function draftOf(i: Integration): IntegrationDraft {
  const type = i.type as IntegrationType;
  return {
    ...emptyDraft(type),
    name: i.name ?? '',
    description: i.description ?? '',
    enabled: i.enabled,
    deviceTypeTokens: i.deviceTypes.map((t) => t.token),
    identity: isGeneric(type) ? '' : i.identity,
    aliasName: isGeneric(type) ? aliasNameOf(i.identity) : '',
    linkMetrics: i.linkMetrics,
    ...parseGeneric(i.generic),
  };
}

/** `custom.<name>`'s name from `ALIAS:custom.<name>` ('' for anything else). */
function aliasNameOf(identity: string): string {
  const scheme = identity.startsWith(IDENTITY_ALIAS_PREFIX) ? identity.slice(IDENTITY_ALIAS_PREFIX.length) : '';
  return scheme.startsWith(CUSTOM_PREFIX) ? scheme.slice(CUSTOM_PREFIX.length) : '';
}

// RFC 6901 pointer: "/" segments, "~" only as ~0 or ~1.
const POINTER = /^(\/([^/~]|~[01])*)+$/;
const HEADER_NAME = /^[A-Za-z0-9][A-Za-z0-9-]{0,127}$/;
const QUERY_NAME = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/;
const ALIAS_NAME = /^[a-z0-9][a-z0-9_-]{0,62}$/;
const MAX_TEXT = 256;

export type IntegrationProblem =
  | 'scope'
  | 'aliasName'
  | 'idValue'
  | 'itemsPointer'
  | 'timePointer'
  | 'framePointer'
  | 'frameEncoding';

const pointerOk = (p: string) => p.length <= MAX_TEXT && POINTER.test(p);

/** What is wrong with the draft, worth telling the operator before a round trip. */
export function integrationProblems(d: IntegrationDraft): IntegrationProblem[] {
  const out: IntegrationProblem[] = [];
  if (d.deviceTypeTokens.length === 0) out.push('scope');
  if (!isGeneric(d.type)) return out;
  if (!ALIAS_NAME.test(d.aliasName.trim())) out.push('aliasName');
  const v = d.idValue.trim();
  const idOk =
    d.idKind === 'pointer' ? d.type !== 'GENERIC_BINARY' && pointerOk(v) : d.idKind === 'header' ? HEADER_NAME.test(v) : QUERY_NAME.test(v);
  if (!idOk) out.push('idValue');
  if (d.type === 'GENERIC_JSON') {
    if (d.itemsPointer.trim() && !pointerOk(d.itemsPointer.trim())) out.push('itemsPointer');
    if (d.timePointer.trim() && !pointerOk(d.timePointer.trim())) out.push('timePointer');
    const fp = d.framePointer.trim();
    if (fp && !pointerOk(fp)) out.push('framePointer');
    if (fp && !(FRAME_ENCODINGS as readonly string[]).includes(d.frameEncoding)) out.push('frameEncoding');
    if (!fp && d.frameEncoding) out.push('framePointer');
  }
  return out;
}

/** The `identity` value the request carries. */
export function identityOf(d: IntegrationDraft): string {
  return isGeneric(d.type) ? `${IDENTITY_ALIAS_PREFIX}${CUSTOM_PREFIX}${d.aliasName.trim()}` : d.identity;
}

/**
 * The generic settings document as the server wants it: a JSON object with only the keys that
 * are set. A binary body names its identifier by header or query value only and takes no
 * pointers, so none are written for it whatever the form still holds.
 */
export function genericSettings(d: IntegrationDraft): string {
  const o: Record<string, string> = { idFrom: `${d.idKind}:${d.idValue.trim()}` };
  if (d.type === 'GENERIC_JSON') {
    if (d.itemsPointer.trim()) o.itemsPointer = d.itemsPointer.trim();
    if (d.timePointer.trim()) o.timePointer = d.timePointer.trim();
    if (d.framePointer.trim()) {
      o.framePointer = d.framePointer.trim();
      o.frameEncoding = d.frameEncoding;
    }
  }
  return JSON.stringify(o);
}

export function integrationCreateRequest(token: string, d: IntegrationDraft): IntegrationCreateRequest {
  return {
    token: token.trim(),
    name: d.name.trim() || null,
    description: d.description.trim() || null,
    type: d.type,
    enabled: d.enabled,
    deviceTypeTokens: d.deviceTypeTokens,
    identity: identityOf(d),
    linkMetrics: d.linkMetrics,
    ...(isGeneric(d.type) ? { generic: genericSettings(d) } : {}),
  };
}

const sameSet = (a: readonly string[], b: readonly string[]) =>
  a.length === b.length && [...a].sort().every((v, i) => v === [...b].sort()[i]);

/** A partial update naming only the fields that differ from `initial`. */
export function integrationEditRequest(initial: IntegrationDraft, d: IntegrationDraft): IntegrationUpdateRequest {
  const r: IntegrationUpdateRequest = {};
  if (d.name.trim() !== initial.name.trim()) r.name = d.name.trim() || null;
  if (d.description.trim() !== initial.description.trim()) r.description = d.description.trim() || null;
  if (d.enabled !== initial.enabled) r.enabled = d.enabled;
  if (!sameSet(d.deviceTypeTokens, initial.deviceTypeTokens)) r.deviceTypeTokens = d.deviceTypeTokens;
  if (d.linkMetrics !== initial.linkMetrics) r.linkMetrics = d.linkMetrics;
  if (identityOf(d) !== identityOf(initial)) r.identity = identityOf(d);
  if (isGeneric(d.type) && genericSettings(d) !== genericSettings(initial)) r.generic = genericSettings(d);
  return r;
}
