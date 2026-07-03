// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the dashboard-management service (ADR-039).
import { gql } from '@devicechain/client';
import { parseDashboardDefinition, serializeDefinition } from '@devicechain/dashboards';
import { graphql } from '@/gql/dashboard-management';
import type {
  DashboardsQuery,
  DashboardQuery,
  DashboardVersionsQuery,
} from '@/gql/dashboard-management/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type DashboardListItem = DashboardsQuery['dashboards']['results'][number];
export type Dashboard = NonNullable<DashboardQuery['dashboard']>;
export type Pagination = DashboardsQuery['dashboards']['pagination'];
export type DashboardSearchResults = DashboardsQuery['dashboards'];
export type DashboardVersion = DashboardVersionsQuery['dashboardVersions'][number];

// ── Dashboards ──────────────────────────────────────────────────────────

// The list omits the heavy `definition` JSON — the table only needs metadata.
const DASHBOARDS = graphql(`
  query Dashboards($criteria: DashboardSearchCriteria!) {
    dashboards(criteria: $criteria) {
      results {
        token
        name
        description
        createdAt
        updatedAt
      }
      pagination {
        pageStart
        pageEnd
        totalRecords
      }
    }
  }
`);

export async function listDashboards(opts: {
  pageNumber: number;
  pageSize: number;
  name?: string;
}): Promise<DashboardSearchResults> {
  const data = await gql('dashboard-management', DASHBOARDS, {
    criteria: {
      pageNumber: opts.pageNumber,
      pageSize: opts.pageSize,
      name: opts.name ?? null,
    },
  });
  return data.dashboards;
}

const DASHBOARD_BY_TOKEN = graphql(`
  query Dashboard($token: String!) {
    dashboard(token: $token) {
      token
      name
      description
      definition
      updatedAt
    }
  }
`);

export async function getDashboard(token: string): Promise<Dashboard | null> {
  const data = await gql('dashboard-management', DASHBOARD_BY_TOKEN, { token });
  return data.dashboard ?? null;
}

const CREATE_DASHBOARD = graphql(`
  mutation CreateDashboard($request: DashboardCreateRequest!) {
    createDashboard(request: $request) {
      token
    }
  }
`);

export async function createDashboard(opts: {
  token: string;
  name?: string;
  description?: string;
}): Promise<{ token: string }> {
  // Seed the dashboard with a canonical empty definition so the stored JSON is
  // always a valid, round-trippable DashboardDefinition (ADR-039).
  const definition = serializeDefinition(
    parseDashboardDefinition({ title: opts.name ?? '', widgets: [] }),
  );
  const data = await gql('dashboard-management', CREATE_DASHBOARD, {
    request: {
      token: opts.token,
      name: opts.name ?? null,
      description: opts.description ?? null,
      definition,
    },
  });
  return data.createDashboard;
}

// The editor persists a full definition snapshot (ADR-039). updateDashboard is a
// full replacement — name/description are sent alongside so a save never wipes
// either field; the caller passes the values it isn't editing back verbatim.
// expectedUpdatedAt is an optimistic-concurrency precondition (ADR-039 versioning):
// pass the updatedAt the editor loaded so a save fails (CONFLICT) if another writer
// changed the dashboard since. It returns the new updatedAt so the caller can
// advance its baseline for the next save.
const UPDATE_DASHBOARD = graphql(`
  mutation UpdateDashboard(
    $token: String!
    $request: DashboardCreateRequest!
    $expectedUpdatedAt: String
  ) {
    updateDashboard(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {
      token
      updatedAt
    }
  }
`);

export async function updateDashboard(
  token: string,
  input: {
    name?: string | null;
    description?: string | null;
    definition: string;
    expectedUpdatedAt?: string | null;
  },
): Promise<{ token: string; updatedAt: string | null }> {
  const data = await gql('dashboard-management', UPDATE_DASHBOARD, {
    request: {
      token,
      name: input.name ?? null,
      description: input.description ?? null,
      definition: input.definition,
    },
    token,
    expectedUpdatedAt: input.expectedUpdatedAt ?? null,
  });
  return data.updateDashboard;
}

// CONFLICT_MARKER matches the backend's ErrConflict message (dashboard-management
// model/api.go). A save that fails the optimistic precondition surfaces as a
// GraphQL error carrying this text; the editor uses it to show a reload-and-retry
// hint rather than a generic error.
export const CONFLICT_MARKER = 'modified by another writer';

// ── Versioning (ADR-039) ──────────────────────────────────────────────────

const DASHBOARD_VERSIONS = graphql(`
  query DashboardVersions($token: String!) {
    dashboardVersions(token: $token) {
      version
      label
      description
      publishedAt
      publishedBy
    }
  }
`);

export async function listDashboardVersions(token: string): Promise<DashboardVersion[]> {
  const data = await gql('dashboard-management', DASHBOARD_VERSIONS, { token });
  return data.dashboardVersions;
}

const PUBLISH_DASHBOARD = graphql(`
  mutation PublishDashboard(
    $token: String!
    $label: String
    $description: String
    $expectedUpdatedAt: String
  ) {
    publishDashboard(
      token: $token
      label: $label
      description: $description
      expectedUpdatedAt: $expectedUpdatedAt
    ) {
      version
    }
  }
`);

// publishDashboard freezes the current (saved) draft into a new immutable version.
// expectedUpdatedAt is the same precondition as updateDashboard: publish fails with
// CONFLICT if the server draft moved on since — so it can't freeze another writer's
// content while the author believes they published their own view.
export async function publishDashboard(
  token: string,
  input: { label?: string; description?: string; expectedUpdatedAt?: string | null },
): Promise<{ version: number }> {
  const data = await gql('dashboard-management', PUBLISH_DASHBOARD, {
    token,
    label: input.label?.trim() ? input.label.trim() : null,
    description: input.description?.trim() ? input.description.trim() : null,
    expectedUpdatedAt: input.expectedUpdatedAt ?? null,
  });
  return data.publishDashboard;
}

const ROLLBACK_DASHBOARD = graphql(`
  mutation RollbackDashboard($token: String!, $version: Int!) {
    rollbackDashboard(token: $token, version: $version) {
      definition
      updatedAt
    }
  }
`);

// rollbackDashboard re-drafts a published version into the draft, returning the new
// draft definition + updatedAt so the editor can re-baseline without a reload.
export async function rollbackDashboard(
  token: string,
  version: number,
): Promise<{ definition: string; updatedAt: string | null }> {
  const data = await gql('dashboard-management', ROLLBACK_DASHBOARD, { token, version });
  return data.rollbackDashboard;
}

const DELETE_DASHBOARD = graphql(`
  mutation DeleteDashboard($token: String!) {
    deleteDashboard(token: $token)
  }
`);

export async function deleteDashboard(token: string): Promise<boolean> {
  const data = await gql('dashboard-management', DELETE_DASHBOARD, { token });
  return data.deleteDashboard;
}
