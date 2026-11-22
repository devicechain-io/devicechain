// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { gql } from '@devicechain/client';
import { graphql } from '@/gql/command-delivery';
import type { CommandAgentProposalsQuery, AgentProposalSearchCriteria } from '@/gql/command-delivery/graphql';

export type CommandAgentProposal = CommandAgentProposalsQuery['agentProposals']['results'][number];
const LIST = graphql(`
  query CommandAgentProposals($criteria: AgentProposalSearchCriteria!) {
    agentProposals(criteria: $criteria) {
      results { token kind state payload targets digest impact targetCount proposerPrincipal proposerClient clientAttested rationale policyVersion requiredApprovals approvals { principal approvedAt } reasons createdAt expiresAt decidedAt decidedBy note deviceToken command commandCutoff resultRef }
      pagination { pageStart pageEnd totalRecords }
    }
  }
`);
const DETAIL = graphql(`
  query CommandAgentProposalDetail($token: String!) {
    agentProposal(token: $token) { token kind state payload targets digest impact targetCount proposerPrincipal proposerClient clientAttested rationale policyVersion requiredApprovals approvals { principal approvedAt } reasons createdAt expiresAt decidedAt decidedBy note deviceToken command commandCutoff resultRef }
  }
`);
const APPROVE = graphql(`
  mutation ApproveCommandAgentProposal($token: String!, $digest: String!) {
    approveAgentProposal(token: $token, digest: $digest) { token state reasons }
  }
`);
const REJECT = graphql(`
  mutation RejectCommandAgentProposal($token: String!, $note: String) {
    rejectAgentProposal(token: $token, note: $note) { token }
  }
`);
export async function listCommandProposals(criteria: AgentProposalSearchCriteria) {
  return (await gql('command-delivery', LIST, { criteria })).agentProposals;
}
export async function getCommandProposal(token: string): Promise<CommandAgentProposal> {
  return (await gql('command-delivery', DETAIL, { token })).agentProposal;
}
export async function approveCommandProposal(token: string, digest: string) {
  return (await gql('command-delivery', APPROVE, { token, digest })).approveAgentProposal;
}
export async function rejectCommandProposal(token: string, note?: string): Promise<void> {
  await gql('command-delivery', REJECT, { token, note });
}
