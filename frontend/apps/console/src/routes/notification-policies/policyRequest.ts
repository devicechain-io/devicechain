// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pure request builders of the notification-policy form: what an edit sends (only what
// changed) and what a create sends. Kept free of React so the "omit what did not change" and
// "turning quiet hours off sends two nulls" rules are tested without rendering.

import type {
  NotificationPolicyCreateRequest,
  NotificationPolicyRecord,
  NotificationPolicyUpdateRequest,
} from '@/lib/api/notifications';
import { parseSlots, serializeSlots, slotsEqual, type SlotDraft } from '@/routes/maintenance-windows/slots';

/** The alarm severities a bypass can name, most severe first (the server's vocabulary). */
export const BYPASS_SEVERITIES = ['CRITICAL', 'MAJOR', 'MINOR', 'WARNING', 'INDETERMINATE'] as const;

/** The bypass the form proposes the first time quiet hours are turned on. */
export const DEFAULT_BYPASS_SEVERITY = 'CRITICAL';

/** A sensible first quiet stretch: weeknights from 22:00 for eight hours. */
export function newQuietSlot(): SlotDraft {
  return { day: 'MONDAY', start: '22:00', duration: '480' };
}

export interface PolicyDraft {
  name: string;
  description: string;
  enabled: boolean;
  /** Raw text of the number fields; blank means "not set". */
  throttleSeconds: string;
  escalateAfterSeconds: string;
  maxEscalations: string;
  quietHoursOn: boolean;
  timezone: string;
  slots: SlotDraft[];
  /** A severity, or '' for none (quiet hours hold every severity). */
  bypassSeverity: string;
}

/** A whole number of seconds/count, null when blank, undefined when the text is not one. */
export function parseCount(text: string): number | null | undefined {
  const trimmed = text.trim();
  if (trimmed === '') return null;
  return /^\d+$/.test(trimmed) ? Number(trimmed) : undefined;
}

type Stored = Pick<
  NotificationPolicyRecord,
  | 'name'
  | 'description'
  | 'enabled'
  | 'throttleSeconds'
  | 'escalateAfterSeconds'
  | 'maxEscalations'
  | 'quietHoursTimezone'
  | 'quietHoursSlots'
  | 'quietHoursBypassSeverity'
>;

/** Whether the stored policy has quiet hours at all. */
export function hasQuietHours(entity: Pick<Stored, 'quietHoursTimezone' | 'quietHoursSlots'>): boolean {
  return entity.quietHoursTimezone != null || entity.quietHoursSlots != null;
}

/**
 * The update request for an edit: ONLY what differs from the stored policy. `rules` is never
 * named (omitting it leaves the stored rules untouched). Turning quiet hours off clears the
 * zone and the slots in ONE request and leaves the bypass alone; a stored schedule this
 * console cannot read is never written over, so its three fields are not named at all.
 */
export function policyEditRequest(entity: Stored, next: PolicyDraft): NotificationPolicyUpdateRequest {
  const request: NotificationPolicyUpdateRequest = {};
  if (next.name.trim() !== (entity.name ?? '')) request.name = next.name.trim() || null;
  if (next.description.trim() !== (entity.description ?? '')) {
    request.description = next.description.trim() || null;
  }
  if (next.enabled !== entity.enabled) request.enabled = next.enabled;
  const throttle = parseCount(next.throttleSeconds);
  if (throttle !== undefined && throttle !== (entity.throttleSeconds ?? null)) {
    request.throttleSeconds = throttle;
  }
  const escalate = parseCount(next.escalateAfterSeconds);
  if (escalate !== undefined && escalate !== (entity.escalateAfterSeconds ?? null)) {
    request.escalateAfterSeconds = escalate;
  }
  const maxEsc = parseCount(next.maxEscalations);
  if (maxEsc !== undefined && maxEsc !== (entity.maxEscalations ?? null)) {
    request.maxEscalations = maxEsc;
  }

  const storedOn = hasQuietHours(entity);
  const storedSlots = parseSlots(entity.quietHoursSlots);
  if (storedOn && storedSlots === null) return request; // unreadable: hands off

  if (!next.quietHoursOn) {
    if (storedOn) {
      request.quietHoursTimezone = null;
      request.quietHoursSlots = null;
    }
    return request;
  }
  if (next.timezone !== (entity.quietHoursTimezone ?? '')) request.quietHoursTimezone = next.timezone;
  if (storedSlots === null || !slotsEqual(storedSlots, next.slots)) {
    request.quietHoursSlots = serializeSlots(next.slots);
  }
  if (next.bypassSeverity !== (entity.quietHoursBypassSeverity ?? '')) {
    request.quietHoursBypassSeverity = next.bypassSeverity || null;
  }
  return request;
}

/** The create request: a policy made here has no rules yet (`rules: []`). */
export function policyCreateRequest(token: string, next: PolicyDraft): NotificationPolicyCreateRequest {
  const request: NotificationPolicyCreateRequest = {
    token,
    name: next.name.trim() || null,
    description: next.description.trim() || null,
    enabled: next.enabled,
    throttleSeconds: parseCount(next.throttleSeconds) ?? null,
    escalateAfterSeconds: parseCount(next.escalateAfterSeconds) ?? null,
    maxEscalations: parseCount(next.maxEscalations) ?? null,
    rules: [],
  };
  if (next.quietHoursOn) {
    request.quietHoursTimezone = next.timezone;
    request.quietHoursSlots = serializeSlots(next.slots);
    request.quietHoursBypassSeverity = next.bypassSeverity || null;
  }
  return request;
}
