// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The rules behind a command batch's TARGET: building the device list a batch is fired
// at, and reading back what the service said about the set it resolved.
//
// A pure module rather than logic inside the drawer, because none of it is rendering —
// and because each rule here is one an operator would have no way to check by eye. The
// form is a thin renderer over these functions; the functions get tests.

/**
 * The service's ceiling on one batch (model.MaxBatchDeviceTokens). Anything larger is
 * refused as BATCH_TOO_LARGE.
 *
 * 🔑 It is mirrored here to give the SAME limit as inline feedback instead of a
 * round-trip refusal — it decides nothing. The service re-checks, and the service wins:
 * a batch under this number can still be refused, and if the ceiling ever moves this
 * copy is a hint that is merely stale, never a gate that is wrong.
 */
export const MAX_BATCH_DEVICES = 10000;

/**
 * The device tokens a batch will be fired at, and what building them discarded.
 *
 * 🔴 ORDER IS PART OF THE REQUEST, not a display choice. A partially-admitted batch
 * admits in the order the devices were named, so this order is how an operator expresses
 * priority — which is exactly why the pasted list is kept as pasted rather than sorted or
 * set-ified.
 */
export interface DeviceTargetSet {
  /** The tokens to send, in the order they were named, with repeats collapsed. */
  tokens: string[];
  /** How many entries collapsed into an earlier one. Zero when nothing repeated. */
  duplicates: number;
  /** Whether the set is over the service's per-batch ceiling. */
  overCap: boolean;
}

/**
 * Split a pasted block into device tokens.
 *
 * Newline-, comma- OR whitespace-separated, because a paste comes from wherever the
 * operator had the list — a spreadsheet column, a CSV cell, the output of a query — and
 * a box that accepted only one of those shapes would silently read a comma-separated
 * paste as ONE enormous token, which the service then refuses as an unknown device.
 *
 * Nothing is validated here beyond emptiness. The token grammar is the server's, and a
 * device that does not exist is a refusal the batch record is designed to carry.
 */
export function parsePastedTokens(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((t) => t.trim())
    .filter((t) => t.length > 0);
}

/**
 * Build the device target set from the two ways the form can name devices.
 *
 * Picked devices come first, then the pasted block, and the first occurrence of a token
 * wins — so a device named twice is sent to once, at the position it was first named.
 * Sending it twice would be two physical actuations from one line of a list an operator
 * believes names it once.
 *
 * The two inputs are not redundant. The picker is bounded by the options it was given a
 * page of; the paste box is the only way to express a target set anywhere near the
 * service's ceiling, and the only way to state an order deliberately.
 */
export function buildDeviceTargets(
  picked: readonly string[],
  pastedText: string,
): DeviceTargetSet {
  const seen = new Set<string>();
  const tokens: string[] = [];
  let duplicates = 0;
  for (const raw of [...picked, ...parsePastedTokens(pastedText)]) {
    const token = raw.trim();
    if (token.length === 0) continue;
    if (seen.has(token)) {
      duplicates += 1;
      continue;
    }
    seen.add(token);
    tokens.push(token);
  }
  return { tokens, duplicates, overCap: tokens.length > MAX_BATCH_DEVICES };
}

/**
 * Whether a group's membership is decided by a selector, and therefore versioned.
 *
 * 🔴 The wire value is LOWERCASE — the service writes "static" / "dynamic" (see
 * model.MembershipDynamic) — but the comparison is case-insensitive because this decides
 * whether a `groupVersion` input is OFFERED, and getting that backwards is not cosmetic:
 * naming a version for a static group is REFUSED by the service rather than ignored, and
 * withholding the input from a dynamic group silently pins a fleet actuation to whatever
 * selector was published most recently instead of the one the operator meant.
 */
export function isVersionedGroup(membershipMode: string | null | undefined): boolean {
  return (membershipMode ?? '').trim().toLowerCase() === 'dynamic';
}

/**
 * What a rejection's `resolved` actually says.
 *
 * 🔴 NULL IS NOT ZERO, and `?? 0` is the defect this exists to make impossible.
 *
 *   'unknown'  — null: no target set was ever established. The refusal happened first
 *                (an ambiguous target, an unusable group, a malformed payload), so
 *                nothing is known about how many devices were involved. Reporting that
 *                as "0 devices" tells the operator their target matched nothing, which
 *                is a claim the service did not make and which sends them off to debug a
 *                group that may be perfectly fine.
 *   'none'     — 0: a target that genuinely resolved to no devices.
 *   'some'     — a real count of devices the target resolved to before the refusal.
 *
 * It is a DISCRIMINATED UNION rather than a three-valued tag beside the raw number
 * deliberately: the count is reachable only through the one arm that has one, so a
 * renderer cannot reach for `resolved` in the null case at all. A tag plus `resolved ??
 * 0` at the call site would type-check perfectly and print the exact lie above.
 */
export type ResolvedClaim =
  | { kind: 'unknown' }
  | { kind: 'none' }
  | { kind: 'some'; count: number };

export function resolvedClaim(resolved: number | null | undefined): ResolvedClaim {
  if (resolved == null) return { kind: 'unknown' };
  if (resolved === 0) return { kind: 'none' };
  return { kind: 'some', count: resolved };
}
