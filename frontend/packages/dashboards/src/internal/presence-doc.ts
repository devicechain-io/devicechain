// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The typed device-state document the hub's presence read drives, hand-authored (packages carry no
// graphql-codegen; see measurement-doc).
//
// `deviceStatesByDeviceToken` is the BATCH read (up to 1000 tokens): one round trip covers a whole
// board's worth of devices. A device with no state yet is ABSENT from the result, not present with
// nulls -- the service's contract, which the channel keeps. Requires state:read.

import type { TypedDocument } from '@devicechain/client';

import type { DevicePresence } from '../types';

export interface DeviceStatesResult {
  deviceStatesByDeviceToken: DevicePresence[];
}

export interface DeviceStatesVariables {
  deviceTokens: string[];
}

export const DEVICE_PRESENCE_QUERY = `
  query DashboardDevicePresence($deviceTokens: [String!]!) {
    deviceStatesByDeviceToken(deviceTokens: $deviceTokens) {
      deviceToken
      active
      lastActivityTime
      inactivityTimeout
    }
  }
` as unknown as TypedDocument<DeviceStatesResult, DeviceStatesVariables>;
