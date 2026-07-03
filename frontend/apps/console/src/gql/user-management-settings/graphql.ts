/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type SettingsQueryVariables = Exact<{ [key: string]: never; }>;


export type SettingsQuery = { settings: Array<{ key: string, description: string, value: string, overridden: boolean, updatedAt: string | null, updatedBy: string | null }> };

export type SetSettingMutationVariables = Exact<{
  key: string;
  value: string;
}>;


export type SetSettingMutation = { setSetting: { key: string, description: string, value: string, overridden: boolean, updatedAt: string | null, updatedBy: string | null } };

export type ClearSettingMutationVariables = Exact<{
  key: string;
}>;


export type ClearSettingMutation = { clearSetting: { key: string, description: string, value: string, overridden: boolean, updatedAt: string | null, updatedBy: string | null } };

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

export const SettingsDocument = new TypedDocumentString(`
    query Settings {
  settings {
    key
    description
    value
    overridden
    updatedAt
    updatedBy
  }
}
    `) as unknown as TypedDocumentString<SettingsQuery, SettingsQueryVariables>;
export const SetSettingDocument = new TypedDocumentString(`
    mutation SetSetting($key: String!, $value: String!) {
  setSetting(key: $key, value: $value) {
    key
    description
    value
    overridden
    updatedAt
    updatedBy
  }
}
    `) as unknown as TypedDocumentString<SetSettingMutation, SetSettingMutationVariables>;
export const ClearSettingDocument = new TypedDocumentString(`
    mutation ClearSetting($key: String!) {
  clearSetting(key: $key) {
    key
    description
    value
    overridden
    updatedAt
    updatedBy
  }
}
    `) as unknown as TypedDocumentString<ClearSettingMutation, ClearSettingMutationVariables>;