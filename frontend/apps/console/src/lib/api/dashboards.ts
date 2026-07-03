// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the dashboard-management service (ADR-039).
import { gql } from '@devicechain/client';
import { parseDashboardDefinition, serializeDefinition } from '@devicechain/dashboards';
import { graphql } from '@/gql/dashboard-management';
import type { DashboardsQuery, DashboardQuery } from '@/gql/dashboard-management/graphql';

// Public types are derived from the generated operation results so they always
// reflect the actual selection sets and can never drift from the schema.
export type DashboardListItem = DashboardsQuery['dashboards']['results'][number];
export type Dashboard = NonNullable<DashboardQuery['dashboard']>;
export type Pagination = DashboardsQuery['dashboards']['pagination'];
export type DashboardSearchResults = DashboardsQuery['dashboards'];

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
const UPDATE_DASHBOARD = graphql(`
  mutation UpdateDashboard($token: String!, $request: DashboardCreateRequest!) {
    updateDashboard(token: $token, request: $request) {
      token
    }
  }
`);

export async function updateDashboard(
  token: string,
  input: { name?: string | null; description?: string | null; definition: string },
): Promise<{ token: string }> {
  const data = await gql('dashboard-management', UPDATE_DASHBOARD, {
    request: {
      token,
      name: input.name ?? null,
      description: input.description ?? null,
      definition: input.definition,
    },
    token,
  });
  return data.updateDashboard;
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
