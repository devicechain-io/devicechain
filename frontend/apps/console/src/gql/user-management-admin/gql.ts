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
    "\n  query Identities {\n    identities {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.IdentitiesDocument,
    "\n  query Tenants {\n    tenants {\n      id\n      token\n      name\n      enabled\n      # The deletion lifecycle. Deleting a tenant does not remove it on the spot: the\n      # row survives while its data is reclaimed across every functional area, holding\n      # its token so nothing can be created at it in the meantime — every area stores\n      # that tenant's rows under the token, and a tenant created at it too early would\n      # inherit them. The row (and the token) go once the reclamation finishes.\n      purgeState\n      purgeEpoch\n      # Whether the tenant's people must sign in through single sign-on.\n      ssoEnforced\n      tier {\n        token\n        name\n        color\n      }\n      config\n      ingestReadingsPerSecond\n      ingestBurst\n      outboundCallsPerSecond\n      outboundBurst\n      aiExternalEnabled\n      aiInferenceRequestsPerMinute\n      aiInferenceBurst\n      provisioningRegistrationsPerSecond\n      provisioningBurst\n      # 🔴 Selected so the edit form can SHOW them — and, because TenantForm restates\n      # every field it renders on every save, so it can carry them over rather than\n      # sending a null for a value it never read.\n      #\n      # This used to be the only thing standing between an unrelated edit and an erased\n      # ceiling: updateTenant was a full replace, so a field this query did not read back\n      # was a field the next save silently cleared. It was first written here for\n      # heldCommandCeiling with shedPriority left out for a release, which is exactly the\n      # failure it describes. The server now leaves an omitted field alone, so the\n      # failure mode is bounded to the fields the form does render — but a form that\n      # renders a field it cannot read back still shows a blank and then clears it, so\n      # every override above stays selected.\n      heldCommandCeiling\n      shedPriority\n      geoFencePositionCeiling\n      geoFenceCeiling\n      geoFencePositionBudget\n      # What the tenant is ACTUALLY metered at, with the provenance that makes it\n      # readable as tier + delta (ADR-065 decision 7). Resolved server-side by the\n      # same cascade the data plane reads its ceilings through — deliberately not\n      # recomputed here from the fields above, which would be a second implementation\n      # that eventually tells an operator something the platform does not do.\n      #\n      # Selected only on this query, not on the tenant-returning mutations: the one\n      # screen that renders it reads through listTenants and reloads after a write.\n      effectiveSettings {\n        dimension {\n          name\n          label\n          rateUnit\n        }\n        rate {\n          source\n          value\n          tier\n          override\n        }\n        burst {\n          source\n          value\n          tier\n          override\n        }\n      }\n      effectiveScalarSettings {\n        key\n        label\n        source\n        value\n        tier\n        override\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.TenantsDocument,
    "\n  query GovernanceDimensions {\n    governanceDimensions {\n      name\n      label\n      rateField\n      burstField\n      rateUnit\n    }\n  }\n": typeof types.GovernanceDimensionsDocument,
    "\n  query TenantTiers {\n    tenantTiers {\n      id\n      token\n      name\n      description\n      color\n      displayOrder\n    }\n  }\n": typeof types.TenantTiersDocument,
    "\n  query TenantTierCatalog {\n    tenantTiers {\n      id\n      token\n      name\n      description\n      config\n      color\n      displayOrder\n      tenantCount\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.TenantTierCatalogDocument,
    "\n  mutation CreateTenantTier($request: AdminTenantTierCreateRequest!) {\n    createTenantTier(request: $request) {\n      id\n      token\n      name\n      description\n      config\n      color\n      displayOrder\n      tenantCount\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.CreateTenantTierDocument,
    "\n  mutation UpdateTenantTier($token: String!, $request: AdminTenantTierUpdateRequest!) {\n    updateTenantTier(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      config\n      color\n      displayOrder\n      tenantCount\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.UpdateTenantTierDocument,
    "\n  mutation DeleteTenantTier($token: String!) {\n    deleteTenantTier(token: $token)\n  }\n": typeof types.DeleteTenantTierDocument,
    "\n  query TierColorPalette {\n    tierColorPalette {\n      colors\n    }\n  }\n": typeof types.TierColorPaletteDocument,
    "\n  mutation ReorderTenantTiers($orderedTokens: [String!]!) {\n    reorderTenantTiers(orderedTokens: $orderedTokens) {\n      id\n      token\n      displayOrder\n    }\n  }\n": typeof types.ReorderTenantTiersDocument,
    "\n  query Roles($scope: String) {\n    roles(scope: $scope) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.RolesDocument,
    "\n  query AdminAuditEvents($criteria: AdminAuditEventSearchCriteria!) {\n    auditEvents(criteria: $criteria) {\n      results {\n        id\n        occurredTime\n        category\n        tenant\n        actor\n        operation\n        tableName\n        entityPk\n        entityLabel\n        rowsAffected\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.AdminAuditEventsDocument,
    "\n  query Authorities($scope: String) {\n    authorities(scope: $scope)\n  }\n": typeof types.AuthoritiesDocument,
    "\n  mutation CreateIdentity($request: AdminIdentityCreateRequest!) {\n    createIdentity(request: $request) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.CreateIdentityDocument,
    "\n  mutation SetIdentityEnabled($email: String!, $enabled: Boolean!) {\n    setIdentityEnabled(email: $email, enabled: $enabled) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.SetIdentityEnabledDocument,
    "\n  mutation SetSystemRoles($email: String!, $roleTokens: [String!]!) {\n    setSystemRoles(email: $email, roleTokens: $roleTokens) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.SetSystemRolesDocument,
    "\n  mutation SetPassword($email: String!, $password: String!) {\n    setPassword(email: $email, password: $password) {\n      id\n      email\n      enabled\n    }\n  }\n": typeof types.SetPasswordDocument,
    "\n  mutation DeleteIdentity($email: String!) {\n    deleteIdentity(email: $email)\n  }\n": typeof types.DeleteIdentityDocument,
    "\n  mutation AddMembership($email: String!, $tenant: String!, $roleTokens: [String!]!) {\n    addMembership(email: $email, tenant: $tenant, roleTokens: $roleTokens) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": typeof types.AddMembershipDocument,
    "\n  mutation SetMembershipRoles($email: String!, $tenant: String!, $roleTokens: [String!]!) {\n    setMembershipRoles(email: $email, tenant: $tenant, roleTokens: $roleTokens) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": typeof types.SetMembershipRolesDocument,
    "\n  mutation SetMembershipEnabled($email: String!, $tenant: String!, $enabled: Boolean!) {\n    setMembershipEnabled(email: $email, tenant: $tenant, enabled: $enabled) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": typeof types.SetMembershipEnabledDocument,
    "\n  mutation RemoveMembership($email: String!, $tenant: String!) {\n    removeMembership(email: $email, tenant: $tenant) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": typeof types.RemoveMembershipDocument,
    "\n  mutation CreateRole($request: AdminRoleCreateRequest!) {\n    createRole(request: $request) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.CreateRoleDocument,
    "\n  mutation UpdateRole($scope: String!, $token: String!, $request: AdminRoleUpdateRequest!) {\n    updateRole(scope: $scope, token: $token, request: $request) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.UpdateRoleDocument,
    "\n  mutation DeleteRole($scope: String!, $token: String!) {\n    deleteRole(scope: $scope, token: $token)\n  }\n": typeof types.DeleteRoleDocument,
    "\n  mutation CreateTenant($request: AdminTenantCreateRequest!) {\n    createTenant(request: $request) {\n      id\n      token\n      name\n      enabled\n      tier {\n        token\n        name\n        color\n      }\n      config\n      ingestReadingsPerSecond\n      ingestBurst\n      outboundCallsPerSecond\n      outboundBurst\n      aiExternalEnabled\n      aiInferenceRequestsPerMinute\n      aiInferenceBurst\n      provisioningRegistrationsPerSecond\n      provisioningBurst\n      heldCommandCeiling\n      shedPriority\n      geoFencePositionCeiling\n      geoFenceCeiling\n      geoFencePositionBudget\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.CreateTenantDocument,
    "\n  mutation UpdateTenant($token: String!, $request: AdminTenantUpdateRequest!) {\n    updateTenant(token: $token, request: $request) {\n      id\n      token\n      name\n      enabled\n      tier {\n        token\n        name\n        color\n      }\n      config\n      ingestReadingsPerSecond\n      ingestBurst\n      outboundCallsPerSecond\n      outboundBurst\n      aiExternalEnabled\n      aiInferenceRequestsPerMinute\n      aiInferenceBurst\n      provisioningRegistrationsPerSecond\n      provisioningBurst\n      heldCommandCeiling\n      shedPriority\n      geoFencePositionCeiling\n      geoFenceCeiling\n      geoFencePositionBudget\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.UpdateTenantDocument,
    "\n  mutation SetTenantEnabled($token: String!, $enabled: Boolean!) {\n    setTenantEnabled(token: $token, enabled: $enabled) {\n      id\n      token\n      name\n      enabled\n      tier {\n        token\n        name\n        color\n      }\n      config\n      ingestReadingsPerSecond\n      ingestBurst\n      outboundCallsPerSecond\n      outboundBurst\n      aiExternalEnabled\n      aiInferenceRequestsPerMinute\n      aiInferenceBurst\n      provisioningRegistrationsPerSecond\n      provisioningBurst\n      heldCommandCeiling\n      shedPriority\n      geoFencePositionCeiling\n      geoFenceCeiling\n      geoFencePositionBudget\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.SetTenantEnabledDocument,
    "\n  mutation SetTenantSsoEnforced($tenant: String!, $enforced: Boolean!) {\n    setTenantSsoEnforced(tenant: $tenant, enforced: $enforced) {\n      token\n      ssoEnforced\n    }\n  }\n": typeof types.SetTenantSsoEnforcedDocument,
    "\n  mutation DeleteTenant($token: String!) {\n    deleteTenant(token: $token)\n  }\n": typeof types.DeleteTenantDocument,
    "\n  query TenantDeletion($token: String!, $epoch: String) {\n    tenantDeletion(token: $token, epoch: $epoch) {\n      token\n      epoch\n      completedAt\n      rowsErased\n      awaiting\n      elapsesAt\n      blockedBy\n      stores {\n        store\n        complete\n        rowsErased\n        retaining\n        lastError\n        note\n        attemptedAt\n        cleanSince\n      }\n    }\n  }\n": typeof types.TenantDeletionDocument,
    "\n  query TenantDeletions($criteria: TenantDeletionSearchCriteria!) {\n    tenantDeletions(criteria: $criteria) {\n      results {\n        token\n        epoch\n        retainedAuditTenant\n        completedAt\n        rowsErased\n        awaiting\n        elapsesAt\n        blockedBy\n        stores {\n          store\n          complete\n          rowsErased\n          retaining\n          lastError\n          note\n          attemptedAt\n          cleanSince\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.TenantDeletionsDocument,
    "\n  query AdminFunctionalAreas {\n    functionalAreas\n  }\n": typeof types.AdminFunctionalAreasDocument,
    "\n  query IdpConnections {\n    idpConnections {\n      token\n      name\n      kind\n      enabled\n      issuer\n      hasSecret\n      tenants\n      domains {\n        domain\n      }\n      discovery {\n        approved\n        fingerprint\n      }\n    }\n  }\n": typeof types.IdpConnectionsDocument,
    "\n  query IdpConnection($token: String!) {\n    idpConnection(token: $token) {\n      token\n      name\n      kind\n      enabled\n      issuer\n      clientId\n      hasSecret\n      scopes\n      emailClaim\n      requiredClaims {\n        name\n        value\n      }\n      requiredAmr\n      domains {\n        domain\n        verifiedAt\n        verifiedBy\n      }\n      tenants\n      callbackUrl\n      providerSignOut\n      signOutReturnUrls\n      caCertificates {\n        fingerprintSha256\n        subject\n        issuer\n        notBefore\n        notAfter\n        isCa\n      }\n      discovery {\n        authorizationEndpoint\n        tokenEndpoint\n        jwksUri\n        endSessionEndpoint\n        fetchedAt\n        fingerprint\n        approved\n        approvedBy\n        approvedAt\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": typeof types.IdpConnectionDocument,
    "\n  query SsoLinkRequests {\n    ssoLinkRequests {\n      id\n      connection\n      subject\n      email\n      createdAt\n      identityExists\n      subjectChanged\n      manualMemberships\n    }\n  }\n": typeof types.SsoLinkRequestsDocument,
    "\n  mutation CreateIdpConnection($request: IdpConnectionCreateRequest!) {\n    createIdpConnection(request: $request) {\n      token\n    }\n  }\n": typeof types.CreateIdpConnectionDocument,
    "\n  mutation UpdateIdpConnection($token: String!, $request: IdpConnectionUpdateRequest!) {\n    updateIdpConnection(token: $token, request: $request) {\n      token\n    }\n  }\n": typeof types.UpdateIdpConnectionDocument,
    "\n  mutation SetIdpRequiredClaims($token: String!, $claims: [IdpClaimRequirementInput!]!) {\n    setIdpRequiredClaims(token: $token, claims: $claims) {\n      token\n    }\n  }\n": typeof types.SetIdpRequiredClaimsDocument,
    "\n  mutation DeleteIdpConnection($token: String!) {\n    deleteIdpConnection(token: $token)\n  }\n": typeof types.DeleteIdpConnectionDocument,
    "\n  mutation SetIdpConnectionSecret($token: String!, $secret: String!) {\n    setIdpConnectionSecret(token: $token, secret: $secret) {\n      token\n    }\n  }\n": typeof types.SetIdpConnectionSecretDocument,
    "\n  mutation SetIdpCaBundle($token: String!, $caBundle: String!) {\n    setIdpCaBundle(token: $token, caBundle: $caBundle) {\n      token\n    }\n  }\n": typeof types.SetIdpCaBundleDocument,
    "\n  mutation SetIdpConnectionTenants($token: String!, $tenants: [String!]!) {\n    setIdpConnectionTenants(token: $token, tenants: $tenants) {\n      token\n    }\n  }\n": typeof types.SetIdpConnectionTenantsDocument,
    "\n  mutation AddIdpDomain($token: String!, $domain: String!) {\n    addIdpDomain(token: $token, domain: $domain) {\n      token\n    }\n  }\n": typeof types.AddIdpDomainDocument,
    "\n  mutation RemoveIdpDomain($token: String!, $domain: String!) {\n    removeIdpDomain(token: $token, domain: $domain) {\n      token\n    }\n  }\n": typeof types.RemoveIdpDomainDocument,
    "\n  mutation RefreshIdpDiscovery($token: String!) {\n    refreshIdpDiscovery(token: $token) {\n      token\n    }\n  }\n": typeof types.RefreshIdpDiscoveryDocument,
    "\n  mutation ApproveIdpDiscovery($token: String!, $fingerprint: String!) {\n    approveIdpDiscovery(token: $token, fingerprint: $fingerprint) {\n      token\n    }\n  }\n": typeof types.ApproveIdpDiscoveryDocument,
    "\n  mutation ApproveSsoLinkRequest($id: ID!, $membershipDisposition: String) {\n    approveSsoLinkRequest(id: $id, membershipDisposition: $membershipDisposition)\n  }\n": typeof types.ApproveSsoLinkRequestDocument,
    "\n  mutation RejectSsoLinkRequest($id: ID!) {\n    rejectSsoLinkRequest(id: $id)\n  }\n": typeof types.RejectSsoLinkRequestDocument,
};
const documents: Documents = {
    "\n  query Identities {\n    identities {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": types.IdentitiesDocument,
    "\n  query Tenants {\n    tenants {\n      id\n      token\n      name\n      enabled\n      # The deletion lifecycle. Deleting a tenant does not remove it on the spot: the\n      # row survives while its data is reclaimed across every functional area, holding\n      # its token so nothing can be created at it in the meantime — every area stores\n      # that tenant's rows under the token, and a tenant created at it too early would\n      # inherit them. The row (and the token) go once the reclamation finishes.\n      purgeState\n      purgeEpoch\n      # Whether the tenant's people must sign in through single sign-on.\n      ssoEnforced\n      tier {\n        token\n        name\n        color\n      }\n      config\n      ingestReadingsPerSecond\n      ingestBurst\n      outboundCallsPerSecond\n      outboundBurst\n      aiExternalEnabled\n      aiInferenceRequestsPerMinute\n      aiInferenceBurst\n      provisioningRegistrationsPerSecond\n      provisioningBurst\n      # 🔴 Selected so the edit form can SHOW them — and, because TenantForm restates\n      # every field it renders on every save, so it can carry them over rather than\n      # sending a null for a value it never read.\n      #\n      # This used to be the only thing standing between an unrelated edit and an erased\n      # ceiling: updateTenant was a full replace, so a field this query did not read back\n      # was a field the next save silently cleared. It was first written here for\n      # heldCommandCeiling with shedPriority left out for a release, which is exactly the\n      # failure it describes. The server now leaves an omitted field alone, so the\n      # failure mode is bounded to the fields the form does render — but a form that\n      # renders a field it cannot read back still shows a blank and then clears it, so\n      # every override above stays selected.\n      heldCommandCeiling\n      shedPriority\n      geoFencePositionCeiling\n      geoFenceCeiling\n      geoFencePositionBudget\n      # What the tenant is ACTUALLY metered at, with the provenance that makes it\n      # readable as tier + delta (ADR-065 decision 7). Resolved server-side by the\n      # same cascade the data plane reads its ceilings through — deliberately not\n      # recomputed here from the fields above, which would be a second implementation\n      # that eventually tells an operator something the platform does not do.\n      #\n      # Selected only on this query, not on the tenant-returning mutations: the one\n      # screen that renders it reads through listTenants and reloads after a write.\n      effectiveSettings {\n        dimension {\n          name\n          label\n          rateUnit\n        }\n        rate {\n          source\n          value\n          tier\n          override\n        }\n        burst {\n          source\n          value\n          tier\n          override\n        }\n      }\n      effectiveScalarSettings {\n        key\n        label\n        source\n        value\n        tier\n        override\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": types.TenantsDocument,
    "\n  query GovernanceDimensions {\n    governanceDimensions {\n      name\n      label\n      rateField\n      burstField\n      rateUnit\n    }\n  }\n": types.GovernanceDimensionsDocument,
    "\n  query TenantTiers {\n    tenantTiers {\n      id\n      token\n      name\n      description\n      color\n      displayOrder\n    }\n  }\n": types.TenantTiersDocument,
    "\n  query TenantTierCatalog {\n    tenantTiers {\n      id\n      token\n      name\n      description\n      config\n      color\n      displayOrder\n      tenantCount\n      createdAt\n      updatedAt\n    }\n  }\n": types.TenantTierCatalogDocument,
    "\n  mutation CreateTenantTier($request: AdminTenantTierCreateRequest!) {\n    createTenantTier(request: $request) {\n      id\n      token\n      name\n      description\n      config\n      color\n      displayOrder\n      tenantCount\n      createdAt\n      updatedAt\n    }\n  }\n": types.CreateTenantTierDocument,
    "\n  mutation UpdateTenantTier($token: String!, $request: AdminTenantTierUpdateRequest!) {\n    updateTenantTier(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      config\n      color\n      displayOrder\n      tenantCount\n      createdAt\n      updatedAt\n    }\n  }\n": types.UpdateTenantTierDocument,
    "\n  mutation DeleteTenantTier($token: String!) {\n    deleteTenantTier(token: $token)\n  }\n": types.DeleteTenantTierDocument,
    "\n  query TierColorPalette {\n    tierColorPalette {\n      colors\n    }\n  }\n": types.TierColorPaletteDocument,
    "\n  mutation ReorderTenantTiers($orderedTokens: [String!]!) {\n    reorderTenantTiers(orderedTokens: $orderedTokens) {\n      id\n      token\n      displayOrder\n    }\n  }\n": types.ReorderTenantTiersDocument,
    "\n  query Roles($scope: String) {\n    roles(scope: $scope) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": types.RolesDocument,
    "\n  query AdminAuditEvents($criteria: AdminAuditEventSearchCriteria!) {\n    auditEvents(criteria: $criteria) {\n      results {\n        id\n        occurredTime\n        category\n        tenant\n        actor\n        operation\n        tableName\n        entityPk\n        entityLabel\n        rowsAffected\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.AdminAuditEventsDocument,
    "\n  query Authorities($scope: String) {\n    authorities(scope: $scope)\n  }\n": types.AuthoritiesDocument,
    "\n  mutation CreateIdentity($request: AdminIdentityCreateRequest!) {\n    createIdentity(request: $request) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": types.CreateIdentityDocument,
    "\n  mutation SetIdentityEnabled($email: String!, $enabled: Boolean!) {\n    setIdentityEnabled(email: $email, enabled: $enabled) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": types.SetIdentityEnabledDocument,
    "\n  mutation SetSystemRoles($email: String!, $roleTokens: [String!]!) {\n    setSystemRoles(email: $email, roleTokens: $roleTokens) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": types.SetSystemRolesDocument,
    "\n  mutation SetPassword($email: String!, $password: String!) {\n    setPassword(email: $email, password: $password) {\n      id\n      email\n      enabled\n    }\n  }\n": types.SetPasswordDocument,
    "\n  mutation DeleteIdentity($email: String!) {\n    deleteIdentity(email: $email)\n  }\n": types.DeleteIdentityDocument,
    "\n  mutation AddMembership($email: String!, $tenant: String!, $roleTokens: [String!]!) {\n    addMembership(email: $email, tenant: $tenant, roleTokens: $roleTokens) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": types.AddMembershipDocument,
    "\n  mutation SetMembershipRoles($email: String!, $tenant: String!, $roleTokens: [String!]!) {\n    setMembershipRoles(email: $email, tenant: $tenant, roleTokens: $roleTokens) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": types.SetMembershipRolesDocument,
    "\n  mutation SetMembershipEnabled($email: String!, $tenant: String!, $enabled: Boolean!) {\n    setMembershipEnabled(email: $email, tenant: $tenant, enabled: $enabled) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": types.SetMembershipEnabledDocument,
    "\n  mutation RemoveMembership($email: String!, $tenant: String!) {\n    removeMembership(email: $email, tenant: $tenant) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n": types.RemoveMembershipDocument,
    "\n  mutation CreateRole($request: AdminRoleCreateRequest!) {\n    createRole(request: $request) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": types.CreateRoleDocument,
    "\n  mutation UpdateRole($scope: String!, $token: String!, $request: AdminRoleUpdateRequest!) {\n    updateRole(scope: $scope, token: $token, request: $request) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n": types.UpdateRoleDocument,
    "\n  mutation DeleteRole($scope: String!, $token: String!) {\n    deleteRole(scope: $scope, token: $token)\n  }\n": types.DeleteRoleDocument,
    "\n  mutation CreateTenant($request: AdminTenantCreateRequest!) {\n    createTenant(request: $request) {\n      id\n      token\n      name\n      enabled\n      tier {\n        token\n        name\n        color\n      }\n      config\n      ingestReadingsPerSecond\n      ingestBurst\n      outboundCallsPerSecond\n      outboundBurst\n      aiExternalEnabled\n      aiInferenceRequestsPerMinute\n      aiInferenceBurst\n      provisioningRegistrationsPerSecond\n      provisioningBurst\n      heldCommandCeiling\n      shedPriority\n      geoFencePositionCeiling\n      geoFenceCeiling\n      geoFencePositionBudget\n      createdAt\n      updatedAt\n    }\n  }\n": types.CreateTenantDocument,
    "\n  mutation UpdateTenant($token: String!, $request: AdminTenantUpdateRequest!) {\n    updateTenant(token: $token, request: $request) {\n      id\n      token\n      name\n      enabled\n      tier {\n        token\n        name\n        color\n      }\n      config\n      ingestReadingsPerSecond\n      ingestBurst\n      outboundCallsPerSecond\n      outboundBurst\n      aiExternalEnabled\n      aiInferenceRequestsPerMinute\n      aiInferenceBurst\n      provisioningRegistrationsPerSecond\n      provisioningBurst\n      heldCommandCeiling\n      shedPriority\n      geoFencePositionCeiling\n      geoFenceCeiling\n      geoFencePositionBudget\n      createdAt\n      updatedAt\n    }\n  }\n": types.UpdateTenantDocument,
    "\n  mutation SetTenantEnabled($token: String!, $enabled: Boolean!) {\n    setTenantEnabled(token: $token, enabled: $enabled) {\n      id\n      token\n      name\n      enabled\n      tier {\n        token\n        name\n        color\n      }\n      config\n      ingestReadingsPerSecond\n      ingestBurst\n      outboundCallsPerSecond\n      outboundBurst\n      aiExternalEnabled\n      aiInferenceRequestsPerMinute\n      aiInferenceBurst\n      provisioningRegistrationsPerSecond\n      provisioningBurst\n      heldCommandCeiling\n      shedPriority\n      geoFencePositionCeiling\n      geoFenceCeiling\n      geoFencePositionBudget\n      createdAt\n      updatedAt\n    }\n  }\n": types.SetTenantEnabledDocument,
    "\n  mutation SetTenantSsoEnforced($tenant: String!, $enforced: Boolean!) {\n    setTenantSsoEnforced(tenant: $tenant, enforced: $enforced) {\n      token\n      ssoEnforced\n    }\n  }\n": types.SetTenantSsoEnforcedDocument,
    "\n  mutation DeleteTenant($token: String!) {\n    deleteTenant(token: $token)\n  }\n": types.DeleteTenantDocument,
    "\n  query TenantDeletion($token: String!, $epoch: String) {\n    tenantDeletion(token: $token, epoch: $epoch) {\n      token\n      epoch\n      completedAt\n      rowsErased\n      awaiting\n      elapsesAt\n      blockedBy\n      stores {\n        store\n        complete\n        rowsErased\n        retaining\n        lastError\n        note\n        attemptedAt\n        cleanSince\n      }\n    }\n  }\n": types.TenantDeletionDocument,
    "\n  query TenantDeletions($criteria: TenantDeletionSearchCriteria!) {\n    tenantDeletions(criteria: $criteria) {\n      results {\n        token\n        epoch\n        retainedAuditTenant\n        completedAt\n        rowsErased\n        awaiting\n        elapsesAt\n        blockedBy\n        stores {\n          store\n          complete\n          rowsErased\n          retaining\n          lastError\n          note\n          attemptedAt\n          cleanSince\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.TenantDeletionsDocument,
    "\n  query AdminFunctionalAreas {\n    functionalAreas\n  }\n": types.AdminFunctionalAreasDocument,
    "\n  query IdpConnections {\n    idpConnections {\n      token\n      name\n      kind\n      enabled\n      issuer\n      hasSecret\n      tenants\n      domains {\n        domain\n      }\n      discovery {\n        approved\n        fingerprint\n      }\n    }\n  }\n": types.IdpConnectionsDocument,
    "\n  query IdpConnection($token: String!) {\n    idpConnection(token: $token) {\n      token\n      name\n      kind\n      enabled\n      issuer\n      clientId\n      hasSecret\n      scopes\n      emailClaim\n      requiredClaims {\n        name\n        value\n      }\n      requiredAmr\n      domains {\n        domain\n        verifiedAt\n        verifiedBy\n      }\n      tenants\n      callbackUrl\n      providerSignOut\n      signOutReturnUrls\n      caCertificates {\n        fingerprintSha256\n        subject\n        issuer\n        notBefore\n        notAfter\n        isCa\n      }\n      discovery {\n        authorizationEndpoint\n        tokenEndpoint\n        jwksUri\n        endSessionEndpoint\n        fetchedAt\n        fingerprint\n        approved\n        approvedBy\n        approvedAt\n      }\n      createdAt\n      updatedAt\n    }\n  }\n": types.IdpConnectionDocument,
    "\n  query SsoLinkRequests {\n    ssoLinkRequests {\n      id\n      connection\n      subject\n      email\n      createdAt\n      identityExists\n      subjectChanged\n      manualMemberships\n    }\n  }\n": types.SsoLinkRequestsDocument,
    "\n  mutation CreateIdpConnection($request: IdpConnectionCreateRequest!) {\n    createIdpConnection(request: $request) {\n      token\n    }\n  }\n": types.CreateIdpConnectionDocument,
    "\n  mutation UpdateIdpConnection($token: String!, $request: IdpConnectionUpdateRequest!) {\n    updateIdpConnection(token: $token, request: $request) {\n      token\n    }\n  }\n": types.UpdateIdpConnectionDocument,
    "\n  mutation SetIdpRequiredClaims($token: String!, $claims: [IdpClaimRequirementInput!]!) {\n    setIdpRequiredClaims(token: $token, claims: $claims) {\n      token\n    }\n  }\n": types.SetIdpRequiredClaimsDocument,
    "\n  mutation DeleteIdpConnection($token: String!) {\n    deleteIdpConnection(token: $token)\n  }\n": types.DeleteIdpConnectionDocument,
    "\n  mutation SetIdpConnectionSecret($token: String!, $secret: String!) {\n    setIdpConnectionSecret(token: $token, secret: $secret) {\n      token\n    }\n  }\n": types.SetIdpConnectionSecretDocument,
    "\n  mutation SetIdpCaBundle($token: String!, $caBundle: String!) {\n    setIdpCaBundle(token: $token, caBundle: $caBundle) {\n      token\n    }\n  }\n": types.SetIdpCaBundleDocument,
    "\n  mutation SetIdpConnectionTenants($token: String!, $tenants: [String!]!) {\n    setIdpConnectionTenants(token: $token, tenants: $tenants) {\n      token\n    }\n  }\n": types.SetIdpConnectionTenantsDocument,
    "\n  mutation AddIdpDomain($token: String!, $domain: String!) {\n    addIdpDomain(token: $token, domain: $domain) {\n      token\n    }\n  }\n": types.AddIdpDomainDocument,
    "\n  mutation RemoveIdpDomain($token: String!, $domain: String!) {\n    removeIdpDomain(token: $token, domain: $domain) {\n      token\n    }\n  }\n": types.RemoveIdpDomainDocument,
    "\n  mutation RefreshIdpDiscovery($token: String!) {\n    refreshIdpDiscovery(token: $token) {\n      token\n    }\n  }\n": types.RefreshIdpDiscoveryDocument,
    "\n  mutation ApproveIdpDiscovery($token: String!, $fingerprint: String!) {\n    approveIdpDiscovery(token: $token, fingerprint: $fingerprint) {\n      token\n    }\n  }\n": types.ApproveIdpDiscoveryDocument,
    "\n  mutation ApproveSsoLinkRequest($id: ID!, $membershipDisposition: String) {\n    approveSsoLinkRequest(id: $id, membershipDisposition: $membershipDisposition)\n  }\n": types.ApproveSsoLinkRequestDocument,
    "\n  mutation RejectSsoLinkRequest($id: ID!) {\n    rejectSsoLinkRequest(id: $id)\n  }\n": types.RejectSsoLinkRequestDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Identities {\n    identities {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').IdentitiesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Tenants {\n    tenants {\n      id\n      token\n      name\n      enabled\n      # The deletion lifecycle. Deleting a tenant does not remove it on the spot: the\n      # row survives while its data is reclaimed across every functional area, holding\n      # its token so nothing can be created at it in the meantime — every area stores\n      # that tenant's rows under the token, and a tenant created at it too early would\n      # inherit them. The row (and the token) go once the reclamation finishes.\n      purgeState\n      purgeEpoch\n      # Whether the tenant's people must sign in through single sign-on.\n      ssoEnforced\n      tier {\n        token\n        name\n        color\n      }\n      config\n      ingestReadingsPerSecond\n      ingestBurst\n      outboundCallsPerSecond\n      outboundBurst\n      aiExternalEnabled\n      aiInferenceRequestsPerMinute\n      aiInferenceBurst\n      provisioningRegistrationsPerSecond\n      provisioningBurst\n      # 🔴 Selected so the edit form can SHOW them — and, because TenantForm restates\n      # every field it renders on every save, so it can carry them over rather than\n      # sending a null for a value it never read.\n      #\n      # This used to be the only thing standing between an unrelated edit and an erased\n      # ceiling: updateTenant was a full replace, so a field this query did not read back\n      # was a field the next save silently cleared. It was first written here for\n      # heldCommandCeiling with shedPriority left out for a release, which is exactly the\n      # failure it describes. The server now leaves an omitted field alone, so the\n      # failure mode is bounded to the fields the form does render — but a form that\n      # renders a field it cannot read back still shows a blank and then clears it, so\n      # every override above stays selected.\n      heldCommandCeiling\n      shedPriority\n      geoFencePositionCeiling\n      geoFenceCeiling\n      geoFencePositionBudget\n      # What the tenant is ACTUALLY metered at, with the provenance that makes it\n      # readable as tier + delta (ADR-065 decision 7). Resolved server-side by the\n      # same cascade the data plane reads its ceilings through — deliberately not\n      # recomputed here from the fields above, which would be a second implementation\n      # that eventually tells an operator something the platform does not do.\n      #\n      # Selected only on this query, not on the tenant-returning mutations: the one\n      # screen that renders it reads through listTenants and reloads after a write.\n      effectiveSettings {\n        dimension {\n          name\n          label\n          rateUnit\n        }\n        rate {\n          source\n          value\n          tier\n          override\n        }\n        burst {\n          source\n          value\n          tier\n          override\n        }\n      }\n      effectiveScalarSettings {\n        key\n        label\n        source\n        value\n        tier\n        override\n      }\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').TenantsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query GovernanceDimensions {\n    governanceDimensions {\n      name\n      label\n      rateField\n      burstField\n      rateUnit\n    }\n  }\n"): typeof import('./graphql').GovernanceDimensionsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query TenantTiers {\n    tenantTiers {\n      id\n      token\n      name\n      description\n      color\n      displayOrder\n    }\n  }\n"): typeof import('./graphql').TenantTiersDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query TenantTierCatalog {\n    tenantTiers {\n      id\n      token\n      name\n      description\n      config\n      color\n      displayOrder\n      tenantCount\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').TenantTierCatalogDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateTenantTier($request: AdminTenantTierCreateRequest!) {\n    createTenantTier(request: $request) {\n      id\n      token\n      name\n      description\n      config\n      color\n      displayOrder\n      tenantCount\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').CreateTenantTierDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateTenantTier($token: String!, $request: AdminTenantTierUpdateRequest!) {\n    updateTenantTier(token: $token, request: $request) {\n      id\n      token\n      name\n      description\n      config\n      color\n      displayOrder\n      tenantCount\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').UpdateTenantTierDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteTenantTier($token: String!) {\n    deleteTenantTier(token: $token)\n  }\n"): typeof import('./graphql').DeleteTenantTierDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query TierColorPalette {\n    tierColorPalette {\n      colors\n    }\n  }\n"): typeof import('./graphql').TierColorPaletteDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation ReorderTenantTiers($orderedTokens: [String!]!) {\n    reorderTenantTiers(orderedTokens: $orderedTokens) {\n      id\n      token\n      displayOrder\n    }\n  }\n"): typeof import('./graphql').ReorderTenantTiersDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Roles($scope: String) {\n    roles(scope: $scope) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').RolesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AdminAuditEvents($criteria: AdminAuditEventSearchCriteria!) {\n    auditEvents(criteria: $criteria) {\n      results {\n        id\n        occurredTime\n        category\n        tenant\n        actor\n        operation\n        tableName\n        entityPk\n        entityLabel\n        rowsAffected\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').AdminAuditEventsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Authorities($scope: String) {\n    authorities(scope: $scope)\n  }\n"): typeof import('./graphql').AuthoritiesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateIdentity($request: AdminIdentityCreateRequest!) {\n    createIdentity(request: $request) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').CreateIdentityDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetIdentityEnabled($email: String!, $enabled: Boolean!) {\n    setIdentityEnabled(email: $email, enabled: $enabled) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').SetIdentityEnabledDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetSystemRoles($email: String!, $roleTokens: [String!]!) {\n    setSystemRoles(email: $email, roleTokens: $roleTokens) {\n      id\n      email\n      firstName\n      lastName\n      enabled\n      systemRoles\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').SetSystemRolesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetPassword($email: String!, $password: String!) {\n    setPassword(email: $email, password: $password) {\n      id\n      email\n      enabled\n    }\n  }\n"): typeof import('./graphql').SetPasswordDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteIdentity($email: String!) {\n    deleteIdentity(email: $email)\n  }\n"): typeof import('./graphql').DeleteIdentityDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation AddMembership($email: String!, $tenant: String!, $roleTokens: [String!]!) {\n    addMembership(email: $email, tenant: $tenant, roleTokens: $roleTokens) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n"): typeof import('./graphql').AddMembershipDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetMembershipRoles($email: String!, $tenant: String!, $roleTokens: [String!]!) {\n    setMembershipRoles(email: $email, tenant: $tenant, roleTokens: $roleTokens) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n"): typeof import('./graphql').SetMembershipRolesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetMembershipEnabled($email: String!, $tenant: String!, $enabled: Boolean!) {\n    setMembershipEnabled(email: $email, tenant: $tenant, enabled: $enabled) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n"): typeof import('./graphql').SetMembershipEnabledDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RemoveMembership($email: String!, $tenant: String!) {\n    removeMembership(email: $email, tenant: $tenant) {\n      id\n      email\n      memberships {\n        tenant\n        enabled\n        roles\n      }\n    }\n  }\n"): typeof import('./graphql').RemoveMembershipDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateRole($request: AdminRoleCreateRequest!) {\n    createRole(request: $request) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').CreateRoleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateRole($scope: String!, $token: String!, $request: AdminRoleUpdateRequest!) {\n    updateRole(scope: $scope, token: $token, request: $request) {\n      id\n      scope\n      token\n      name\n      description\n      authorities\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').UpdateRoleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteRole($scope: String!, $token: String!) {\n    deleteRole(scope: $scope, token: $token)\n  }\n"): typeof import('./graphql').DeleteRoleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateTenant($request: AdminTenantCreateRequest!) {\n    createTenant(request: $request) {\n      id\n      token\n      name\n      enabled\n      tier {\n        token\n        name\n        color\n      }\n      config\n      ingestReadingsPerSecond\n      ingestBurst\n      outboundCallsPerSecond\n      outboundBurst\n      aiExternalEnabled\n      aiInferenceRequestsPerMinute\n      aiInferenceBurst\n      provisioningRegistrationsPerSecond\n      provisioningBurst\n      heldCommandCeiling\n      shedPriority\n      geoFencePositionCeiling\n      geoFenceCeiling\n      geoFencePositionBudget\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').CreateTenantDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateTenant($token: String!, $request: AdminTenantUpdateRequest!) {\n    updateTenant(token: $token, request: $request) {\n      id\n      token\n      name\n      enabled\n      tier {\n        token\n        name\n        color\n      }\n      config\n      ingestReadingsPerSecond\n      ingestBurst\n      outboundCallsPerSecond\n      outboundBurst\n      aiExternalEnabled\n      aiInferenceRequestsPerMinute\n      aiInferenceBurst\n      provisioningRegistrationsPerSecond\n      provisioningBurst\n      heldCommandCeiling\n      shedPriority\n      geoFencePositionCeiling\n      geoFenceCeiling\n      geoFencePositionBudget\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').UpdateTenantDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetTenantEnabled($token: String!, $enabled: Boolean!) {\n    setTenantEnabled(token: $token, enabled: $enabled) {\n      id\n      token\n      name\n      enabled\n      tier {\n        token\n        name\n        color\n      }\n      config\n      ingestReadingsPerSecond\n      ingestBurst\n      outboundCallsPerSecond\n      outboundBurst\n      aiExternalEnabled\n      aiInferenceRequestsPerMinute\n      aiInferenceBurst\n      provisioningRegistrationsPerSecond\n      provisioningBurst\n      heldCommandCeiling\n      shedPriority\n      geoFencePositionCeiling\n      geoFenceCeiling\n      geoFencePositionBudget\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').SetTenantEnabledDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetTenantSsoEnforced($tenant: String!, $enforced: Boolean!) {\n    setTenantSsoEnforced(tenant: $tenant, enforced: $enforced) {\n      token\n      ssoEnforced\n    }\n  }\n"): typeof import('./graphql').SetTenantSsoEnforcedDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteTenant($token: String!) {\n    deleteTenant(token: $token)\n  }\n"): typeof import('./graphql').DeleteTenantDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query TenantDeletion($token: String!, $epoch: String) {\n    tenantDeletion(token: $token, epoch: $epoch) {\n      token\n      epoch\n      completedAt\n      rowsErased\n      awaiting\n      elapsesAt\n      blockedBy\n      stores {\n        store\n        complete\n        rowsErased\n        retaining\n        lastError\n        note\n        attemptedAt\n        cleanSince\n      }\n    }\n  }\n"): typeof import('./graphql').TenantDeletionDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query TenantDeletions($criteria: TenantDeletionSearchCriteria!) {\n    tenantDeletions(criteria: $criteria) {\n      results {\n        token\n        epoch\n        retainedAuditTenant\n        completedAt\n        rowsErased\n        awaiting\n        elapsesAt\n        blockedBy\n        stores {\n          store\n          complete\n          rowsErased\n          retaining\n          lastError\n          note\n          attemptedAt\n          cleanSince\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').TenantDeletionsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query AdminFunctionalAreas {\n    functionalAreas\n  }\n"): typeof import('./graphql').AdminFunctionalAreasDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query IdpConnections {\n    idpConnections {\n      token\n      name\n      kind\n      enabled\n      issuer\n      hasSecret\n      tenants\n      domains {\n        domain\n      }\n      discovery {\n        approved\n        fingerprint\n      }\n    }\n  }\n"): typeof import('./graphql').IdpConnectionsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query IdpConnection($token: String!) {\n    idpConnection(token: $token) {\n      token\n      name\n      kind\n      enabled\n      issuer\n      clientId\n      hasSecret\n      scopes\n      emailClaim\n      requiredClaims {\n        name\n        value\n      }\n      requiredAmr\n      domains {\n        domain\n        verifiedAt\n        verifiedBy\n      }\n      tenants\n      callbackUrl\n      providerSignOut\n      signOutReturnUrls\n      caCertificates {\n        fingerprintSha256\n        subject\n        issuer\n        notBefore\n        notAfter\n        isCa\n      }\n      discovery {\n        authorizationEndpoint\n        tokenEndpoint\n        jwksUri\n        endSessionEndpoint\n        fetchedAt\n        fingerprint\n        approved\n        approvedBy\n        approvedAt\n      }\n      createdAt\n      updatedAt\n    }\n  }\n"): typeof import('./graphql').IdpConnectionDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query SsoLinkRequests {\n    ssoLinkRequests {\n      id\n      connection\n      subject\n      email\n      createdAt\n      identityExists\n      subjectChanged\n      manualMemberships\n    }\n  }\n"): typeof import('./graphql').SsoLinkRequestsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateIdpConnection($request: IdpConnectionCreateRequest!) {\n    createIdpConnection(request: $request) {\n      token\n    }\n  }\n"): typeof import('./graphql').CreateIdpConnectionDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateIdpConnection($token: String!, $request: IdpConnectionUpdateRequest!) {\n    updateIdpConnection(token: $token, request: $request) {\n      token\n    }\n  }\n"): typeof import('./graphql').UpdateIdpConnectionDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetIdpRequiredClaims($token: String!, $claims: [IdpClaimRequirementInput!]!) {\n    setIdpRequiredClaims(token: $token, claims: $claims) {\n      token\n    }\n  }\n"): typeof import('./graphql').SetIdpRequiredClaimsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteIdpConnection($token: String!) {\n    deleteIdpConnection(token: $token)\n  }\n"): typeof import('./graphql').DeleteIdpConnectionDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetIdpConnectionSecret($token: String!, $secret: String!) {\n    setIdpConnectionSecret(token: $token, secret: $secret) {\n      token\n    }\n  }\n"): typeof import('./graphql').SetIdpConnectionSecretDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetIdpCaBundle($token: String!, $caBundle: String!) {\n    setIdpCaBundle(token: $token, caBundle: $caBundle) {\n      token\n    }\n  }\n"): typeof import('./graphql').SetIdpCaBundleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetIdpConnectionTenants($token: String!, $tenants: [String!]!) {\n    setIdpConnectionTenants(token: $token, tenants: $tenants) {\n      token\n    }\n  }\n"): typeof import('./graphql').SetIdpConnectionTenantsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation AddIdpDomain($token: String!, $domain: String!) {\n    addIdpDomain(token: $token, domain: $domain) {\n      token\n    }\n  }\n"): typeof import('./graphql').AddIdpDomainDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RemoveIdpDomain($token: String!, $domain: String!) {\n    removeIdpDomain(token: $token, domain: $domain) {\n      token\n    }\n  }\n"): typeof import('./graphql').RemoveIdpDomainDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RefreshIdpDiscovery($token: String!) {\n    refreshIdpDiscovery(token: $token) {\n      token\n    }\n  }\n"): typeof import('./graphql').RefreshIdpDiscoveryDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation ApproveIdpDiscovery($token: String!, $fingerprint: String!) {\n    approveIdpDiscovery(token: $token, fingerprint: $fingerprint) {\n      token\n    }\n  }\n"): typeof import('./graphql').ApproveIdpDiscoveryDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation ApproveSsoLinkRequest($id: ID!, $membershipDisposition: String) {\n    approveSsoLinkRequest(id: $id, membershipDisposition: $membershipDisposition)\n  }\n"): typeof import('./graphql').ApproveSsoLinkRequestDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RejectSsoLinkRequest($id: ID!) {\n    rejectSsoLinkRequest(id: $id)\n  }\n"): typeof import('./graphql').RejectSsoLinkRequestDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
