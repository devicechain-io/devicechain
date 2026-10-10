/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
/** Fields for a new dashboard. */
export type DashboardCreateRequest = {
  /** The initial draft definition, as a JSON document of at most 1 MiB. */
  definition: string;
  /** Free-text description of what the dashboard is for. */
  description?: string | null | undefined;
  /** Human-readable name shown in dashboard lists. */
  name?: string | null | undefined;
  /** Unique identifier for the new dashboard within the tenant. */
  token: string;
};

/** Criteria for searching dashboards. */
export type DashboardSearchCriteria = {
  /** Return only dashboards whose name contains this text. The match is case-sensitive. */
  name?: string | null | undefined;
  /** Page to return, starting at 1. */
  pageNumber: number;
  /** Results per page. Below 1 means the default of 100; above 1000 is capped at 1000. */
  pageSize: number;
};

/**
 * A partial update to a dashboard's draft. Omit a field to leave the stored value alone,
 * send a value to set it, or send an explicit null to clear it. The dashboard is named by
 * the mutation's token argument, so there is no token here.
 */
export type DashboardUpdateRequest = {
  /**
   * New draft definition (JSON, at most 1 MiB). Omit it to keep the stored definition;
   * an explicit null is refused, because a dashboard must have a definition.
   */
  definition?: string | null | undefined;
  /** New description, or null to clear it. */
  description?: string | null | undefined;
  /** New name, or null to clear it. */
  name?: string | null | undefined;
};

export type DashboardsQueryVariables = Exact<{
  criteria: DashboardSearchCriteria;
}>;


export type DashboardsQuery = { dashboards: { results: Array<{ token: string, name: string | null, description: string | null, createdAt: string | null, updatedAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type DashboardQueryVariables = Exact<{
  token: string;
}>;


export type DashboardQuery = { dashboard: { token: string, name: string | null, description: string | null, definition: string, updatedAt: string | null } | null };

export type CreateDashboardMutationVariables = Exact<{
  request: DashboardCreateRequest;
}>;


export type CreateDashboardMutation = { createDashboard: { token: string } };

export type UpdateDashboardMutationVariables = Exact<{
  token: string;
  request: DashboardUpdateRequest;
  expectedUpdatedAt?: string | null | undefined;
}>;


export type UpdateDashboardMutation = { updateDashboard: { token: string, updatedAt: string | null } };

export type DashboardVersionsQueryVariables = Exact<{
  token: string;
  limit?: number | null | undefined;
}>;


export type DashboardVersionsQuery = { dashboardVersions: Array<{ version: number, label: string | null, description: string | null, publishedAt: string, publishedBy: string | null }> };

export type PublishDashboardMutationVariables = Exact<{
  token: string;
  label?: string | null | undefined;
  description?: string | null | undefined;
  expectedUpdatedAt?: string | null | undefined;
}>;


export type PublishDashboardMutation = { publishDashboard: { version: number } };

export type RollbackDashboardMutationVariables = Exact<{
  token: string;
  version: number;
  expectedUpdatedAt?: string | null | undefined;
}>;


export type RollbackDashboardMutation = { rollbackDashboard: { definition: string, updatedAt: string | null } };

export type DeleteDashboardMutationVariables = Exact<{
  token: string;
}>;


export type DeleteDashboardMutation = { deleteDashboard: boolean };

export class TypedDocumentString<TResult, TVariables>
  extends String
  implements DocumentTypeDecoration<TResult, TVariables>
{
  __apiType?: NonNullable<DocumentTypeDecoration<TResult, TVariables>['__apiType']>;
  private value: string;
  public __meta__?: Record<string, any> | undefined;

  constructor(value: string, __meta__?: Record<string, any> | undefined) {
    super(value);
    this.value = value;
    this.__meta__ = __meta__;
  }

  override toString(): string & DocumentTypeDecoration<TResult, TVariables> {
    return this.value;
  }
}

export const DashboardsDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<DashboardsQuery, DashboardsQueryVariables>;
export const DashboardDocument = new TypedDocumentString(`
    query Dashboard($token: String!) {
  dashboard(token: $token) {
    token
    name
    description
    definition
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<DashboardQuery, DashboardQueryVariables>;
export const CreateDashboardDocument = new TypedDocumentString(`
    mutation CreateDashboard($request: DashboardCreateRequest!) {
  createDashboard(request: $request) {
    token
  }
}
    `) as unknown as TypedDocumentString<CreateDashboardMutation, CreateDashboardMutationVariables>;
export const UpdateDashboardDocument = new TypedDocumentString(`
    mutation UpdateDashboard($token: String!, $request: DashboardUpdateRequest!, $expectedUpdatedAt: String) {
  updateDashboard(
    token: $token
    request: $request
    expectedUpdatedAt: $expectedUpdatedAt
  ) {
    token
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<UpdateDashboardMutation, UpdateDashboardMutationVariables>;
export const DashboardVersionsDocument = new TypedDocumentString(`
    query DashboardVersions($token: String!, $limit: Int) {
  dashboardVersions(token: $token, limit: $limit) {
    version
    label
    description
    publishedAt
    publishedBy
  }
}
    `) as unknown as TypedDocumentString<DashboardVersionsQuery, DashboardVersionsQueryVariables>;
export const PublishDashboardDocument = new TypedDocumentString(`
    mutation PublishDashboard($token: String!, $label: String, $description: String, $expectedUpdatedAt: String) {
  publishDashboard(
    token: $token
    label: $label
    description: $description
    expectedUpdatedAt: $expectedUpdatedAt
  ) {
    version
  }
}
    `) as unknown as TypedDocumentString<PublishDashboardMutation, PublishDashboardMutationVariables>;
export const RollbackDashboardDocument = new TypedDocumentString(`
    mutation RollbackDashboard($token: String!, $version: Int!, $expectedUpdatedAt: String) {
  rollbackDashboard(
    token: $token
    version: $version
    expectedUpdatedAt: $expectedUpdatedAt
  ) {
    definition
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<RollbackDashboardMutation, RollbackDashboardMutationVariables>;
export const DeleteDashboardDocument = new TypedDocumentString(`
    mutation DeleteDashboard($token: String!) {
  deleteDashboard(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteDashboardMutation, DeleteDashboardMutationVariables>;