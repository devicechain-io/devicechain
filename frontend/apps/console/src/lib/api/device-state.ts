// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-state service.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-state';
import type {
  DeviceStatesByDeviceTokenQuery,
  LatestMeasurementsQuery,
} from '@/gql/device-state/graphql';

// Public types derived from the generated operation results so they always
// reflect the actual selection set and can never drift from the schema.
export type DeviceState = DeviceStatesByDeviceTokenQuery['deviceStatesByDeviceToken'][number];
export type LatestMeasurement = LatestMeasurementsQuery['latestMeasurements'][number];

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
