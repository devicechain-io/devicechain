// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type { AgentProposalsQuery, AgentProposalSearchCriteria } from '@/gql/device-management/graphql';

export type AgentProposal = AgentProposalsQuery['agentProposals']['results'][number];
const LIST = graphql(`
  query AgentProposals($criteria: AgentProposalSearchCriteria!) {
    agentProposals(criteria: $criteria) {
      results { token kind state payload targets digest impact targetCount proposerPrincipal proposerClient clientAttested rationale policyVersion requiredApprovals approvals { principal approvedAt } reasons createdAt expiresAt decidedAt decidedBy note }
      pagination { pageStart pageEnd totalRecords }
    }
  }
`);
const DETAIL = graphql(`
  query AgentProposalDetail($token: String!) {
    agentProposal(token: $token) { token kind state payload targets digest impact targetCount proposerPrincipal proposerClient clientAttested rationale policyVersion requiredApprovals approvals { principal approvedAt } reasons createdAt expiresAt decidedAt decidedBy note }
  }
`);
const APPROVE = graphql(`
  mutation ApproveAgentProposal($token: String!, $digest: String!) {
    approveAgentProposal(token: $token, digest: $digest) { token }
  }
`);
const REJECT = graphql(`
  mutation RejectAgentProposal($token: String!, $note: String) {
    rejectAgentProposal(token: $token, note: $note) { token }
  }
`);
export async function listAgentProposals(criteria: AgentProposalSearchCriteria) {
  return (await gql('device-management', LIST, { criteria })).agentProposals;
}
export async function getAgentProposal(token: string): Promise<AgentProposal> {
  return (await gql('device-management', DETAIL, { token })).agentProposal;
}
export async function approveAgentProposal(token: string, digest: string): Promise<void> {
  await gql('device-management', APPROVE, { token, digest });
}
export async function rejectAgentProposal(token: string, note?: string): Promise<void> {
  await gql('device-management', REJECT, { token, note });
}
