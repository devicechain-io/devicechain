// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// How a command's lifecycle status LOOKS in the console.
//
// 🔴 ONE COPY, for the same reason the status SETS have one copy in
// @devicechain/dashboards: two screens now render command rows — the device's command
// history and a batch's per-device rows — and a second hand-written switch is how the
// two would come to disagree about what an operator is looking at. The predicates
// themselves (isTerminalCommandStatus / isCancellableCommandStatus) are NOT redeclared
// here; import them from @devicechain/dashboards.
//
// It lives in the console rather than in that package because a Badge variant is a
// console-kit token: `dashboards` is deliberately Tailwind-free and framework-neutral
// so it can run inside a host, and it has no opinion about how a badge is painted.

/** The Badge variants a command status can map to. `default` is deliberately unused. */
export type CommandStatusVariant = 'success' | 'destructive' | 'outline' | 'secondary';

export function commandStatusVariant(status: string): CommandStatusVariant {
  switch (status) {
    case 'SUCCESSFUL':
      return 'success';
    case 'FAILED':
    case 'TIMEOUT':
      return 'destructive';
    // Terminal but not a failure: nobody broke, the command simply stopped. EXPIRED ran
    // out of time, CANCELLED was called off on purpose. The muted outline says "over"
    // without the red that would report a fault to an operator scanning the column.
    case 'EXPIRED':
    case 'CANCELLED':
      return 'outline';
    // HELD and PARKED are waiting on the DEVICE, not lost — they carry the same in-flight
    // styling as QUEUED / SENT on purpose, because the command still stands and can still
    // be cancelled. HELD was never dispatched (the device was known absent); PARKED was
    // published and found nobody there, and the platform will deliver it when the device
    // wakes. Both are spelled out rather than left to the default so this stays a decision
    // rather than an accident of fall-through.
    case 'HELD':
    case 'PARKED':
      return 'secondary';
    default:
      // QUEUED / SENT — still in flight, as is any status this console does not yet know.
      return 'secondary';
  }
}
