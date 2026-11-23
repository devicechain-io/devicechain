// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/** The areas that own agent proposals. Kept dependency-free so routes can import it. */
export type ProposalArea = 'device-management' | 'command-delivery';
/** The area that owns agent command proposals. */
export const COMMAND_PROPOSAL_AREA: ProposalArea = 'command-delivery';
/** Every area the Approvals inbox lists, in tab order; omission elsewhere means device-management. */
export const PROPOSAL_AREAS: readonly ProposalArea[] = ['device-management', 'command-delivery'];
