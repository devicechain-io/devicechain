// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { policyCreateRequest, policyEditRequest, type PolicyDraft } from './policyRequest';

const SLOTS = '[{"day":"MONDAY","start":"22:00","durationMinutes":480}]';

const stored = (over: Record<string, unknown> = {}) => ({
  name: 'Night' as string | null,
  description: null as string | null,
  enabled: true,
  throttleSeconds: 300 as number | null,
  escalateAfterSeconds: null as number | null,
  maxEscalations: null as number | null,
  quietHoursTimezone: 'Europe/Berlin' as string | null,
  quietHoursSlots: SLOTS as string | null,
  quietHoursBypassSeverity: 'CRITICAL' as string | null,
  ...over,
});

const same: PolicyDraft = {
  name: 'Night',
  description: '',
  enabled: true,
  throttleSeconds: '300',
  escalateAfterSeconds: '',
  maxEscalations: '',
  quietHoursOn: true,
  timezone: 'Europe/Berlin',
  slots: [{ day: 'MONDAY', start: '22:00', duration: '480' }],
  bypassSeverity: 'CRITICAL',
};

describe('policyEditRequest', () => {
  it('is empty when nothing changed, and never names rules', () => {
    const req = policyEditRequest(stored(), same);
    expect(req).toEqual({});
    expect('rules' in req).toBe(false);
  });

  it('names only what changed', () => {
    expect(policyEditRequest(stored(), { ...same, enabled: false })).toEqual({ enabled: false });
    expect(policyEditRequest(stored(), { ...same, throttleSeconds: '' })).toEqual({ throttleSeconds: null });
    expect(policyEditRequest(stored(), { ...same, escalateAfterSeconds: '600' })).toEqual({
      escalateAfterSeconds: 600,
    });
    expect(policyEditRequest(stored(), { ...same, timezone: 'UTC' })).toEqual({ quietHoursTimezone: 'UTC' });
    expect(policyEditRequest(stored(), { ...same, bypassSeverity: '' })).toEqual({
      quietHoursBypassSeverity: null,
    });
    expect(
      policyEditRequest(stored(), { ...same, slots: [{ day: 'SUNDAY', start: '23:00', duration: '60' }] }),
    ).toEqual({ quietHoursSlots: '[{"day":"SUNDAY","start":"23:00","durationMinutes":60}]' });
  });

  it('turning quiet hours off clears the zone and the slots together and leaves the bypass alone', () => {
    const req = policyEditRequest(stored(), { ...same, quietHoursOn: false, bypassSeverity: 'MAJOR' });
    expect(req).toEqual({ quietHoursTimezone: null, quietHoursSlots: null });
    expect('quietHoursBypassSeverity' in req).toBe(false);
  });

  it('sends nothing about quiet hours that were off and stay off', () => {
    const none = stored({ quietHoursTimezone: null, quietHoursSlots: null, quietHoursBypassSeverity: null });
    expect(policyEditRequest(none, { ...same, quietHoursOn: false, bypassSeverity: '' })).toEqual({});
  });

  it('turning quiet hours on sends the zone, the slots and the bypass', () => {
    const none = stored({ quietHoursTimezone: null, quietHoursSlots: null, quietHoursBypassSeverity: null });
    expect(policyEditRequest(none, same)).toEqual({
      quietHoursTimezone: 'Europe/Berlin',
      quietHoursSlots: SLOTS,
      quietHoursBypassSeverity: 'CRITICAL',
    });
  });

  it('never writes over a stored schedule it cannot read', () => {
    const odd = stored({ quietHoursSlots: '{"weird":true}' });
    expect(policyEditRequest(odd, { ...same, quietHoursOn: false, name: 'Renamed' })).toEqual({ name: 'Renamed' });
  });
});

describe('policyCreateRequest', () => {
  it('creates with no rules and no quiet hours by default', () => {
    const req = policyCreateRequest('p1', { ...same, quietHoursOn: false });
    expect(req.rules).toEqual([]);
    expect('quietHoursTimezone' in req).toBe(false);
    expect(req.enabled).toBe(true);
  });

  it('carries quiet hours when on', () => {
    const req = policyCreateRequest('p1', same);
    expect(req.quietHoursTimezone).toBe('Europe/Berlin');
    expect(req.quietHoursSlots).toBe(SLOTS);
    expect(req.quietHoursBypassSeverity).toBe('CRITICAL');
  });
});
