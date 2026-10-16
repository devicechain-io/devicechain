// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pure rules of the maintenance-window slot editor: build and read the `slots` JSON the
// server stores, and the client-side HINTS shown while editing.
//
// 🔴 EVERYTHING HERE IS A HINT. It mirrors the server's limits so an obviously wrong row is
// flagged before a round trip; it decides nothing, and the server re-checks every rule (and
// owns the real ones — the zone, unknown keys, what the schedule means). The console also
// never computes when a window is open: that comes from `maintenanceWindowOpening`.

export const DAYS = [
  'MONDAY',
  'TUESDAY',
  'WEDNESDAY',
  'THURSDAY',
  'FRIDAY',
  'SATURDAY',
  'SUNDAY',
] as const;
export type Day = (typeof DAYS)[number];

/** Mirrors the server's limits (hints only). */
export const MIN_SLOTS = 1;
export const MAX_SLOTS = 56;
export const MIN_DURATION_MINUTES = 1;
export const MAX_DURATION_MINUTES = 10080;

/**
 * One editable row. `duration` is the raw text of the minutes field, so a half-typed or
 * empty entry survives in the editor instead of being coerced under the operator's cursor.
 */
export interface SlotDraft {
  day: Day;
  /** 24-hour wall-clock HH:MM in the window's zone. */
  start: string;
  /** Elapsed minutes, as typed. */
  duration: string;
}

export function newSlot(): SlotDraft {
  return { day: 'SATURDAY', start: '02:00', duration: '120' };
}

/**
 * The canonical `slots` document the server expects: a JSON array of
 * `{day, start, durationMinutes}`, in the order the operator listed them. A duration that
 * is not a whole number has no honest value to send, so this throws rather than inventing
 * one — callers check `slotProblems` first.
 */
export function serializeSlots(drafts: readonly SlotDraft[]): string {
  return JSON.stringify(
    drafts.map((s) => {
      const minutes = parseDuration(s.duration);
      if (minutes === null) {
        throw new Error(`slot duration "${s.duration}" is not a whole number of minutes`);
      }
      return { day: s.day, start: s.start, durationMinutes: minutes };
    }),
  );
}

/** A whole number of minutes, or null when the text is not one. */
export function parseDuration(text: string): number | null {
  const trimmed = text.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  return Number(trimmed);
}

/**
 * Read a stored `slots` document back into rows. Null when it is not a readable list —
 * the editor then refuses to open rather than showing an empty schedule that a save would
 * write over the real one.
 */
export function parseSlots(json: string | null | undefined): SlotDraft[] | null {
  if (!json) return null;
  let doc: unknown;
  try {
    doc = JSON.parse(json);
  } catch {
    return null;
  }
  if (!Array.isArray(doc)) return null;
  const out: SlotDraft[] = [];
  for (const raw of doc) {
    if (typeof raw !== 'object' || raw === null) return null;
    const r = raw as Record<string, unknown>;
    if (
      typeof r.day !== 'string' ||
      !(DAYS as readonly string[]).includes(r.day) ||
      typeof r.start !== 'string' ||
      typeof r.durationMinutes !== 'number'
    ) {
      return null;
    }
    out.push({ day: r.day as Day, start: r.start, duration: String(r.durationMinutes) });
  }
  return out;
}

export type SlotProblem = 'start' | 'duration';

export interface SlotHints {
  /** True when the list as a whole is outside 1..56 rows. */
  count: 'tooFew' | 'tooMany' | null;
  /** Per-row problems, keyed by row index. */
  rows: Map<number, SlotProblem[]>;
  /** True when nothing is flagged. */
  ok: boolean;
}

const HH_MM = /^([01]\d|2[0-3]):[0-5]\d$/;

export function slotProblems(drafts: readonly SlotDraft[]): SlotHints {
  const rows = new Map<number, SlotProblem[]>();
  drafts.forEach((s, i) => {
    const problems: SlotProblem[] = [];
    if (!HH_MM.test(s.start)) problems.push('start');
    const minutes = parseDuration(s.duration);
    if (minutes === null || minutes < MIN_DURATION_MINUTES || minutes > MAX_DURATION_MINUTES) {
      problems.push('duration');
    }
    if (problems.length > 0) rows.set(i, problems);
  });
  const count =
    drafts.length < MIN_SLOTS ? 'tooFew' : drafts.length > MAX_SLOTS ? 'tooMany' : null;
  return { count, rows, ok: count === null && rows.size === 0 };
}

