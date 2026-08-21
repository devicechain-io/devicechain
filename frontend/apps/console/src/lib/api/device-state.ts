// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-state service.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-state';
import { isForbiddenError } from '@/lib/api/errors';
import type {
  DeviceStatesByDeviceTokenQuery,
  LatestMeasurementsQuery,
  LatestLocationQuery,
} from '@/gql/device-state/graphql';

// Public types derived from the generated operation results so they always
// reflect the actual selection set and can never drift from the schema.
export type DeviceState = DeviceStatesByDeviceTokenQuery['deviceStatesByDeviceToken'][number];
export type LatestMeasurement = LatestMeasurementsQuery['latestMeasurements'][number];
export type LatestLocation = NonNullable<LatestLocationQuery['latestLocation']>;

// 🔴 `presenceSource` is not decoration. It discriminates a REPORTED disconnect
// (ASSERTED — a transport told us the device went away) from mere silence (INFERRED —
// nothing has arrived inside the inactivity window). Without it every screen reads
// `active: false` as the former, which is a claim about the DEVICE the platform cannot
// support for an inferred row. lib/presence.ts is the one place that folds the two
// fields into an answer; do not re-derive it from `active` alone.
//
// The explanation lives here rather than inside the document because the client preset
// keys its generated lookup tables on the document's LITERAL TEXT — a comment in the
// template is copied verbatim into three generated string keys and ships in the bundle.
const DEVICE_STATES_BY_DEVICE_TOKEN = graphql(`
  query DeviceStatesByDeviceToken($deviceTokens: [String!]!) {
    deviceStatesByDeviceToken(deviceTokens: $deviceTokens) {
      id
      deviceToken
      active
      lastConnectTime
      lastDisconnectTime
      lastActivityTime
      inactivityTimeout
      presenceSource
    }
  }
`);

export async function getDeviceState(deviceToken: string): Promise<DeviceState | null> {
  const data = await gql('device-state', DEVICE_STATES_BY_DEVICE_TOKEN, { deviceTokens: [deviceToken] });
  return data.deviceStatesByDeviceToken[0] ?? null;
}

// getDeviceStates batch-fetches state for several devices at once — used to
// annotate a device list with online/offline status in one round-trip.
export async function getDeviceStates(deviceTokens: string[]): Promise<DeviceState[]> {
  if (deviceTokens.length === 0) return [];
  const data = await gql('device-state', DEVICE_STATES_BY_DEVICE_TOKEN, { deviceTokens });
  return data.deviceStatesByDeviceToken;
}

const LATEST_MEASUREMENTS = graphql(`
  query LatestMeasurements($deviceToken: String!) {
    latestMeasurements(deviceToken: $deviceToken) {
      id
      name
      value
      occurredTime
    }
  }
`);

// getLatestMeasurements returns the current value of every measurement name for a
// device — the live "current readings" projection. Requires state:read.
export async function getLatestMeasurements(deviceToken: string): Promise<LatestMeasurement[]> {
  const data = await gql('device-state', LATEST_MEASUREMENTS, { deviceToken });
  return data.latestMeasurements;
}

const LATEST_LOCATION = graphql(`
  query LatestLocation($deviceToken: String!) {
    latestLocation(deviceToken: $deviceToken) {
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
`);

/**
 * The three distinct answers to "where is this device?", as a value rather than
 * as an exception.
 *
 * 🔴 `forbidden` is not an error case. A device's POSITION is gated on its own
 * `location:read` authority, deliberately absent from the read-only viewer
 * baseline, so an ordinary member who can see all of a device's telemetry is
 * routinely refused here. Folding that into `never-located` would tell the
 * operator the device has no position — a claim about the DEVICE — when the
 * truth is a claim about the CALLER. Folding it into a thrown error would show
 * an outage where the platform is working exactly as designed.
 */
export type LatestLocationResult =
  | { kind: 'located'; location: LatestLocation }
  | { kind: 'never-located' }
  | { kind: 'forbidden' };

/**
 * getLatestLocation returns a device's last-known position — the O(1)
 * projection, not a scan of its history. Requires location:read.
 */
export async function getLatestLocation(deviceToken: string): Promise<LatestLocationResult> {
  try {
    const data = await gql('device-state', LATEST_LOCATION, { deviceToken });
    return data.latestLocation
      ? { kind: 'located', location: data.latestLocation }
      : { kind: 'never-located' };
  } catch (err) {
    // Only a refusal becomes a value; every other failure stays a failure, so a
    // broken service is never dressed up as a permission boundary.
    if (isForbiddenError(err)) return { kind: 'forbidden' };
    throw err;
  }
}
