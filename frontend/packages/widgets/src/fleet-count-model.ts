// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the fleet count card says, decided apart from the drawing: how many of a fleet's devices
// are in an abnormal band of one measurement. Pure, so each rule is a test.
//
//   - a device NEEDS ATTENTION when its newest reading of the measurement falls in a band of a
//     level that is not `normal` (below the lowest threshold is also not a band). The normal band
//     is the healthy state, so a fleet in it counts zero and the card stays neutral (ISA-101);
//   - a device with no reading of the measurement is neither healthy nor flagged: it is counted
//     apart as not reporting, never folded into either figure;
//   - the card names the band when every flagged device is in the same labelled one ("Low"), and
//     says "need attention" otherwise.

import type { FleetMember } from '@devicechain/dashboards';
import { bandAt, SEVERITY_LEVELS, isAlarmLevel, type SeverityLevel, type Threshold } from '@devicechain/widget-sdk';

import { cellFor } from './fleet-model';

export interface AttentionCount {
  // The members counted (those the feed delivered; fewer than the fleet when it is truncated).
  members: number;
  flagged: number;
  // Members with a reading of the measurement.
  reporting: number;
  // The worst level among the flagged, or undefined when none is.
  worst: SeverityLevel | undefined;
  // The shared band label of the flagged devices, when they all sit in one labelled band.
  label: string | undefined;
}

export function attentionCount(members: readonly FleetMember[], measurement: string, thresholds: readonly Threshold[]): AttentionCount {
  let flagged = 0;
  let reporting = 0;
  let worst: SeverityLevel | undefined;
  let band: Threshold | undefined;
  let mixed = false;
  for (const m of members) {
    const cell = cellFor(m, measurement);
    if (cell.kind !== 'value') continue;
    reporting++;
    const hit = bandAt(thresholds, cell.value);
    if (!hit || !isAlarmLevel(hit.level)) continue;
    flagged++;
    if (worst === undefined || SEVERITY_LEVELS.indexOf(hit.level) > SEVERITY_LEVELS.indexOf(worst)) worst = hit.level;
    if (band === undefined) band = hit;
    else if (band !== hit) mixed = true;
  }
  return { members: members.length, flagged, reporting, worst, label: !mixed ? band?.label : undefined };
}