export type CoverageProblem = 'overlap' | 'wholeWeek';

const MINUTES_PER_DAY = 1440;
const MINUTES_PER_WEEK = 7 * MINUTES_PER_DAY;

/**
 * A HINT for the one rule a notification policy's quiet hours add to the slot document:
 * slots may not overlap in the week AS WRITTEN (touching is fine; a slot running past the end
 * of Sunday wraps into Monday), and together they may not cover the whole week. The server
 * decides; this only saves a round trip. Rows that fail `slotProblems` are skipped, so the
 * hint never fires on a half-typed row.
 */
export function slotCoverageProblem(drafts: readonly SlotDraft[]): CoverageProblem | null {
  const pieces: Array<[number, number]> = [];
  let total = 0;
  for (const s of drafts) {
    const minutes = parseDuration(s.duration);
    const m = HH_MM.exec(s.start);
    if (minutes === null || m === null || minutes < MIN_DURATION_MINUTES || minutes > MAX_DURATION_MINUTES) {
      continue;
    }
    const start =
      DAYS.indexOf(s.day) * MINUTES_PER_DAY + Number(s.start.slice(0, 2)) * 60 + Number(s.start.slice(3));
    const end = start + minutes;
    total += minutes;
    if (end <= MINUTES_PER_WEEK) {
      pieces.push([start, end]);
    } else {
      pieces.push([start, MINUTES_PER_WEEK], [0, end - MINUTES_PER_WEEK]);
    }
  }
  pieces.sort((a, b) => a[0] - b[0]);
  for (let i = 1; i < pieces.length; i++) {
    if (pieces[i][0] < pieces[i - 1][1]) return 'overlap';
  }
  return total >= MINUTES_PER_WEEK ? 'wholeWeek' : null;
}

/** Split elapsed minutes into whole hours and the remainder, for "2 h 30 min". */
export function splitDuration(minutes: number): { hours: number; minutes: number } {
  return { hours: Math.floor(minutes / 60), minutes: minutes % 60 };
}

/** Whether two row lists describe the same schedule (so an untouched editor is not "dirty"). */
export function slotsEqual(a: readonly SlotDraft[], b: readonly SlotDraft[]): boolean {
  return (
    a.length === b.length &&
    a.every(
      (s, i) =>
        s.day === b[i].day &&
        s.start === b[i].start &&
        parseDuration(s.duration) === parseDuration(b[i].duration),
    )
  );
}

// A short fallback for a runtime without Intl.supportedValuesOf: the zones an operator
// most often needs. The server accepts any IANA name, so this is a convenience list, not
// the vocabulary.
const FALLBACK_TIME_ZONES = [
  'UTC',
  'America/New_York',
  'America/Chicago',
  'America/Denver',
  'America/Los_Angeles',
  'America/Sao_Paulo',
  'Europe/London',
  'Europe/Berlin',
  'Europe/Madrid',
  'Europe/Paris',
  'Africa/Johannesburg',
  'Asia/Dubai',
  'Asia/Kolkata',
  'Asia/Shanghai',
  'Asia/Tokyo',
  'Australia/Sydney',
  'Pacific/Auckland',
];

/**
 * The IANA zones offered by the picker. `current` (a stored zone) is always included so an
 * existing window whose zone this runtime does not list still shows what it has. "UTC" is
 * added when the runtime's list omits it.
 */
export function timeZoneChoices(current?: string | null): string[] {
  let zones: string[];
  try {
    const supported = (
      Intl as unknown as { supportedValuesOf?: (key: string) => string[] }
    ).supportedValuesOf;
    zones = supported ? supported('timeZone') : FALLBACK_TIME_ZONES;
    if (!Array.isArray(zones) || zones.length === 0) zones = FALLBACK_TIME_ZONES;
  } catch {
    zones = FALLBACK_TIME_ZONES;
  }
  const set = new Set(zones);
  set.add('UTC');
  if (current) set.add(current);
  return [...set].sort();
}
