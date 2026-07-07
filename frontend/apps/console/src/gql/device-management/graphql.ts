/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type AlarmDefinitionCreateRequest = {
  alarmKey: string;
  conditionType: string;
  description?: string | null | undefined;
  deviceProfileToken: string;
  durationSeconds?: number | null | undefined;
  enabled: boolean;
  metadata?: string | null | undefined;
  metricKey: string;
  name?: string | null | undefined;
  operator: string;
  repeatCount?: number | null | undefined;
  repeatWindowSeconds?: number | null | undefined;
  severity: string;
  threshold?: number | null | undefined;
  thresholdAttr?: string | null | undefined;
  token: string;
};

export type AlarmDefinitionSearchCriteria = {
  deviceProfile?: string | null | undefined;
  metricKey?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type AlarmSearchCriteria = {
  acknowledged?: boolean | null | undefined;
  alarmKey?: string | null | undefined;
  originator?: string | null | undefined;
  originatorType?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
  severity?: string | null | undefined;
  state?: string | null | undefined;
};

export type AreaCreateRequest = {
  areaTypeToken: string;
  description?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type AreaGroupCreateRequest = {
  backgroundColor?: string | null | undefined;
  borderColor?: string | null | undefined;
  description?: string | null | undefined;
  foregroundColor?: string | null | undefined;
  icon?: string | null | undefined;
  imageUrl?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type AreaGroupSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

export type AreaSearchCriteria = {
  areaTypeToken?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type AreaTypeCreateRequest = {
  backgroundColor?: string | null | undefined;
  borderColor?: string | null | undefined;
  description?: string | null | undefined;
  foregroundColor?: string | null | undefined;
  icon?: string | null | undefined;
  imageUrl?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type AreaTypeSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

export type AssetCreateRequest = {
  assetTypeToken: string;
  description?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type AssetGroupCreateRequest = {
  backgroundColor?: string | null | undefined;
  borderColor?: string | null | undefined;
  description?: string | null | undefined;
  foregroundColor?: string | null | undefined;
  icon?: string | null | undefined;
  imageUrl?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type AssetGroupSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

export type AssetSearchCriteria = {
  assetTypeToken?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type AssetTypeCreateRequest = {
  backgroundColor?: string | null | undefined;
  borderColor?: string | null | undefined;
  description?: string | null | undefined;
  foregroundColor?: string | null | undefined;
  icon?: string | null | undefined;
  imageUrl?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type AssetTypeSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

export type AuditEventSearchCriteria = {
  actor?: string | null | undefined;
  category?: string | null | undefined;
  endTime?: string | null | undefined;
  entityPk?: string | null | undefined;
  operation?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
  startTime?: string | null | undefined;
  tableName?: string | null | undefined;
};

export type CommandDefinitionCreateRequest = {
  commandKey: string;
  description?: string | null | undefined;
  deviceProfileToken: string;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  parameterSchema?: string | null | undefined;
  token: string;
};

export type CommandDefinitionSearchCriteria = {
  commandKey?: string | null | undefined;
  deviceProfile?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type CustomerCreateRequest = {
  customerTypeToken: string;
  description?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type CustomerGroupCreateRequest = {
  backgroundColor?: string | null | undefined;
  borderColor?: string | null | undefined;
  description?: string | null | undefined;
  foregroundColor?: string | null | undefined;
  icon?: string | null | undefined;
  imageUrl?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type CustomerGroupSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

export type CustomerSearchCriteria = {
  customerTypeToken?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type CustomerTypeCreateRequest = {
  backgroundColor?: string | null | undefined;
  borderColor?: string | null | undefined;
  description?: string | null | undefined;
  foregroundColor?: string | null | undefined;
  icon?: string | null | undefined;
  imageUrl?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type CustomerTypeSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

export type DeviceCreateRequest = {
  description?: string | null | undefined;
  deviceTypeToken: string;
  externalId?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type DeviceCredentialCreateRequest = {
  credentialId: string;
  credentialType: string;
  credentialValue?: string | null | undefined;
  deviceToken: string;
  enabled: boolean;
  expiresAt?: string | null | undefined;
  metadata?: string | null | undefined;
  token: string;
};

export type DeviceCredentialSearchCriteria = {
  credentialId?: string | null | undefined;
  credentialType?: string | null | undefined;
  device?: string | null | undefined;
  enabled?: boolean | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type DeviceFacet =
  | 'CATEGORY'
  | 'MANUFACTURER'
  | 'MODEL';

export type DeviceGroupCreateRequest = {
  backgroundColor?: string | null | undefined;
  borderColor?: string | null | undefined;
  description?: string | null | undefined;
  foregroundColor?: string | null | undefined;
  icon?: string | null | undefined;
  imageUrl?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type DeviceGroupSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

export type DeviceProfileCreateRequest = {
  category?: string | null | undefined;
  description?: string | null | undefined;
  metadata?: string | null | undefined;
  name?: string | null | undefined;
  token: string;
};

export type DeviceProfileSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

export type DeviceSearchCriteria = {
  deviceType?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type DeviceTypeCreateRequest = {
  backgroundColor?: string | null | undefined;
  borderColor?: string | null | undefined;
  description?: string | null | undefined;
  foregroundColor?: string | null | undefined;
  icon?: string | null | undefined;
  imageUrl?: string | null | undefined;
  manufacturer?: string | null | undefined;
  metadata?: string | null | undefined;
  model?: string | null | undefined;
  name?: string | null | undefined;
  profileToken?: string | null | undefined;
  token: string;
};

export type DeviceTypeSearchCriteria = {
  pageNumber: number;
  pageSize: number;
};

export type EntityRelationshipCreateRequest = {
  metadata?: string | null | undefined;
  relationshipType: string;
  source: string;
  sourceType: string;
  target: string;
  targetType: string;
  token: string;
};

export type EntityRelationshipSearchCriteria = {
  pageNumber: number;
  pageSize: number;
  relationshipType?: string | null | undefined;
  source?: string | null | undefined;
  sourceType?: string | null | undefined;
  target?: string | null | undefined;
  targetType?: string | null | undefined;
  tracked?: boolean | null | undefined;
};

export type MetricDefinitionCreateRequest = {
  dataType: string;
  description?: string | null | undefined;
  descriptor?: string | null | undefined;
  deviceProfileToken: string;
  enum?: string | null | undefined;
  maxValue?: number | null | undefined;
  metadata?: string | null | undefined;
  metricKey: string;
  minValue?: number | null | undefined;
  name?: string | null | undefined;
  token: string;
  unit?: string | null | undefined;
};

export type MetricDefinitionSearchCriteria = {
  deviceProfile?: string | null | undefined;
  metricKey?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
};

export type AlarmsQueryVariables = Exact<{
  criteria: AlarmSearchCriteria;
}>;


export type AlarmsQuery = { alarms: { results: Array<{ id: string, token: string, originatorType: string, originatorId: string, originatorToken: string | null, alarmKey: string, metricKey: string, state: string, acknowledged: boolean, severity: string, raisedTime: string | null, clearedTime: string | null, acknowledgedTime: string | null, acknowledgedBy: string | null, lastValue: number | null, message: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AcknowledgeAlarmMutationVariables = Exact<{
  token: string;
}>;


export type AcknowledgeAlarmMutation = { acknowledgeAlarm: { id: string } };

export type ClearAlarmMutationVariables = Exact<{
  token: string;
}>;


export type ClearAlarmMutation = { clearAlarm: { id: string } };

export type AlarmStreamSubscriptionVariables = Exact<{
  originatorType?: string | null | undefined;
  originator?: string | null | undefined;
  state?: string | null | undefined;
  severity?: string | null | undefined;
  alarmKey?: string | null | undefined;
}>;


export type AlarmStreamSubscription = { alarmStream: { eventType: string, alarmToken: string, originatorType: string, originatorId: string, originatorToken: string | null, alarmKey: string, metricKey: string, state: string, severity: string, previousSeverity: string | null, acknowledged: boolean, acknowledgedBy: string | null, lastValue: number | null, message: string | null, raisedTime: string | null, occurredTime: string } };

export type AreasQueryVariables = Exact<{
  criteria: AreaSearchCriteria;
}>;


export type AreasQuery = { areas: { results: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, areaType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AreaByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type AreaByTokenQuery = { areasByToken: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, areaType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } }> };

export type CreateAreaMutationVariables = Exact<{
  request?: AreaCreateRequest | null | undefined;
}>;


export type CreateAreaMutation = { createArea: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, areaType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } } };

export type UpdateAreaMutationVariables = Exact<{
  token: string;
  request?: AreaCreateRequest | null | undefined;
}>;


export type UpdateAreaMutation = { updateArea: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, areaType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } } };

export type DeleteAreaMutationVariables = Exact<{
  token: string;
}>;


export type DeleteAreaMutation = { deleteArea: boolean };

export type AreaTypesQueryVariables = Exact<{
  criteria: AreaTypeSearchCriteria;
}>;


export type AreaTypesQuery = { areaTypes: { results: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AreaTypeByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type AreaTypeByTokenQuery = { areaTypesByToken: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }> };

export type CreateAreaTypeMutationVariables = Exact<{
  request?: AreaTypeCreateRequest | null | undefined;
}>;


export type CreateAreaTypeMutation = { createAreaType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type UpdateAreaTypeMutationVariables = Exact<{
  token: string;
  request?: AreaTypeCreateRequest | null | undefined;
}>;


export type UpdateAreaTypeMutation = { updateAreaType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type DeleteAreaTypeMutationVariables = Exact<{
  token: string;
}>;


export type DeleteAreaTypeMutation = { deleteAreaType: boolean };

export type AreaGroupsQueryVariables = Exact<{
  criteria: AreaGroupSearchCriteria;
}>;


export type AreaGroupsQuery = { areaGroups: { results: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AreaGroupByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type AreaGroupByTokenQuery = { areaGroupsByToken: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null }> };

export type CreateAreaGroupMutationVariables = Exact<{
  request?: AreaGroupCreateRequest | null | undefined;
}>;


export type CreateAreaGroupMutation = { createAreaGroup: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null } };

export type UpdateAreaGroupMutationVariables = Exact<{
  token: string;
  request?: AreaGroupCreateRequest | null | undefined;
}>;


export type UpdateAreaGroupMutation = { updateAreaGroup: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null } };

export type DeleteAreaGroupMutationVariables = Exact<{
  token: string;
}>;


export type DeleteAreaGroupMutation = { deleteAreaGroup: boolean };

export type AssetsQueryVariables = Exact<{
  criteria: AssetSearchCriteria;
}>;


export type AssetsQuery = { assets: { results: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, assetType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AssetByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type AssetByTokenQuery = { assetsByToken: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, assetType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } }> };

export type CreateAssetMutationVariables = Exact<{
  request?: AssetCreateRequest | null | undefined;
}>;


export type CreateAssetMutation = { createAsset: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, assetType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } } };

export type UpdateAssetMutationVariables = Exact<{
  token: string;
  request?: AssetCreateRequest | null | undefined;
}>;


export type UpdateAssetMutation = { updateAsset: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, assetType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } } };

export type DeleteAssetMutationVariables = Exact<{
  token: string;
}>;


export type DeleteAssetMutation = { deleteAsset: boolean };

export type AssetTypesQueryVariables = Exact<{
  criteria: AssetTypeSearchCriteria;
}>;


export type AssetTypesQuery = { assetTypes: { results: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AssetTypeByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type AssetTypeByTokenQuery = { assetTypesByToken: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }> };

export type CreateAssetTypeMutationVariables = Exact<{
  request?: AssetTypeCreateRequest | null | undefined;
}>;


export type CreateAssetTypeMutation = { createAssetType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type UpdateAssetTypeMutationVariables = Exact<{
  token: string;
  request?: AssetTypeCreateRequest | null | undefined;
}>;


export type UpdateAssetTypeMutation = { updateAssetType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type DeleteAssetTypeMutationVariables = Exact<{
  token: string;
}>;


export type DeleteAssetTypeMutation = { deleteAssetType: boolean };

export type AssetGroupsQueryVariables = Exact<{
  criteria: AssetGroupSearchCriteria;
}>;


export type AssetGroupsQuery = { assetGroups: { results: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type AssetGroupByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type AssetGroupByTokenQuery = { assetGroupsByToken: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null }> };

export type CreateAssetGroupMutationVariables = Exact<{
  request?: AssetGroupCreateRequest | null | undefined;
}>;


export type CreateAssetGroupMutation = { createAssetGroup: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null } };

export type UpdateAssetGroupMutationVariables = Exact<{
  token: string;
  request?: AssetGroupCreateRequest | null | undefined;
}>;


export type UpdateAssetGroupMutation = { updateAssetGroup: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null } };

export type DeleteAssetGroupMutationVariables = Exact<{
  token: string;
}>;


export type DeleteAssetGroupMutation = { deleteAssetGroup: boolean };

export type AuditEventsQueryVariables = Exact<{
  criteria: AuditEventSearchCriteria;
}>;


export type AuditEventsQuery = { auditEvents: { results: Array<{ id: string, occurredTime: string, category: string, actor: string, operation: string, tableName: string | null, entityPk: string | null, entityLabel: string | null, rowsAffected: number }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type DeviceCredentialsQueryVariables = Exact<{
  criteria: DeviceCredentialSearchCriteria;
}>;


export type DeviceCredentialsQuery = { deviceCredentials: { results: Array<{ id: string, token: string, credentialType: string, credentialId: string, enabled: boolean, expiresAt: string | null, createdAt: string | null }>, pagination: { totalRecords: number | null } } };

export type CreateDeviceCredentialMutationVariables = Exact<{
  request?: DeviceCredentialCreateRequest | null | undefined;
}>;


export type CreateDeviceCredentialMutation = { createDeviceCredential: { id: string, token: string, credentialType: string, credentialId: string, enabled: boolean } };

export type DeleteDeviceCredentialMutationVariables = Exact<{
  token: string;
}>;


export type DeleteDeviceCredentialMutation = { deleteDeviceCredential: boolean };

export type CustomersQueryVariables = Exact<{
  criteria: CustomerSearchCriteria;
}>;


export type CustomersQuery = { customers: { results: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, customerType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CustomerByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type CustomerByTokenQuery = { customersByToken: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, customerType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } }> };

export type CreateCustomerMutationVariables = Exact<{
  request?: CustomerCreateRequest | null | undefined;
}>;


export type CreateCustomerMutation = { createCustomer: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, customerType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } } };

export type UpdateCustomerMutationVariables = Exact<{
  token: string;
  request?: CustomerCreateRequest | null | undefined;
}>;


export type UpdateCustomerMutation = { updateCustomer: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, customerType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } } };

export type DeleteCustomerMutationVariables = Exact<{
  token: string;
}>;


export type DeleteCustomerMutation = { deleteCustomer: boolean };

export type CustomerTypesQueryVariables = Exact<{
  criteria: CustomerTypeSearchCriteria;
}>;


export type CustomerTypesQuery = { customerTypes: { results: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CustomerTypeByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type CustomerTypeByTokenQuery = { customerTypesByToken: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null }> };

export type CreateCustomerTypeMutationVariables = Exact<{
  request?: CustomerTypeCreateRequest | null | undefined;
}>;


export type CreateCustomerTypeMutation = { createCustomerType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type UpdateCustomerTypeMutationVariables = Exact<{
  token: string;
  request?: CustomerTypeCreateRequest | null | undefined;
}>;


export type UpdateCustomerTypeMutation = { updateCustomerType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, createdAt: string | null } };

export type DeleteCustomerTypeMutationVariables = Exact<{
  token: string;
}>;


export type DeleteCustomerTypeMutation = { deleteCustomerType: boolean };

export type CustomerGroupsQueryVariables = Exact<{
  criteria: CustomerGroupSearchCriteria;
}>;


export type CustomerGroupsQuery = { customerGroups: { results: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CustomerGroupByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type CustomerGroupByTokenQuery = { customerGroupsByToken: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null }> };

export type CreateCustomerGroupMutationVariables = Exact<{
  request?: CustomerGroupCreateRequest | null | undefined;
}>;


export type CreateCustomerGroupMutation = { createCustomerGroup: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null } };

export type UpdateCustomerGroupMutationVariables = Exact<{
  token: string;
  request?: CustomerGroupCreateRequest | null | undefined;
}>;


export type UpdateCustomerGroupMutation = { updateCustomerGroup: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null } };

export type DeleteCustomerGroupMutationVariables = Exact<{
  token: string;
}>;


export type DeleteCustomerGroupMutation = { deleteCustomerGroup: boolean };

export type DevicesQueryVariables = Exact<{
  criteria: DeviceSearchCriteria;
}>;


export type DevicesQuery = { devices: { results: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, deviceType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type DeviceByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type DeviceByTokenQuery = { devicesByToken: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null, deviceType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } }> };

export type CreateDeviceMutationVariables = Exact<{
  request?: DeviceCreateRequest | null | undefined;
}>;


export type CreateDeviceMutation = { createDevice: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, deviceType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } } };

export type UpdateDeviceMutationVariables = Exact<{
  token: string;
  request?: DeviceCreateRequest | null | undefined;
}>;


export type UpdateDeviceMutation = { updateDevice: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null, deviceType: { id: string, token: string, name: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null } } };

export type DeleteDeviceMutationVariables = Exact<{
  token: string;
}>;


export type DeleteDeviceMutation = { deleteDevice: boolean };

export type DeviceTypesQueryVariables = Exact<{
  criteria: DeviceTypeSearchCriteria;
}>;


export type DeviceTypesQuery = { deviceTypes: { results: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, imageUrl: string | null, manufacturer: string | null, model: string | null, metadata: string | null, createdAt: string | null, profile: { token: string, name: string | null, category: string | null } | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type DeviceTypeByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type DeviceTypeByTokenQuery = { deviceTypesByToken: Array<{ id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, imageUrl: string | null, manufacturer: string | null, model: string | null, metadata: string | null, createdAt: string | null, profile: { token: string, name: string | null, category: string | null } | null }> };

export type CreateDeviceTypeMutationVariables = Exact<{
  request?: DeviceTypeCreateRequest | null | undefined;
}>;


export type CreateDeviceTypeMutation = { createDeviceType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, imageUrl: string | null, manufacturer: string | null, model: string | null, metadata: string | null, createdAt: string | null, profile: { token: string, name: string | null, category: string | null } | null } };

export type UpdateDeviceTypeMutationVariables = Exact<{
  token: string;
  request?: DeviceTypeCreateRequest | null | undefined;
}>;


export type UpdateDeviceTypeMutation = { updateDeviceType: { id: string, token: string, name: string | null, description: string | null, icon: string | null, backgroundColor: string | null, foregroundColor: string | null, borderColor: string | null, imageUrl: string | null, manufacturer: string | null, model: string | null, metadata: string | null, createdAt: string | null, profile: { token: string, name: string | null, category: string | null } | null } };

export type DeleteDeviceTypeMutationVariables = Exact<{
  token: string;
}>;


export type DeleteDeviceTypeMutation = { deleteDeviceType: boolean };

export type DeviceGroupsQueryVariables = Exact<{
  criteria: DeviceGroupSearchCriteria;
}>;


export type DeviceGroupsQuery = { deviceGroups: { results: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type DeviceGroupByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type DeviceGroupByTokenQuery = { deviceGroupsByToken: Array<{ id: string, token: string, name: string | null, description: string | null, createdAt: string | null }> };

export type CreateDeviceGroupMutationVariables = Exact<{
  request?: DeviceGroupCreateRequest | null | undefined;
}>;


export type CreateDeviceGroupMutation = { createDeviceGroup: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null } };

export type UpdateDeviceGroupMutationVariables = Exact<{
  token: string;
  request?: DeviceGroupCreateRequest | null | undefined;
}>;


export type UpdateDeviceGroupMutation = { updateDeviceGroup: { id: string, token: string, name: string | null, description: string | null, createdAt: string | null } };

export type DeleteDeviceGroupMutationVariables = Exact<{
  token: string;
}>;


export type DeleteDeviceGroupMutation = { deleteDeviceGroup: boolean };

export type DeviceProfilesQueryVariables = Exact<{
  criteria: DeviceProfileSearchCriteria;
}>;


export type DeviceProfilesQuery = { deviceProfiles: { results: Array<{ id: string, token: string, name: string | null, description: string | null, category: string | null, activeVersion: number | null, deviceTypeCount: number, metadata: string | null, createdAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type DeviceProfileByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type DeviceProfileByTokenQuery = { deviceProfilesByToken: Array<{ id: string, token: string, name: string | null, description: string | null, category: string | null, activeVersion: number | null, deviceTypeCount: number, metadata: string | null, createdAt: string | null }> };

export type CreateDeviceProfileMutationVariables = Exact<{
  request?: DeviceProfileCreateRequest | null | undefined;
}>;


export type CreateDeviceProfileMutation = { createDeviceProfile: { id: string, token: string, name: string | null, description: string | null, category: string | null, activeVersion: number | null, deviceTypeCount: number, metadata: string | null, createdAt: string | null } };

export type UpdateDeviceProfileMutationVariables = Exact<{
  token: string;
  request?: DeviceProfileCreateRequest | null | undefined;
}>;


export type UpdateDeviceProfileMutation = { updateDeviceProfile: { id: string, token: string, name: string | null, description: string | null, category: string | null, activeVersion: number | null, deviceTypeCount: number, metadata: string | null, createdAt: string | null } };

export type DeleteDeviceProfileMutationVariables = Exact<{
  token: string;
}>;


export type DeleteDeviceProfileMutation = { deleteDeviceProfile: boolean };

export type DeviceProfileVersionsQueryVariables = Exact<{
  token: string;
}>;


export type DeviceProfileVersionsQuery = { deviceProfileVersions: Array<{ version: number, label: string | null, description: string | null, publishedAt: string, publishedBy: string | null }> };

export type FacetValuesQueryVariables = Exact<{
  facet: DeviceFacet;
}>;


export type FacetValuesQuery = { facetValues: Array<string> };

export type PublishDeviceProfileMutationVariables = Exact<{
  token: string;
  label?: string | null | undefined;
  description?: string | null | undefined;
}>;


export type PublishDeviceProfileMutation = { publishDeviceProfile: { version: number } };

export type RollbackDeviceProfileMutationVariables = Exact<{
  token: string;
  version: number;
}>;


export type RollbackDeviceProfileMutation = { rollbackDeviceProfile: { token: string, activeVersion: number | null } };

export type MetricDefinitionsQueryVariables = Exact<{
  criteria: MetricDefinitionSearchCriteria;
}>;


export type MetricDefinitionsQuery = { metricDefinitions: { results: Array<{ id: string, token: string, name: string | null, description: string | null, metricKey: string, dataType: string, unit: string | null, minValue: number | null, maxValue: number | null, enum: string | null, descriptor: string | null, metadata: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CreateMetricDefinitionMutationVariables = Exact<{
  request?: MetricDefinitionCreateRequest | null | undefined;
}>;


export type CreateMetricDefinitionMutation = { createMetricDefinition: { id: string, token: string } };

export type UpdateMetricDefinitionMutationVariables = Exact<{
  token: string;
  request?: MetricDefinitionCreateRequest | null | undefined;
}>;


export type UpdateMetricDefinitionMutation = { updateMetricDefinition: { id: string, token: string } };

export type DeleteMetricDefinitionMutationVariables = Exact<{
  token: string;
}>;


export type DeleteMetricDefinitionMutation = { deleteMetricDefinition: boolean };

export type CommandDefinitionsQueryVariables = Exact<{
  criteria: CommandDefinitionSearchCriteria;
}>;


export type CommandDefinitionsQuery = { commandDefinitions: { results: Array<{ id: string, token: string, name: string | null, description: string | null, commandKey: string, parameterSchema: string | null, metadata: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CreateCommandDefinitionMutationVariables = Exact<{
  request?: CommandDefinitionCreateRequest | null | undefined;
}>;


export type CreateCommandDefinitionMutation = { createCommandDefinition: { id: string, token: string } };

export type UpdateCommandDefinitionMutationVariables = Exact<{
  token: string;
  request?: CommandDefinitionCreateRequest | null | undefined;
}>;


export type UpdateCommandDefinitionMutation = { updateCommandDefinition: { id: string, token: string } };

export type DeleteCommandDefinitionMutationVariables = Exact<{
  token: string;
}>;


export type DeleteCommandDefinitionMutation = { deleteCommandDefinition: boolean };

export type AlarmDefinitionsQueryVariables = Exact<{
  criteria: AlarmDefinitionSearchCriteria;
}>;


export type AlarmDefinitionsQuery = { alarmDefinitions: { results: Array<{ id: string, token: string, name: string | null, description: string | null, alarmKey: string, metricKey: string, conditionType: string, operator: string, severity: string, threshold: number | null, thresholdAttr: string | null, durationSeconds: number | null, repeatCount: number | null, repeatWindowSeconds: number | null, enabled: boolean, metadata: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CreateAlarmDefinitionMutationVariables = Exact<{
  request: AlarmDefinitionCreateRequest;
}>;


export type CreateAlarmDefinitionMutation = { createAlarmDefinition: { id: string, token: string } };

export type UpdateAlarmDefinitionMutationVariables = Exact<{
  token: string;
  request: AlarmDefinitionCreateRequest;
}>;


export type UpdateAlarmDefinitionMutation = { updateAlarmDefinition: { id: string, token: string } };

export type DeleteAlarmDefinitionMutationVariables = Exact<{
  token: string;
}>;


export type DeleteAlarmDefinitionMutation = { deleteAlarmDefinition: boolean };

export type EntityRelationshipsQueryVariables = Exact<{
  criteria: EntityRelationshipSearchCriteria;
}>;


export type EntityRelationshipsQuery = { entityRelationships: { results: Array<{ id: string, token: string, targetType: string, target:
        | { id: string, token: string }
        | { id: string, token: string }
        | { id: string, token: string }
        | { id: string, token: string }
        | { id: string, token: string }
        | { id: string, token: string }
        | { id: string, token: string }
        | { id: string, token: string }
       }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CreateEntityRelationshipsMutationVariables = Exact<{
  requests: Array<EntityRelationshipCreateRequest> | EntityRelationshipCreateRequest;
}>;


export type CreateEntityRelationshipsMutation = { createEntityRelationships: Array<{ id: string, token: string }> };

export type RemoveEntityRelationshipsMutationVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type RemoveEntityRelationshipsMutation = { removeEntityRelationships: boolean };

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

export const AlarmsDocument = new TypedDocumentString(`
    query Alarms($criteria: AlarmSearchCriteria!) {
  alarms(criteria: $criteria) {
    results {
      id
      token
      originatorType
      originatorId
      originatorToken
      alarmKey
      metricKey
      state
      acknowledged
      severity
      raisedTime
      clearedTime
      acknowledgedTime
      acknowledgedBy
      lastValue
      message
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<AlarmsQuery, AlarmsQueryVariables>;
export const AcknowledgeAlarmDocument = new TypedDocumentString(`
    mutation AcknowledgeAlarm($token: String!) {
  acknowledgeAlarm(token: $token) {
    id
  }
}
    `) as unknown as TypedDocumentString<AcknowledgeAlarmMutation, AcknowledgeAlarmMutationVariables>;
export const ClearAlarmDocument = new TypedDocumentString(`
    mutation ClearAlarm($token: String!) {
  clearAlarm(token: $token) {
    id
  }
}
    `) as unknown as TypedDocumentString<ClearAlarmMutation, ClearAlarmMutationVariables>;
export const AlarmStreamDocument = new TypedDocumentString(`
    subscription AlarmStream($originatorType: String, $originator: String, $state: String, $severity: String, $alarmKey: String) {
  alarmStream(
    originatorType: $originatorType
    originator: $originator
    state: $state
    severity: $severity
    alarmKey: $alarmKey
  ) {
    eventType
    alarmToken
    originatorType
    originatorId
    originatorToken
    alarmKey
    metricKey
    state
    severity
    previousSeverity
    acknowledged
    acknowledgedBy
    lastValue
    message
    raisedTime
    occurredTime
  }
}
    `) as unknown as TypedDocumentString<AlarmStreamSubscription, AlarmStreamSubscriptionVariables>;
export const AreasDocument = new TypedDocumentString(`
    query Areas($criteria: AreaSearchCriteria!) {
  areas(criteria: $criteria) {
    results {
      id
      token
      name
      description
      createdAt
      areaType {
        id
        token
        name
        icon
        backgroundColor
        foregroundColor
        borderColor
      }
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<AreasQuery, AreasQueryVariables>;
export const AreaByTokenDocument = new TypedDocumentString(`
    query AreaByToken($tokens: [String!]!) {
  areasByToken(tokens: $tokens) {
    id
    token
    name
    description
    createdAt
    areaType {
      id
      token
      name
      icon
      backgroundColor
      foregroundColor
      borderColor
    }
  }
}
    `) as unknown as TypedDocumentString<AreaByTokenQuery, AreaByTokenQueryVariables>;
export const CreateAreaDocument = new TypedDocumentString(`
    mutation CreateArea($request: AreaCreateRequest) {
  createArea(request: $request) {
    id
    token
    name
    description
    createdAt
    areaType {
      id
      token
      name
      icon
      backgroundColor
      foregroundColor
      borderColor
    }
  }
}
    `) as unknown as TypedDocumentString<CreateAreaMutation, CreateAreaMutationVariables>;
export const UpdateAreaDocument = new TypedDocumentString(`
    mutation UpdateArea($token: String!, $request: AreaCreateRequest) {
  updateArea(token: $token, request: $request) {
    id
    token
    name
    description
    createdAt
    areaType {
      id
      token
      name
      icon
      backgroundColor
      foregroundColor
      borderColor
    }
  }
}
    `) as unknown as TypedDocumentString<UpdateAreaMutation, UpdateAreaMutationVariables>;
export const DeleteAreaDocument = new TypedDocumentString(`
    mutation DeleteArea($token: String!) {
  deleteArea(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteAreaMutation, DeleteAreaMutationVariables>;
export const AreaTypesDocument = new TypedDocumentString(`
    query AreaTypes($criteria: AreaTypeSearchCriteria!) {
  areaTypes(criteria: $criteria) {
    results {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      createdAt
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<AreaTypesQuery, AreaTypesQueryVariables>;
export const AreaTypeByTokenDocument = new TypedDocumentString(`
    query AreaTypeByToken($tokens: [String!]!) {
  areaTypesByToken(tokens: $tokens) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    createdAt
  }
}
    `) as unknown as TypedDocumentString<AreaTypeByTokenQuery, AreaTypeByTokenQueryVariables>;
export const CreateAreaTypeDocument = new TypedDocumentString(`
    mutation CreateAreaType($request: AreaTypeCreateRequest) {
  createAreaType(request: $request) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    createdAt
  }
}
    `) as unknown as TypedDocumentString<CreateAreaTypeMutation, CreateAreaTypeMutationVariables>;
export const UpdateAreaTypeDocument = new TypedDocumentString(`
    mutation UpdateAreaType($token: String!, $request: AreaTypeCreateRequest) {
  updateAreaType(token: $token, request: $request) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    createdAt
  }
}
    `) as unknown as TypedDocumentString<UpdateAreaTypeMutation, UpdateAreaTypeMutationVariables>;
export const DeleteAreaTypeDocument = new TypedDocumentString(`
    mutation DeleteAreaType($token: String!) {
  deleteAreaType(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteAreaTypeMutation, DeleteAreaTypeMutationVariables>;
export const AreaGroupsDocument = new TypedDocumentString(`
    query AreaGroups($criteria: AreaGroupSearchCriteria!) {
  areaGroups(criteria: $criteria) {
    results {
      id
      token
      name
      description
      createdAt
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<AreaGroupsQuery, AreaGroupsQueryVariables>;
export const AreaGroupByTokenDocument = new TypedDocumentString(`
    query AreaGroupByToken($tokens: [String!]!) {
  areaGroupsByToken(tokens: $tokens) {
    id
    token
    name
    description
    createdAt
  }
}
    `) as unknown as TypedDocumentString<AreaGroupByTokenQuery, AreaGroupByTokenQueryVariables>;
export const CreateAreaGroupDocument = new TypedDocumentString(`
    mutation CreateAreaGroup($request: AreaGroupCreateRequest) {
  createAreaGroup(request: $request) {
    id
    token
    name
    description
    createdAt
  }
}
    `) as unknown as TypedDocumentString<CreateAreaGroupMutation, CreateAreaGroupMutationVariables>;
export const UpdateAreaGroupDocument = new TypedDocumentString(`
    mutation UpdateAreaGroup($token: String!, $request: AreaGroupCreateRequest) {
  updateAreaGroup(token: $token, request: $request) {
    id
    token
    name
    description
    createdAt
  }
}
    `) as unknown as TypedDocumentString<UpdateAreaGroupMutation, UpdateAreaGroupMutationVariables>;
export const DeleteAreaGroupDocument = new TypedDocumentString(`
    mutation DeleteAreaGroup($token: String!) {
  deleteAreaGroup(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteAreaGroupMutation, DeleteAreaGroupMutationVariables>;
export const AssetsDocument = new TypedDocumentString(`
    query Assets($criteria: AssetSearchCriteria!) {
  assets(criteria: $criteria) {
    results {
      id
      token
      name
      description
      createdAt
      assetType {
        id
        token
        name
        icon
        backgroundColor
        foregroundColor
        borderColor
      }
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<AssetsQuery, AssetsQueryVariables>;
export const AssetByTokenDocument = new TypedDocumentString(`
    query AssetByToken($tokens: [String!]!) {
  assetsByToken(tokens: $tokens) {
    id
    token
    name
    description
    createdAt
    assetType {
      id
      token
      name
      icon
      backgroundColor
      foregroundColor
      borderColor
    }
  }
}
    `) as unknown as TypedDocumentString<AssetByTokenQuery, AssetByTokenQueryVariables>;
export const CreateAssetDocument = new TypedDocumentString(`
    mutation CreateAsset($request: AssetCreateRequest) {
  createAsset(request: $request) {
    id
    token
    name
    description
    createdAt
    assetType {
      id
      token
      name
      icon
      backgroundColor
      foregroundColor
      borderColor
    }
  }
}
    `) as unknown as TypedDocumentString<CreateAssetMutation, CreateAssetMutationVariables>;
export const UpdateAssetDocument = new TypedDocumentString(`
    mutation UpdateAsset($token: String!, $request: AssetCreateRequest) {
  updateAsset(token: $token, request: $request) {
    id
    token
    name
    description
    createdAt
    assetType {
      id
      token
      name
      icon
      backgroundColor
      foregroundColor
      borderColor
    }
  }
}
    `) as unknown as TypedDocumentString<UpdateAssetMutation, UpdateAssetMutationVariables>;
export const DeleteAssetDocument = new TypedDocumentString(`
    mutation DeleteAsset($token: String!) {
  deleteAsset(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteAssetMutation, DeleteAssetMutationVariables>;
export const AssetTypesDocument = new TypedDocumentString(`
    query AssetTypes($criteria: AssetTypeSearchCriteria!) {
  assetTypes(criteria: $criteria) {
    results {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      createdAt
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<AssetTypesQuery, AssetTypesQueryVariables>;
export const AssetTypeByTokenDocument = new TypedDocumentString(`
    query AssetTypeByToken($tokens: [String!]!) {
  assetTypesByToken(tokens: $tokens) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    createdAt
  }
}
    `) as unknown as TypedDocumentString<AssetTypeByTokenQuery, AssetTypeByTokenQueryVariables>;
export const CreateAssetTypeDocument = new TypedDocumentString(`
    mutation CreateAssetType($request: AssetTypeCreateRequest) {
  createAssetType(request: $request) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    createdAt
  }
}
    `) as unknown as TypedDocumentString<CreateAssetTypeMutation, CreateAssetTypeMutationVariables>;
export const UpdateAssetTypeDocument = new TypedDocumentString(`
    mutation UpdateAssetType($token: String!, $request: AssetTypeCreateRequest) {
  updateAssetType(token: $token, request: $request) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    createdAt
  }
}
    `) as unknown as TypedDocumentString<UpdateAssetTypeMutation, UpdateAssetTypeMutationVariables>;
export const DeleteAssetTypeDocument = new TypedDocumentString(`
    mutation DeleteAssetType($token: String!) {
  deleteAssetType(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteAssetTypeMutation, DeleteAssetTypeMutationVariables>;
export const AssetGroupsDocument = new TypedDocumentString(`
    query AssetGroups($criteria: AssetGroupSearchCriteria!) {
  assetGroups(criteria: $criteria) {
    results {
      id
      token
      name
      description
      createdAt
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<AssetGroupsQuery, AssetGroupsQueryVariables>;
export const AssetGroupByTokenDocument = new TypedDocumentString(`
    query AssetGroupByToken($tokens: [String!]!) {
  assetGroupsByToken(tokens: $tokens) {
    id
    token
    name
    description
    createdAt
  }
}
    `) as unknown as TypedDocumentString<AssetGroupByTokenQuery, AssetGroupByTokenQueryVariables>;
export const CreateAssetGroupDocument = new TypedDocumentString(`
    mutation CreateAssetGroup($request: AssetGroupCreateRequest) {
  createAssetGroup(request: $request) {
    id
    token
    name
    description
    createdAt
  }
}
    `) as unknown as TypedDocumentString<CreateAssetGroupMutation, CreateAssetGroupMutationVariables>;
export const UpdateAssetGroupDocument = new TypedDocumentString(`
    mutation UpdateAssetGroup($token: String!, $request: AssetGroupCreateRequest) {
  updateAssetGroup(token: $token, request: $request) {
    id
    token
    name
    description
    createdAt
  }
}
    `) as unknown as TypedDocumentString<UpdateAssetGroupMutation, UpdateAssetGroupMutationVariables>;
export const DeleteAssetGroupDocument = new TypedDocumentString(`
    mutation DeleteAssetGroup($token: String!) {
  deleteAssetGroup(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteAssetGroupMutation, DeleteAssetGroupMutationVariables>;
export const AuditEventsDocument = new TypedDocumentString(`
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
      entityLabel
      rowsAffected
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<AuditEventsQuery, AuditEventsQueryVariables>;
export const DeviceCredentialsDocument = new TypedDocumentString(`
    query DeviceCredentials($criteria: DeviceCredentialSearchCriteria!) {
  deviceCredentials(criteria: $criteria) {
    results {
      id
      token
      credentialType
      credentialId
      enabled
      expiresAt
      createdAt
    }
    pagination {
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<DeviceCredentialsQuery, DeviceCredentialsQueryVariables>;
export const CreateDeviceCredentialDocument = new TypedDocumentString(`
    mutation CreateDeviceCredential($request: DeviceCredentialCreateRequest) {
  createDeviceCredential(request: $request) {
    id
    token
    credentialType
    credentialId
    enabled
  }
}
    `) as unknown as TypedDocumentString<CreateDeviceCredentialMutation, CreateDeviceCredentialMutationVariables>;
export const DeleteDeviceCredentialDocument = new TypedDocumentString(`
    mutation DeleteDeviceCredential($token: String!) {
  deleteDeviceCredential(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteDeviceCredentialMutation, DeleteDeviceCredentialMutationVariables>;
export const CustomersDocument = new TypedDocumentString(`
    query Customers($criteria: CustomerSearchCriteria!) {
  customers(criteria: $criteria) {
    results {
      id
      token
      name
      description
      createdAt
      customerType {
        id
        token
        name
        icon
        backgroundColor
        foregroundColor
        borderColor
      }
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<CustomersQuery, CustomersQueryVariables>;
export const CustomerByTokenDocument = new TypedDocumentString(`
    query CustomerByToken($tokens: [String!]!) {
  customersByToken(tokens: $tokens) {
    id
    token
    name
    description
    createdAt
    customerType {
      id
      token
      name
      icon
      backgroundColor
      foregroundColor
      borderColor
    }
  }
}
    `) as unknown as TypedDocumentString<CustomerByTokenQuery, CustomerByTokenQueryVariables>;
export const CreateCustomerDocument = new TypedDocumentString(`
    mutation CreateCustomer($request: CustomerCreateRequest) {
  createCustomer(request: $request) {
    id
    token
    name
    description
    createdAt
    customerType {
      id
      token
      name
      icon
      backgroundColor
      foregroundColor
      borderColor
    }
  }
}
    `) as unknown as TypedDocumentString<CreateCustomerMutation, CreateCustomerMutationVariables>;
export const UpdateCustomerDocument = new TypedDocumentString(`
    mutation UpdateCustomer($token: String!, $request: CustomerCreateRequest) {
  updateCustomer(token: $token, request: $request) {
    id
    token
    name
    description
    createdAt
    customerType {
      id
      token
      name
      icon
      backgroundColor
      foregroundColor
      borderColor
    }
  }
}
    `) as unknown as TypedDocumentString<UpdateCustomerMutation, UpdateCustomerMutationVariables>;
export const DeleteCustomerDocument = new TypedDocumentString(`
    mutation DeleteCustomer($token: String!) {
  deleteCustomer(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteCustomerMutation, DeleteCustomerMutationVariables>;
export const CustomerTypesDocument = new TypedDocumentString(`
    query CustomerTypes($criteria: CustomerTypeSearchCriteria!) {
  customerTypes(criteria: $criteria) {
    results {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      createdAt
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<CustomerTypesQuery, CustomerTypesQueryVariables>;
export const CustomerTypeByTokenDocument = new TypedDocumentString(`
    query CustomerTypeByToken($tokens: [String!]!) {
  customerTypesByToken(tokens: $tokens) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    createdAt
  }
}
    `) as unknown as TypedDocumentString<CustomerTypeByTokenQuery, CustomerTypeByTokenQueryVariables>;
export const CreateCustomerTypeDocument = new TypedDocumentString(`
    mutation CreateCustomerType($request: CustomerTypeCreateRequest) {
  createCustomerType(request: $request) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    createdAt
  }
}
    `) as unknown as TypedDocumentString<CreateCustomerTypeMutation, CreateCustomerTypeMutationVariables>;
export const UpdateCustomerTypeDocument = new TypedDocumentString(`
    mutation UpdateCustomerType($token: String!, $request: CustomerTypeCreateRequest) {
  updateCustomerType(token: $token, request: $request) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    createdAt
  }
}
    `) as unknown as TypedDocumentString<UpdateCustomerTypeMutation, UpdateCustomerTypeMutationVariables>;
export const DeleteCustomerTypeDocument = new TypedDocumentString(`
    mutation DeleteCustomerType($token: String!) {
  deleteCustomerType(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteCustomerTypeMutation, DeleteCustomerTypeMutationVariables>;
export const CustomerGroupsDocument = new TypedDocumentString(`
    query CustomerGroups($criteria: CustomerGroupSearchCriteria!) {
  customerGroups(criteria: $criteria) {
    results {
      id
      token
      name
      description
      createdAt
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<CustomerGroupsQuery, CustomerGroupsQueryVariables>;
export const CustomerGroupByTokenDocument = new TypedDocumentString(`
    query CustomerGroupByToken($tokens: [String!]!) {
  customerGroupsByToken(tokens: $tokens) {
    id
    token
    name
    description
    createdAt
  }
}
    `) as unknown as TypedDocumentString<CustomerGroupByTokenQuery, CustomerGroupByTokenQueryVariables>;
export const CreateCustomerGroupDocument = new TypedDocumentString(`
    mutation CreateCustomerGroup($request: CustomerGroupCreateRequest) {
  createCustomerGroup(request: $request) {
    id
    token
    name
    description
    createdAt
  }
}
    `) as unknown as TypedDocumentString<CreateCustomerGroupMutation, CreateCustomerGroupMutationVariables>;
export const UpdateCustomerGroupDocument = new TypedDocumentString(`
    mutation UpdateCustomerGroup($token: String!, $request: CustomerGroupCreateRequest) {
  updateCustomerGroup(token: $token, request: $request) {
    id
    token
    name
    description
    createdAt
  }
}
    `) as unknown as TypedDocumentString<UpdateCustomerGroupMutation, UpdateCustomerGroupMutationVariables>;
export const DeleteCustomerGroupDocument = new TypedDocumentString(`
    mutation DeleteCustomerGroup($token: String!) {
  deleteCustomerGroup(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteCustomerGroupMutation, DeleteCustomerGroupMutationVariables>;
export const DevicesDocument = new TypedDocumentString(`
    query Devices($criteria: DeviceSearchCriteria!) {
  devices(criteria: $criteria) {
    results {
      id
      token
      name
      description
      createdAt
      deviceType {
        id
        token
        name
        icon
        backgroundColor
        foregroundColor
        borderColor
      }
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<DevicesQuery, DevicesQueryVariables>;
export const DeviceByTokenDocument = new TypedDocumentString(`
    query DeviceByToken($tokens: [String!]!) {
  devicesByToken(tokens: $tokens) {
    id
    token
    name
    description
    createdAt
    deviceType {
      id
      token
      name
      icon
      backgroundColor
      foregroundColor
      borderColor
    }
  }
}
    `) as unknown as TypedDocumentString<DeviceByTokenQuery, DeviceByTokenQueryVariables>;
export const CreateDeviceDocument = new TypedDocumentString(`
    mutation CreateDevice($request: DeviceCreateRequest) {
  createDevice(request: $request) {
    id
    token
    name
    description
    createdAt
    deviceType {
      id
      token
      name
      icon
      backgroundColor
      foregroundColor
      borderColor
    }
  }
}
    `) as unknown as TypedDocumentString<CreateDeviceMutation, CreateDeviceMutationVariables>;
export const UpdateDeviceDocument = new TypedDocumentString(`
    mutation UpdateDevice($token: String!, $request: DeviceCreateRequest) {
  updateDevice(token: $token, request: $request) {
    id
    token
    name
    description
    createdAt
    deviceType {
      id
      token
      name
      icon
      backgroundColor
      foregroundColor
      borderColor
    }
  }
}
    `) as unknown as TypedDocumentString<UpdateDeviceMutation, UpdateDeviceMutationVariables>;
export const DeleteDeviceDocument = new TypedDocumentString(`
    mutation DeleteDevice($token: String!) {
  deleteDevice(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteDeviceMutation, DeleteDeviceMutationVariables>;
export const DeviceTypesDocument = new TypedDocumentString(`
    query DeviceTypes($criteria: DeviceTypeSearchCriteria!) {
  deviceTypes(criteria: $criteria) {
    results {
      id
      token
      name
      description
      icon
      backgroundColor
      foregroundColor
      borderColor
      imageUrl
      manufacturer
      model
      metadata
      profile {
        token
        name
        category
      }
      createdAt
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<DeviceTypesQuery, DeviceTypesQueryVariables>;
export const DeviceTypeByTokenDocument = new TypedDocumentString(`
    query DeviceTypeByToken($tokens: [String!]!) {
  deviceTypesByToken(tokens: $tokens) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    imageUrl
    manufacturer
    model
    metadata
    profile {
      token
      name
      category
    }
    createdAt
  }
}
    `) as unknown as TypedDocumentString<DeviceTypeByTokenQuery, DeviceTypeByTokenQueryVariables>;
export const CreateDeviceTypeDocument = new TypedDocumentString(`
    mutation CreateDeviceType($request: DeviceTypeCreateRequest) {
  createDeviceType(request: $request) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    imageUrl
    manufacturer
    model
    metadata
    profile {
      token
      name
      category
    }
    createdAt
  }
}
    `) as unknown as TypedDocumentString<CreateDeviceTypeMutation, CreateDeviceTypeMutationVariables>;
export const UpdateDeviceTypeDocument = new TypedDocumentString(`
    mutation UpdateDeviceType($token: String!, $request: DeviceTypeCreateRequest) {
  updateDeviceType(token: $token, request: $request) {
    id
    token
    name
    description
    icon
    backgroundColor
    foregroundColor
    borderColor
    imageUrl
    manufacturer
    model
    metadata
    profile {
      token
      name
      category
    }
    createdAt
  }
}
    `) as unknown as TypedDocumentString<UpdateDeviceTypeMutation, UpdateDeviceTypeMutationVariables>;
export const DeleteDeviceTypeDocument = new TypedDocumentString(`
    mutation DeleteDeviceType($token: String!) {
  deleteDeviceType(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteDeviceTypeMutation, DeleteDeviceTypeMutationVariables>;
export const DeviceGroupsDocument = new TypedDocumentString(`
    query DeviceGroups($criteria: DeviceGroupSearchCriteria!) {
  deviceGroups(criteria: $criteria) {
    results {
      id
      token
      name
      description
      createdAt
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<DeviceGroupsQuery, DeviceGroupsQueryVariables>;
export const DeviceGroupByTokenDocument = new TypedDocumentString(`
    query DeviceGroupByToken($tokens: [String!]!) {
  deviceGroupsByToken(tokens: $tokens) {
    id
    token
    name
    description
    createdAt
  }
}
    `) as unknown as TypedDocumentString<DeviceGroupByTokenQuery, DeviceGroupByTokenQueryVariables>;
export const CreateDeviceGroupDocument = new TypedDocumentString(`
    mutation CreateDeviceGroup($request: DeviceGroupCreateRequest) {
  createDeviceGroup(request: $request) {
    id
    token
    name
    description
    createdAt
  }
}
    `) as unknown as TypedDocumentString<CreateDeviceGroupMutation, CreateDeviceGroupMutationVariables>;
export const UpdateDeviceGroupDocument = new TypedDocumentString(`
    mutation UpdateDeviceGroup($token: String!, $request: DeviceGroupCreateRequest) {
  updateDeviceGroup(token: $token, request: $request) {
    id
    token
    name
    description
    createdAt
  }
}
    `) as unknown as TypedDocumentString<UpdateDeviceGroupMutation, UpdateDeviceGroupMutationVariables>;
export const DeleteDeviceGroupDocument = new TypedDocumentString(`
    mutation DeleteDeviceGroup($token: String!) {
  deleteDeviceGroup(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteDeviceGroupMutation, DeleteDeviceGroupMutationVariables>;
export const DeviceProfilesDocument = new TypedDocumentString(`
    query DeviceProfiles($criteria: DeviceProfileSearchCriteria!) {
  deviceProfiles(criteria: $criteria) {
    results {
      id
      token
      name
      description
      category
      activeVersion
      deviceTypeCount
      metadata
      createdAt
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<DeviceProfilesQuery, DeviceProfilesQueryVariables>;
export const DeviceProfileByTokenDocument = new TypedDocumentString(`
    query DeviceProfileByToken($tokens: [String!]!) {
  deviceProfilesByToken(tokens: $tokens) {
    id
    token
    name
    description
    category
    activeVersion
    deviceTypeCount
    metadata
    createdAt
  }
}
    `) as unknown as TypedDocumentString<DeviceProfileByTokenQuery, DeviceProfileByTokenQueryVariables>;
export const CreateDeviceProfileDocument = new TypedDocumentString(`
    mutation CreateDeviceProfile($request: DeviceProfileCreateRequest) {
  createDeviceProfile(request: $request) {
    id
    token
    name
    description
    category
    activeVersion
    deviceTypeCount
    metadata
    createdAt
  }
}
    `) as unknown as TypedDocumentString<CreateDeviceProfileMutation, CreateDeviceProfileMutationVariables>;
export const UpdateDeviceProfileDocument = new TypedDocumentString(`
    mutation UpdateDeviceProfile($token: String!, $request: DeviceProfileCreateRequest) {
  updateDeviceProfile(token: $token, request: $request) {
    id
    token
    name
    description
    category
    activeVersion
    deviceTypeCount
    metadata
    createdAt
  }
}
    `) as unknown as TypedDocumentString<UpdateDeviceProfileMutation, UpdateDeviceProfileMutationVariables>;
export const DeleteDeviceProfileDocument = new TypedDocumentString(`
    mutation DeleteDeviceProfile($token: String!) {
  deleteDeviceProfile(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteDeviceProfileMutation, DeleteDeviceProfileMutationVariables>;
export const DeviceProfileVersionsDocument = new TypedDocumentString(`
    query DeviceProfileVersions($token: String!) {
  deviceProfileVersions(token: $token) {
    version
    label
    description
    publishedAt
    publishedBy
  }
}
    `) as unknown as TypedDocumentString<DeviceProfileVersionsQuery, DeviceProfileVersionsQueryVariables>;
export const FacetValuesDocument = new TypedDocumentString(`
    query FacetValues($facet: DeviceFacet!) {
  facetValues(facet: $facet)
}
    `) as unknown as TypedDocumentString<FacetValuesQuery, FacetValuesQueryVariables>;
export const PublishDeviceProfileDocument = new TypedDocumentString(`
    mutation PublishDeviceProfile($token: String!, $label: String, $description: String) {
  publishDeviceProfile(token: $token, label: $label, description: $description) {
    version
  }
}
    `) as unknown as TypedDocumentString<PublishDeviceProfileMutation, PublishDeviceProfileMutationVariables>;
export const RollbackDeviceProfileDocument = new TypedDocumentString(`
    mutation RollbackDeviceProfile($token: String!, $version: Int!) {
  rollbackDeviceProfile(token: $token, version: $version) {
    token
    activeVersion
  }
}
    `) as unknown as TypedDocumentString<RollbackDeviceProfileMutation, RollbackDeviceProfileMutationVariables>;
export const MetricDefinitionsDocument = new TypedDocumentString(`
    query MetricDefinitions($criteria: MetricDefinitionSearchCriteria!) {
  metricDefinitions(criteria: $criteria) {
    results {
      id
      token
      name
      description
      metricKey
      dataType
      unit
      minValue
      maxValue
      enum
      descriptor
      metadata
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<MetricDefinitionsQuery, MetricDefinitionsQueryVariables>;
export const CreateMetricDefinitionDocument = new TypedDocumentString(`
    mutation CreateMetricDefinition($request: MetricDefinitionCreateRequest) {
  createMetricDefinition(request: $request) {
    id
    token
  }
}
    `) as unknown as TypedDocumentString<CreateMetricDefinitionMutation, CreateMetricDefinitionMutationVariables>;
export const UpdateMetricDefinitionDocument = new TypedDocumentString(`
    mutation UpdateMetricDefinition($token: String!, $request: MetricDefinitionCreateRequest) {
  updateMetricDefinition(token: $token, request: $request) {
    id
    token
  }
}
    `) as unknown as TypedDocumentString<UpdateMetricDefinitionMutation, UpdateMetricDefinitionMutationVariables>;
export const DeleteMetricDefinitionDocument = new TypedDocumentString(`
    mutation DeleteMetricDefinition($token: String!) {
  deleteMetricDefinition(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteMetricDefinitionMutation, DeleteMetricDefinitionMutationVariables>;
export const CommandDefinitionsDocument = new TypedDocumentString(`
    query CommandDefinitions($criteria: CommandDefinitionSearchCriteria!) {
  commandDefinitions(criteria: $criteria) {
    results {
      id
      token
      name
      description
      commandKey
      parameterSchema
      metadata
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<CommandDefinitionsQuery, CommandDefinitionsQueryVariables>;
export const CreateCommandDefinitionDocument = new TypedDocumentString(`
    mutation CreateCommandDefinition($request: CommandDefinitionCreateRequest) {
  createCommandDefinition(request: $request) {
    id
    token
  }
}
    `) as unknown as TypedDocumentString<CreateCommandDefinitionMutation, CreateCommandDefinitionMutationVariables>;
export const UpdateCommandDefinitionDocument = new TypedDocumentString(`
    mutation UpdateCommandDefinition($token: String!, $request: CommandDefinitionCreateRequest) {
  updateCommandDefinition(token: $token, request: $request) {
    id
    token
  }
}
    `) as unknown as TypedDocumentString<UpdateCommandDefinitionMutation, UpdateCommandDefinitionMutationVariables>;
export const DeleteCommandDefinitionDocument = new TypedDocumentString(`
    mutation DeleteCommandDefinition($token: String!) {
  deleteCommandDefinition(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteCommandDefinitionMutation, DeleteCommandDefinitionMutationVariables>;
export const AlarmDefinitionsDocument = new TypedDocumentString(`
    query AlarmDefinitions($criteria: AlarmDefinitionSearchCriteria!) {
  alarmDefinitions(criteria: $criteria) {
    results {
      id
      token
      name
      description
      alarmKey
      metricKey
      conditionType
      operator
      severity
      threshold
      thresholdAttr
      durationSeconds
      repeatCount
      repeatWindowSeconds
      enabled
      metadata
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<AlarmDefinitionsQuery, AlarmDefinitionsQueryVariables>;
export const CreateAlarmDefinitionDocument = new TypedDocumentString(`
    mutation CreateAlarmDefinition($request: AlarmDefinitionCreateRequest!) {
  createAlarmDefinition(request: $request) {
    id
    token
  }
}
    `) as unknown as TypedDocumentString<CreateAlarmDefinitionMutation, CreateAlarmDefinitionMutationVariables>;
export const UpdateAlarmDefinitionDocument = new TypedDocumentString(`
    mutation UpdateAlarmDefinition($token: String!, $request: AlarmDefinitionCreateRequest!) {
  updateAlarmDefinition(token: $token, request: $request) {
    id
    token
  }
}
    `) as unknown as TypedDocumentString<UpdateAlarmDefinitionMutation, UpdateAlarmDefinitionMutationVariables>;
export const DeleteAlarmDefinitionDocument = new TypedDocumentString(`
    mutation DeleteAlarmDefinition($token: String!) {
  deleteAlarmDefinition(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteAlarmDefinitionMutation, DeleteAlarmDefinitionMutationVariables>;
export const EntityRelationshipsDocument = new TypedDocumentString(`
    query EntityRelationships($criteria: EntityRelationshipSearchCriteria!) {
  entityRelationships(criteria: $criteria) {
    results {
      id
      token
      targetType
      target {
        id
        token
      }
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<EntityRelationshipsQuery, EntityRelationshipsQueryVariables>;
export const CreateEntityRelationshipsDocument = new TypedDocumentString(`
    mutation CreateEntityRelationships($requests: [EntityRelationshipCreateRequest!]!) {
  createEntityRelationships(requests: $requests) {
    id
    token
  }
}
    `) as unknown as TypedDocumentString<CreateEntityRelationshipsMutation, CreateEntityRelationshipsMutationVariables>;
export const RemoveEntityRelationshipsDocument = new TypedDocumentString(`
    mutation RemoveEntityRelationships($tokens: [String!]!) {
  removeEntityRelationships(tokens: $tokens)
}
    `) as unknown as TypedDocumentString<RemoveEntityRelationshipsMutation, RemoveEntityRelationshipsMutationVariables>;