// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { gql } from '@devicechain/client';
import { graphql } from '@/gql/device-management';
import type { AutomationReviewDetailQuery, AutomationReviewSearchCriteria, RunAutomationReviewInput, AutomationReviewPolicyInput, AutomationReviewPolicyQuery } from '@/gql/device-management/graphql';

export type AutomationReview = NonNullable<AutomationReviewDetailQuery['automationReview']>;
const LIST = graphql(`
  query AutomationReviews($criteria: AutomationReviewSearchCriteria!) {
    automationReviews(criteria: $criteria) {
      results { token profileToken ruleToken kind state outcome createdBy createdAt approvedAt }
      pagination { pageStart pageEnd totalRecords }
    }
  }
`);
const DETAIL = graphql(`
  query AutomationReviewDetail($token: String!) {
    automationReview(token: $token) {
      consumedVersion token profileToken ruleToken kind state outcome reviewDigest draftDigest baselineDigest baselineVersion
      createdBy createdAt requiredApprovals approvedAt expiresAt decidedBy decidedAt note draftAuthors reasons deviceTokens
      profileChanges { area key change automation }
      approvals { principal approvedAt policyRevision }
      windows { role start end }
      shiftWindowSeconds expectationInputs { window name expect }
      fidelity { status reasons label }
      expectations { index name window verdict baseline reason baselineReason evidence { device at value spanSeconds } }
      windowResults {
        role start end leadInStart stream firstSeq lastSeq inputDigest degraded changesTruncated
        counts { unchanged added removed shifted contentChanged }
        devices { device readings { key count } versions { key count } stateBase stateDraft divergence lateBase lateDraft first last }
        changes {
          kind device subject discriminator baseAt draftAt shiftSeconds contentChanged afterDivergence
          baseContent { severity payload relativeExpirySeconds expiryLabel configDigest bodyDigest method }
          draftContent { severity payload relativeExpirySeconds expiryLabel configDigest bodyDigest method }
          reading { device at value spanSeconds }
        }
      }
      inputDigest resultDigest engineBuild result matchesCurrentDraft
    }
  }
`);
const RUN = graphql(`
  mutation RunAutomationReview($input: RunAutomationReviewInput!) {
    runAutomationReview(input: $input) { token }
  }
`);
const APPROVE = graphql(`
  mutation ApproveAutomationReview($token: String!, $digest: String!) {
    approveAutomationReview(token: $token, digest: $digest) { token state }
  }
`);
const REJECT = graphql(`
  mutation RejectAutomationReview($token: String!, $note: String) {
    rejectAutomationReview(token: $token, note: $note) { token state }
  }
`);
export async function listAutomationReviews(criteria: AutomationReviewSearchCriteria) {
  return (await gql('device-management', LIST, { criteria })).automationReviews;
}
// A filtered search computes the draft digest once per page instead of once per row.
export async function matchingDraftReviewTokens(profileToken: string) {
  const tokens = new Set<string>();
  let pageNumber = 1;
  for (;;) {
    const page = await listAutomationReviews({ profileToken, matchingCurrentDraft: true, pageNumber, pageSize: 100 });
    if (!page) throw new Error('Automation review search returned no result');
    page.results.forEach(row => tokens.add(row.token));
    if (!page.results.length || pageNumber * 100 >= (page.pagination.totalRecords ?? 0)) return tokens;
    pageNumber += 1;
  }
}
export async function getAutomationReview(token: string) {
  return (await gql('device-management', DETAIL, { token })).automationReview;
}
export async function runAutomationReview(input: RunAutomationReviewInput) {
  return (await gql('device-management', RUN, { input })).runAutomationReview;
}
export async function approveAutomationReview(token: string, digest: string) {
  return (await gql('device-management', APPROVE, { token, digest })).approveAutomationReview;
}
export async function rejectAutomationReview(token: string, note?: string) {
  return (await gql('device-management', REJECT, { token, note })).rejectAutomationReview;
}

export type AutomationReviewPolicy = NonNullable<AutomationReviewPolicyQuery['automationReviewPolicy']>;
const POLICY = graphql(`
  query AutomationReviewPolicy($profileToken: String!) {
    automationReviewPolicy(profileToken: $profileToken) { profileToken effective { required requiredApprovals allowSelfApproval approvalTtlSeconds stepUpMaxAgeSeconds } revision updatedBy updatedAt pendingChange { token proposed { required requiredApprovals allowSelfApproval approvalTtlSeconds stepUpMaxAgeSeconds } proposedBy proposedAt effectiveAt state decidedBy decidedAt } }
  }
`);
export async function getAutomationReviewPolicy(profileToken: string) {
  return (await gql('device-management', POLICY, { profileToken })).automationReviewPolicy;
}
const SET_POLICY = graphql(`
  mutation SetAutomationReviewPolicy($profileToken: String!, $settings: AutomationReviewPolicyInput!) {
    setAutomationReviewPolicy(profileToken: $profileToken, settings: $settings) { applied policy { profileToken effective { required requiredApprovals allowSelfApproval approvalTtlSeconds stepUpMaxAgeSeconds } revision updatedBy updatedAt pendingChange { token proposed { required requiredApprovals allowSelfApproval approvalTtlSeconds stepUpMaxAgeSeconds } proposedBy proposedAt effectiveAt state decidedBy decidedAt } } }
  }
`);
export async function setAutomationReviewPolicy(profileToken: string, settings: AutomationReviewPolicyInput) {
  return (await gql('device-management', SET_POLICY, { profileToken, settings })).setAutomationReviewPolicy;
}
const APPROVE_POLICY = graphql(`
  mutation ApproveAutomationReviewPolicyChange($token: String!) {
    approveAutomationReviewPolicyChange(token: $token) { profileToken effective { required requiredApprovals allowSelfApproval approvalTtlSeconds stepUpMaxAgeSeconds } revision updatedBy updatedAt pendingChange { token proposed { required requiredApprovals allowSelfApproval approvalTtlSeconds stepUpMaxAgeSeconds } proposedBy proposedAt effectiveAt state decidedBy decidedAt } }
  }
`);
export async function approveAutomationReviewPolicyChange(token: string) {
  return (await gql('device-management', APPROVE_POLICY, { token })).approveAutomationReviewPolicyChange;
}
const CANCEL_POLICY = graphql(`
  mutation CancelAutomationReviewPolicyChange($token: String!) {
    cancelAutomationReviewPolicyChange(token: $token) { profileToken effective { required requiredApprovals allowSelfApproval approvalTtlSeconds stepUpMaxAgeSeconds } revision updatedBy updatedAt pendingChange { token proposed { required requiredApprovals allowSelfApproval approvalTtlSeconds stepUpMaxAgeSeconds } proposedBy proposedAt effectiveAt state decidedBy decidedAt } }
  }
`);
export async function cancelAutomationReviewPolicyChange(token: string) {
  return (await gql('device-management', CANCEL_POLICY, { token })).cancelAutomationReviewPolicyChange;
}
const BREAK_GLASS = graphql(`
  query AutomationReviewBreakGlass($profileToken: String!) {
    automationReviewBreakGlass(profileToken: $profileToken) { action target reason actor at }
  }
`);
export async function listAutomationReviewBreakGlass(profileToken: string) {
  return (await gql('device-management', BREAK_GLASS, { profileToken })).automationReviewBreakGlass;
}

export async function listApprovedReviewsForDraft(profileToken: string) {
  return listAutomationReviews({ profileToken, state: 'APPROVED', matchingCurrentDraft: true, pageNumber: 1, pageSize: 25 });
}
