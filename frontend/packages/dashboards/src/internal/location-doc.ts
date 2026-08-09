// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The typed location document the hub's location channel drives, hand-authored.
//
// Packages carry no graphql-codegen (only apps do), so — like measurement-doc,
// alarm-doc and command-doc — this is written by hand and cast to TypedDocument.
//
// The channel is poll-only: device-state exposes NO location subscription, so this is
// the whole wire. It reads the BATCH form (`latestLocations`, plural), which the
// service documents as the fleet-map query: one round trip for a whole board's worth
// of devices instead of one per marker, which is what keeps a 200-device map's poll
// the same cost as a 2-device map's.
//
// 🔴 A device that has never been located is ABSENT from the result rather than
// present with null coordinates. That is the service's contract, and the channel
// preserves it: the snapshot reports the devices the selector resolved to alongside
// the positions, so a widget can tell "no devices bound" from "devices bound, none
// has ever reported a position" — which are different things to tell an operator.
//
// Requires location:read, which is deliberately NOT in the read-only viewer baseline,
// so a refusal here is an ordinary state (see the hub's forbidden handling) rather
// than a fault.

import type { TypedDocument } from '@devicechain/client';

import type { LocationSample } from '../types';

export interface LatestLocationsResult {
  latestLocations: LocationSample[];
}

export interface LatestLocationsVariables {
  deviceTokens: string[];
}

export const LATEST_LOCATIONS_QUERY = `
  query DashboardLatestLocations($deviceTokens: [String!]!) {
    latestLocations(deviceTokens: $deviceTokens) {
      id
      deviceToken
      latitude
      longitude
      elevation
      accuracy
      speed
      heading
      occurredTime
    }
  }
` as unknown as TypedDocument<LatestLocationsResult, LatestLocationsVariables>;
