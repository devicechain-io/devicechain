// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The rules for choosing which targets of a finished fleet write to retry.
//
// Plain, unit-tested functions rather than logic inside the panel, because they ARE the
// safety of the feature: the panel must never offer a target that may already have acted as
// though it were safe, and must never let one be sent without the operator saying so.
//
// 🔴 CLASSES COME FROM THE STORED OUTCOME, NEVER FROM STATUS OR ERROR TEXT. FAILED spans
// three different facts (never delivered, the device said no, outcome unknown) and only the
// writer that finished the command knew which; the service records that as `outcome`, and
// this module reads nothing else. A finished command with an unrecognised outcome is treated
// as UNKNOWN — the class that asks for an acknowledgement — rather than offered as safe.

export type RetryClass = 'NOT_DELIVERED' | 'REJECTED' | 'UNKNOWN' | 'REFUSED';

// The order the panel lists them in: proven-safe first, may-have-acted last.
export const RETRY_CLASSES: RetryClass[] = ['NOT_DELIVERED', 'REJECTED', 'REFUSED', 'UNKNOWN'];

export interface RetryCandidate {
  deviceToken: string;
  cls: RetryClass;
  // The command's own error text, or the refusal reason: displayed, never parsed.
  detail: string | null;
}

interface CommandLike {
  deviceToken: string;
  outcome?: string | null;
  error?: string | null;
}

interface RefusalLike {
  deviceToken: string;
  code: string;
  reason: string;
}

interface RefusalCountLike {
  code: string;
  count: number;
}

// classOf maps a finished command to the class a retry treats it as, or null when it is not
// retryable at all (it succeeded, or has not finished — no outcome yet).
export function classOf(c: CommandLike): RetryClass | null {
  switch (c.outcome) {
    case 'NOT_DELIVERED':
    case 'REJECTED':
    case 'UNKNOWN':
      return c.outcome;
    case 'SUCCEEDED':
    case null:
    case undefined:
      return null;
    default:
      // An outcome this build does not know: conservative, never "safe".
      return 'UNKNOWN';
  }
}

// completeRefusals returns the refused devices the batch record holds IN FULL: the record
// keeps a bounded sample per code beside exact per-code totals, so a device is only offered
// when its code's sample equals that code's total. A device from a truncated code might or
// might not have been a member, and the service refuses it — so it is not offered.
export function completeRefusals(
  refusals: RefusalLike[],
  counts: RefusalCountLike[],
): RefusalLike[] {
  const total = new Map(counts.map((c) => [c.code, c.count]));
  const held = new Map<string, number>();
  for (const r of refusals) held.set(r.code, (held.get(r.code) ?? 0) + 1);
  return refusals.filter((r) => held.get(r.code) === total.get(r.code));
}

// buildCandidates folds the unsuccessful commands and the completely-recorded refusals into
// one list, one entry per device (a device with a command row is never also listed as
// refused).
export function buildCandidates(
  commands: CommandLike[],
  refusals: RefusalLike[],
  counts: RefusalCountLike[],
): RetryCandidate[] {
  const out: RetryCandidate[] = [];
  const seen = new Set<string>();
  for (const c of commands) {
    const cls = classOf(c);
    if (!cls || seen.has(c.deviceToken)) continue;
    seen.add(c.deviceToken);
    out.push({ deviceToken: c.deviceToken, cls, detail: c.error ?? null });
  }
  for (const r of completeRefusals(refusals, counts)) {
    if (seen.has(r.deviceToken)) continue;
    seen.add(r.deviceToken);
    out.push({ deviceToken: r.deviceToken, cls: 'REFUSED', detail: r.reason });
  }
  return out;
}

// The classes selected when the panel opens: the ones that provably did not act. REFUSED
// devices are offered but not preselected (the reason they were refused may still hold), and
// UNKNOWN is never preselected.
export function defaultSelection(candidates: RetryCandidate[]): Set<string> {
  return new Set(
    candidates
      .filter((c) => c.cls === 'NOT_DELIVERED' || c.cls === 'REJECTED')
      .map((c) => c.deviceToken),
  );
}

// An UNKNOWN target can be selected only once the operator has acknowledged it.
export function isSelectable(c: RetryCandidate, acknowledgedUnknown: boolean): boolean {
  return c.cls !== 'UNKNOWN' || acknowledgedUnknown;
}

export interface RetrySubmission {
  deviceTokens: string[];
  includeUnknown: boolean;
}

// planSubmission is what goes on the wire: the selected devices, in listed order, and
// `includeUnknown` set exactly when an unknown-outcome device is among them AND it was
// acknowledged. Selected UNKNOWN devices whose acknowledgement was withdrawn are dropped,
// not sent.
export function planSubmission(
  candidates: RetryCandidate[],
  selected: Set<string>,
  acknowledgedUnknown: boolean,
): RetrySubmission {
  const chosen = candidates.filter(
    (c) => selected.has(c.deviceToken) && isSelectable(c, acknowledgedUnknown),
  );
  return {
    deviceTokens: chosen.map((c) => c.deviceToken),
    includeUnknown: acknowledgedUnknown && chosen.some((c) => c.cls === 'UNKNOWN'),
  };
}
