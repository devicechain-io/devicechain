// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations for the device-replacement lifecycle: binding a new
// physical unit to an existing logical device identity, and reading the
// append-only journal of past swaps.
//
// The two halves sit at DIFFERENT authority levels, and the split is deliberate:
//
//   - `replaceDevice` returns the credential minted for the incoming unit, whose
//     credentialId is the bearer for ACCESS_TOKEN, so it needs device:write —
//     exactly like createDeviceCredential.
//   - `deviceReplacements` names credentials only by their entity tokens, so it
//     carries no bearer and reads at device:read. A maintenance history an
//     operator cannot open answers nobody's question.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type { DeviceReplacementsQuery } from '@/gql/device-management/graphql';

export type DeviceReplacement =
  DeviceReplacementsQuery['deviceReplacements']['results'][number];
export type DeviceReplacementSearchResults = DeviceReplacementsQuery['deviceReplacements'];

const DEVICE_REPLACEMENTS = graphql(`
  query DeviceReplacements($criteria: DeviceReplacementSearchCriteria!) {
    deviceReplacements(criteria: $criteria) {
      results {
        id
        occurredTime
        actor
        reason
        unitIdentifier
        retiredCredentialTokens
        newCredentialToken
        newCredentialType
        device {
          id
          token
        }
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

const REPLACE_DEVICE = graphql(`
  mutation ReplaceDevice($request: DeviceReplaceRequest!) {
    replaceDevice(request: $request) {
      device {
        id
        token
      }
      replacement {
        id
        occurredTime
        actor
        reason
        unitIdentifier
      }
      newCredential {
        id
        token
        credentialType
        credentialId
      }
      retiredCredentialTokens
    }
  }
`);

// A page of a device's replacement history, newest first. Requires device:read.
export async function listDeviceReplacements(
  deviceToken: string,
  opts: { pageNumber: number; pageSize: number },
): Promise<DeviceReplacementSearchResults> {
  const data = await gql('device-management', DEVICE_REPLACEMENTS, {
    criteria: { device: deviceToken, ...opts },
  });
  return data.deviceReplacements;
}

// What the console can say about a swap. Deliberately narrower than the mutation
// input: the identity fields are absent from the API's own request shape, so there
// is nothing here to omit — the console cannot move a device's token, external id
// or type through a replacement because the server has no field for it.
export interface ReplaceDeviceRequest {
  deviceToken: string;
  credentialType?: string | null;
  credentialId?: string | null;
  credentialValue?: string | null;
  reason?: string | null;
  unitIdentifier?: string | null;
}

// Bind a new physical unit to an existing device identity. Requires device:write.
//
// The returned newCredential is the ONLY time the incoming unit's material is
// readable, so the caller must show it once and never re-fetch it: the journal
// stores the credential's entity token, not its id, and re-reading the credential
// itself needs the separately-gated credential queries.
export async function replaceDevice(request: ReplaceDeviceRequest) {
  const data = await gql('device-management', REPLACE_DEVICE, { request });
  return data.replaceDevice;
}
