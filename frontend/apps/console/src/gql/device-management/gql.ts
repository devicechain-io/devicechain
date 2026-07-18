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
    "\n  query AuditEvents($criteria: AuditEventSearchCriteria!) {\n    auditEvents(criteria: $criteria) {\n      results {\n        id\n        occurredTime\n        category\n        actor\n        operation\n        tableName\n        entityPk\n        entityLabel\n        rowsAffected\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.AuditEventsDocument,
    "\n  query PreviewSelector($memberType: String!, $selector: String!, $pagination: PaginationInput!) {\n    previewSelector(memberType: $memberType, selector: $selector, pagination: $pagination) {\n      valid\n      error\n      members {\n        results {\n          id\n          token\n        }\n        pagination {\n          pageStart\n          pageEnd\n          totalRecords\n        }\n      }\n    }\n  }\n": typeof types.PreviewSelectorDocument,
    "\n  query DynamicGroups($memberType: String!) {\n    entityGroups(criteria: { pageNumber: 1, pageSize: 200, memberType: $memberType, membershipMode: \"dynamic\" }) {\n      results {\n        id\n        token\n        name\n        memberType\n        membershipMode\n        selector\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n": typeof types.DynamicGroupsDocument,
    "\n  query GroupMembers($tokens: [String!]!, $pagination: PaginationInput!) {\n    entityGroupsByToken(tokens: $tokens) {\n      token\n      members(pagination: $pagination) {\n        results {\n          id\n          token\n        }\n        pagination {\n          pageStart\n          pageEnd\n          totalRecords\n        }\n      }\n    }\n  }\n": typeof types.GroupMembersDocument,
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
    "\n  query EntityGroups($criteria: EntityGroupSearchCriteria!) {\n    entityGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        memberType\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.EntityGroupsDocument,
    "\n  query EntityGroupByToken($tokens: [String!]!) {\n    entityGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      memberType\n    }\n  }\n": typeof types.EntityGroupByTokenDocument,
    "\n  mutation CreateEntityGroup($request: EntityGroupCreateRequest) {\n    createEntityGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      memberType\n    }\n  }\n": typeof types.CreateEntityGroupDocument,
    "\n  mutation UpdateEntityGroup($token: String!, $request: EntityGroupCreateRequest) {\n    updateEntityGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      memberType\n    }\n  }\n": typeof types.UpdateEntityGroupDocument,
    "\n  mutation DeleteEntityGroup($token: String!) {\n    deleteEntityGroup(token: $token)\n  }\n": typeof types.DeleteEntityGroupDocument,
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
    "\n  query DeviceCommandVocabulary($deviceToken: String!) {\n    deviceCommandVocabulary(deviceToken: $deviceToken) {\n      constrained\n      commands {\n        commandKey\n        name\n        description\n        parameterSchema\n      }\n    }\n  }\n": typeof types.DeviceCommandVocabularyDocument,
    "\n  mutation CreateCommandDefinition($request: CommandDefinitionCreateRequest) {\n    createCommandDefinition(request: $request) {\n      id\n      token\n    }\n  }\n": typeof types.CreateCommandDefinitionDocument,
    "\n  mutation UpdateCommandDefinition($token: String!, $request: CommandDefinitionCreateRequest) {\n    updateCommandDefinition(token: $token, request: $request) {\n      id\n      token\n    }\n  }\n": typeof types.UpdateCommandDefinitionDocument,
    "\n  mutation DeleteCommandDefinition($token: String!) {\n    deleteCommandDefinition(token: $token)\n  }\n": typeof types.DeleteCommandDefinitionDocument,
    "\n  query DetectionRules($criteria: DetectionRuleSearchCriteria!) {\n    detectionRules(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        definition\n        authoringGraph\n        enabled\n        metadata\n        entityGroupToken\n        entityGroupVersion\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.DetectionRulesDocument,
    "\n  query ScopeGroups {\n    entityGroups(criteria: { pageNumber: 1, pageSize: 500, membershipMode: \"dynamic\" }) {\n      results {\n        token\n        name\n        memberType\n        activeVersion\n      }\n    }\n  }\n": typeof types.ScopeGroupsDocument,
    "\n  query EntityGroupVersions($token: String!) {\n    entityGroupVersions(token: $token) {\n      version\n      selector\n      memberType\n      label\n    }\n  }\n": typeof types.EntityGroupVersionsDocument,
    "\n  mutation CreateDetectionRule($request: DetectionRuleCreateRequest!) {\n    createDetectionRule(request: $request) {\n      id\n      token\n    }\n  }\n": typeof types.CreateDetectionRuleDocument,
    "\n  mutation UpdateDetectionRule($token: String!, $request: DetectionRuleCreateRequest!) {\n    updateDetectionRule(token: $token, request: $request) {\n      id\n      token\n    }\n  }\n": typeof types.UpdateDetectionRuleDocument,
    "\n  mutation DeleteDetectionRule($token: String!) {\n    deleteDetectionRule(token: $token)\n  }\n": typeof types.DeleteDetectionRuleDocument,
    "\n  query FacetKeys($criteria: FacetKeySearchCriteria!) {\n    facetKeys(criteria: $criteria) {\n      results {\n        id\n        memberType\n        key\n        valueType\n        source\n        values\n        label\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.FacetKeysDocument,
    "\n  mutation SetFacetKey($request: FacetKeySetRequest!) {\n    setFacetKey(request: $request) {\n      memberType\n      key\n    }\n  }\n": typeof types.SetFacetKeyDocument,
    "\n  mutation DeleteFacetKey($memberType: String!, $key: String!) {\n    deleteFacetKey(memberType: $memberType, key: $key)\n  }\n": typeof types.DeleteFacetKeyDocument,
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
    "\n  query AuditEvents($criteria: AuditEventSearchCriteria!) {\n    auditEvents(criteria: $criteria) {\n      results {\n        id\n        occurredTime\n        category\n        actor\n        operation\n        tableName\n        entityPk\n        entityLabel\n        rowsAffected\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.AuditEventsDocument,
    "\n  query PreviewSelector($memberType: String!, $selector: String!, $pagination: PaginationInput!) {\n    previewSelector(memberType: $memberType, selector: $selector, pagination: $pagination) {\n      valid\n      error\n      members {\n        results {\n          id\n          token\n        }\n        pagination {\n          pageStart\n          pageEnd\n          totalRecords\n        }\n      }\n    }\n  }\n": types.PreviewSelectorDocument,
    "\n  query DynamicGroups($memberType: String!) {\n    entityGroups(criteria: { pageNumber: 1, pageSize: 200, memberType: $memberType, membershipMode: \"dynamic\" }) {\n      results {\n        id\n        token\n        name\n        memberType\n        membershipMode\n        selector\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n": types.DynamicGroupsDocument,
    "\n  query GroupMembers($tokens: [String!]!, $pagination: PaginationInput!) {\n    entityGroupsByToken(tokens: $tokens) {\n      token\n      members(pagination: $pagination) {\n        results {\n          id\n          token\n        }\n        pagination {\n          pageStart\n          pageEnd\n          totalRecords\n        }\n      }\n    }\n  }\n": types.GroupMembersDocument,
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
    "\n  query EntityGroups($criteria: EntityGroupSearchCriteria!) {\n    entityGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        memberType\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.EntityGroupsDocument,
    "\n  query EntityGroupByToken($tokens: [String!]!) {\n    entityGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      memberType\n    }\n  }\n": types.EntityGroupByTokenDocument,
    "\n  mutation CreateEntityGroup($request: EntityGroupCreateRequest) {\n    createEntityGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      memberType\n    }\n  }\n": types.CreateEntityGroupDocument,
    "\n  mutation UpdateEntityGroup($token: String!, $request: EntityGroupCreateRequest) {\n    updateEntityGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      memberType\n    }\n  }\n": types.UpdateEntityGroupDocument,
    "\n  mutation DeleteEntityGroup($token: String!) {\n    deleteEntityGroup(token: $token)\n  }\n": types.DeleteEntityGroupDocument,
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
    "\n  query DeviceCommandVocabulary($deviceToken: String!) {\n    deviceCommandVocabulary(deviceToken: $deviceToken) {\n      constrained\n      commands {\n        commandKey\n        name\n        description\n        parameterSchema\n      }\n    }\n  }\n": types.DeviceCommandVocabularyDocument,
    "\n  mutation CreateCommandDefinition($request: CommandDefinitionCreateRequest) {\n    createCommandDefinition(request: $request) {\n      id\n      token\n    }\n  }\n": types.CreateCommandDefinitionDocument,
    "\n  mutation UpdateCommandDefinition($token: String!, $request: CommandDefinitionCreateRequest) {\n    updateCommandDefinition(token: $token, request: $request) {\n      id\n      token\n    }\n  }\n": types.UpdateCommandDefinitionDocument,
    "\n  mutation DeleteCommandDefinition($token: String!) {\n    deleteCommandDefinition(token: $token)\n  }\n": types.DeleteCommandDefinitionDocument,
    "\n  query DetectionRules($criteria: DetectionRuleSearchCriteria!) {\n    detectionRules(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        definition\n        authoringGraph\n        enabled\n        metadata\n        entityGroupToken\n        entityGroupVersion\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.DetectionRulesDocument,
    "\n  query ScopeGroups {\n    entityGroups(criteria: { pageNumber: 1, pageSize: 500, membershipMode: \"dynamic\" }) {\n      results {\n        token\n        name\n        memberType\n        activeVersion\n      }\n    }\n  }\n": types.ScopeGroupsDocument,
    "\n  query EntityGroupVersions($token: String!) {\n    entityGroupVersions(token: $token) {\n      version\n      selector\n      memberType\n      label\n    }\n  }\n": types.EntityGroupVersionsDocument,
    "\n  mutation CreateDetectionRule($request: DetectionRuleCreateRequest!) {\n    createDetectionRule(request: $request) {\n      id\n      token\n    }\n  }\n": types.CreateDetectionRuleDocument,
    "\n  mutation UpdateDetectionRule($token: String!, $request: DetectionRuleCreateRequest!) {\n    updateDetectionRule(token: $token, request: $request) {\n      id\n      token\n    }\n  }\n": types.UpdateDetectionRuleDocument,
    "\n  mutation DeleteDetectionRule($token: String!) {\n    deleteDetectionRule(token: $token)\n  }\n": types.DeleteDetectionRuleDocument,
    "\n  query FacetKeys($criteria: FacetKeySearchCriteria!) {\n    facetKeys(criteria: $criteria) {\n      results {\n        id\n        memberType\n        key\n        valueType\n        source\n        values\n        label\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.FacetKeysDocument,
    "\n  mutation SetFacetKey($request: FacetKeySetRequest!) {\n    setFacetKey(request: $request) {\n      memberType\n      key\n    }\n  }\n": types.SetFacetKeyDocument,
    "\n  mutation DeleteFacetKey($memberType: String!, $key: String!) {\n    deleteFacetKey(memberType: $memberType, key: $key)\n  }\n": types.DeleteFacetKeyDocument,
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
export function graphql(source: "\n  query AuditEvents($criteria: AuditEventSearchCriteria!) {\n    auditEvents(criteria: $criteria) {\n      results {\n        id\n        occurredTime\n        category\n        actor\n        operation\n        tableName\n        entityPk\n        entityLabel\n        rowsAffected\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').AuditEventsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query PreviewSelector($memberType: String!, $selector: String!, $pagination: PaginationInput!) {\n    previewSelector(memberType: $memberType, selector: $selector, pagination: $pagination) {\n      valid\n      error\n      members {\n        results {\n          id\n          token\n        }\n        pagination {\n          pageStart\n          pageEnd\n          totalRecords\n        }\n      }\n    }\n  }\n"): typeof import('./graphql').PreviewSelectorDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query DynamicGroups($memberType: String!) {\n    entityGroups(criteria: { pageNumber: 1, pageSize: 200, memberType: $memberType, membershipMode: \"dynamic\" }) {\n      results {\n        id\n        token\n        name\n        memberType\n        membershipMode\n        selector\n      }\n      pagination {\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').DynamicGroupsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query GroupMembers($tokens: [String!]!, $pagination: PaginationInput!) {\n    entityGroupsByToken(tokens: $tokens) {\n      token\n      members(pagination: $pagination) {\n        results {\n          id\n          token\n        }\n        pagination {\n          pageStart\n          pageEnd\n          totalRecords\n        }\n      }\n    }\n  }\n"): typeof import('./graphql').GroupMembersDocument;
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
export function graphql(source: "\n  query EntityGroups($criteria: EntityGroupSearchCriteria!) {\n    entityGroups(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        createdAt\n        memberType\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').EntityGroupsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query EntityGroupByToken($tokens: [String!]!) {\n    entityGroupsByToken(tokens: $tokens) {\n      id\n      token\n      name\n      description\n      createdAt\n      memberType\n    }\n  }\n"): typeof import('./graphql').EntityGroupByTokenDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateEntityGroup($request: EntityGroupCreateRequest) {\n    createEntityGroup(request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      memberType\n    }\n  }\n"): typeof import('./graphql').CreateEntityGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateEntityGroup($token: String!, $request: EntityGroupCreateRequest) {\n    updateEntityGroup(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      createdAt\n      memberType\n    }\n  }\n"): typeof import('./graphql').UpdateEntityGroupDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteEntityGroup($token: String!) {\n    deleteEntityGroup(token: $token)\n  }\n"): typeof import('./graphql').DeleteEntityGroupDocument;
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
export function graphql(source: "\n  query DeviceCommandVocabulary($deviceToken: String!) {\n    deviceCommandVocabulary(deviceToken: $deviceToken) {\n      constrained\n      commands {\n        commandKey\n        name\n        description\n        parameterSchema\n      }\n    }\n  }\n"): typeof import('./graphql').DeviceCommandVocabularyDocument;
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
export function graphql(source: "\n  query DetectionRules($criteria: DetectionRuleSearchCriteria!) {\n    detectionRules(criteria: $criteria) {\n      results {\n        id\n        token\n        name\n        description\n        definition\n        authoringGraph\n        enabled\n        metadata\n        entityGroupToken\n        entityGroupVersion\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').DetectionRulesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query ScopeGroups {\n    entityGroups(criteria: { pageNumber: 1, pageSize: 500, membershipMode: \"dynamic\" }) {\n      results {\n        token\n        name\n        memberType\n        activeVersion\n      }\n    }\n  }\n"): typeof import('./graphql').ScopeGroupsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query EntityGroupVersions($token: String!) {\n    entityGroupVersions(token: $token) {\n      version\n      selector\n      memberType\n      label\n    }\n  }\n"): typeof import('./graphql').EntityGroupVersionsDocument;
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
export function graphql(source: "\n  query FacetKeys($criteria: FacetKeySearchCriteria!) {\n    facetKeys(criteria: $criteria) {\n      results {\n        id\n        memberType\n        key\n        valueType\n        source\n        values\n        label\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').FacetKeysDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetFacetKey($request: FacetKeySetRequest!) {\n    setFacetKey(request: $request) {\n      memberType\n      key\n    }\n  }\n"): typeof import('./graphql').SetFacetKeyDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteFacetKey($memberType: String!, $key: String!) {\n    deleteFacetKey(memberType: $memberType, key: $key)\n  }\n"): typeof import('./graphql').DeleteFacetKeyDocument;
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
