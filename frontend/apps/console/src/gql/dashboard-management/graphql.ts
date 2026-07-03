/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type DashboardCreateRequest = {
  definition: string;
  description?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type DashboardSearchCriteria = {
  name?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
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
  request: DashboardCreateRequest;
  expectedUpdatedAt?: string | null | undefined;
}>;


export type UpdateDashboardMutation = { updateDashboard: { token: string, updatedAt: string | null } };

export type DashboardVersionsQueryVariables = Exact<{
  token: string;
}>;


export type DashboardVersionsQuery = { dashboardVersions: Array<{ version: number, label: string | null, description: string | null, publishedAt: string, publishedBy: string | null }> };

export type PublishDashboardMutationVariables = Exact<{
  token: string;
  label?: string | null | undefined;
  description?: string | null | undefined;
}>;


export type PublishDashboardMutation = { publishDashboard: { version: number } };

export type RollbackDashboardMutationVariables = Exact<{
  token: string;
  version: number;
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
    mutation UpdateDashboard($token: String!, $request: DashboardCreateRequest!, $expectedUpdatedAt: String) {
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
    query DashboardVersions($token: String!) {
  dashboardVersions(token: $token) {
    version
    label
    description
    publishedAt
    publishedBy
  }
}
    `) as unknown as TypedDocumentString<DashboardVersionsQuery, DashboardVersionsQueryVariables>;
export const PublishDashboardDocument = new TypedDocumentString(`
    mutation PublishDashboard($token: String!, $label: String, $description: String) {
  publishDashboard(token: $token, label: $label, description: $description) {
    version
  }
}
    `) as unknown as TypedDocumentString<PublishDashboardMutation, PublishDashboardMutationVariables>;
export const RollbackDashboardDocument = new TypedDocumentString(`
    mutation RollbackDashboard($token: String!, $version: Int!) {
  rollbackDashboard(token: $token, version: $version) {
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