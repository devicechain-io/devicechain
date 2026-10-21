// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the fleet count card says, decided apart from the drawing: how many of a fleet's devices
// are in an abnormal band of one measurement. Pure, so each rule is a test.
//
//   - a device NEEDS ATTENTION when its newest reading of the measurement falls in a band of a
//     level that is not `normal` (below the lowest threshold is also not a band). The normal band
//     is the healthy state, so a fleet in it counts zero and the card stays neutral (ISA-101);
//   - a device with no reading of the measurement is neither healthy nor flagged: it is counted
//     apart, never folded into either figure -- as NOT REPORTING when its read settled and found
//     nothing (it sent nothing in the span, or sent other measurements), and as NOT READ when the
//     read failed or did not load its newest reading (the device may well be reporting);
//   - a device that reports OTHER measurements but never this one (a plant counted among the
//     machines in a fuel count) is outside the question, not silent: it is left out of the "of N"
//     and counted apart again (NOT MEASURED), so the card neither calls it "not reporting" nor
//     drops it unseen -- a sensor that stopped sending this one measurement still shows;
//   - while any member's read is still loading there is no count yet: a figure over the members
//     read so far would undercount, and nothing on the card would say so;
//   - the card names the band when every flagged device is in the same labelled one ("Low"), and
//     says "need attention" otherwise.

import type { FleetMember } from '@devicechain/dashboards';
import { bandAt, SEVERITY_LEVELS, isAlarmLevel, type SeverityLevel, type Threshold } from '@devicechain/widget-sdk';

import { cellFor } from './fleet-model';

export interface AttentionCount {
  // The members the question covers: those the feed delivered (fewer than the fleet when it is
  // truncated), less the ones that do not report the measurement at all.
  members: number;
  // Members that reported other measurements in the span but never this one.
  notMeasured: number;
  flagged: number;
  // Members with a reading of the measurement.
  reporting: number;
  // Members whose read is still loading: while any is, there is no figure yet.
  pending: number;
  // Members whose read failed, or did not load their newest reading: not known to be silent.
  unread: number;
  // The worst level among the flagged, or undefined when none is.
  worst: SeverityLevel | undefined;
  // The shared band label of the flagged devices, when they all sit in one labelled band.
  label: string | undefined;
}

export function attentionCount(members: readonly FleetMember[], measurement: string, thresholds: readonly Threshold[]): AttentionCount {
  let flagged = 0;
  let reporting = 0;
  let pending = 0;
  let unread = 0;
  let notMeasured = 0;
  let worst: SeverityLevel | undefined;
  let band: Threshold | undefined;
  let mixed = false;
  for (const m of members) {
    const cell = cellFor(m, measurement);
    if (cell.kind === 'loading') pending++;
    else if (cell.kind === 'failed' || cell.kind === 'unread') unread++;
    else if (cell.kind === 'not-reported') notMeasured++;
    if (cell.kind !== 'value') continue;
    reporting++;
    const hit = bandAt(thresholds, cell.value);
    if (!hit || !isAlarmLevel(hit.level)) continue;
    flagged++;
    if (worst === undefined || SEVERITY_LEVELS.indexOf(hit.level) > SEVERITY_LEVELS.indexOf(worst)) worst = hit.level;
    if (band === undefined) band = hit;
    else if (band !== hit) mixed = true;
  }
  return { members: members.length - notMeasured, notMeasured, flagged, reporting, pending, unread, worst, label: !mixed ? band?.label : undefined };
}
