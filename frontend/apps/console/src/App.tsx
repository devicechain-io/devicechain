// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { lazy } from 'react';
import { Navigate, Outlet, Route, Routes, useLocation } from 'react-router-dom';
import { isScoped } from '@devicechain/client';
import { useTranslation } from 'react-i18next';
import { useAuth } from '@/auth/AuthProvider';
import { LoadingState } from '@/components/ui/loading-state';
import LoginPage from '@/routes/Login';
import AppLayout from '@/routes/AppLayout';
import Home from '@/routes/Home';
import { AreaGate } from '@/components/AreaUnavailable';
import { RouteSuspense } from '@/components/RouteSuspense';
import { AREA } from '@/lib/capabilities';
import { ErrorBoundary } from '@/components/ui/error-boundary';
import { scopedRouteAllowed } from '@/lib/scope';

// Route-level code splitting: every page except the sign-in, the layout and the landing page loads
// on first visit, so the first download carries only what those three need.
const LoginSsoCompletePage = lazy(() => import('@/routes/LoginSsoComplete'));
const DevicesPage = lazy(() => import('@/routes/devices/DevicesPage'));
const DeviceDetailPage = lazy(() => import('@/routes/devices/DeviceDetailPage'));
const DashboardsPage = lazy(() => import('@/routes/dashboards/DashboardsPage'));
const DashboardDetailPage = lazy(() => import('@/routes/dashboards/DashboardDetailPage'));
const AlarmsPage = lazy(() => import('@/routes/alarms/AlarmsPage'));
const SignersPage = lazy(() => import('@/routes/updates/SignersPage'));
const CommandBatchesPage = lazy(() => import('@/routes/command-batches/CommandBatchesPage'));
const CommandBatchDetailPage = lazy(() => import('@/routes/command-batches/CommandBatchDetailPage'));
const ArtifactsPage = lazy(() => import('@/routes/updates/ArtifactsPage'));
const ArtifactDetailPage = lazy(() => import('@/routes/updates/ArtifactDetailPage'));
const RolloutsPage = lazy(() => import('@/routes/updates/RolloutsPage'));
const RolloutDetailPage = lazy(() => import('@/routes/updates/RolloutDetailPage'));
const ConnectorsPage = lazy(() => import('@/routes/connectors/ConnectorsPage'));
const ConnectorDetailPage = lazy(() => import('@/routes/connectors/ConnectorDetailPage'));
const AiProvidersPage = lazy(() => import('@/routes/admin/ai-providers/AiProvidersPage'));
const AiProviderDetailPage = lazy(() => import('@/routes/admin/ai-providers/AiProviderDetailPage'));
const AiPackagingPage = lazy(() => import('@/routes/admin/ai-packaging/AiPackagingPage'));
const AuditPage = lazy(() => import('@/routes/audit/AuditPage'));
const FacetKeysPage = lazy(() => import('@/routes/facets/FacetKeysPage'));
const BrowsePage = lazy(() => import('@/routes/browse/BrowsePage'));
const BrandingPage = lazy(() => import('@/routes/BrandingPage'));
const BasemapPage = lazy(() => import('@/routes/BasemapPage'));
const LocalePage = lazy(() => import('@/routes/LocalePage'));
const AdminProtectedRoute = lazy(() => import('@/routes/admin/AdminProtectedRoute'));
const AdminLayout = lazy(() => import('@/routes/admin/AdminLayout'));
const AdminTenantsPage = lazy(() => import('@/routes/admin/TenantsPage'));
const AdminNewTenantPage = lazy(() => import('@/routes/admin/tenants/NewTenantPage'));
const AdminDeletionsPage = lazy(() => import('@/routes/admin/DeletionsPage'));
const AdminTenantDetailPage = lazy(() => import('@/routes/admin/tenants/TenantDetailPage'));
const AdminTiersPage = lazy(() => import('@/routes/admin/TiersPage'));
const AdminNewTierPage = lazy(() => import('@/routes/admin/tiers/NewTierPage'));
const AdminTierDetailPage = lazy(() => import('@/routes/admin/tiers/TierDetailPage'));
const AdminIdentitiesPage = lazy(() => import('@/routes/admin/IdentitiesPage'));
const AdminNewIdentityPage = lazy(() => import('@/routes/admin/identities/NewIdentityPage'));
const AdminIdentityDetailPage = lazy(() => import('@/routes/admin/identities/IdentityDetailPage'));
const AdminRolesPage = lazy(() => import('@/routes/admin/RolesPage'));
const AdminAuditPage = lazy(() => import('@/routes/admin/AuditPage'));
const AdminNewRolePage = lazy(() => import('@/routes/admin/roles/NewRolePage'));
const AdminRoleDetailPage = lazy(() => import('@/routes/admin/roles/RoleDetailPage'));
const AdminSettingsPage = lazy(() => import('@/routes/admin/settings/SettingsPage'));
const AdminIdentityProvidersPage = lazy(() => import('@/routes/admin/identity-providers/IdentityProvidersPage'));
const AdminIdentityProviderDetailPage = lazy(() => import('@/routes/admin/identity-providers/IdentityProviderDetailPage'));
const MembersPage = lazy(() => import('@/routes/members/MembersPage'));
const ServiceAccountsPage = lazy(() => import('@/routes/service-accounts/ServiceAccountsPage'));
const RolesPage = lazy(() => import('@/routes/roles/RolesPage'));
const RoleDetailPage = lazy(() => import('@/routes/roles/RoleDetailPage'));
const RegistryRoute = lazy(() => import('@/routes/RegistryRoute'));
const AcceptInvitationPage = lazy(() => import('@/routes/AcceptInvitation'));


