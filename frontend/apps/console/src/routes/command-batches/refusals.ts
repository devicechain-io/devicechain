// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Joining a batch's TWO refusal shapes into what the Refusals panel renders.
//
// 🔴 THE TWO ARE NOT THE SAME SET, AND ONLY ONE OF THEM IS COMPLETE.
//
//   refusalCounts  the EXACT number of devices refused, per code. Never truncated.
//   refusals       a BOUNDED SAMPLE of the individual devices — capped at 100 per code —
//                  so one fleet write cannot store an unbounded blob.
//
// A batch fired at a large group can refuse far more devices than anyone can read, so the
// names are capped while the totals stay exact. Rendering the sample as if it were the
// whole set is therefore not a cosmetic slip: it tells an operator that 100 devices were
// refused when 4,312 were, and the 4,212 they were not told about are precisely the ones
// that did not get the command. The counts are the authority here; the sample is labelled
// as a sample, and a group whose sample is short of its count says so.
//
// A pure module rather than JSX because it is the rule, not the rendering — the panel is
// a thin renderer over these groups, and the rule gets a test.

export interface RefusalSampleEntry {
  deviceToken: string;
  code: string;
  reason: string;
}

export interface RefusalCount {
  code: string;
  count: number;
}

export interface RefusalGroup {
  /** The backend's stable classification. English by policy — rendered verbatim. */
  code: string;
  /**
   * The exact number of devices refused with this code.
   *
   * NULL MEANS UNKNOWN, NOT ZERO. It occurs only when the sample carries a code the
   * counts do not mention — a server that answered inconsistently. Dropping those sample
   * rows would hide real refused devices; counting them would invent an exact total out
   * of a capped sample. So they are shown, and their total is declared unknown.
   */
  count: number | null;
  /** The individual devices we were given for this code — capped, possibly empty. */
  sample: RefusalSampleEntry[];
}

/** Whether this group's sample is short of the exact total, i.e. names only some of them. */
export function isSampleTruncated(group: RefusalGroup): boolean {
  return group.count !== null && group.count > group.sample.length;
}

/** The exact total number of devices refused, across every code. */
export function totalRefused(counts: readonly RefusalCount[]): number {
  return counts.reduce((sum, c) => sum + c.count, 0);
}

/**
 * Group a batch's refusals by code, with the EXACT count from `refusalCounts` and the
 * bounded sample from `refusals`.
 *
 * Ordering is by count descending, then by code, so the largest refusal — the one most
 * worth acting on — leads. A group with an unknown count sorts last: it is an anomaly,
 * not a headline.
 */
export function groupRefusals(
  counts: readonly RefusalCount[],
  sample: readonly RefusalSampleEntry[],
): RefusalGroup[] {
  const byCode = new Map<string, RefusalGroup>();
  // The counts come first, so every code the server declared gets a group even when the
  // sample happens to carry none of its devices.
  for (const c of counts) {
    byCode.set(c.code, { code: c.code, count: c.count, sample: [] });
  }
  for (const entry of sample) {
    const existing = byCode.get(entry.code);
    if (existing) existing.sample.push(entry);
    else byCode.set(entry.code, { code: entry.code, count: null, sample: [entry] });
  }
  return [...byCode.values()].sort((a, b) => {
    if (a.count === null && b.count !== null) return 1;
    if (b.count === null && a.count !== null) return -1;
    if (a.count !== null && b.count !== null && a.count !== b.count) return b.count - a.count;
    return a.code.localeCompare(b.code);
  });
}
