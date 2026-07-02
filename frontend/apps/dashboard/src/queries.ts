// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Hand-authored typed GraphQL documents for the dashboard app.
//
// Like the SDK packages, this app carries no graphql-codegen — the SDK runs in
// documentMode 'string', so a raw query string cast to TypedDocument<Result, Vars>
// is exactly what a generated document is at runtime. Each doc targets one service
// area (see the `gql(area, ...)` call sites).

import type { TypedDocument } from '@devicechain/client';

// ── user-management: exchange a refresh token for a fresh session ─────────────
// Anonymous (no bearer): used by auth.ts to renew an expired access token from the
// console's stored refresh token so the dashboard doesn't silently go stale.

export interface RefreshResult {
  refresh: { accessToken: string; refreshToken: string };
}
export interface RefreshVariables {
  refreshToken: string;
}

export const REFRESH = `
  mutation Refresh($refreshToken: String!) {
    refresh(refreshToken: $refreshToken) {
      accessToken
      refreshToken
    }
  }
` as unknown as TypedDocument<RefreshResult, RefreshVariables>;

// ── dashboard-management: load a dashboard's definition ──────────────────────

export interface DashboardResult {
  dashboard: { token: string; name: string | null; description: string | null; definition: string } | null;
}
export interface DashboardVariables {
  token: string;
}

export const DASHBOARD_BY_TOKEN = `
  query Dashboard($token: String!) {
    dashboard(token: $token) {
      token
      name
      description
      definition
    }
  }
` as unknown as TypedDocument<DashboardResult, DashboardVariables>;

// ── dashboard-management: save an edited definition ──────────────────────────

export interface UpdateDashboardResult {
  updateDashboard: { token: string };
}
export interface UpdateDashboardVariables {
  token: string;
  request: { token: string; name?: string | null; description?: string | null; definition: string };
}

export const UPDATE_DASHBOARD = `
  mutation UpdateDashboard($token: String!, $request: DashboardCreateRequest!) {
    updateDashboard(token: $token, request: $request) {
      token
    }
  }
` as unknown as TypedDocument<UpdateDashboardResult, UpdateDashboardVariables>;
