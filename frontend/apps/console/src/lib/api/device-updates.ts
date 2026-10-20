// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The two reads a device's Updates tab makes that lib/api/update-management.ts does not.
//
// 🔴 WHY THE SIGNER IS A SEPARATE READ. `signedBy` is the one field of an assignment the
// server answers with an ERROR rather than null when the pin cannot be read (null means
// "pins nothing", and saying that for a signed assignment would be a downgrade). GraphQL
// then fails the WHOLE operation as far as this client is concerned (it throws on any
// entry of `errors`), so a list that selected signedBy would lose every row, and the
// operator's view of what the device is running, to one unreadable pin. The list below
// leaves signedBy out; the signer is read on its own, per assignment, so its failure is
// shown in place, beside the assignment it concerns.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/update-management';
import type {
  DeviceUpdateAssignmentsQuery,
  AssignmentSignerQuery,
  UpdateAssignmentSearchCriteria,
} from '@/gql/update-management/graphql';

type FullAssignment = DeviceUpdateAssignmentsQuery['updateAssignments']['results'][number];
/** `actor` is absent for a member limited to part of the tenant: the server refuses that field to them. */
export type DeviceUpdateAssignment = Omit<FullAssignment, 'actor'> & { actor?: string | null };
export type DeviceUpdateAssignmentPage = {
  results: DeviceUpdateAssignment[];
  pagination: DeviceUpdateAssignmentsQuery['updateAssignments']['pagination'];
};
export type AssignmentSigner = NonNullable<
  NonNullable<AssignmentSignerQuery['updateAssignment']>['signedBy']
>;

const DEVICE_UPDATE_ASSIGNMENTS = graphql(`
  query DeviceUpdateAssignments($criteria: UpdateAssignmentSearchCriteria!) {
    updateAssignments(criteria: $criteria) {
      results {
        id
        createdAt
        token
        deviceToken
        component
        artifact {
          id
          token
          kind
          name
          version
          component
          trustStatus
          state
        }
        actor
        supersededAt
        current
        attempt {
          token
          state
          targetVersion
          requiresReboot
          lastReportAt
          progress {
            bytes
            total
          }
          failure {
            stage
            code
            detail
            platform
          }
          cancelRequested
          reconciled
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

// The same read without `actor` (who made the assignment): the server answers that field with an
// error for a member limited to part of the tenant, and GraphQL then fails the whole operation,
// so a customer user would lose the list to a field the panel shows only as a nicety.
const DEVICE_UPDATE_ASSIGNMENTS_SCOPED = graphql(`
  query DeviceUpdateAssignmentsScoped($criteria: UpdateAssignmentSearchCriteria!) {
    updateAssignments(criteria: $criteria) {
      results {
        id
        createdAt
        token
        deviceToken
        component
        artifact {
          id
          token
          kind
          name
          version
          component
          trustStatus
          state
        }
        supersededAt
        current
        attempt {
          token
          state
          targetVersion
          requiresReboot
          lastReportAt
          progress {
            bytes
            total
          }
          failure {
            stage
            code
            detail
            platform
          }
          cancelRequested
          reconciled
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

const ASSIGNMENT_SIGNER = graphql(`
  query AssignmentSigner($token: String!) {
    updateAssignment(token: $token) {
      token
      signedBy {
        token
        name
        keyId
        algorithm
        state
      }
    }
  }
`);

export async function listDeviceUpdateAssignments(
  criteria: UpdateAssignmentSearchCriteria,
  opts: { scoped?: boolean } = {},
): Promise<DeviceUpdateAssignmentPage> {
  if (opts.scoped) {
    const scoped = await gql('update-management', DEVICE_UPDATE_ASSIGNMENTS_SCOPED, { criteria });
    return scoped.updateAssignments;
  }
  const data = await gql('update-management', DEVICE_UPDATE_ASSIGNMENTS, { criteria });
  return data.updateAssignments;
}

/** The signer an assignment is pinned to; null when it pins none. Throws when the pin is unreadable. */
export async function getAssignmentSigner(token: string): Promise<AssignmentSigner | null> {
  const data = await gql('update-management', ASSIGNMENT_SIGNER, { token });
  return data.updateAssignment?.signedBy ?? null;
}
