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
    "\n  query Members($criteria: MemberSearchCriteria!) {\n    members(criteria: $criteria) {\n      results {\n        email\n        firstName\n        lastName\n        enabled\n        roles {\n          token\n          name\n          owner\n        }\n        scope {\n          kind\n          token\n        }\n        managedByIdentityProvider\n        invitationPending\n        mfaEnabled\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.MembersDocument,
    "\n  query Invitations($criteria: InvitationSearchCriteria!) {\n    invitations(criteria: $criteria) {\n      results {\n        email\n        state\n        createdAt\n        expiresAt\n        acceptedAt\n        revokedAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": typeof types.InvitationsDocument,
    "\n  query TenantRoles {\n    tenantRoles {\n      token\n      name\n      description\n      authorities\n      owner\n    }\n  }\n": typeof types.TenantRolesDocument,
    "\n  query TenantAuthorities {\n    tenantAuthorities\n  }\n": typeof types.TenantAuthoritiesDocument,
    "\n  mutation InviteMember($request: InviteMemberRequest!) {\n    inviteMember(request: $request) {\n      member {\n        email\n      }\n      invitationCode\n    }\n  }\n": typeof types.InviteMemberDocument,
    "\n  mutation SetMemberRoles($email: String!, $roleTokens: [String!]!) {\n    setMemberRoles(email: $email, roleTokens: $roleTokens) {\n      email\n    }\n  }\n": typeof types.SetMemberRolesDocument,
    "\n  mutation SetMemberScope($email: String!, $scope: [ScopeAnchorInput!]!) {\n    setMemberScope(email: $email, scope: $scope) {\n      email\n    }\n  }\n": typeof types.SetMemberScopeDocument,
    "\n  mutation SetMemberAccess($email: String!, $roleTokens: [String!]!, $scope: [ScopeAnchorInput!]!) {\n    setMemberAccess(email: $email, roleTokens: $roleTokens, scope: $scope) {\n      email\n    }\n  }\n": typeof types.SetMemberAccessDocument,
    "\n  mutation SetMemberEnabled($email: String!, $enabled: Boolean!) {\n    setMemberEnabled(email: $email, enabled: $enabled) {\n      email\n    }\n  }\n": typeof types.SetMemberEnabledDocument,
    "\n  mutation RemoveMember($email: String!) {\n    removeMember(email: $email)\n  }\n": typeof types.RemoveMemberDocument,
    "\n  mutation RevokeInvitation($email: String!) {\n    revokeInvitation(email: $email) {\n      email\n    }\n  }\n": typeof types.RevokeInvitationDocument,
    "\n  mutation ReissueInvitation($email: String!) {\n    reissueInvitation(email: $email) {\n      member {\n        email\n      }\n      invitationCode\n    }\n  }\n": typeof types.ReissueInvitationDocument,
    "\n  mutation CreateTenantRole($request: TenantRoleCreateRequest!) {\n    createTenantRole(request: $request) {\n      token\n    }\n  }\n": typeof types.CreateTenantRoleDocument,
    "\n  mutation UpdateTenantRole($token: String!, $request: TenantRoleUpdateRequest!) {\n    updateTenantRole(token: $token, request: $request) {\n      token\n    }\n  }\n": typeof types.UpdateTenantRoleDocument,
    "\n  mutation DeleteTenantRole($token: String!) {\n    deleteTenantRole(token: $token)\n  }\n": typeof types.DeleteTenantRoleDocument,
    "\n  mutation AcceptInvitation($code: String!, $password: String, $identityToken: String) {\n    acceptInvitation(code: $code, password: $password, identityToken: $identityToken) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n        ssoRequired\n      }\n    }\n  }\n": typeof types.AcceptInvitationDocument,
    "\n  mutation BeginTotpEnrollment($identityToken: String!, $password: String!) {\n    beginTotpEnrollment(identityToken: $identityToken, password: $password) {\n      otpauthUri\n      secret\n      expiresAt\n    }\n  }\n": typeof types.BeginTotpEnrollmentDocument,
    "\n  mutation ConfirmTotpEnrollment($identityToken: String!, $password: String!, $code: String!) {\n    confirmTotpEnrollment(identityToken: $identityToken, password: $password, code: $code) {\n      codes\n      auth {\n        identityToken\n        expiresAt\n        superuser\n        memberships {\n          tenant\n          roles\n          ssoRequired\n        }\n      }\n    }\n  }\n": typeof types.ConfirmTotpEnrollmentDocument,
    "\n  mutation RegenerateRecoveryCodes($identityToken: String!, $password: String!, $code: String!) {\n    regenerateRecoveryCodes(identityToken: $identityToken, password: $password, code: $code) {\n      codes\n    }\n  }\n": typeof types.RegenerateRecoveryCodesDocument,
    "\n  mutation DisableTotp($identityToken: String!, $password: String!, $code: String!) {\n    disableTotp(identityToken: $identityToken, password: $password, code: $code)\n  }\n": typeof types.DisableTotpDocument,
    "\n  query MfaStatus($identityToken: String!) {\n    mfaStatus(identityToken: $identityToken) {\n      totpEnabled\n      enrolledAt\n      recoveryCodesRemaining\n    }\n  }\n": typeof types.MfaStatusDocument,
    "\n  query TenantMfa {\n    tenant {\n      token\n      mfaRequired\n      mfaRequiredByTier\n    }\n  }\n": typeof types.TenantMfaDocument,
    "\n  mutation SetOwnTenantMfaRequired($required: Boolean!) {\n    setTenantMfaRequired(required: $required) {\n      token\n      mfaRequired\n      mfaRequiredByTier\n    }\n  }\n": typeof types.SetOwnTenantMfaRequiredDocument,
    "\n  query ServiceAccounts($criteria: ServiceAccountSearchCriteria!) {\n    serviceAccounts(criteria: $criteria) {\n      results {\n        token\n        name\n        description\n        clientId\n        enabled\n        createdBy\n        createdAt\n        roles {\n          token\n          name\n          owner\n        }\n        scope {\n          kind\n          token\n        }\n        secrets {\n          hint\n          createdAt\n          expiresAt\n          lastUsedAt\n          active\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n    serviceAccountTokenEndpoint\n  }\n": typeof types.ServiceAccountsDocument,
    "\n  mutation CreateServiceAccount($request: ServiceAccountCreateRequest!) {\n    createServiceAccount(request: $request) {\n      clientId\n      clientSecret\n      tokenEndpoint\n      serviceAccount {\n        token\n      }\n    }\n  }\n": typeof types.CreateServiceAccountDocument,
    "\n  mutation SetServiceAccountAccess($token: String!, $roleTokens: [String!]!, $scope: [ScopeAnchorInput!]!) {\n    setServiceAccountAccess(token: $token, roleTokens: $roleTokens, scope: $scope) {\n      token\n    }\n  }\n": typeof types.SetServiceAccountAccessDocument,
    "\n  mutation SetServiceAccountEnabled($token: String!, $enabled: Boolean!) {\n    setServiceAccountEnabled(token: $token, enabled: $enabled) {\n      token\n    }\n  }\n": typeof types.SetServiceAccountEnabledDocument,
    "\n  mutation RotateServiceAccountSecret($token: String!, $overlapHours: Int, $expiresInDays: Int) {\n    rotateServiceAccountSecret(token: $token, overlapHours: $overlapHours, expiresInDays: $expiresInDays) {\n      clientId\n      clientSecret\n      tokenEndpoint\n      serviceAccount {\n        token\n      }\n    }\n  }\n": typeof types.RotateServiceAccountSecretDocument,
    "\n  mutation DeleteServiceAccount($token: String!) {\n    deleteServiceAccount(token: $token)\n  }\n": typeof types.DeleteServiceAccountDocument,
    "\n  mutation Login($email: String!, $password: String!) {\n    login(email: $email, password: $password) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n        ssoRequired\n      }\n    }\n  }\n": typeof types.LoginDocument,
    "\n  mutation VerifyMfa($challenge: String!, $code: String!) {\n    verifyMfa(challenge: $challenge, code: $code) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n        ssoRequired\n      }\n    }\n  }\n": typeof types.VerifyMfaDocument,
    "\n  mutation BeginSso($email: String, $connection: String, $returnTo: String!, $codeChallenge: String!) {\n    beginSso(\n      email: $email\n      connection: $connection\n      returnTo: $returnTo\n      codeChallenge: $codeChallenge\n    ) {\n      authorizationUrl\n    }\n  }\n": typeof types.BeginSsoDocument,
    "\n  mutation CompleteSso($code: String!, $codeVerifier: String!) {\n    completeSso(code: $code, codeVerifier: $codeVerifier) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n        ssoRequired\n      }\n    }\n  }\n": typeof types.CompleteSsoDocument,
    "\n  mutation BeginSsoLink($identityToken: String!, $returnTo: String!) {\n    beginSsoLink(identityToken: $identityToken, returnTo: $returnTo) {\n      authorizationUrl\n    }\n  }\n": typeof types.BeginSsoLinkDocument,
    "\n  mutation SelectTenant($identityToken: String!, $tenant: String!) {\n    selectTenant(identityToken: $identityToken, tenant: $tenant) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n": typeof types.SelectTenantDocument,
    "\n  query IdentityMemberships($identityToken: String!) {\n    identityMemberships(identityToken: $identityToken) {\n      tenant\n      roles\n      ssoRequired\n    }\n  }\n": typeof types.IdentityMembershipsDocument,
    "\n  mutation Refresh($refreshToken: String!) {\n    refresh(refreshToken: $refreshToken) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n": typeof types.RefreshDocument,
    "\n  mutation Logout($token: String!) {\n    logout(token: $token, returnTo: CONSOLE) {\n      providerSignOutUrl\n    }\n  }\n": typeof types.LogoutDocument,
    "\n  fragment TenantFields on Tenant {\n    token\n    name\n    description\n    branding {\n      title\n      logo\n      logoMaxHeight\n      primary\n      background\n      foreground\n      accent\n      updatedAt\n    }\n    brandingOverride {\n      title\n      logo\n      logoMaxHeight\n      primary\n      background\n      foreground\n      accent\n      updatedAt\n    }\n    basemap {\n      tileUrl\n      attribution\n      centerLat\n      centerLon\n      zoom\n    }\n    basemapOverride {\n      tileUrl\n      attribution\n      centerLat\n      centerLon\n      zoom\n    }\n    locale\n    localeOverride\n  }\n": typeof types.TenantFieldsFragmentDoc,
    "\n  query CurrentTenant {\n    tenant {\n      ...TenantFields\n    }\n  }\n": typeof types.CurrentTenantDocument,
    "\n  mutation SetTenantBranding($input: TenantBrandingInput!) {\n    setTenantBranding(input: $input) {\n      ...TenantFields\n    }\n  }\n": typeof types.SetTenantBrandingDocument,
    "\n  mutation SetTenantLogo($logo: String) {\n    setTenantLogo(logo: $logo) {\n      ...TenantFields\n    }\n  }\n": typeof types.SetTenantLogoDocument,
    "\n  mutation SetTenantBasemap($input: TenantBasemapInput!) {\n    setTenantBasemap(input: $input) {\n      ...TenantFields\n    }\n  }\n": typeof types.SetTenantBasemapDocument,
    "\n  mutation SetTenantLocale($locale: String) {\n    setTenantLocale(locale: $locale) {\n      ...TenantFields\n    }\n  }\n": typeof types.SetTenantLocaleDocument,
    "\n  query Me {\n    me {\n      email\n      firstName\n      lastName\n    }\n  }\n": typeof types.MeDocument,
    "\n  mutation UpdateProfile($request: ProfileUpdateRequest!) {\n    updateProfile(request: $request) {\n      email\n      firstName\n      lastName\n    }\n  }\n": typeof types.UpdateProfileDocument,
    "\n  query FunctionalAreas {\n    functionalAreas\n  }\n": typeof types.FunctionalAreasDocument,
    "\n  query TenantTokenMasks {\n    tokenMasks\n  }\n": typeof types.TenantTokenMasksDocument,
};
const documents: Documents = {
    "\n  query Members($criteria: MemberSearchCriteria!) {\n    members(criteria: $criteria) {\n      results {\n        email\n        firstName\n        lastName\n        enabled\n        roles {\n          token\n          name\n          owner\n        }\n        scope {\n          kind\n          token\n        }\n        managedByIdentityProvider\n        invitationPending\n        mfaEnabled\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.MembersDocument,
    "\n  query Invitations($criteria: InvitationSearchCriteria!) {\n    invitations(criteria: $criteria) {\n      results {\n        email\n        state\n        createdAt\n        expiresAt\n        acceptedAt\n        revokedAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n": types.InvitationsDocument,
    "\n  query TenantRoles {\n    tenantRoles {\n      token\n      name\n      description\n      authorities\n      owner\n    }\n  }\n": types.TenantRolesDocument,
    "\n  query TenantAuthorities {\n    tenantAuthorities\n  }\n": types.TenantAuthoritiesDocument,
    "\n  mutation InviteMember($request: InviteMemberRequest!) {\n    inviteMember(request: $request) {\n      member {\n        email\n      }\n      invitationCode\n    }\n  }\n": types.InviteMemberDocument,
    "\n  mutation SetMemberRoles($email: String!, $roleTokens: [String!]!) {\n    setMemberRoles(email: $email, roleTokens: $roleTokens) {\n      email\n    }\n  }\n": types.SetMemberRolesDocument,
    "\n  mutation SetMemberScope($email: String!, $scope: [ScopeAnchorInput!]!) {\n    setMemberScope(email: $email, scope: $scope) {\n      email\n    }\n  }\n": types.SetMemberScopeDocument,
    "\n  mutation SetMemberAccess($email: String!, $roleTokens: [String!]!, $scope: [ScopeAnchorInput!]!) {\n    setMemberAccess(email: $email, roleTokens: $roleTokens, scope: $scope) {\n      email\n    }\n  }\n": types.SetMemberAccessDocument,
    "\n  mutation SetMemberEnabled($email: String!, $enabled: Boolean!) {\n    setMemberEnabled(email: $email, enabled: $enabled) {\n      email\n    }\n  }\n": types.SetMemberEnabledDocument,
    "\n  mutation RemoveMember($email: String!) {\n    removeMember(email: $email)\n  }\n": types.RemoveMemberDocument,
    "\n  mutation RevokeInvitation($email: String!) {\n    revokeInvitation(email: $email) {\n      email\n    }\n  }\n": types.RevokeInvitationDocument,
    "\n  mutation ReissueInvitation($email: String!) {\n    reissueInvitation(email: $email) {\n      member {\n        email\n      }\n      invitationCode\n    }\n  }\n": types.ReissueInvitationDocument,
    "\n  mutation CreateTenantRole($request: TenantRoleCreateRequest!) {\n    createTenantRole(request: $request) {\n      token\n    }\n  }\n": types.CreateTenantRoleDocument,
    "\n  mutation UpdateTenantRole($token: String!, $request: TenantRoleUpdateRequest!) {\n    updateTenantRole(token: $token, request: $request) {\n      token\n    }\n  }\n": types.UpdateTenantRoleDocument,
    "\n  mutation DeleteTenantRole($token: String!) {\n    deleteTenantRole(token: $token)\n  }\n": types.DeleteTenantRoleDocument,
    "\n  mutation AcceptInvitation($code: String!, $password: String, $identityToken: String) {\n    acceptInvitation(code: $code, password: $password, identityToken: $identityToken) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n        ssoRequired\n      }\n    }\n  }\n": types.AcceptInvitationDocument,
    "\n  mutation BeginTotpEnrollment($identityToken: String!, $password: String!) {\n    beginTotpEnrollment(identityToken: $identityToken, password: $password) {\n      otpauthUri\n      secret\n      expiresAt\n    }\n  }\n": types.BeginTotpEnrollmentDocument,
    "\n  mutation ConfirmTotpEnrollment($identityToken: String!, $password: String!, $code: String!) {\n    confirmTotpEnrollment(identityToken: $identityToken, password: $password, code: $code) {\n      codes\n      auth {\n        identityToken\n        expiresAt\n        superuser\n        memberships {\n          tenant\n          roles\n          ssoRequired\n        }\n      }\n    }\n  }\n": types.ConfirmTotpEnrollmentDocument,
    "\n  mutation RegenerateRecoveryCodes($identityToken: String!, $password: String!, $code: String!) {\n    regenerateRecoveryCodes(identityToken: $identityToken, password: $password, code: $code) {\n      codes\n    }\n  }\n": types.RegenerateRecoveryCodesDocument,
    "\n  mutation DisableTotp($identityToken: String!, $password: String!, $code: String!) {\n    disableTotp(identityToken: $identityToken, password: $password, code: $code)\n  }\n": types.DisableTotpDocument,
    "\n  query MfaStatus($identityToken: String!) {\n    mfaStatus(identityToken: $identityToken) {\n      totpEnabled\n      enrolledAt\n      recoveryCodesRemaining\n    }\n  }\n": types.MfaStatusDocument,
    "\n  query TenantMfa {\n    tenant {\n      token\n      mfaRequired\n      mfaRequiredByTier\n    }\n  }\n": types.TenantMfaDocument,
    "\n  mutation SetOwnTenantMfaRequired($required: Boolean!) {\n    setTenantMfaRequired(required: $required) {\n      token\n      mfaRequired\n      mfaRequiredByTier\n    }\n  }\n": types.SetOwnTenantMfaRequiredDocument,
    "\n  query ServiceAccounts($criteria: ServiceAccountSearchCriteria!) {\n    serviceAccounts(criteria: $criteria) {\n      results {\n        token\n        name\n        description\n        clientId\n        enabled\n        createdBy\n        createdAt\n        roles {\n          token\n          name\n          owner\n        }\n        scope {\n          kind\n          token\n        }\n        secrets {\n          hint\n          createdAt\n          expiresAt\n          lastUsedAt\n          active\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n    serviceAccountTokenEndpoint\n  }\n": types.ServiceAccountsDocument,
    "\n  mutation CreateServiceAccount($request: ServiceAccountCreateRequest!) {\n    createServiceAccount(request: $request) {\n      clientId\n      clientSecret\n      tokenEndpoint\n      serviceAccount {\n        token\n      }\n    }\n  }\n": types.CreateServiceAccountDocument,
    "\n  mutation SetServiceAccountAccess($token: String!, $roleTokens: [String!]!, $scope: [ScopeAnchorInput!]!) {\n    setServiceAccountAccess(token: $token, roleTokens: $roleTokens, scope: $scope) {\n      token\n    }\n  }\n": types.SetServiceAccountAccessDocument,
    "\n  mutation SetServiceAccountEnabled($token: String!, $enabled: Boolean!) {\n    setServiceAccountEnabled(token: $token, enabled: $enabled) {\n      token\n    }\n  }\n": types.SetServiceAccountEnabledDocument,
    "\n  mutation RotateServiceAccountSecret($token: String!, $overlapHours: Int, $expiresInDays: Int) {\n    rotateServiceAccountSecret(token: $token, overlapHours: $overlapHours, expiresInDays: $expiresInDays) {\n      clientId\n      clientSecret\n      tokenEndpoint\n      serviceAccount {\n        token\n      }\n    }\n  }\n": types.RotateServiceAccountSecretDocument,
    "\n  mutation DeleteServiceAccount($token: String!) {\n    deleteServiceAccount(token: $token)\n  }\n": types.DeleteServiceAccountDocument,
    "\n  mutation Login($email: String!, $password: String!) {\n    login(email: $email, password: $password) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n        ssoRequired\n      }\n    }\n  }\n": types.LoginDocument,
    "\n  mutation VerifyMfa($challenge: String!, $code: String!) {\n    verifyMfa(challenge: $challenge, code: $code) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n        ssoRequired\n      }\n    }\n  }\n": types.VerifyMfaDocument,
    "\n  mutation BeginSso($email: String, $connection: String, $returnTo: String!, $codeChallenge: String!) {\n    beginSso(\n      email: $email\n      connection: $connection\n      returnTo: $returnTo\n      codeChallenge: $codeChallenge\n    ) {\n      authorizationUrl\n    }\n  }\n": types.BeginSsoDocument,
    "\n  mutation CompleteSso($code: String!, $codeVerifier: String!) {\n    completeSso(code: $code, codeVerifier: $codeVerifier) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n        ssoRequired\n      }\n    }\n  }\n": types.CompleteSsoDocument,
    "\n  mutation BeginSsoLink($identityToken: String!, $returnTo: String!) {\n    beginSsoLink(identityToken: $identityToken, returnTo: $returnTo) {\n      authorizationUrl\n    }\n  }\n": types.BeginSsoLinkDocument,
    "\n  mutation SelectTenant($identityToken: String!, $tenant: String!) {\n    selectTenant(identityToken: $identityToken, tenant: $tenant) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n": types.SelectTenantDocument,
    "\n  query IdentityMemberships($identityToken: String!) {\n    identityMemberships(identityToken: $identityToken) {\n      tenant\n      roles\n      ssoRequired\n    }\n  }\n": types.IdentityMembershipsDocument,
    "\n  mutation Refresh($refreshToken: String!) {\n    refresh(refreshToken: $refreshToken) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n": types.RefreshDocument,
    "\n  mutation Logout($token: String!) {\n    logout(token: $token, returnTo: CONSOLE) {\n      providerSignOutUrl\n    }\n  }\n": types.LogoutDocument,
    "\n  fragment TenantFields on Tenant {\n    token\n    name\n    description\n    branding {\n      title\n      logo\n      logoMaxHeight\n      primary\n      background\n      foreground\n      accent\n      updatedAt\n    }\n    brandingOverride {\n      title\n      logo\n      logoMaxHeight\n      primary\n      background\n      foreground\n      accent\n      updatedAt\n    }\n    basemap {\n      tileUrl\n      attribution\n      centerLat\n      centerLon\n      zoom\n    }\n    basemapOverride {\n      tileUrl\n      attribution\n      centerLat\n      centerLon\n      zoom\n    }\n    locale\n    localeOverride\n  }\n": types.TenantFieldsFragmentDoc,
    "\n  query CurrentTenant {\n    tenant {\n      ...TenantFields\n    }\n  }\n": types.CurrentTenantDocument,
    "\n  mutation SetTenantBranding($input: TenantBrandingInput!) {\n    setTenantBranding(input: $input) {\n      ...TenantFields\n    }\n  }\n": types.SetTenantBrandingDocument,
    "\n  mutation SetTenantLogo($logo: String) {\n    setTenantLogo(logo: $logo) {\n      ...TenantFields\n    }\n  }\n": types.SetTenantLogoDocument,
    "\n  mutation SetTenantBasemap($input: TenantBasemapInput!) {\n    setTenantBasemap(input: $input) {\n      ...TenantFields\n    }\n  }\n": types.SetTenantBasemapDocument,
    "\n  mutation SetTenantLocale($locale: String) {\n    setTenantLocale(locale: $locale) {\n      ...TenantFields\n    }\n  }\n": types.SetTenantLocaleDocument,
    "\n  query Me {\n    me {\n      email\n      firstName\n      lastName\n    }\n  }\n": types.MeDocument,
    "\n  mutation UpdateProfile($request: ProfileUpdateRequest!) {\n    updateProfile(request: $request) {\n      email\n      firstName\n      lastName\n    }\n  }\n": types.UpdateProfileDocument,
    "\n  query FunctionalAreas {\n    functionalAreas\n  }\n": types.FunctionalAreasDocument,
    "\n  query TenantTokenMasks {\n    tokenMasks\n  }\n": types.TenantTokenMasksDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Members($criteria: MemberSearchCriteria!) {\n    members(criteria: $criteria) {\n      results {\n        email\n        firstName\n        lastName\n        enabled\n        roles {\n          token\n          name\n          owner\n        }\n        scope {\n          kind\n          token\n        }\n        managedByIdentityProvider\n        invitationPending\n        mfaEnabled\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').MembersDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Invitations($criteria: InvitationSearchCriteria!) {\n    invitations(criteria: $criteria) {\n      results {\n        email\n        state\n        createdAt\n        expiresAt\n        acceptedAt\n        revokedAt\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n  }\n"): typeof import('./graphql').InvitationsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query TenantRoles {\n    tenantRoles {\n      token\n      name\n      description\n      authorities\n      owner\n    }\n  }\n"): typeof import('./graphql').TenantRolesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query TenantAuthorities {\n    tenantAuthorities\n  }\n"): typeof import('./graphql').TenantAuthoritiesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation InviteMember($request: InviteMemberRequest!) {\n    inviteMember(request: $request) {\n      member {\n        email\n      }\n      invitationCode\n    }\n  }\n"): typeof import('./graphql').InviteMemberDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetMemberRoles($email: String!, $roleTokens: [String!]!) {\n    setMemberRoles(email: $email, roleTokens: $roleTokens) {\n      email\n    }\n  }\n"): typeof import('./graphql').SetMemberRolesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetMemberScope($email: String!, $scope: [ScopeAnchorInput!]!) {\n    setMemberScope(email: $email, scope: $scope) {\n      email\n    }\n  }\n"): typeof import('./graphql').SetMemberScopeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetMemberAccess($email: String!, $roleTokens: [String!]!, $scope: [ScopeAnchorInput!]!) {\n    setMemberAccess(email: $email, roleTokens: $roleTokens, scope: $scope) {\n      email\n    }\n  }\n"): typeof import('./graphql').SetMemberAccessDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetMemberEnabled($email: String!, $enabled: Boolean!) {\n    setMemberEnabled(email: $email, enabled: $enabled) {\n      email\n    }\n  }\n"): typeof import('./graphql').SetMemberEnabledDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RemoveMember($email: String!) {\n    removeMember(email: $email)\n  }\n"): typeof import('./graphql').RemoveMemberDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RevokeInvitation($email: String!) {\n    revokeInvitation(email: $email) {\n      email\n    }\n  }\n"): typeof import('./graphql').RevokeInvitationDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation ReissueInvitation($email: String!) {\n    reissueInvitation(email: $email) {\n      member {\n        email\n      }\n      invitationCode\n    }\n  }\n"): typeof import('./graphql').ReissueInvitationDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateTenantRole($request: TenantRoleCreateRequest!) {\n    createTenantRole(request: $request) {\n      token\n    }\n  }\n"): typeof import('./graphql').CreateTenantRoleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateTenantRole($token: String!, $request: TenantRoleUpdateRequest!) {\n    updateTenantRole(token: $token, request: $request) {\n      token\n    }\n  }\n"): typeof import('./graphql').UpdateTenantRoleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteTenantRole($token: String!) {\n    deleteTenantRole(token: $token)\n  }\n"): typeof import('./graphql').DeleteTenantRoleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation AcceptInvitation($code: String!, $password: String, $identityToken: String) {\n    acceptInvitation(code: $code, password: $password, identityToken: $identityToken) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n        ssoRequired\n      }\n    }\n  }\n"): typeof import('./graphql').AcceptInvitationDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation BeginTotpEnrollment($identityToken: String!, $password: String!) {\n    beginTotpEnrollment(identityToken: $identityToken, password: $password) {\n      otpauthUri\n      secret\n      expiresAt\n    }\n  }\n"): typeof import('./graphql').BeginTotpEnrollmentDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation ConfirmTotpEnrollment($identityToken: String!, $password: String!, $code: String!) {\n    confirmTotpEnrollment(identityToken: $identityToken, password: $password, code: $code) {\n      codes\n      auth {\n        identityToken\n        expiresAt\n        superuser\n        memberships {\n          tenant\n          roles\n          ssoRequired\n        }\n      }\n    }\n  }\n"): typeof import('./graphql').ConfirmTotpEnrollmentDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RegenerateRecoveryCodes($identityToken: String!, $password: String!, $code: String!) {\n    regenerateRecoveryCodes(identityToken: $identityToken, password: $password, code: $code) {\n      codes\n    }\n  }\n"): typeof import('./graphql').RegenerateRecoveryCodesDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DisableTotp($identityToken: String!, $password: String!, $code: String!) {\n    disableTotp(identityToken: $identityToken, password: $password, code: $code)\n  }\n"): typeof import('./graphql').DisableTotpDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query MfaStatus($identityToken: String!) {\n    mfaStatus(identityToken: $identityToken) {\n      totpEnabled\n      enrolledAt\n      recoveryCodesRemaining\n    }\n  }\n"): typeof import('./graphql').MfaStatusDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query TenantMfa {\n    tenant {\n      token\n      mfaRequired\n      mfaRequiredByTier\n    }\n  }\n"): typeof import('./graphql').TenantMfaDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetOwnTenantMfaRequired($required: Boolean!) {\n    setTenantMfaRequired(required: $required) {\n      token\n      mfaRequired\n      mfaRequiredByTier\n    }\n  }\n"): typeof import('./graphql').SetOwnTenantMfaRequiredDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query ServiceAccounts($criteria: ServiceAccountSearchCriteria!) {\n    serviceAccounts(criteria: $criteria) {\n      results {\n        token\n        name\n        description\n        clientId\n        enabled\n        createdBy\n        createdAt\n        roles {\n          token\n          name\n          owner\n        }\n        scope {\n          kind\n          token\n        }\n        secrets {\n          hint\n          createdAt\n          expiresAt\n          lastUsedAt\n          active\n        }\n      }\n      pagination {\n        pageStart\n        pageEnd\n        totalRecords\n      }\n    }\n    serviceAccountTokenEndpoint\n  }\n"): typeof import('./graphql').ServiceAccountsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CreateServiceAccount($request: ServiceAccountCreateRequest!) {\n    createServiceAccount(request: $request) {\n      clientId\n      clientSecret\n      tokenEndpoint\n      serviceAccount {\n        token\n      }\n    }\n  }\n"): typeof import('./graphql').CreateServiceAccountDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetServiceAccountAccess($token: String!, $roleTokens: [String!]!, $scope: [ScopeAnchorInput!]!) {\n    setServiceAccountAccess(token: $token, roleTokens: $roleTokens, scope: $scope) {\n      token\n    }\n  }\n"): typeof import('./graphql').SetServiceAccountAccessDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetServiceAccountEnabled($token: String!, $enabled: Boolean!) {\n    setServiceAccountEnabled(token: $token, enabled: $enabled) {\n      token\n    }\n  }\n"): typeof import('./graphql').SetServiceAccountEnabledDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation RotateServiceAccountSecret($token: String!, $overlapHours: Int, $expiresInDays: Int) {\n    rotateServiceAccountSecret(token: $token, overlapHours: $overlapHours, expiresInDays: $expiresInDays) {\n      clientId\n      clientSecret\n      tokenEndpoint\n      serviceAccount {\n        token\n      }\n    }\n  }\n"): typeof import('./graphql').RotateServiceAccountSecretDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DeleteServiceAccount($token: String!) {\n    deleteServiceAccount(token: $token)\n  }\n"): typeof import('./graphql').DeleteServiceAccountDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation Login($email: String!, $password: String!) {\n    login(email: $email, password: $password) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n        ssoRequired\n      }\n    }\n  }\n"): typeof import('./graphql').LoginDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation VerifyMfa($challenge: String!, $code: String!) {\n    verifyMfa(challenge: $challenge, code: $code) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n        ssoRequired\n      }\n    }\n  }\n"): typeof import('./graphql').VerifyMfaDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation BeginSso($email: String, $connection: String, $returnTo: String!, $codeChallenge: String!) {\n    beginSso(\n      email: $email\n      connection: $connection\n      returnTo: $returnTo\n      codeChallenge: $codeChallenge\n    ) {\n      authorizationUrl\n    }\n  }\n"): typeof import('./graphql').BeginSsoDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation CompleteSso($code: String!, $codeVerifier: String!) {\n    completeSso(code: $code, codeVerifier: $codeVerifier) {\n      identityToken\n      expiresAt\n      superuser\n      memberships {\n        tenant\n        roles\n        ssoRequired\n      }\n    }\n  }\n"): typeof import('./graphql').CompleteSsoDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation BeginSsoLink($identityToken: String!, $returnTo: String!) {\n    beginSsoLink(identityToken: $identityToken, returnTo: $returnTo) {\n      authorizationUrl\n    }\n  }\n"): typeof import('./graphql').BeginSsoLinkDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SelectTenant($identityToken: String!, $tenant: String!) {\n    selectTenant(identityToken: $identityToken, tenant: $tenant) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n"): typeof import('./graphql').SelectTenantDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query IdentityMemberships($identityToken: String!) {\n    identityMemberships(identityToken: $identityToken) {\n      tenant\n      roles\n      ssoRequired\n    }\n  }\n"): typeof import('./graphql').IdentityMembershipsDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation Refresh($refreshToken: String!) {\n    refresh(refreshToken: $refreshToken) {\n      accessToken\n      refreshToken\n      expiresAt\n    }\n  }\n"): typeof import('./graphql').RefreshDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation Logout($token: String!) {\n    logout(token: $token, returnTo: CONSOLE) {\n      providerSignOutUrl\n    }\n  }\n"): typeof import('./graphql').LogoutDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  fragment TenantFields on Tenant {\n    token\n    name\n    description\n    branding {\n      title\n      logo\n      logoMaxHeight\n      primary\n      background\n      foreground\n      accent\n      updatedAt\n    }\n    brandingOverride {\n      title\n      logo\n      logoMaxHeight\n      primary\n      background\n      foreground\n      accent\n      updatedAt\n    }\n    basemap {\n      tileUrl\n      attribution\n      centerLat\n      centerLon\n      zoom\n    }\n    basemapOverride {\n      tileUrl\n      attribution\n      centerLat\n      centerLon\n      zoom\n    }\n    locale\n    localeOverride\n  }\n"): typeof import('./graphql').TenantFieldsFragmentDoc;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query CurrentTenant {\n    tenant {\n      ...TenantFields\n    }\n  }\n"): typeof import('./graphql').CurrentTenantDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetTenantBranding($input: TenantBrandingInput!) {\n    setTenantBranding(input: $input) {\n      ...TenantFields\n    }\n  }\n"): typeof import('./graphql').SetTenantBrandingDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetTenantLogo($logo: String) {\n    setTenantLogo(logo: $logo) {\n      ...TenantFields\n    }\n  }\n"): typeof import('./graphql').SetTenantLogoDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetTenantBasemap($input: TenantBasemapInput!) {\n    setTenantBasemap(input: $input) {\n      ...TenantFields\n    }\n  }\n"): typeof import('./graphql').SetTenantBasemapDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation SetTenantLocale($locale: String) {\n    setTenantLocale(locale: $locale) {\n      ...TenantFields\n    }\n  }\n"): typeof import('./graphql').SetTenantLocaleDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Me {\n    me {\n      email\n      firstName\n      lastName\n    }\n  }\n"): typeof import('./graphql').MeDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation UpdateProfile($request: ProfileUpdateRequest!) {\n    updateProfile(request: $request) {\n      email\n      firstName\n      lastName\n    }\n  }\n"): typeof import('./graphql').UpdateProfileDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query FunctionalAreas {\n    functionalAreas\n  }\n"): typeof import('./graphql').FunctionalAreasDocument;
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query TenantTokenMasks {\n    tokenMasks\n  }\n"): typeof import('./graphql').TenantTokenMasksDocument;


export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}
