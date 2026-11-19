// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import type { AlarmSuppressionWindowUpdateRequest as Update, MaintenanceWindow } from '@/lib/api/alarm-maintenance';

export const TARGET_TYPES = ['DEVICE', 'DEVICE_TYPE', 'ENTITY_GROUP', 'AREA', 'ASSET', 'CUSTOMER', 'TENANT'] as const;
export const SEVERITIES = ['CRITICAL', 'MAJOR', 'MINOR', 'WARNING', 'INDETERMINATE'] as const;
export type Target = { type: string; token?: string | null };
export const HOUR = 3600000;

/** UI hint mirroring reach; the server rechecks authority and scope on every write. */
export function isBroad(window: { targets: readonly Target[]; kind: string; startTime?: string | null; endTime?: string | null }): boolean {
  return window.kind !== 'ONE_SHOT' || window.targets.some(t => t.type !== 'DEVICE') ||
    Date.parse(window.endTime ?? '') - Date.parse(window.startTime ?? '') > 24 * HOUR;
}

/** Compare normalized editable fields only. Never re-send metadata or server-owned fields. */
export function partialUpdate(before: Update, next: Update): Update {
  return Object.fromEntries(Object.entries(next).filter(([key, value]) =>
    JSON.stringify(value) !== JSON.stringify(before[key as keyof Update])));
}
export function editable(window: MaintenanceWindow): Update {
  return {
    name: window.name ?? null, reason: window.reason, description: window.description ?? null,
    targets: window.targets.map(({ type, token }) => type === 'TENANT' ? { type } : { type, token }),
    alarmKeys: window.alarmKeys, bypassSeverity: window.bypassSeverity ?? null, kind: window.kind,
    startTime: window.startTime ?? null, endTime: window.endTime ?? null,
    timezone: window.timezone ?? null, slots: window.slots ?? null,
    validFrom: window.validFrom ?? null, validUntil: window.validUntil ?? null,
  };
}
export const validKeys = (keys: string[]) => keys.length <= 20 && new Set(keys).size === keys.length &&
  keys.every(key => key.trim().length > 0 && key.length <= 256);
export function localTime(iso?: string | null): string {
  if (!iso) return '';
  const date = new Date(iso);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}
export const isoTime = (text: string) => text ? new Date(text).toISOString() : null;
export function windowUntil(window: MaintenanceWindow, now = Date.now()): string | undefined {
  return window.kind === 'ONE_SHOT' ? window.endTime ?? undefined :
    window.openings.find(o => Date.parse(o.start) <= now && now < Date.parse(o.end))?.end;
}
