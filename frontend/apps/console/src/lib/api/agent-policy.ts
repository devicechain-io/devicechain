// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { gql } from '@devicechain/client';
import { graphql } from '@/gql/user-management';
import type { AgentPolicyQuery } from '@/gql/user-management/graphql';

const APPROVAL_ACCESS = graphql(`
  query AgentApprovalAccess {
    agentApprovalAccess { instanceEnabled canApprove }
  }
`);
export async function getAgentApprovalAccess() {
  return (await gql('user-management', APPROVAL_ACCESS)).agentApprovalAccess;
}

export type AgentPolicy = AgentPolicyQuery['agentPolicy'];
const POLICY = graphql(`
  query AgentPolicy {
    agentPolicy {
      published { version document origin publishedBy publishedAt }
      operatorSuspended actuationOff instanceEnabled actuationInEffect
    }
  }
`);
const SET_ACTUATION = graphql(`
  mutation SetAgentActuation($enabled: Boolean!) {
    setAgentActuation(enabled: $enabled) {
      published { version document origin publishedBy publishedAt }
      operatorSuspended actuationOff instanceEnabled actuationInEffect
    }
  }
`);
export async function getAgentPolicy(): Promise<AgentPolicy> {
  return (await gql('user-management', POLICY)).agentPolicy;
}
export async function setAgentActuation(enabled: boolean): Promise<AgentPolicy> {
  return (await gql('user-management', SET_ACTUATION, { enabled })).setAgentActuation;
}
