/* eslint-disable */
import * as types from './graphql';



/**
 * Map of all GraphQL operations in the project.
 *
 * This map has several performance disadvantages:
 * 1. It is not tree-shakeable, so it will include all operations in the project.
 * 2. It is not minifiable, so the string of a GraphQL query will be multiple times inside the bundle.
 * 3. It does not support dead code elimination, so it will add unused operations.
 *
 * Therefore it is highly recommended to use the babel or swc plugin for production.
 * Learn more about it here: https://the-guild.dev/graphql/codegen/plugins/presets/preset-client#reducing-bundle-size
 */
type Documents = {
    "\n  query Alarms($criteria: AlarmSearchCriteria!) {\n    alarms(criteria: $criteria) {\n      results {\n        id\n        token\n        originatorType\n        originatorId\n        originatorToken\n        alarmKey\n        metricKey\n        state\n        acknowledged\n        severity\n        raisedTime\n        clearedTime\n        acknowledgedTime\n        acknowledgedBy\n        lastValue\n        message\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.AlarmsDocument,
    "\n  mutation AcknowledgeAlarm($token: String!) {\n    acknowledgeAlarm(token: $token) {\n      id\n    }\n  }\n": typeof types.AcknowledgeAlarmDocument,
    "\n  mutation ClearAlarm($token: String!) {\n    clearAlarm(token: $token) {\n      id\n    }\n  }\n": typeof types.ClearAlarmDocument,
    "\n  subscription AlarmStream(\n    $originatorType: String\n    $originator: String\n    $state: String\n    $severity: String\n    $alarmKey: String\n  ) {\n    alarmStream(\n      originatorType: $originatorType\n      originator: $originator\n      state: $state\n      severity: $severity\n      alarmKey: $alarmKey\n    ) {\n      eventType\n      alarmToken\n      originatorType\n      originatorId\n      originatorToken\n      alarmKey\n      metricKey\n      state\n      severity\n      previousSeverity\n      acknowledged\n      acknowledgedBy\n      lastValue\n      message\n      raisedTime\n      occurredTime\n    }\n  }\n": typeof types.AlarmStreamDocument,
    "\n  query Areas($criteria: AreaSearchCriteria!) {\n    areas(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        areaType {\n          id\n          token\n          name\n          icon\n          backgroundColor\n          foregroundColor\n          borderColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.AreasDocument,
    "\n  query AreaByToken($tokens: [String!]!) {\n    areasByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      areaType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": typeof types.AreaByTokenDocument,
    "\n  mutation CreateArea($request: AreaCreateRequest) {\n    createArea(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      areaType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": typeof types.CreateAreaDocument,
    "\n  mutation UpdateArea($token: String!, $request: AreaCreateRequest) {\n    updateArea(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      areaType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": typeof types.UpdateAreaDocument,
    "\n  mutation DeleteArea($token: String!) {\n    deleteArea(token: $token)\n  }\n": typeof types.DeleteAreaDocument,
    "\n  query AreaTypes($criteria: AreaTypeSearchCriteria!) {\n    areaTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.AreaTypesDocument,
    "\n  query AreaTypeByToken($tokens: [String!]!) {\n    areaTypesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": typeof types.AreaTypeByTokenDocument,
    "\n  mutation CreateAreaType($request: AreaTypeCreateRequest) {\n    createAreaType(request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": typeof types.CreateAreaTypeDocument,
    "\n  mutation UpdateAreaType($token: String!, $request: AreaTypeCreateRequest) {\n    updateAreaType(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": typeof types.UpdateAreaTypeDocument,
    "\n  mutation DeleteAreaType($token: String!) {\n    deleteAreaType(token: $token)\n  }\n": typeof types.DeleteAreaTypeDocument,
    "\n  query AreaGroups($criteria: AreaGroupSearchCriteria!) {\n    areaGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.AreaGroupsDocument,
    "\n  query AreaGroupByToken($tokens: [String!]!) {\n    areaGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": typeof types.AreaGroupByTokenDocument,
    "\n  mutation CreateAreaGroup($request: AreaGroupCreateRequest) {\n    createAreaGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": typeof types.CreateAreaGroupDocument,
    "\n  mutation UpdateAreaGroup($token: String!, $request: AreaGroupCreateRequest) {\n    updateAreaGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": typeof types.UpdateAreaGroupDocument,
    "\n  mutation DeleteAreaGroup($token: String!) {\n    deleteAreaGroup(token: $token)\n  }\n": typeof types.DeleteAreaGroupDocument,
    "\n  query Assets($criteria: AssetSearchCriteria!) {\n    assets(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        assetType {\n          id\n          token\n          name\n          icon\n          backgroundColor\n          foregroundColor\n          borderColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.AssetsDocument,
    "\n  query AssetByToken($tokens: [String!]!) {\n    assetsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      assetType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": typeof types.AssetByTokenDocument,
    "\n  mutation CreateAsset($request: AssetCreateRequest) {\n    createAsset(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      assetType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": typeof types.CreateAssetDocument,
    "\n  mutation UpdateAsset($token: String!, $request: AssetCreateRequest) {\n    updateAsset(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      assetType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": typeof types.UpdateAssetDocument,
    "\n  mutation DeleteAsset($token: String!) {\n    deleteAsset(token: $token)\n  }\n": typeof types.DeleteAssetDocument,
    "\n  query AssetTypes($criteria: AssetTypeSearchCriteria!) {\n    assetTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.AssetTypesDocument,
    "\n  query AssetTypeByToken($tokens: [String!]!) {\n    assetTypesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": typeof types.AssetTypeByTokenDocument,
    "\n  mutation CreateAssetType($request: AssetTypeCreateRequest) {\n    createAssetType(request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": typeof types.CreateAssetTypeDocument,
    "\n  mutation UpdateAssetType($token: String!, $request: AssetTypeCreateRequest) {\n    updateAssetType(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": typeof types.UpdateAssetTypeDocument,
    "\n  mutation DeleteAssetType($token: String!) {\n    deleteAssetType(token: $token)\n  }\n": typeof types.DeleteAssetTypeDocument,
    "\n  query AssetGroups($criteria: AssetGroupSearchCriteria!) {\n    assetGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.AssetGroupsDocument,
    "\n  query AssetGroupByToken($tokens: [String!]!) {\n    assetGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": typeof types.AssetGroupByTokenDocument,
    "\n  mutation CreateAssetGroup($request: AssetGroupCreateRequest) {\n    createAssetGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": typeof types.CreateAssetGroupDocument,
    "\n  mutation UpdateAssetGroup($token: String!, $request: AssetGroupCreateRequest) {\n    updateAssetGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": typeof types.UpdateAssetGroupDocument,
    "\n  mutation DeleteAssetGroup($token: String!) {\n    deleteAssetGroup(token: $token)\n  }\n": typeof types.DeleteAssetGroupDocument,
    "\n  query AuditEvents($criteria: AuditEventSearchCriteria!) {\n    auditEvents(criteria: $criteria) {\n      results {\n        id\n        occurredTime\n        category\n        actor\n        operation\n        tableName\n        entityPk\n        entityLabel\n        rowsAffected\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.AuditEventsDocument,
    "\n  query DeviceCredentials($criteria: DeviceCredentialSearchCriteria!) {\n    deviceCredentials(criteria: $criteria) {\n      results {\n        id\n        token\n        credentialType\n        credentialId\n        enabled\n        expiresAt\n        createdAt\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n": typeof types.DeviceCredentialsDocument,
    "\n  mutation CreateDeviceCredential($request: DeviceCredentialCreateRequest) {\n    createDeviceCredential(request: $request) {\n      id\n      token\n      credentialType\n      credentialId\n      enabled\n    }\n  }\n": typeof types.CreateDeviceCredentialDocument,
    "\n  mutation DeleteDeviceCredential($token: String!) {\n    deleteDeviceCredential(token: $token)\n  }\n": typeof types.DeleteDeviceCredentialDocument,
    "\n  query Customers($criteria: CustomerSearchCriteria!) {\n    customers(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        customerType {\n          id\n          token\n          name\n          icon\n          backgroundColor\n          foregroundColor\n          borderColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.CustomersDocument,
    "\n  query CustomerByToken($tokens: [String!]!) {\n    customersByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      customerType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": typeof types.CustomerByTokenDocument,
    "\n  mutation CreateCustomer($request: CustomerCreateRequest) {\n    createCustomer(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      customerType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": typeof types.CreateCustomerDocument,
    "\n  mutation UpdateCustomer($token: String!, $request: CustomerCreateRequest) {\n    updateCustomer(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      customerType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": typeof types.UpdateCustomerDocument,
    "\n  mutation DeleteCustomer($token: String!) {\n    deleteCustomer(token: $token)\n  }\n": typeof types.DeleteCustomerDocument,
    "\n  query CustomerTypes($criteria: CustomerTypeSearchCriteria!) {\n    customerTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.CustomerTypesDocument,
    "\n  query CustomerTypeByToken($tokens: [String!]!) {\n    customerTypesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": typeof types.CustomerTypeByTokenDocument,
    "\n  mutation CreateCustomerType($request: CustomerTypeCreateRequest) {\n    createCustomerType(request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": typeof types.CreateCustomerTypeDocument,
    "\n  mutation UpdateCustomerType($token: String!, $request: CustomerTypeCreateRequest) {\n    updateCustomerType(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": typeof types.UpdateCustomerTypeDocument,
    "\n  mutation DeleteCustomerType($token: String!) {\n    deleteCustomerType(token: $token)\n  }\n": typeof types.DeleteCustomerTypeDocument,
    "\n  query CustomerGroups($criteria: CustomerGroupSearchCriteria!) {\n    customerGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.CustomerGroupsDocument,
    "\n  query CustomerGroupByToken($tokens: [String!]!) {\n    customerGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": typeof types.CustomerGroupByTokenDocument,
    "\n  mutation CreateCustomerGroup($request: CustomerGroupCreateRequest) {\n    createCustomerGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": typeof types.CreateCustomerGroupDocument,
    "\n  mutation UpdateCustomerGroup($token: String!, $request: CustomerGroupCreateRequest) {\n    updateCustomerGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": typeof types.UpdateCustomerGroupDocument,
    "\n  mutation DeleteCustomerGroup($token: String!) {\n    deleteCustomerGroup(token: $token)\n  }\n": typeof types.DeleteCustomerGroupDocument,
    "\n  query Devices($criteria: DeviceSearchCriteria!) {\n    devices(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        deviceType {\n          id\n          token\n          name\n          icon\n          backgroundColor\n          foregroundColor\n          borderColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.DevicesDocument,
    "\n  query DeviceByToken($tokens: [String!]!) {\n    devicesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      deviceType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": typeof types.DeviceByTokenDocument,
    "\n  mutation CreateDevice($request: DeviceCreateRequest) {\n    createDevice(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      deviceType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": typeof types.CreateDeviceDocument,
    "\n  mutation UpdateDevice($token: String!, $request: DeviceCreateRequest) {\n    updateDevice(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      deviceType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": typeof types.UpdateDeviceDocument,
    "\n  mutation DeleteDevice($token: String!) {\n    deleteDevice(token: $token)\n  }\n": typeof types.DeleteDeviceDocument,
    "\n  query DeviceTypes($criteria: DeviceTypeSearchCriteria!) {\n    deviceTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        imageUrl\n        manufacturer\n        model\n        metadata\n        profile {\n          token\n          name\n          category\n        }\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.DeviceTypesDocument,
    "\n  query DeviceTypeByToken($tokens: [String!]!) {\n    deviceTypesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      imageUrl\n      manufacturer\n      model\n      metadata\n      profile {\n        token\n        name\n        category\n      }\n      createdAt\n    }\n  }\n": typeof types.DeviceTypeByTokenDocument,
    "\n  mutation CreateDeviceType($request: DeviceTypeCreateRequest) {\n    createDeviceType(request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      imageUrl\n      manufacturer\n      model\n      metadata\n      profile {\n        token\n        name\n        category\n      }\n      createdAt\n    }\n  }\n": typeof types.CreateDeviceTypeDocument,
    "\n  mutation UpdateDeviceType($token: String!, $request: DeviceTypeCreateRequest) {\n    updateDeviceType(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      imageUrl\n      manufacturer\n      model\n      metadata\n      profile {\n        token\n        name\n        category\n      }\n      createdAt\n    }\n  }\n": typeof types.UpdateDeviceTypeDocument,
    "\n  mutation DeleteDeviceType($token: String!) {\n    deleteDeviceType(token: $token)\n  }\n": typeof types.DeleteDeviceTypeDocument,
    "\n  query DeviceGroups($criteria: DeviceGroupSearchCriteria!) {\n    deviceGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.DeviceGroupsDocument,
    "\n  query DeviceGroupByToken($tokens: [String!]!) {\n    deviceGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": typeof types.DeviceGroupByTokenDocument,
    "\n  mutation CreateDeviceGroup($request: DeviceGroupCreateRequest) {\n    createDeviceGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": typeof types.CreateDeviceGroupDocument,
    "\n  mutation UpdateDeviceGroup($token: String!, $request: DeviceGroupCreateRequest) {\n    updateDeviceGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": typeof types.UpdateDeviceGroupDocument,
    "\n  mutation DeleteDeviceGroup($token: String!) {\n    deleteDeviceGroup(token: $token)\n  }\n": typeof types.DeleteDeviceGroupDocument,
    "\n  query DeviceProfiles($criteria: DeviceProfileSearchCriteria!) {\n    deviceProfiles(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        category\n        activeVersion\n        deviceTypeCount\n        metadata\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.DeviceProfilesDocument,
    "\n  query DeviceProfileByToken($tokens: [String!]!) {\n    deviceProfilesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      category\n      activeVersion\n      deviceTypeCount\n      metadata\n      createdAt\n    }\n  }\n": typeof types.DeviceProfileByTokenDocument,
    "\n  mutation CreateDeviceProfile($request: DeviceProfileCreateRequest) {\n    createDeviceProfile(request: $request) {\n      id\n      token\n      name\n      description\n      category\n      activeVersion\n      deviceTypeCount\n      metadata\n      createdAt\n    }\n  }\n": typeof types.CreateDeviceProfileDocument,
    "\n  mutation UpdateDeviceProfile($token: String!, $request: DeviceProfileCreateRequest) {\n    updateDeviceProfile(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      category\n      activeVersion\n      deviceTypeCount\n      metadata\n      createdAt\n    }\n  }\n": typeof types.UpdateDeviceProfileDocument,
    "\n  mutation DeleteDeviceProfile($token: String!) {\n    deleteDeviceProfile(token: $token)\n  }\n": typeof types.DeleteDeviceProfileDocument,
    "\n  query DeviceProfileVersions($token: String!) {\n    deviceProfileVersions(token: $token) {\n      version\n      label\n      description\n      publishedAt\n      publishedBy\n    }\n  }\n": typeof types.DeviceProfileVersionsDocument,
    "\n  query FacetValues($facet: DeviceFacet!) {\n    facetValues(facet: $facet)\n  }\n": typeof types.FacetValuesDocument,
    "\n  mutation PublishDeviceProfile($token: String!, $label: String, $description: String) {\n    publishDeviceProfile(token: $token, label: $label, description: $description) {\n      version\n    }\n  }\n": typeof types.PublishDeviceProfileDocument,
    "\n  mutation RollbackDeviceProfile($token: String!, $version: Int!) {\n    rollbackDeviceProfile(token: $token, version: $version) {\n      token\n      activeVersion\n    }\n  }\n": typeof types.RollbackDeviceProfileDocument,
    "\n  query MetricDefinitions($criteria: MetricDefinitionSearchCriteria!) {\n    metricDefinitions(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        metricKey\n        dataType\n        unit\n        minValue\n        maxValue\n        enum\n        descriptor\n        metadata\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.MetricDefinitionsDocument,
    "\n  mutation CreateMetricDefinition($request: MetricDefinitionCreateRequest) {\n    createMetricDefinition(request: $request) {\n      id\n      token\n    }\n  }\n": typeof types.CreateMetricDefinitionDocument,
    "\n  mutation UpdateMetricDefinition($token: String!, $request: MetricDefinitionCreateRequest) {\n    updateMetricDefinition(token: $token, request: $request) {\n      id\n      token\n    }\n  }\n": typeof types.UpdateMetricDefinitionDocument,
    "\n  mutation DeleteMetricDefinition($token: String!) {\n    deleteMetricDefinition(token: $token)\n  }\n": typeof types.DeleteMetricDefinitionDocument,
    "\n  query CommandDefinitions($criteria: CommandDefinitionSearchCriteria!) {\n    commandDefinitions(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        commandKey\n        parameterSchema\n        metadata\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.CommandDefinitionsDocument,
    "\n  mutation CreateCommandDefinition($request: CommandDefinitionCreateRequest) {\n    createCommandDefinition(request: $request) {\n      id\n      token\n    }\n  }\n": typeof types.CreateCommandDefinitionDocument,
    "\n  mutation UpdateCommandDefinition($token: String!, $request: CommandDefinitionCreateRequest) {\n    updateCommandDefinition(token: $token, request: $request) {\n      id\n      token\n    }\n  }\n": typeof types.UpdateCommandDefinitionDocument,
    "\n  mutation DeleteCommandDefinition($token: String!) {\n    deleteCommandDefinition(token: $token)\n  }\n": typeof types.DeleteCommandDefinitionDocument,
    "\n  query DetectionRules($criteria: DetectionRuleSearchCriteria!) {\n    detectionRules(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        definition\n        enabled\n        metadata\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.DetectionRulesDocument,
    "\n  mutation CreateDetectionRule($request: DetectionRuleCreateRequest!) {\n    createDetectionRule(request: $request) {\n      id\n      token\n    }\n  }\n": typeof types.CreateDetectionRuleDocument,
    "\n  mutation UpdateDetectionRule($token: String!, $request: DetectionRuleCreateRequest!) {\n    updateDetectionRule(token: $token, request: $request) {\n      id\n      token\n    }\n  }\n": typeof types.UpdateDetectionRuleDocument,
    "\n  mutation DeleteDetectionRule($token: String!) {\n    deleteDetectionRule(token: $token)\n  }\n": typeof types.DeleteDetectionRuleDocument,
    "\n  query EntityRelationships($criteria: EntityRelationshipSearchCriteria!) {\n    entityRelationships(criteria: $criteria) {\n      results {\n        id\n        token\n        targetType\n        target {\n          id\n          token\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.EntityRelationshipsDocument,
    "\n  mutation CreateEntityRelationships($requests: [EntityRelationshipCreateRequest!]!) {\n    createEntityRelationships(requests: $requests) {\n      id\n      token\n    }\n  }\n": typeof types.CreateEntityRelationshipsDocument,
    "\n  mutation RemoveEntityRelationships($tokens: [String!]!) {\n    removeEntityRelationships(tokens: $tokens)\n  }\n": typeof types.RemoveEntityRelationshipsDocument,
};
const documents: Documents = {
    "\n  query Alarms($criteria: AlarmSearchCriteria!) {\n    alarms(criteria: $criteria) {\n      results {\n        id\n        token\n        originatorType\n        originatorId\n        originatorToken\n        alarmKey\n        metricKey\n        state\n        acknowledged\n        severity\n        raisedTime\n        clearedTime\n        acknowledgedTime\n        acknowledgedBy\n        lastValue\n        message\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.AlarmsDocument,
    "\n  mutation AcknowledgeAlarm($token: String!) {\n    acknowledgeAlarm(token: $token) {\n      id\n    }\n  }\n": types.AcknowledgeAlarmDocument,
    "\n  mutation ClearAlarm($token: String!) {\n    clearAlarm(token: $token) {\n      id\n    }\n  }\n": types.ClearAlarmDocument,
    "\n  subscription AlarmStream(\n    $originatorType: String\n    $originator: String\n    $state: String\n    $severity: String\n    $alarmKey: String\n  ) {\n    alarmStream(\n      originatorType: $originatorType\n      originator: $originator\n      state: $state\n      severity: $severity\n      alarmKey: $alarmKey\n    ) {\n      eventType\n      alarmToken\n      originatorType\n      originatorId\n      originatorToken\n      alarmKey\n      metricKey\n      state\n      severity\n      previousSeverity\n      acknowledged\n      acknowledgedBy\n      lastValue\n      message\n      raisedTime\n      occurredTime\n    }\n  }\n": types.AlarmStreamDocument,
    "\n  query Areas($criteria: AreaSearchCriteria!) {\n    areas(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        areaType {\n          id\n          token\n          name\n          icon\n          backgroundColor\n          foregroundColor\n          borderColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.AreasDocument,
    "\n  query AreaByToken($tokens: [String!]!) {\n    areasByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      areaType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": types.AreaByTokenDocument,
    "\n  mutation CreateArea($request: AreaCreateRequest) {\n    createArea(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      areaType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": types.CreateAreaDocument,
    "\n  mutation UpdateArea($token: String!, $request: AreaCreateRequest) {\n    updateArea(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      areaType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": types.UpdateAreaDocument,
    "\n  mutation DeleteArea($token: String!) {\n    deleteArea(token: $token)\n  }\n": types.DeleteAreaDocument,
    "\n  query AreaTypes($criteria: AreaTypeSearchCriteria!) {\n    areaTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.AreaTypesDocument,
    "\n  query AreaTypeByToken($tokens: [String!]!) {\n    areaTypesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": types.AreaTypeByTokenDocument,
    "\n  mutation CreateAreaType($request: AreaTypeCreateRequest) {\n    createAreaType(request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": types.CreateAreaTypeDocument,
    "\n  mutation UpdateAreaType($token: String!, $request: AreaTypeCreateRequest) {\n    updateAreaType(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": types.UpdateAreaTypeDocument,
    "\n  mutation DeleteAreaType($token: String!) {\n    deleteAreaType(token: $token)\n  }\n": types.DeleteAreaTypeDocument,
    "\n  query AreaGroups($criteria: AreaGroupSearchCriteria!) {\n    areaGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.AreaGroupsDocument,
    "\n  query AreaGroupByToken($tokens: [String!]!) {\n    areaGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": types.AreaGroupByTokenDocument,
    "\n  mutation CreateAreaGroup($request: AreaGroupCreateRequest) {\n    createAreaGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": types.CreateAreaGroupDocument,
    "\n  mutation UpdateAreaGroup($token: String!, $request: AreaGroupCreateRequest) {\n    updateAreaGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": types.UpdateAreaGroupDocument,
    "\n  mutation DeleteAreaGroup($token: String!) {\n    deleteAreaGroup(token: $token)\n  }\n": types.DeleteAreaGroupDocument,
    "\n  query Assets($criteria: AssetSearchCriteria!) {\n    assets(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        assetType {\n          id\n          token\n          name\n          icon\n          backgroundColor\n          foregroundColor\n          borderColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.AssetsDocument,
    "\n  query AssetByToken($tokens: [String!]!) {\n    assetsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      assetType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": types.AssetByTokenDocument,
    "\n  mutation CreateAsset($request: AssetCreateRequest) {\n    createAsset(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      assetType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": types.CreateAssetDocument,
    "\n  mutation UpdateAsset($token: String!, $request: AssetCreateRequest) {\n    updateAsset(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      assetType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": types.UpdateAssetDocument,
    "\n  mutation DeleteAsset($token: String!) {\n    deleteAsset(token: $token)\n  }\n": types.DeleteAssetDocument,
    "\n  query AssetTypes($criteria: AssetTypeSearchCriteria!) {\n    assetTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.AssetTypesDocument,
    "\n  query AssetTypeByToken($tokens: [String!]!) {\n    assetTypesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": types.AssetTypeByTokenDocument,
    "\n  mutation CreateAssetType($request: AssetTypeCreateRequest) {\n    createAssetType(request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": types.CreateAssetTypeDocument,
    "\n  mutation UpdateAssetType($token: String!, $request: AssetTypeCreateRequest) {\n    updateAssetType(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": types.UpdateAssetTypeDocument,
    "\n  mutation DeleteAssetType($token: String!) {\n    deleteAssetType(token: $token)\n  }\n": types.DeleteAssetTypeDocument,
    "\n  query AssetGroups($criteria: AssetGroupSearchCriteria!) {\n    assetGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.AssetGroupsDocument,
    "\n  query AssetGroupByToken($tokens: [String!]!) {\n    assetGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": types.AssetGroupByTokenDocument,
    "\n  mutation CreateAssetGroup($request: AssetGroupCreateRequest) {\n    createAssetGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": types.CreateAssetGroupDocument,
    "\n  mutation UpdateAssetGroup($token: String!, $request: AssetGroupCreateRequest) {\n    updateAssetGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": types.UpdateAssetGroupDocument,
    "\n  mutation DeleteAssetGroup($token: String!) {\n    deleteAssetGroup(token: $token)\n  }\n": types.DeleteAssetGroupDocument,
    "\n  query AuditEvents($criteria: AuditEventSearchCriteria!) {\n    auditEvents(criteria: $criteria) {\n      results {\n        id\n        occurredTime\n        category\n        actor\n        operation\n        tableName\n        entityPk\n        entityLabel\n        rowsAffected\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.AuditEventsDocument,
    "\n  query DeviceCredentials($criteria: DeviceCredentialSearchCriteria!) {\n    deviceCredentials(criteria: $criteria) {\n      results {\n        id\n        token\n        credentialType\n        credentialId\n        enabled\n        expiresAt\n        createdAt\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n": types.DeviceCredentialsDocument,
    "\n  mutation CreateDeviceCredential($request: DeviceCredentialCreateRequest) {\n    createDeviceCredential(request: $request) {\n      id\n      token\n      credentialType\n      credentialId\n      enabled\n    }\n  }\n": types.CreateDeviceCredentialDocument,
    "\n  mutation DeleteDeviceCredential($token: String!) {\n    deleteDeviceCredential(token: $token)\n  }\n": types.DeleteDeviceCredentialDocument,
    "\n  query Customers($criteria: CustomerSearchCriteria!) {\n    customers(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        customerType {\n          id\n          token\n          name\n          icon\n          backgroundColor\n          foregroundColor\n          borderColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.CustomersDocument,
    "\n  query CustomerByToken($tokens: [String!]!) {\n    customersByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      customerType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": types.CustomerByTokenDocument,
    "\n  mutation CreateCustomer($request: CustomerCreateRequest) {\n    createCustomer(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      customerType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": types.CreateCustomerDocument,
    "\n  mutation UpdateCustomer($token: String!, $request: CustomerCreateRequest) {\n    updateCustomer(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      customerType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": types.UpdateCustomerDocument,
    "\n  mutation DeleteCustomer($token: String!) {\n    deleteCustomer(token: $token)\n  }\n": types.DeleteCustomerDocument,
    "\n  query CustomerTypes($criteria: CustomerTypeSearchCriteria!) {\n    customerTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.CustomerTypesDocument,
    "\n  query CustomerTypeByToken($tokens: [String!]!) {\n    customerTypesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": types.CustomerTypeByTokenDocument,
    "\n  mutation CreateCustomerType($request: CustomerTypeCreateRequest) {\n    createCustomerType(request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": types.CreateCustomerTypeDocument,
    "\n  mutation UpdateCustomerType($token: String!, $request: CustomerTypeCreateRequest) {\n    updateCustomerType(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n": types.UpdateCustomerTypeDocument,
    "\n  mutation DeleteCustomerType($token: String!) {\n    deleteCustomerType(token: $token)\n  }\n": types.DeleteCustomerTypeDocument,
    "\n  query CustomerGroups($criteria: CustomerGroupSearchCriteria!) {\n    customerGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.CustomerGroupsDocument,
    "\n  query CustomerGroupByToken($tokens: [String!]!) {\n    customerGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": types.CustomerGroupByTokenDocument,
    "\n  mutation CreateCustomerGroup($request: CustomerGroupCreateRequest) {\n    createCustomerGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": types.CreateCustomerGroupDocument,
    "\n  mutation UpdateCustomerGroup($token: String!, $request: CustomerGroupCreateRequest) {\n    updateCustomerGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": types.UpdateCustomerGroupDocument,
    "\n  mutation DeleteCustomerGroup($token: String!) {\n    deleteCustomerGroup(token: $token)\n  }\n": types.DeleteCustomerGroupDocument,
    "\n  query Devices($criteria: DeviceSearchCriteria!) {\n    devices(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        deviceType {\n          id\n          token\n          name\n          icon\n          backgroundColor\n          foregroundColor\n          borderColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.DevicesDocument,
    "\n  query DeviceByToken($tokens: [String!]!) {\n    devicesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      deviceType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": types.DeviceByTokenDocument,
    "\n  mutation CreateDevice($request: DeviceCreateRequest) {\n    createDevice(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      deviceType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": types.CreateDeviceDocument,
    "\n  mutation UpdateDevice($token: String!, $request: DeviceCreateRequest) {\n    updateDevice(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      deviceType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n": types.UpdateDeviceDocument,
    "\n  mutation DeleteDevice($token: String!) {\n    deleteDevice(token: $token)\n  }\n": types.DeleteDeviceDocument,
    "\n  query DeviceTypes($criteria: DeviceTypeSearchCriteria!) {\n    deviceTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        imageUrl\n        manufacturer\n        model\n        metadata\n        profile {\n          token\n          name\n          category\n        }\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.DeviceTypesDocument,
    "\n  query DeviceTypeByToken($tokens: [String!]!) {\n    deviceTypesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      imageUrl\n      manufacturer\n      model\n      metadata\n      profile {\n        token\n        name\n        category\n      }\n      createdAt\n    }\n  }\n": types.DeviceTypeByTokenDocument,
    "\n  mutation CreateDeviceType($request: DeviceTypeCreateRequest) {\n    createDeviceType(request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      imageUrl\n      manufacturer\n      model\n      metadata\n      profile {\n        token\n        name\n        category\n      }\n      createdAt\n    }\n  }\n": types.CreateDeviceTypeDocument,
    "\n  mutation UpdateDeviceType($token: String!, $request: DeviceTypeCreateRequest) {\n    updateDeviceType(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      imageUrl\n      manufacturer\n      model\n      metadata\n      profile {\n        token\n        name\n        category\n      }\n      createdAt\n    }\n  }\n": types.UpdateDeviceTypeDocument,
    "\n  mutation DeleteDeviceType($token: String!) {\n    deleteDeviceType(token: $token)\n  }\n": types.DeleteDeviceTypeDocument,
    "\n  query DeviceGroups($criteria: DeviceGroupSearchCriteria!) {\n    deviceGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.DeviceGroupsDocument,
    "\n  query DeviceGroupByToken($tokens: [String!]!) {\n    deviceGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": types.DeviceGroupByTokenDocument,
    "\n  mutation CreateDeviceGroup($request: DeviceGroupCreateRequest) {\n    createDeviceGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": types.CreateDeviceGroupDocument,
    "\n  mutation UpdateDeviceGroup($token: String!, $request: DeviceGroupCreateRequest) {\n    updateDeviceGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n": types.UpdateDeviceGroupDocument,
    "\n  mutation DeleteDeviceGroup($token: String!) {\n    deleteDeviceGroup(token: $token)\n  }\n": types.DeleteDeviceGroupDocument,
    "\n  query DeviceProfiles($criteria: DeviceProfileSearchCriteria!) {\n    deviceProfiles(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        category\n        activeVersion\n        deviceTypeCount\n        metadata\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.DeviceProfilesDocument,
    "\n  query DeviceProfileByToken($tokens: [String!]!) {\n    deviceProfilesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      category\n      activeVersion\n      deviceTypeCount\n      metadata\n      createdAt\n    }\n  }\n": types.DeviceProfileByTokenDocument,
    "\n  mutation CreateDeviceProfile($request: DeviceProfileCreateRequest) {\n    createDeviceProfile(request: $request) {\n      id\n      token\n      name\n      description\n      category\n      activeVersion\n      deviceTypeCount\n      metadata\n      createdAt\n    }\n  }\n": types.CreateDeviceProfileDocument,
    "\n  mutation UpdateDeviceProfile($token: String!, $request: DeviceProfileCreateRequest) {\n    updateDeviceProfile(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      category\n      activeVersion\n      deviceTypeCount\n      metadata\n      createdAt\n    }\n  }\n": types.UpdateDeviceProfileDocument,
    "\n  mutation DeleteDeviceProfile($token: String!) {\n    deleteDeviceProfile(token: $token)\n  }\n": types.DeleteDeviceProfileDocument,
    "\n  query DeviceProfileVersions($token: String!) {\n    deviceProfileVersions(token: $token) {\n      version\n      label\n      description\n      publishedAt\n      publishedBy\n    }\n  }\n": types.DeviceProfileVersionsDocument,
    "\n  query FacetValues($facet: DeviceFacet!) {\n    facetValues(facet: $facet)\n  }\n": types.FacetValuesDocument,
    "\n  mutation PublishDeviceProfile($token: String!, $label: String, $description: String) {\n    publishDeviceProfile(token: $token, label: $label, description: $description) {\n      version\n    }\n  }\n": types.PublishDeviceProfileDocument,
    "\n  mutation RollbackDeviceProfile($token: String!, $version: Int!) {\n    rollbackDeviceProfile(token: $token, version: $version) {\n      token\n      activeVersion\n    }\n  }\n": types.RollbackDeviceProfileDocument,
    "\n  query MetricDefinitions($criteria: MetricDefinitionSearchCriteria!) {\n    metricDefinitions(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        metricKey\n        dataType\n        unit\n        minValue\n        maxValue\n        enum\n        descriptor\n        metadata\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.MetricDefinitionsDocument,
    "\n  mutation CreateMetricDefinition($request: MetricDefinitionCreateRequest) {\n    createMetricDefinition(request: $request) {\n      id\n      token\n    }\n  }\n": types.CreateMetricDefinitionDocument,
    "\n  mutation UpdateMetricDefinition($token: String!, $request: MetricDefinitionCreateRequest) {\n    updateMetricDefinition(token: $token, request: $request) {\n      id\n      token\n    }\n  }\n": types.UpdateMetricDefinitionDocument,
    "\n  mutation DeleteMetricDefinition($token: String!) {\n    deleteMetricDefinition(token: $token)\n  }\n": types.DeleteMetricDefinitionDocument,
    "\n  query CommandDefinitions($criteria: CommandDefinitionSearchCriteria!) {\n    commandDefinitions(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        commandKey\n        parameterSchema\n        metadata\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.CommandDefinitionsDocument,
    "\n  mutation CreateCommandDefinition($request: CommandDefinitionCreateRequest) {\n    createCommandDefinition(request: $request) {\n      id\n      token\n    }\n  }\n": types.CreateCommandDefinitionDocument,
    "\n  mutation UpdateCommandDefinition($token: String!, $request: CommandDefinitionCreateRequest) {\n    updateCommandDefinition(token: $token, request: $request) {\n      id\n      token\n    }\n  }\n": types.UpdateCommandDefinitionDocument,
    "\n  mutation DeleteCommandDefinition($token: String!) {\n    deleteCommandDefinition(token: $token)\n  }\n": types.DeleteCommandDefinitionDocument,
    "\n  query DetectionRules($criteria: DetectionRuleSearchCriteria!) {\n    detectionRules(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        definition\n        enabled\n        metadata\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.DetectionRulesDocument,
    "\n  mutation CreateDetectionRule($request: DetectionRuleCreateRequest!) {\n    createDetectionRule(request: $request) {\n      id\n      token\n    }\n  }\n": types.CreateDetectionRuleDocument,
    "\n  mutation UpdateDetectionRule($token: String!, $request: DetectionRuleCreateRequest!) {\n    updateDetectionRule(token: $token, request: $request) {\n      id\n      token\n    }\n  }\n": types.UpdateDetectionRuleDocument,
    "\n  mutation DeleteDetectionRule($token: String!) {\n    deleteDetectionRule(token: $token)\n  }\n": types.DeleteDetectionRuleDocument,
    "\n  query EntityRelationships($criteria: EntityRelationshipSearchCriteria!) {\n    entityRelationships(criteria: $criteria) {\n      results {\n        id\n        token\n        targetType\n        target {\n          id\n          token\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.EntityRelationshipsDocument,
    "\n  mutation CreateEntityRelationships($requests: [EntityRelationshipCreateRequest!]!) {\n    createEntityRelationships(requests: $requests) {\n      id\n      token\n    }\n  }\n": types.CreateEntityRelationshipsDocument,
    "\n  mutation RemoveEntityRelationships($tokens: [String!]!) {\n    removeEntityRelationships(tokens: $tokens)\n  }\n": types.RemoveEntityRelationshipsDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Alarms($criteria: AlarmSearchCriteria!) {\n    alarms(criteria: $criteria) {\n      results {\n        id\n        token\n        originatorType\n        originatorId\n        originatorToken\n        alarmKey\n        metricKey\n        state\n        acknowledged\n        severity\n        raisedTime\n        clearedTime\n        acknowledgedTime\n        acknowledgedBy\n        lastValue\n        message\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').AlarmsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation AcknowledgeAlarm($token: String!) {\n    acknowledgeAlarm(token: $token) {\n      id\n    }\n  }\n"): typeof import('./graphql').AcknowledgeAlarmDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation ClearAlarm($token: String!) {\n    clearAlarm(token: $token) {\n      id\n    }\n  }\n"): typeof import('./graphql').ClearAlarmDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  subscription AlarmStream(\n    $originatorType: String\n    $originator: String\n    $state: String\n    $severity: String\n    $alarmKey: String\n  ) {\n    alarmStream(\n      originatorType: $originatorType\n      originator: $originator\n      state: $state\n      severity: $severity\n      alarmKey: $alarmKey\n    ) {\n      eventType\n      alarmToken\n      originatorType\n      originatorId\n      originatorToken\n      alarmKey\n      metricKey\n      state\n      severity\n      previousSeverity\n      acknowledged\n      acknowledgedBy\n      lastValue\n      message\n      raisedTime\n      occurredTime\n    }\n  }\n"): typeof import('./graphql').AlarmStreamDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Areas($criteria: AreaSearchCriteria!) {\n    areas(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        areaType {\n          id\n          token\n          name\n          icon\n          backgroundColor\n          foregroundColor\n          borderColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').AreasDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AreaByToken($tokens: [String!]!) {\n    areasByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      areaType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n"): typeof import('./graphql').AreaByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateArea($request: AreaCreateRequest) {\n    createArea(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      areaType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n"): typeof import('./graphql').CreateAreaDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateArea($token: String!, $request: AreaCreateRequest) {\n    updateArea(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      areaType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n"): typeof import('./graphql').UpdateAreaDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteArea($token: String!) {\n    deleteArea(token: $token)\n  }\n"): typeof import('./graphql').DeleteAreaDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AreaTypes($criteria: AreaTypeSearchCriteria!) {\n    areaTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').AreaTypesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AreaTypeByToken($tokens: [String!]!) {\n    areaTypesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n"): typeof import('./graphql').AreaTypeByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateAreaType($request: AreaTypeCreateRequest) {\n    createAreaType(request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n"): typeof import('./graphql').CreateAreaTypeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateAreaType($token: String!, $request: AreaTypeCreateRequest) {\n    updateAreaType(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n"): typeof import('./graphql').UpdateAreaTypeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteAreaType($token: String!) {\n    deleteAreaType(token: $token)\n  }\n"): typeof import('./graphql').DeleteAreaTypeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AreaGroups($criteria: AreaGroupSearchCriteria!) {\n    areaGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').AreaGroupsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AreaGroupByToken($tokens: [String!]!) {\n    areaGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n"): typeof import('./graphql').AreaGroupByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateAreaGroup($request: AreaGroupCreateRequest) {\n    createAreaGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n"): typeof import('./graphql').CreateAreaGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateAreaGroup($token: String!, $request: AreaGroupCreateRequest) {\n    updateAreaGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n"): typeof import('./graphql').UpdateAreaGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteAreaGroup($token: String!) {\n    deleteAreaGroup(token: $token)\n  }\n"): typeof import('./graphql').DeleteAreaGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Assets($criteria: AssetSearchCriteria!) {\n    assets(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        assetType {\n          id\n          token\n          name\n          icon\n          backgroundColor\n          foregroundColor\n          borderColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').AssetsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AssetByToken($tokens: [String!]!) {\n    assetsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      assetType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n"): typeof import('./graphql').AssetByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateAsset($request: AssetCreateRequest) {\n    createAsset(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      assetType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n"): typeof import('./graphql').CreateAssetDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateAsset($token: String!, $request: AssetCreateRequest) {\n    updateAsset(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      assetType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n"): typeof import('./graphql').UpdateAssetDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteAsset($token: String!) {\n    deleteAsset(token: $token)\n  }\n"): typeof import('./graphql').DeleteAssetDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AssetTypes($criteria: AssetTypeSearchCriteria!) {\n    assetTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').AssetTypesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AssetTypeByToken($tokens: [String!]!) {\n    assetTypesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n"): typeof import('./graphql').AssetTypeByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateAssetType($request: AssetTypeCreateRequest) {\n    createAssetType(request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n"): typeof import('./graphql').CreateAssetTypeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateAssetType($token: String!, $request: AssetTypeCreateRequest) {\n    updateAssetType(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n"): typeof import('./graphql').UpdateAssetTypeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteAssetType($token: String!) {\n    deleteAssetType(token: $token)\n  }\n"): typeof import('./graphql').DeleteAssetTypeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AssetGroups($criteria: AssetGroupSearchCriteria!) {\n    assetGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').AssetGroupsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AssetGroupByToken($tokens: [String!]!) {\n    assetGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n"): typeof import('./graphql').AssetGroupByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateAssetGroup($request: AssetGroupCreateRequest) {\n    createAssetGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n"): typeof import('./graphql').CreateAssetGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateAssetGroup($token: String!, $request: AssetGroupCreateRequest) {\n    updateAssetGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n"): typeof import('./graphql').UpdateAssetGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteAssetGroup($token: String!) {\n    deleteAssetGroup(token: $token)\n  }\n"): typeof import('./graphql').DeleteAssetGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AuditEvents($criteria: AuditEventSearchCriteria!) {\n    auditEvents(criteria: $criteria) {\n      results {\n        id\n        occurredTime\n        category\n        actor\n        operation\n        tableName\n        entityPk\n        entityLabel\n        rowsAffected\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').AuditEventsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DeviceCredentials($criteria: DeviceCredentialSearchCriteria!) {\n    deviceCredentials(criteria: $criteria) {\n      results {\n        id\n        token\n        credentialType\n        credentialId\n        enabled\n        expiresAt\n        createdAt\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').DeviceCredentialsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateDeviceCredential($request: DeviceCredentialCreateRequest) {\n    createDeviceCredential(request: $request) {\n      id\n      token\n      credentialType\n      credentialId\n      enabled\n    }\n  }\n"): typeof import('./graphql').CreateDeviceCredentialDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteDeviceCredential($token: String!) {\n    deleteDeviceCredential(token: $token)\n  }\n"): typeof import('./graphql').DeleteDeviceCredentialDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Customers($criteria: CustomerSearchCriteria!) {\n    customers(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        customerType {\n          id\n          token\n          name\n          icon\n          backgroundColor\n          foregroundColor\n          borderColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').CustomersDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CustomerByToken($tokens: [String!]!) {\n    customersByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      customerType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n"): typeof import('./graphql').CustomerByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateCustomer($request: CustomerCreateRequest) {\n    createCustomer(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      customerType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n"): typeof import('./graphql').CreateCustomerDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateCustomer($token: String!, $request: CustomerCreateRequest) {\n    updateCustomer(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      customerType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n"): typeof import('./graphql').UpdateCustomerDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteCustomer($token: String!) {\n    deleteCustomer(token: $token)\n  }\n"): typeof import('./graphql').DeleteCustomerDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CustomerTypes($criteria: CustomerTypeSearchCriteria!) {\n    customerTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').CustomerTypesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CustomerTypeByToken($tokens: [String!]!) {\n    customerTypesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n"): typeof import('./graphql').CustomerTypeByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateCustomerType($request: CustomerTypeCreateRequest) {\n    createCustomerType(request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n"): typeof import('./graphql').CreateCustomerTypeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateCustomerType($token: String!, $request: CustomerTypeCreateRequest) {\n    updateCustomerType(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      createdAt\n    }\n  }\n"): typeof import('./graphql').UpdateCustomerTypeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteCustomerType($token: String!) {\n    deleteCustomerType(token: $token)\n  }\n"): typeof import('./graphql').DeleteCustomerTypeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CustomerGroups($criteria: CustomerGroupSearchCriteria!) {\n    customerGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').CustomerGroupsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CustomerGroupByToken($tokens: [String!]!) {\n    customerGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n"): typeof import('./graphql').CustomerGroupByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateCustomerGroup($request: CustomerGroupCreateRequest) {\n    createCustomerGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n"): typeof import('./graphql').CreateCustomerGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateCustomerGroup($token: String!, $request: CustomerGroupCreateRequest) {\n    updateCustomerGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n"): typeof import('./graphql').UpdateCustomerGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteCustomerGroup($token: String!) {\n    deleteCustomerGroup(token: $token)\n  }\n"): typeof import('./graphql').DeleteCustomerGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Devices($criteria: DeviceSearchCriteria!) {\n    devices(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        deviceType {\n          id\n          token\n          name\n          icon\n          backgroundColor\n          foregroundColor\n          borderColor\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').DevicesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DeviceByToken($tokens: [String!]!) {\n    devicesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      deviceType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n"): typeof import('./graphql').DeviceByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateDevice($request: DeviceCreateRequest) {\n    createDevice(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      deviceType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n"): typeof import('./graphql').CreateDeviceDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateDevice($token: String!, $request: DeviceCreateRequest) {\n    updateDevice(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      deviceType {\n        id\n        token\n        name\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n      }\n    }\n  }\n"): typeof import('./graphql').UpdateDeviceDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteDevice($token: String!) {\n    deleteDevice(token: $token)\n  }\n"): typeof import('./graphql').DeleteDeviceDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DeviceTypes($criteria: DeviceTypeSearchCriteria!) {\n    deviceTypes(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        icon\n        backgroundColor\n        foregroundColor\n        borderColor\n        imageUrl\n        manufacturer\n        model\n        metadata\n        profile {\n          token\n          name\n          category\n        }\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').DeviceTypesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DeviceTypeByToken($tokens: [String!]!) {\n    deviceTypesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      imageUrl\n      manufacturer\n      model\n      metadata\n      profile {\n        token\n        name\n        category\n      }\n      createdAt\n    }\n  }\n"): typeof import('./graphql').DeviceTypeByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateDeviceType($request: DeviceTypeCreateRequest) {\n    createDeviceType(request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      imageUrl\n      manufacturer\n      model\n      metadata\n      profile {\n        token\n        name\n        category\n      }\n      createdAt\n    }\n  }\n"): typeof import('./graphql').CreateDeviceTypeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateDeviceType($token: String!, $request: DeviceTypeCreateRequest) {\n    updateDeviceType(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      icon\n      backgroundColor\n      foregroundColor\n      borderColor\n      imageUrl\n      manufacturer\n      model\n      metadata\n      profile {\n        token\n        name\n        category\n      }\n      createdAt\n    }\n  }\n"): typeof import('./graphql').UpdateDeviceTypeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteDeviceType($token: String!) {\n    deleteDeviceType(token: $token)\n  }\n"): typeof import('./graphql').DeleteDeviceTypeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DeviceGroups($criteria: DeviceGroupSearchCriteria!) {\n    deviceGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').DeviceGroupsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DeviceGroupByToken($tokens: [String!]!) {\n    deviceGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n"): typeof import('./graphql').DeviceGroupByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateDeviceGroup($request: DeviceGroupCreateRequest) {\n    createDeviceGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n"): typeof import('./graphql').CreateDeviceGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateDeviceGroup($token: String!, $request: DeviceGroupCreateRequest) {\n    updateDeviceGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n    }\n  }\n"): typeof import('./graphql').UpdateDeviceGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteDeviceGroup($token: String!) {\n    deleteDeviceGroup(token: $token)\n  }\n"): typeof import('./graphql').DeleteDeviceGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DeviceProfiles($criteria: DeviceProfileSearchCriteria!) {\n    deviceProfiles(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        category\n        activeVersion\n        deviceTypeCount\n        metadata\n        createdAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').DeviceProfilesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DeviceProfileByToken($tokens: [String!]!) {\n    deviceProfilesByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      category\n      activeVersion\n      deviceTypeCount\n      metadata\n      createdAt\n    }\n  }\n"): typeof import('./graphql').DeviceProfileByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateDeviceProfile($request: DeviceProfileCreateRequest) {\n    createDeviceProfile(request: $request) {\n      id\n      token\n      name\n      description\n      category\n      activeVersion\n      deviceTypeCount\n      metadata\n      createdAt\n    }\n  }\n"): typeof import('./graphql').CreateDeviceProfileDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateDeviceProfile($token: String!, $request: DeviceProfileCreateRequest) {\n    updateDeviceProfile(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      category\n      activeVersion\n      deviceTypeCount\n      metadata\n      createdAt\n    }\n  }\n"): typeof import('./graphql').UpdateDeviceProfileDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteDeviceProfile($token: String!) {\n    deleteDeviceProfile(token: $token)\n  }\n"): typeof import('./graphql').DeleteDeviceProfileDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DeviceProfileVersions($token: String!) {\n    deviceProfileVersions(token: $token) {\n      version\n      label\n      description\n      publishedAt\n      publishedBy\n    }\n  }\n"): typeof import('./graphql').DeviceProfileVersionsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query FacetValues($facet: DeviceFacet!) {\n    facetValues(facet: $facet)\n  }\n"): typeof import('./graphql').FacetValuesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation PublishDeviceProfile($token: String!, $label: String, $description: String) {\n    publishDeviceProfile(token: $token, label: $label, description: $description) {\n      version\n    }\n  }\n"): typeof import('./graphql').PublishDeviceProfileDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RollbackDeviceProfile($token: String!, $version: Int!) {\n    rollbackDeviceProfile(token: $token, version: $version) {\n      token\n      activeVersion\n    }\n  }\n"): typeof import('./graphql').RollbackDeviceProfileDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query MetricDefinitions($criteria: MetricDefinitionSearchCriteria!) {\n    metricDefinitions(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        metricKey\n        dataType\n        unit\n        minValue\n        maxValue\n        enum\n        descriptor\n        metadata\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').MetricDefinitionsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateMetricDefinition($request: MetricDefinitionCreateRequest) {\n    createMetricDefinition(request: $request) {\n      id\n      token\n    }\n  }\n"): typeof import('./graphql').CreateMetricDefinitionDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateMetricDefinition($token: String!, $request: MetricDefinitionCreateRequest) {\n    updateMetricDefinition(token: $token, request: $request) {\n      id\n      token\n    }\n  }\n"): typeof import('./graphql').UpdateMetricDefinitionDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteMetricDefinition($token: String!) {\n    deleteMetricDefinition(token: $token)\n  }\n"): typeof import('./graphql').DeleteMetricDefinitionDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CommandDefinitions($criteria: CommandDefinitionSearchCriteria!) {\n    commandDefinitions(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        commandKey\n        parameterSchema\n        metadata\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').CommandDefinitionsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateCommandDefinition($request: CommandDefinitionCreateRequest) {\n    createCommandDefinition(request: $request) {\n      id\n      token\n    }\n  }\n"): typeof import('./graphql').CreateCommandDefinitionDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateCommandDefinition($token: String!, $request: CommandDefinitionCreateRequest) {\n    updateCommandDefinition(token: $token, request: $request) {\n      id\n      token\n    }\n  }\n"): typeof import('./graphql').UpdateCommandDefinitionDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteCommandDefinition($token: String!) {\n    deleteCommandDefinition(token: $token)\n  }\n"): typeof import('./graphql').DeleteCommandDefinitionDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DetectionRules($criteria: DetectionRuleSearchCriteria!) {\n    detectionRules(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        definition\n        enabled\n        metadata\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').DetectionRulesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateDetectionRule($request: DetectionRuleCreateRequest!) {\n    createDetectionRule(request: $request) {\n      id\n      token\n    }\n  }\n"): typeof import('./graphql').CreateDetectionRuleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateDetectionRule($token: String!, $request: DetectionRuleCreateRequest!) {\n    updateDetectionRule(token: $token, request: $request) {\n      id\n      token\n    }\n  }\n"): typeof import('./graphql').UpdateDetectionRuleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteDetectionRule($token: String!) {\n    deleteDetectionRule(token: $token)\n  }\n"): typeof import('./graphql').DeleteDetectionRuleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query EntityRelationships($criteria: EntityRelationshipSearchCriteria!) {\n    entityRelationships(criteria: $criteria) {\n      results {\n        id\n        token\n        targetType\n        target {\n          id\n          token\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').EntityRelationshipsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateEntityRelationships($requests: [EntityRelationshipCreateRequest!]!) {\n    createEntityRelationships(requests: $requests) {\n      id\n      token\n    }\n  }\n"): typeof import('./graphql').CreateEntityRelationshipsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RemoveEntityRelationships($tokens: [String!]!) {\n    removeEntityRelationships(tokens: $tokens)\n  }\n"): typeof import('./graphql').RemoveEntityRelationshipsDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