function ProtectedRoute() {
  const { t } = useTranslation('common');
  const { isAuthenticated, isLoading, claims } = useAuth();
  const { pathname } = useLocation();

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <LoadingState description={t('loading')} />
      </div>
    );
  }
  if (!isAuthenticated) {
    return <Navigate to="/login" replace />;
  }
  // A member limited to part of the tenant is sent home from any page the server would
  // refuse them (lib/scope.ts lists the ones it serves). The nav hides the same pages; this
  // covers the deep link and the bookmark, which the nav never sees.
  if (isScoped(claims) && !scopedRouteAllowed(pathname)) {
    return <Navigate to="/" replace />;
  }
  return <Outlet />;
}

export default function App() {
  return (
    <ErrorBoundary>
      <RouteSuspense>
      <Routes>
      <Route path="/login" element={<LoginPage />} />
      {/* Where the identity provider's callback sends the browser back with a one-time code. */}
      <Route path="/login/sso/complete" element={<LoginSsoCompletePage />} />
      {/* Where an invitation link lands. Public: the person has no session yet, and the
          one-time code rides the URL fragment, which a browser never sends to a server. */}
      <Route path="/invitation" element={<AcceptInvitationPage />} />

      <Route element={<ProtectedRoute />}>
        <Route path="/" element={<AppLayout />}>
          <Route index element={<Home />} />
          <Route path="devices" element={<DevicesPage />} />
          <Route path="devices/:token" element={<DeviceDetailPage />} />
          <Route path="dashboards" element={<DashboardsPage />} />
          <Route path="dashboards/:token" element={<DashboardDetailPage />} />
          <Route path="alarms" element={<AlarmsPage />} />
          {/* Not wrapped in AreaGate: the page renders its own "not deployed" state on
              status === 'absent', before any query fires. */}
          <Route path="updates/signers" element={<SignersPage />} />
          {/* Batch commands are read-only here (list + detail); creating and
              cancelling one are separate slices. Gated because command-delivery is
              absent from the telemetry / ingest-only profiles. */}
          <Route path="command-batches" element={<AreaGate area={AREA.commandDelivery}><CommandBatchesPage /></AreaGate>} />
          <Route path="command-batches/:token" element={<AreaGate area={AREA.commandDelivery}><CommandBatchDetailPage /></AreaGate>} />
          {/* Over-the-air updates: an opt-in area, absent from the default profile. The Signers
              route is a separate slice. */}
          <Route path="updates/artifacts" element={<AreaGate area={AREA.updateManagement}><ArtifactsPage /></AreaGate>} />
          <Route path="updates/artifacts/:token" element={<AreaGate area={AREA.updateManagement}><ArtifactDetailPage /></AreaGate>} />
          <Route path="updates/rollouts" element={<AreaGate area={AREA.updateManagement}><RolloutsPage /></AreaGate>} />
          <Route path="updates/rollouts/:token" element={<AreaGate area={AREA.updateManagement}><RolloutDetailPage /></AreaGate>} />
          <Route path="connectors" element={<AreaGate area={AREA.outboundConnectors}><ConnectorsPage /></AreaGate>} />
          <Route path="connectors/:token" element={<AreaGate area={AREA.outboundConnectors}><ConnectorDetailPage /></AreaGate>} />
          <Route path="audit" element={<AuditPage />} />
          <Route path="facets" element={<FacetKeysPage />} />
          <Route path="browse" element={<BrowsePage />} />
          <Route path="branding" element={<BrandingPage />} />
          <Route path="basemap" element={<BasemapPage />} />
          <Route path="locale" element={<LocalePage />} />
          <Route path="members" element={<MembersPage />} />
          <Route path="service-accounts" element={<ServiceAccountsPage />} />
          <Route path="roles" element={<RolesPage />} />
          <Route path="roles/new" element={<RoleDetailPage />} />
          <Route path="roles/:token" element={<RoleDetailPage />} />
          {/* Every registry family (device types, assets, areas, ...) renders through the one
              generic list/detail page. They are resolved inside one lazily loaded module so the
              resource definitions, their forms and their GraphQL documents stay out of the first
              download; a path that is none of them goes home, as the catch-all below does. */}
          <Route path="*" element={<RegistryRoute />} />
        </Route>
      </Route>

      {/* The instance-scoped admin console (ADR-033), gated on a superuser
          identity session — separate from the tenant ProtectedRoute. */}
      <Route element={<AdminProtectedRoute />}>
        <Route path="/admin" element={<AdminLayout />}>
          <Route index element={<Navigate to="/admin/tenants" replace />} />
          <Route path="tenants" element={<AdminTenantsPage />} />
          <Route path="tenants/new" element={<AdminNewTenantPage />} />
          <Route path="tenants/:token" element={<AdminTenantDetailPage />} />
          <Route path="deletions" element={<AdminDeletionsPage />} />
          <Route path="tiers" element={<AdminTiersPage />} />
          <Route path="tiers/new" element={<AdminNewTierPage />} />
          <Route path="tiers/:token" element={<AdminTierDetailPage />} />
          <Route path="identities" element={<AdminIdentitiesPage />} />
          <Route path="identities/new" element={<AdminNewIdentityPage />} />
          <Route path="identities/:email" element={<AdminIdentityDetailPage />} />
          <Route path="roles" element={<AdminRolesPage />} />
          <Route path="roles/new" element={<AdminNewRolePage />} />
          <Route path="roles/:scope/:token" element={<AdminRoleDetailPage />} />
          <Route path="identity-providers" element={<AdminIdentityProvidersPage />} />
          <Route path="identity-providers/:token" element={<AdminIdentityProviderDetailPage />} />
          <Route path="audit" element={<AdminAuditPage />} />
          {/* Instance-scoped AI provider config: an operator resource, so it lives
              on the admin plane rather than inside a tenant (ADR-065). */}
          <Route path="ai-providers" element={<AreaGate area={AREA.aiInferenceAdmin}><AiProvidersPage /></AreaGate>} />
          <Route path="ai-providers/:token" element={<AreaGate area={AREA.aiInferenceAdmin}><AiProviderDetailPage /></AreaGate>} />
          <Route path="ai-packaging" element={<AreaGate area={AREA.aiInferenceAdmin}><AiPackagingPage /></AreaGate>} />
          <Route path="settings" element={<AdminSettingsPage />} />
        </Route>
      </Route>

        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
      </RouteSuspense>
    </ErrorBoundary>
  );
}