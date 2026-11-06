// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations behind the device-profile Decoder tab: the draft decoder and the
// one frozen into the active published version, the save / clear mutations (device:write),
// the test door (device:read, except a stored sample which needs device:write) and the
// function library that drives the editor's help.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type {
  PayloadDecoderTestInput,
  ProfileDecoderQuery,
  TestPayloadDecoderQuery,
  PayloadDecoderFunctionsQuery,
} from '@/gql/device-management/graphql';

export type { PayloadDecoderTestInput };
export type ProfileDecoders = NonNullable<ProfileDecoderQuery['deviceProfilesByToken'][number]>;
export type StoredDecoder = NonNullable<ProfileDecoders['decoder']>;
export type PayloadDecoderTestResult = TestPayloadDecoderQuery['testPayloadDecoder'];
export type PayloadDecoderFunction = PayloadDecoderFunctionsQuery['payloadDecoderFunctions'][number];

const PROFILE_DECODER = graphql(`
  query ProfileDecoder($tokens: [String!]!) {
    deviceProfilesByToken(tokens: $tokens) {
      token
      decoder {
        kind
        envVersion
        input
        document
        maxReadings
        estimatedCost
      }
      activeProfileVersion {
        version
        decoder {
          kind
          envVersion
          input
          document
          maxReadings
          estimatedCost
        }
      }
    }
  }
`);

/** The draft decoder and the active version's, by profile token. null when no profile has the token. */
export async function getProfileDecoders(token: string): Promise<ProfileDecoders | null> {
  const data = await gql('device-management', PROFILE_DECODER, { tokens: [token] });
  return data.deviceProfilesByToken[0] ?? null;
}

const SET_DECODER = graphql(`
  mutation SetDeviceProfileDecoder($profileToken: String!, $document: String!) {
    setDeviceProfileDecoder(profileToken: $profileToken, document: $document) {
      token
      decoder {
        kind
        envVersion
        input
        document
        maxReadings
        estimatedCost
      }
    }
  }
`);

export async function setProfileDecoder(profileToken: string, document: string): Promise<StoredDecoder | null> {
  const data = await gql('device-management', SET_DECODER, { profileToken, document });
  return data.setDeviceProfileDecoder.decoder ?? null;
}

const CLEAR_DECODER = graphql(`
  mutation ClearDeviceProfileDecoder($profileToken: String!) {
    clearDeviceProfileDecoder(profileToken: $profileToken) {
      token
    }
  }
`);

export async function clearProfileDecoder(profileToken: string): Promise<void> {
  await gql('device-management', CLEAR_DECODER, { profileToken });
}

const TEST_DECODER = graphql(`
  query TestPayloadDecoder($input: PayloadDecoderTestInput!) {
    testPayloadDecoder(input: $input) {
      ok
      ignored
      section
      readings {
        key
        value
      }
      skipped {
        key
        reason
      }
      error {
        reason
        path
        message
        offset
      }
      cost
      vectors {
        name
        ok
        error
        cost
      }
      uplinks
    }
  }
`);

export async function testPayloadDecoder(input: PayloadDecoderTestInput): Promise<PayloadDecoderTestResult> {
  const data = await gql('device-management', TEST_DECODER, { input });
  return data.testPayloadDecoder;
}

const DECODER_FUNCTIONS = graphql(`
  query PayloadDecoderFunctions {
    payloadDecoderFunctions {
      name
      signature
      description
    }
  }
`);

export async function listPayloadDecoderFunctions(): Promise<PayloadDecoderFunction[]> {
  const data = await gql('device-management', DECODER_FUNCTIONS);
  return data.payloadDecoderFunctions;
}
