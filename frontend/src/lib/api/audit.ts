// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the append-only audit journal (ADR-019),
// served by the device-management schema.
import { gql } from '@/lib/graphql/client';
import { graphql } from '@/gql/device-management';
import type {
  AuditEventsQuery,
  AuditEventSearchCriteria,
} from '@/gql/device-management/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection set and can never drift from the schema.
export type AuditEvent = AuditEventsQuery['auditEvents']['results'][number];
export type AuditEventSearchResults = AuditEventsQuery['auditEvents'];
export type { AuditEventSearchCriteria };

const AUDIT_EVENTS = graphql(`
  query AuditEvents($criteria: AuditEventSearchCriteria!) {
    auditEvents(criteria: $criteria) {
      results {
        id
        occurredTime
        category
        actor
        operation
        tableName
        entityPk
        rowsAffected
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

// List audit-journal rows (newest first). The journal is tenant-scoped
// server-side, so this returns only the current tenant's rows and requires the
// audit:read authority.
export async function listAuditEvents(
  criteria: AuditEventSearchCriteria,
): Promise<AuditEventSearchResults> {
  return (await gql('device-management', AUDIT_EVENTS, { criteria })).auditEvents;
}
