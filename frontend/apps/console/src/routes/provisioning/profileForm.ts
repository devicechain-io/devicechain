// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pure half of the provisioning-profile form: the draft the inputs edit, the problems worth
// showing before a round trip, and the two requests built from it. No React and no network, so
// every rule here is unit-tested.
//
// 🔴 AN EDIT SENDS ONLY WHAT THE OPERATOR CHANGED. updateProvisioningProfile is a PARTIAL update
// (omit = leave alone, null = clear, value = set), so `profileEditRequest` compares each field
// with the value the form was seeded from and names only the ones that differ. Two things
// follow, and both are the point:
//
//   * a field this form does not show (metadata, the credential type) is never named, so it
//     survives every edit;
//   * a field it does show but the operator left alone is not named either, so two operators
//     editing different fields never write each other's values back from a stale snapshot, and
//     an expiry that has since been changed elsewhere is not reverted by a rename.
//
// The provision secret is not here at all: it is not an input of create or update. The server
// generates it (see SecretReveal.tsx), and generates none for a certificate profile.

import type {
  ProvisioningProfile,
  ProvisioningProfileCreateRequest,
  ProvisioningProfileUpdateRequest,
} from '@/lib/api/provisioning';

/** What the form's inputs hold. Every field is a string or boolean, as an input has it. */
export interface ProfileDraft {
  name: string;
  description: string;
  provisionKey: string;
  strategy: string;
  deviceTypeToken: string;
  /** Whether the profile accepts registrations. */
  enabled: boolean;
  /** A datetime-local value (`YYYY-MM-DDTHH:mm`, the viewer's zone), or '' for never. */
  expiresAt: string;
  /** A whole number of devices, or '' for no cap. */
  maxDevices: string;
  /**
   * A certificate profile authenticates its devices by a client certificate that chains to a
   * trust anchor: it has no provision secret and no key to choose. Fixed once the profile exists
   * (a profile cannot change how it authenticates, only which anchor it names).
   */
  certificate: boolean;
  /** The token of the trust anchor a certificate profile names; '' until one is picked. */
  trustAnchorToken: string;
}

export const emptyDraft = (): ProfileDraft => ({
  name: '',
  description: '',
  provisionKey: '',
  strategy: 'CHECK_PRE_PROVISIONED',
  deviceTypeToken: '',
  enabled: true,
  expiresAt: '',
  maxDevices: '',
  certificate: false,
  trustAnchorToken: '',
});

const pad = (n: number) => String(n).padStart(2, '0');

/** An RFC 3339 instant as a datetime-local value in the viewer's zone ('' when absent or unreadable). */
export function toLocalInput(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** A datetime-local value as the RFC 3339 instant the server reads; null when blank or unreadable. */
export function fromLocalInput(value: string): string | null {
  if (!value) return null;
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? null : d.toISOString();
}

/** The draft a stored profile seeds the edit form with. */
export function draftOf(p: ProvisioningProfile): ProfileDraft {
  return {
    name: p.name ?? '',
    description: p.description ?? '',
    provisionKey: p.provisionKey,
    strategy: p.strategy,
    deviceTypeToken: p.deviceType.token,
    enabled: p.enabled,
    expiresAt: toLocalInput(p.expiresAt),
    maxDevices: p.maxDevices == null ? '' : String(p.maxDevices),
    certificate: p.trustAnchor != null,
    trustAnchorToken: p.trustAnchor?.token ?? '',
  };
}

/** The cap as a number, '' as null (no cap), and undefined for text that is not a whole number >= 1. */
export function parseMaxDevices(value: string): number | null | undefined {
  const v = value.trim();
  if (v === '') return null;
  if (!/^\d+$/.test(v)) return undefined;
  const n = Number(v);
  return Number.isSafeInteger(n) && n >= 1 ? n : undefined;
}

export type ProfileProblem = 'key' | 'type' | 'cap' | 'expiry' | 'anchor';

/** What is wrong with the draft, as a hint. The server decides; this only saves a round trip. */
export function profileProblems(draft: ProfileDraft): ProfileProblem[] {
  const out: ProfileProblem[] = [];
  // A certificate profile has no key to choose (see profileCreateRequest); it must name an anchor.
  if (draft.certificate) {
    if (!draft.trustAnchorToken) out.push('anchor');
  } else if (!draft.provisionKey.trim()) out.push('key');
  if (!draft.deviceTypeToken) out.push('type');
  if (parseMaxDevices(draft.maxDevices) === undefined) out.push('cap');
  if (draft.expiresAt && fromLocalInput(draft.expiresAt) === null) out.push('expiry');
  return out;
}

/** The create request for a draft. `metadata` and the credential type are left to the server. */
export function profileCreateRequest(token: string, draft: ProfileDraft): ProvisioningProfileCreateRequest {
  const request: ProvisioningProfileCreateRequest = {
    token: token.trim(),
    name: draft.name.trim() || null,
    description: draft.description.trim() || null,
    provisionKey: draft.provisionKey,
    strategy: draft.strategy,
    deviceTypeToken: draft.deviceTypeToken,
    enabled: draft.enabled,
  };
  if (draft.certificate) {
    request.trustAnchorToken = draft.trustAnchorToken;
    request.credentialType = 'X509_CERTIFICATE';
    // The server still requires a provision key to be present, though nothing presents it for a
    // certificate profile (no secret exists). The profile's own token is unique, so it is used.
    request.provisionKey = request.token;
  }
  const expires = fromLocalInput(draft.expiresAt);
  if (expires) request.expiresAt = expires;
  const cap = parseMaxDevices(draft.maxDevices);
  if (cap != null) request.maxDevices = cap;
  return request;
}

/**
 * The update request for an edit: ONLY the fields whose value differs from `initial`, the draft
 * the form was seeded from. Empty when nothing changed (the form then offers no save).
 */
export function profileEditRequest(initial: ProfileDraft, next: ProfileDraft): ProvisioningProfileUpdateRequest {
  const request: ProvisioningProfileUpdateRequest = {};
  if (next.name.trim() !== initial.name.trim()) request.name = next.name.trim() || null;
  if (next.description.trim() !== initial.description.trim()) {
    request.description = next.description.trim() || null;
  }
  // Compared and sent as typed: a key is stored exactly as sent, padding included. A
  // certificate profile's key is not shown or editable, so it is never named.
  if (!next.certificate && next.provisionKey !== initial.provisionKey) request.provisionKey = next.provisionKey;
  // Re-pointing a certificate profile at another anchor. Never sent for a secret profile (the
  // server refuses it) and never cleared (the server refuses that too).
  if (next.certificate && next.trustAnchorToken && next.trustAnchorToken !== initial.trustAnchorToken) {
    request.trustAnchorToken = next.trustAnchorToken;
  }
  if (next.strategy !== initial.strategy) request.strategy = next.strategy;
  if (next.deviceTypeToken !== initial.deviceTypeToken) request.deviceTypeToken = next.deviceTypeToken;
  if (next.enabled !== initial.enabled) request.enabled = next.enabled;
  if (next.expiresAt !== initial.expiresAt) request.expiresAt = fromLocalInput(next.expiresAt);
  if (next.maxDevices.trim() !== initial.maxDevices.trim()) {
    request.maxDevices = parseMaxDevices(next.maxDevices) ?? null;
  }
  return request;
}

/** True when a profile's expiry has passed: it answers registrations with a refusal. */
export function isExpired(expiresAt: string | null | undefined, now: number = Date.now()): boolean {
  if (!expiresAt) return false;
  const t = new Date(expiresAt).getTime();
  return !Number.isNaN(t) && t <= now;
}
