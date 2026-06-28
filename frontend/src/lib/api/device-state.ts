// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the device-state service.
import { gql } from '@/lib/graphql/client';
import { graphql } from '@/gql/device-state';
import type { DeviceStatesByDeviceIdQuery } from '@/gql/device-state/graphql';

// Public type derived from the generated operation result so it always reflects
// the actual selection set and can never drift from the schema.
export type DeviceState = DeviceStatesByDeviceIdQuery['deviceStatesByDeviceId'][number];

const DEVICE_STATES_BY_DEVICE_ID = graphql(`
  query DeviceStatesByDeviceId($deviceIds: [Int!]!) {
    deviceStatesByDeviceId(deviceIds: $deviceIds) {
      id
      deviceId
      active
      lastConnectTime
      lastDisconnectTime
      lastActivityTime
      inactivityTimeout
    }
  }
`);

export async function getDeviceState(deviceId: number): Promise<DeviceState | null> {
  const data = await gql('device-state', DEVICE_STATES_BY_DEVICE_ID, { deviceIds: [deviceId] });
  return data.deviceStatesByDeviceId[0] ?? null;
}

// getDeviceStates batch-fetches state for several devices at once — used to
// annotate a device list with online/offline status in one round-trip.
export async function getDeviceStates(deviceIds: number[]): Promise<DeviceState[]> {
  if (deviceIds.length === 0) return [];
  const data = await gql('device-state', DEVICE_STATES_BY_DEVICE_ID, { deviceIds });
  return data.deviceStatesByDeviceId;
}
