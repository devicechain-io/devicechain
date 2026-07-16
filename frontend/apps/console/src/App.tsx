// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { Navigate, Outlet, Route, Routes } from 'react-router-dom';
import { useAuth } from '@/auth/AuthProvider';
import { LoadingState } from '@/components/ui/loading-state';
import LoginPage from '@/routes/Login';
import AppLayout from '@/routes/AppLayout';
import Dashboard from '@/routes/Dashboard';
import DevicesPage from '@/routes/devices/DevicesPage';
import DeviceDetailPage from '@/routes/devices/DeviceDetailPage';
import DashboardsPage from '@/routes/dashboards/DashboardsPage';
import DashboardDetailPage from '@/routes/dashboards/DashboardDetailPage';
import AlarmsPage from '@/routes/alarms/AlarmsPage';
import ConnectorsPage from '@/routes/connectors/ConnectorsPage';
import ConnectorDetailPage from '@/routes/connectors/ConnectorDetailPage';
import AiProvidersPage from '@/routes/admin/ai-providers/AiProvidersPage';
import AiProviderDetailPage from '@/routes/admin/ai-providers/AiProviderDetailPage';
import AuditPage from '@/routes/audit/AuditPage';
import FacetKeysPage from '@/routes/facets/FacetKeysPage';
import BrowsePage from '@/routes/browse/BrowsePage';
import BrandingPage from '@/routes/BrandingPage';
import { ResourceListPage, ResourceDetailPage, type RegistryResource } from '@/components/registry';
import { ErrorBoundary } from '@/components/ui/error-boundary';
import { deviceTypeResource } from '@/routes/device-types/resource';
import { deviceProfileResource } from '@/routes/device-profiles/resource';
import { deviceGroupResource } from '@/routes/device-groups/resource';
import { assetResource } from '@/routes/assets/resource';
import { assetTypeResource } from '@/routes/asset-types/resource';
import { assetGroupResource } from '@/routes/asset-groups/resource';
import { customerResource } from '@/routes/customers/resource';
import { customerTypeResource } from '@/routes/customer-types/resource';
import { customerGroupResource } from '@/routes/customer-groups/resource';
import { areaResource } from '@/routes/areas/resource';
import { areaTypeResource } from '@/routes/area-types/resource';
import { areaGroupResource } from '@/routes/area-groups/resource';
import AdminProtectedRoute from '@/routes/admin/AdminProtectedRoute';
import AdminLayout from '@/routes/admin/AdminLayout';
import AdminTenantsPage from '@/routes/admin/TenantsPage';
import AdminNewTenantPage from '@/routes/admin/tenants/NewTenantPage';
import AdminTenantDetailPage from '@/routes/admin/tenants/TenantDetailPage';
import AdminTiersPage from '@/routes/admin/TiersPage';
import AdminNewTierPage from '@/routes/admin/tiers/NewTierPage';
import AdminTierDetailPage from '@/routes/admin/tiers/TierDetailPage';
import AdminIdentitiesPage from '@/routes/admin/IdentitiesPage';
import AdminNewIdentityPage from '@/routes/admin/identities/NewIdentityPage';
import AdminIdentityDetailPage from '@/routes/admin/identities/IdentityDetailPage';
import AdminRolesPage from '@/routes/admin/RolesPage';
import AdminAuditPage from '@/routes/admin/AuditPage';
import AdminNewRolePage from '@/routes/admin/roles/NewRolePage';
import AdminRoleDetailPage from '@/routes/admin/roles/RoleDetailPage';
import AdminSettingsPage from '@/routes/admin/SettingsPage';

// Registry families served by the generic list/detail pages. `any` because the
// array mixes RegistryResource<DeviceType | Asset | Customer | …> and the
// element T is invariant; each is consumed by a generic page that re-narrows it.
const REGISTRY_RESOURCES: RegistryResource<any>[] = [
  deviceTypeResource,
  deviceProfileResource,
  deviceGroupResource,
  assetResource,
  assetTypeResource,
  assetGroupResource,
  customerResource,
  customerTypeResource,
  customerGroupResource,
  areaResource,
  areaTypeResource,
  areaGroupResource,
];

function ProtectedRoute() {
  const { isAuthenticated, isLoading } = useAuth();

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <LoadingState description="Loading…" />
      </div>
    );
  }
  if (!isAuthenticated) {
    return <Navigate to="/login" replace />;
  }
  return <Outlet />;
}

export default function App() {
  return (
    <ErrorBoundary>
      <Routes>
      <Route path="/login" element={<LoginPage />} />

      <Route element={<ProtectedRoute />}>
        <Route path="/" element={<AppLayout />}>
          <Route index element={<Dashboard />} />
          <Route path="devices" element={<DevicesPage />} />
          <Route path="devices/:token" element={<DeviceDetailPage />} />
          <Route path="dashboards" element={<DashboardsPage />} />
          <Route path="dashboards/:token" element={<DashboardDetailPage />} />
          <Route path="alarms" element={<AlarmsPage />} />
          <Route path="connectors" element={<ConnectorsPage />} />
          <Route path="connectors/:token" element={<ConnectorDetailPage />} />
          <Route path="audit" element={<AuditPage />} />
          <Route path="facets" element={<FacetKeysPage />} />
          <Route path="browse" element={<BrowsePage />} />
          <Route path="branding" element={<BrandingPage />} />
          {/* Every registry list/detail renders through the one generic page
              component, so React reuses the instance across routes. Key each
              element by its resource's base path to force a fresh mount on
              switch — otherwise the previous resource's data bleeds through. */}
          {REGISTRY_RESOURCES.flatMap((r) => [
            <Route
              key={`${r.basePath}-list`}
              path={r.basePath.slice(1)}
              element={<ResourceListPage key={r.basePath} resource={r} />}
            />,
            <Route
              key={`${r.basePath}-detail`}
              path={`${r.basePath.slice(1)}/:token`}
              element={<ResourceDetailPage key={r.basePath} resource={r} />}
            />,
          ])}
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
          <Route path="tiers" element={<AdminTiersPage />} />
          <Route path="tiers/new" element={<AdminNewTierPage />} />
          <Route path="tiers/:token" element={<AdminTierDetailPage />} />
          <Route path="identities" element={<AdminIdentitiesPage />} />
          <Route path="identities/new" element={<AdminNewIdentityPage />} />
          <Route path="identities/:email" element={<AdminIdentityDetailPage />} />
          <Route path="roles" element={<AdminRolesPage />} />
          <Route path="roles/new" element={<AdminNewRolePage />} />
          <Route path="roles/:scope/:token" element={<AdminRoleDetailPage />} />
          <Route path="audit" element={<AdminAuditPage />} />
          {/* Instance-scoped AI provider config: an operator resource, so it lives
              on the admin plane rather than inside a tenant (ADR-065). */}
          <Route path="ai-providers" element={<AiProvidersPage />} />
          <Route path="ai-providers/:token" element={<AiProviderDetailPage />} />
          <Route path="settings" element={<AdminSettingsPage />} />
        </Route>
      </Route>

        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </ErrorBoundary>
  );
}