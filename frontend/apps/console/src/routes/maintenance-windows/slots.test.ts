// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import {
  MAX_SLOTS,
  parseSlots,
  serializeSlots,
  slotProblems,
  slotsEqual,
  splitDuration,
  timeZoneChoices,
  type SlotDraft,
} from './slots';

const row = (over: Partial<SlotDraft> = {}): SlotDraft => ({
  day: 'SATURDAY',
  start: '02:00',
  duration: '120',
  ...over,
});

describe('serializeSlots', () => {
  it('produces the canonical document the server expects', () => {
    const json = serializeSlots([row(), row({ day: 'SUNDAY', start: '23:30', duration: '10080' })]);
    expect(json).toBe(
      '[{"day":"SATURDAY","start":"02:00","durationMinutes":120},' +
        '{"day":"SUNDAY","start":"23:30","durationMinutes":10080}]',
    );
  });

  it('sends the duration as a number, not the text typed', () => {
    const [slot] = JSON.parse(serializeSlots([row({ duration: ' 45 ' })]));
    expect(slot.durationMinutes).toBe(45);
  });

  it('refuses to invent a number for a duration that is not one', () => {
    expect(() => serializeSlots([row({ duration: '' })])).toThrow();
    expect(() => serializeSlots([row({ duration: '1.5' })])).toThrow();
  });

  it('round-trips through parseSlots', () => {
    const rows = [row(), row({ day: 'MONDAY', start: '00:00', duration: '1' })];
    expect(parseSlots(serializeSlots(rows))).toEqual(rows);
  });
});

describe('parseSlots', () => {
  it('returns null for anything that is not a readable list, never an empty schedule', () => {
    expect(parseSlots(null)).toBeNull();
    expect(parseSlots('')).toBeNull();
    expect(parseSlots('{not json')).toBeNull();
    expect(parseSlots('{"day":"MONDAY"}')).toBeNull();
    expect(parseSlots('[{"day":"FUNDAY","start":"02:00","durationMinutes":5}]')).toBeNull();
    expect(parseSlots('[{"day":"MONDAY","start":"02:00"}]')).toBeNull();
  });
});

describe('slotProblems (a hint, not a verdict)', () => {
  it('accepts a well-formed schedule', () => {
    expect(slotProblems([row()]).ok).toBe(true);
  });

  it('flags a start that is not HH:MM and a duration outside 1..10080', () => {
    const hints = slotProblems([row({ start: '2am' }), row({ duration: '0' }), row({ duration: '10081' }), row({ duration: 'x' })]);
    expect(hints.rows.get(0)).toEqual(['start']);
    expect(hints.rows.get(1)).toEqual(['duration']);
    expect(hints.rows.get(2)).toEqual(['duration']);
    expect(hints.rows.get(3)).toEqual(['duration']);
    expect(hints.ok).toBe(false);
  });

  it('flags the row count outside 1..56', () => {
    expect(slotProblems([]).count).toBe('tooFew');
    expect(slotProblems(Array.from({ length: MAX_SLOTS + 1 }, () => row())).count).toBe('tooMany');
    expect(slotProblems(Array.from({ length: MAX_SLOTS }, () => row())).ok).toBe(true);
  });
});

describe('helpers', () => {
  it('splits elapsed minutes into hours and minutes', () => {
    expect(splitDuration(150)).toEqual({ hours: 2, minutes: 30 });
    expect(splitDuration(45)).toEqual({ hours: 0, minutes: 45 });
  });

  it('compares schedules by meaning, not by the text typed', () => {
    expect(slotsEqual([row({ duration: '120' })], [row({ duration: '0120' })])).toBe(true);
    expect(slotsEqual([row()], [row({ start: '03:00' })])).toBe(false);
  });

  it('always offers the stored zone and UTC', () => {
    const zones = timeZoneChoices('Mars/Olympus_Mons');
    expect(zones).toContain('Mars/Olympus_Mons');
    expect(zones).toContain('UTC');
  });
});
