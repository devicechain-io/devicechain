// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type { AgentProposalsQuery, AgentProposalSearchCriteria } from '@/gql/device-management/graphql';

import { listCommandProposals, getCommandProposal, approveCommandProposal, rejectCommandProposal, type CommandAgentProposal, type CommandAgentProposalRow } from './command-proposals';
import type { AgentProposalSearchCriteria as CommandProposalSearchCriteria } from '@/gql/command-delivery/graphql';

import type { ProposalArea } from '@/lib/proposal-areas';
export { COMMAND_PROPOSAL_AREA, PROPOSAL_AREAS, type ProposalArea } from '@/lib/proposal-areas';
export type AgentProposal = AgentProposalsQuery['agentProposals']['results'][number] | CommandAgentProposal;
/** An inbox row: an alarm proposal row, or a command proposal summary (no payload, no rationale). */
export type AgentProposalRow = AgentProposalsQuery['agentProposals']['results'][number] | CommandAgentProposalRow;
export type AgentProposalPage = {
  results: AgentProposalRow[];
  pagination: AgentProposalsQuery['agentProposals']['pagination'];
};
export const proposalPath = (area: ProposalArea, token: string) => area === 'command-delivery'
  ? `/approvals/command-delivery/${encodeURIComponent(token)}` : `/approvals/${encodeURIComponent(token)}`;
function checkArea(area: string): asserts area is ProposalArea {
  if (area !== 'device-management' && area !== 'command-delivery') throw new Error('Unknown proposal area');
}

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
    approveAgentProposal(token: $token, digest: $digest) { token state reasons }
  }
`);
const REJECT = graphql(`
  mutation RejectAgentProposal($token: String!, $note: String) {
    rejectAgentProposal(token: $token, note: $note) { token }
  }
`);
export async function listAgentProposals(criteria: AgentProposalSearchCriteria): Promise<AgentProposalsQuery['agentProposals']>;
export async function listAgentProposals(area: 'device-management', criteria: AgentProposalSearchCriteria): Promise<AgentProposalsQuery['agentProposals']>;
export async function listAgentProposals(area: ProposalArea, criteria: Omit<AgentProposalSearchCriteria, 'kind'>): Promise<AgentProposalsQuery['agentProposals'] | Awaited<ReturnType<typeof listCommandProposals>>>;
export async function listAgentProposals(area: 'command-delivery', criteria: CommandProposalSearchCriteria): Promise<Awaited<ReturnType<typeof listCommandProposals>>>;
export async function listAgentProposals(first: AgentProposalSearchCriteria | ProposalArea, second?: AgentProposalSearchCriteria | CommandProposalSearchCriteria) {
  if (typeof first !== 'string') return (await gql('device-management', LIST, { criteria: first })).agentProposals;
  checkArea(first);
  if (!second) throw new Error('Missing proposal criteria');
  const { kind, ...criteria } = second;
  if (first === 'command-delivery') {
    if (kind && kind !== 'COMMAND_SEND') throw new Error('Invalid command proposal kind');
    return listCommandProposals({ ...criteria, kind: kind || undefined });
  }
  if (kind && kind !== 'ALARM_ACK' && kind !== 'ALARM_CLEAR') throw new Error('Invalid alarm proposal kind');
  return (await gql('device-management', LIST, { criteria: { ...criteria, kind: kind || undefined } })).agentProposals;
}
export async function getAgentProposal(token: string): Promise<AgentProposal>;
export async function getAgentProposal(area: ProposalArea, token: string): Promise<AgentProposal>;
export async function getAgentProposal(first: string, second?: string): Promise<AgentProposal> {
  const area = second === undefined ? 'device-management' : first;
  const token = second ?? first;
  checkArea(area);
  return area === 'command-delivery' ? getCommandProposal(token) : (await gql('device-management', DETAIL, { token })).agentProposal;
}
export async function approveAgentProposal(token: string, digest: string): Promise<Awaited<ReturnType<typeof approveCommandProposal>>>;
export async function approveAgentProposal(area: ProposalArea, token: string, digest: string): Promise<Awaited<ReturnType<typeof approveCommandProposal>>>;
export async function approveAgentProposal(first: string, second: string, third?: string) {
  const area = third === undefined ? 'device-management' : first;
  const token = third === undefined ? first : second;
  const digest = third ?? second;
  checkArea(area);
  return area === 'command-delivery' ? approveCommandProposal(token, digest) : (await gql('device-management', APPROVE, { token, digest })).approveAgentProposal;
}
export async function rejectAgentProposal(token: string, note?: string): Promise<void>;
export async function rejectAgentProposal(area: ProposalArea, token: string, note: string | undefined): Promise<void>;
export async function rejectAgentProposal(...args: [string, string?] | [ProposalArea, string, string | undefined]): Promise<void> {
  const area = args.length === 3 ? args[0] : 'device-management';
  const token = args.length === 3 ? args[1] : args[0];
  const note = args.length === 3 ? args[2] : args[1];
  checkArea(area);
  if (area === 'command-delivery') await rejectCommandProposal(token, note);
  else await gql('device-management', REJECT, { token, note });
}
