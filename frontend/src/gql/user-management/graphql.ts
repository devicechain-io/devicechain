/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type LoginMutationVariables = Exact<{
  email: string;
  password: string;
}>;


export type LoginMutation = { login: { identityToken: string, expiresAt: string, superuser: boolean, memberships: Array<{ tenant: string, roles: Array<string> }> } };

export type SelectTenantMutationVariables = Exact<{
  identityToken: string;
  tenant: string;
}>;


export type SelectTenantMutation = { selectTenant: { accessToken: string, refreshToken: string, expiresAt: string } };

export type RefreshMutationVariables = Exact<{
  refreshToken: string;
}>;


export type RefreshMutation = { refresh: { accessToken: string, refreshToken: string, expiresAt: string } };

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

export const LoginDocument = new TypedDocumentString(`
    mutation Login($email: String!, $password: String!) {
  login(email: $email, password: $password) {
    identityToken
    expiresAt
    superuser
    memberships {
      tenant
      roles
    }
  }
}
    `) as unknown as TypedDocumentString<LoginMutation, LoginMutationVariables>;
export const SelectTenantDocument = new TypedDocumentString(`
    mutation SelectTenant($identityToken: String!, $tenant: String!) {
  selectTenant(identityToken: $identityToken, tenant: $tenant) {
    accessToken
    refreshToken
    expiresAt
  }
}
    `) as unknown as TypedDocumentString<SelectTenantMutation, SelectTenantMutationVariables>;
export const RefreshDocument = new TypedDocumentString(`
    mutation Refresh($refreshToken: String!) {
  refresh(refreshToken: $refreshToken) {
    accessToken
    refreshToken
    expiresAt
  }
}
    `) as unknown as TypedDocumentString<RefreshMutation, RefreshMutationVariables>;